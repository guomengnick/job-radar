package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// Label buttons let the user say, under a judgement, what they think of the
// job: 投, 看 or 不. Each press is stored as a Label, and radar eval
// compares the model with them. A button's data is lbl:<104 code>:<y|m|n|i>;
// i is the job's own button, which only says what it is.
var (
	labelData  = regexp.MustCompile(`^lbl:([0-9a-z]{1,20}):([ymni])$`)
	jobCode    = regexp.MustCompile(`^[0-9a-z]{1,20}$`)
	labelWants = map[string]string{"y": "建議投", "m": "可考慮", "n": "不建議"}
	labelShort = map[string]string{"建議投": "投", "可考慮": "看", "不建議": "不"}
)

// maxLabelRows bounds the buttons under a batch summary; more would bury it.
const maxLabelRows = 8

// LabelButtons is the row under the judgement of a 104 job. A job elsewhere
// gets none: its key is a URL, too long for a button.
func LabelButtons(key string) []notify.Button {
	code, ok := strings.CutPrefix(key, alertATS+"#")
	if !ok || !jobCode.MatchString(code) {
		return nil
	}
	return []notify.Button{
		{Text: "👍 投", Data: "lbl:" + code + ":y"},
		{Text: "🤔 看", Data: "lbl:" + code + ":m"},
		{Text: "👎 不", Data: "lbl:" + code + ":n"},
	}
}

// batchLabelRows are the rows under a batch summary: the best judged 104
// jobs, each led by a button naming it.
func batchLabelRows(results []batchResult) [][]notify.Button {
	var ok []batchResult
	for _, r := range results {
		if r.err == nil && LabelButtons(r.item.Key) != nil {
			ok = append(ok, r)
		}
	}
	sort.SliceStable(ok, func(i, k int) bool { return ok[i].a.Score > ok[k].a.Score })
	var rows [][]notify.Button
	for _, r := range ok {
		if len(rows) == maxLabelRows {
			break
		}
		code := strings.TrimPrefix(r.item.Key, alertATS+"#")
		name := truncate(titleOr(r.a.Company, r.a.JobTitle), 8)
		row := append([]notify.Button{{Text: strconv.Itoa(r.a.Score) + " " + name, Data: "lbl:" + code + ":i"}}, LabelButtons(r.item.Key)...)
		rows = append(rows, row)
	}
	return rows
}

// sendWithButtons sends text with the rows when the notifier can show them.
func sendWithButtons(ctx context.Context, n notify.Notifier, text string, rows [][]notify.Button) error {
	if bs, ok := n.(notify.ButtonSender); ok && len(rows) > 0 {
		return bs.SendWithButtons(ctx, text, rows)
	}
	return n.Send(ctx, text)
}

// TelegramUpdate is the part of a Telegram webhook update a label needs.
type TelegramUpdate struct {
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// ErrLabelNotSaved means a press was not stored; Telegram should send the
// update again. A failure to answer the press is not worth a retry.
var ErrLabelNotSaved = errors.New("label not saved")

// Callbacker answers a pressed button; notify.Telegram is one.
type Callbacker interface {
	AnswerCallback(ctx context.Context, id, text string) error
}

// HandleLabel stores the verdict a pressed button carries and answers the
// press with what was recorded. An update that is no button press, or comes
// from another chat than the user's, is ignored.
func HandleLabel(ctx context.Context, d Deps, cb Callbacker, chatID string, u TelegramUpdate) error {
	q := u.CallbackQuery
	if q == nil || q.Message == nil || strconv.FormatInt(q.Message.Chat.ID, 10) != chatID {
		return nil
	}
	m := labelData.FindStringSubmatch(q.Data)
	if m == nil {
		return cb.AnswerCallback(ctx, q.ID, "看不懂這個按鈕")
	}
	key := alertATS + "#" + m[1]
	name := key
	if a, ok, err := d.Store.AssessmentOf(ctx, "assess#"+key); err == nil && ok {
		name = titleOr(a.Company, a.Title) + "｜AI " + strconv.Itoa(a.Score) + " " + a.Verdict
	}
	if m[2] == "i" {
		return cb.AnswerCallback(ctx, q.ID, name)
	}
	want := labelWants[m[2]]
	if err := d.Store.SaveLabel(ctx, store.Label{Key: "label#" + key, Want: want, LabeledAt: d.Now()}); err != nil {
		return fmt.Errorf("%w: %v", ErrLabelNotSaved, err)
	}
	d.Log.Info("labelled", "key", key, "want", want)
	return cb.AnswerCallback(ctx, q.ID, "已記下：你覺得「"+labelShort[want]+"」｜"+name)
}

// LabelCases turns the stored labels into eval cases.
func LabelCases(labels []store.Label) []EvalCase {
	out := make([]EvalCase, 0, len(labels))
	for _, l := range labels {
		out = append(out, EvalCase{Key: strings.TrimPrefix(l.Key, "label#"), Want: l.Want, Note: "Telegram"})
	}
	return out
}
