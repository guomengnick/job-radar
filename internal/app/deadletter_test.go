package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/store"
)

type memLetters struct {
	got  []Letter
	fail bool
}

func (m *memLetters) Send(_ context.Context, l Letter) error {
	if m.fail {
		return errors.New("sqs down")
	}
	m.got = append(m.got, l)
	return nil
}

func classed(c assess.Class, msg string) error { return &assess.ClassError{Class: c, Err: errors.New(msg)} }

func TestFailedJudgementsRetryThenDeadLetter(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	pages := &memArchive{}
	for _, c := range []string{"t1", "u1", "p1", "x1"} {
		pages.Save(ctx, "jd/104/"+c+".json", assess.Page{URL: "https://www.104.com.tw/job/" + c, Text: "JD " + c})
	}
	item := func(id, code string, attempt int) store.BatchItem {
		return store.BatchItem{ID: id, Key: "104#" + code, URL: "https://www.104.com.tw/job/" + code, Title: code, JD: "jd/104/" + code + ".json", Attempt: attempt}
	}
	b := store.Batch{Key: "batch#msgbatch_0", Pending: store.BatchPending, BatchID: "msgbatch_0", Model: "claude-opus-5-5",
		SubmittedAt: clk.t, Items: []store.BatchItem{item("p0", "t1", 0), item("p1", "u1", 1), item("p2", "p1", 0), item("p3", "x1", 2), item("p4", "ok1", 0)}}
	st.SaveBatch(ctx, b)
	fb := &fakeBatches{ended: true, results: []assess.Result{
		{ID: "p0", Err: classed(assess.Transient, "anthropic: overloaded_error: busy")},
		{ID: "p1", Err: errors.New("anthropic: reply has no assessment")},
		{ID: "p2", Err: classed(assess.Permanent, "anthropic: invalid_request_error: prompt is too long")},
		{ID: "p3", Err: classed(assess.Transient, "anthropic: not processed within 24 hours")},
		{ID: "p4", Assessment: assess.Assessment{Score: 70, Verdict: "可考慮", JobTitle: "ok"}},
	}}
	tg, dead := &sent{}, &memLetters{}
	retry := Retry{Pages: pages, Profile: func(context.Context) (string, error) { return "評分檔案", nil }, Letters: dead}
	if n, err := CheckBatches(ctx, d, fb, tg, []store.Batch{b}, retry); n != 1 || err != nil {
		t.Fatalf("closed %d, %v", n, err)
	}

	// The transient and the unknown one go out again, one attempt more.
	if len(fb.submitted) != 2 || fb.submitted[0].Page.Text != "JD t1" || fb.submitted[1].Page.Text != "JD u1" || fb.profile != "評分檔案" {
		t.Fatalf("resubmitted %+v", fb.submitted)
	}
	open, _ := st.OpenBatches(ctx)
	if len(open) != 1 || open[0].BatchID != "msgbatch_1" || open[0].Items[0].Attempt != 1 || open[0].Items[1].Attempt != 2 {
		t.Fatalf("retry batch %+v", open)
	}
	// The permanent one, and the one out of attempts, are dead letters.
	if len(dead.got) != 2 || dead.got[0].Key != "104#p1" || dead.got[0].Class != "permanent" ||
		dead.got[1].Key != "104#x1" || dead.got[1].Attempts != 2 || dead.got[1].JD != "jd/104/x1.json" {
		t.Fatalf("letters %+v", dead.got)
	}
	msg := tg.msgs[0]
	for _, want := range []string{
		"｜🔁 自動重試第 1 次（批次 msgbatch_1）",
		"｜🔁 自動重試第 2 次（批次 msgbatch_1）",
		"prompt is too long｜🪦 永久錯誤，不重試，進了 DLQ",
		"not processed within 24 hours｜🪦 重試 2 次仍失敗，進了 DLQ",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary missing %q:\n%s", want, msg)
		}
	}
}

func TestRetryThatCannotBeSentIsADeadLetter(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	pages := &memArchive{}
	pages.Save(ctx, "jd/104/t1.json", assess.Page{Text: "x"})
	b := store.Batch{Key: "batch#b0", BatchID: "b0", Pending: store.BatchPending, SubmittedAt: clk.t,
		Items: []store.BatchItem{{ID: "p0", Key: "104#t1", JD: "jd/104/t1.json"}}}
	st.SaveBatch(ctx, b)
	fb := &fakeBatches{ended: true, results: []assess.Result{{ID: "p0", Err: classed(assess.Transient, "busy")}}}
	fb.submitErr = errors.New("anthropic down")
	dead := &memLetters{}
	retry := Retry{Pages: pages, Profile: func(context.Context) (string, error) { return "p", nil }, Letters: dead}
	tg := &sent{}
	CheckBatches(ctx, d, fb, tg, []store.Batch{b}, retry)
	if len(dead.got) != 1 || !strings.Contains(dead.got[0].Error, "重試送不出去") || !strings.Contains(tg.msgs[0], "進了 DLQ") {
		t.Fatalf("letters %+v, summary %q", dead.got, tg.msgs)
	}
	// Without a queue a failure is still reported, not lost silently.
	b2 := b
	b2.Key, b2.BatchID = "batch#b1", "b1"
	st.SaveBatch(ctx, b2)
	fb2 := &fakeBatches{ended: true, results: []assess.Result{{ID: "p0", Err: classed(assess.Permanent, "bad")}}}
	tg2 := &sent{}
	CheckBatches(ctx, d, fb2, tg2, []store.Batch{b2}, Retry{})
	if !strings.Contains(tg2.msgs[0], "⚠️ 永久錯誤，不重試（沒有 DLQ，只記在這裡）") {
		t.Errorf("summary %q", tg2.msgs[0])
	}
}

func TestStuckBatchWarnsOnce(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	b := store.Batch{Key: "batch#b0", BatchID: "b0", Pending: store.BatchPending, SubmittedAt: clk.t,
		Items: []store.BatchItem{{ID: "p0", Key: "104#a"}}}
	st.SaveBatch(ctx, b)
	fb, tg := &fakeBatches{}, &sent{} // never ends
	check := func() {
		open, _ := st.OpenBatches(ctx)
		CheckBatches(ctx, d, fb, tg, open, Retry{})
	}
	clk.t = clk.t.Add(25 * time.Hour)
	check()
	if len(tg.msgs) != 0 {
		t.Fatalf("warned at 25 h: %q", tg.msgs)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	check()
	check()
	if len(tg.msgs) != 1 || !strings.Contains(tg.msgs[0], "⏳ 批次 b0（1 個職缺）送出超過 26 小時還沒結束") {
		t.Fatalf("warnings %q", tg.msgs)
	}
	if open, _ := st.OpenBatches(ctx); len(open) != 1 || open[0].WarnedAt.IsZero() {
		t.Errorf("still open and marked warned: %+v", open)
	}
}
