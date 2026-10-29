package app

import (
	"context"
	"fmt"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/store"
)

// A judgement that failed is batched again or given up on, by its class
// (assess.Class):
//
//   - permanent (a page too long, a request the API refuses): the same
//     request fails again, so it goes straight to the dead-letter queue;
//   - transient (busy, rate-limited, down, expired) and unknown: batched
//     again, at most maxRetries times, then the dead-letter queue.
//
// Every page given up on is a Letter in the dead-letter queue, with what it
// takes to try again by hand (radar redrive), and its line in the batch
// summary says so: the summary is how the user hears of it.
const maxRetries = 2

// stuckAfter: a batch ends within 24 hours. One still open after 26 means
// something is wrong with it, or with this checker.
const stuckAfter = 26 * time.Hour

// Letter is one failure no automatic retry will fix.
type Letter struct {
	Kind     string    `json:"kind"` // "judgement"
	Key      string    `json:"key"`
	URL      string    `json:"url"`
	Title    string    `json:"title"`
	JD       string    `json:"jd,omitempty"` // the kept page, for redrive
	Class    string    `json:"class"`
	Error    string    `json:"error"`
	Attempts int       `json:"attempts"` // how many times it was batched again
	At       time.Time `json:"at"`
}

// DeadLetters keeps letters; the SQS dead-letter queue is one.
type DeadLetters interface {
	Send(ctx context.Context, l Letter) error
}

// Retry is what CheckBatches needs to batch failed judgements again and
// to keep the ones it gives up on. Without Pages and Profile nothing is
// retried; without Letters a failure is only reported.
type Retry struct {
	Pages   KeptPages
	Profile func(ctx context.Context) (Profile, error) // read only when something is retried
	Letters DeadLetters
}

// handleFailures decides the fate of each failed result of batch b and
// records it in the result for the summary.
func (d Deps) handleFailures(ctx context.Context, a BatchAssessor, r Retry, b store.Batch, done []batchResult) {
	var again []int
	for i := range done {
		j := &done[i]
		if j.err == nil {
			continue
		}
		class := assess.ClassOf(j.err)
		if class != assess.Permanent && j.item.Attempt < maxRetries && j.item.JD != "" && r.Pages != nil && r.Profile != nil {
			again = append(again, i)
			continue
		}
		d.giveUp(ctx, r, j, class)
	}
	if len(again) == 0 {
		return
	}
	items := make([]store.BatchItem, len(again))
	for k, i := range again {
		items[k] = done[i].item
	}
	id, err := d.resubmit(ctx, a, r, b, items)
	for _, i := range again {
		j := &done[i]
		if err != nil {
			j.err = fmt.Errorf("%v；重試送不出去：%v", j.err, err)
			d.giveUp(ctx, r, j, assess.ClassOf(j.err))
			continue
		}
		j.fate = fmt.Sprintf("🔁 自動重試第 %d 次（批次 %s）", j.item.Attempt+1, id)
	}
}

// giveUp puts one failed judgement in the dead-letter queue.
func (d Deps) giveUp(ctx context.Context, r Retry, j *batchResult, class assess.Class) {
	why := "重試 " + fmt.Sprint(j.item.Attempt) + " 次仍失敗"
	if class == assess.Permanent {
		why = "永久錯誤，不重試"
	}
	l := Letter{Kind: "judgement", Key: j.item.Key, URL: j.item.URL, Title: j.item.Title, JD: j.item.JD,
		Class: string(class), Error: j.err.Error(), Attempts: j.item.Attempt, At: d.Now()}
	switch {
	case r.Letters == nil:
		j.fate = "⚠️ " + why + "（沒有 DLQ，只記在這裡）"
	default:
		if err := r.Letters.Send(ctx, l); err != nil {
			d.Log.Error("dead letter", "key", l.Key, "err", err)
			j.fate = "⚠️ " + why + "，進 DLQ 失敗"
			return
		}
		j.fate = "🪦 " + why + "，進了 DLQ"
	}
	d.Log.Warn("gave up on judgement", "key", l.Key, "class", l.Class, "attempts", l.Attempts, "err", l.Error)
}

// resubmit batches the kept pages of the given items again, as a new open
// batch whose items count one more attempt.
func (d Deps) resubmit(ctx context.Context, a BatchAssessor, r Retry, b store.Batch, items []store.BatchItem) (string, error) {
	profile, err := r.Profile(ctx)
	if err != nil {
		return "", err
	}
	reqs := make([]assess.BatchItem, 0, len(items))
	kept := make([]store.BatchItem, 0, len(items))
	for i, it := range items {
		var p assess.Page
		if err := r.Pages.Load(ctx, it.JD, &p); err != nil {
			return "", fmt.Errorf("read %s: %w", it.JD, err)
		}
		it.ID, it.Attempt = fmt.Sprintf("p%d", i), it.Attempt+1
		reqs = append(reqs, assess.BatchItem{ID: it.ID, Page: p})
		kept = append(kept, it)
	}
	id, err := a.Submit(ctx, profile.Text, reqs)
	if err != nil {
		return "", err
	}
	nb := store.Batch{Key: "batch#" + id, Pending: store.BatchPending, SubmittedAt: d.Now(), BatchID: id, Model: b.Model,
		ProfileVersion: profile.Version, Commit: d.Commit, Items: kept}
	if err := d.Store.SaveBatch(ctx, nb); err != nil {
		return id, fmt.Errorf("retry batch %s submitted but not recorded: %w", id, err)
	}
	d.Log.Info("retry batch submitted", "batch", id, "from", b.BatchID, "items", len(kept))
	return id, nil
}

// warnIfStuck tells the user, once, about a batch open past stuckAfter.
func (d Deps) warnIfStuck(ctx context.Context, n interface {
	Send(context.Context, string) error
}, b store.Batch) error {
	if d.Now().Sub(b.SubmittedAt) < stuckAfter || !b.WarnedAt.IsZero() {
		return nil
	}
	msg := fmt.Sprintf("⏳ 批次 %s（%d 個職缺）送出超過 26 小時還沒結束。Anthropic 最久 24 小時，可能出了問題：看 /aws/lambda/job-radar-batch 的 log。",
		b.BatchID, len(b.Items))
	if err := n.Send(ctx, msg); err != nil {
		return err
	}
	b.WarnedAt = d.Now()
	return d.Store.SaveBatch(ctx, b)
}
