package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/filter"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// BatchAssessor judges pages through a Message Batch; assess.Client is the
// real one.
type BatchAssessor interface {
	Submit(ctx context.Context, profile string, items []assess.BatchItem) (string, error)
	Ended(ctx context.Context, batchID string) (bool, error)
	Results(ctx context.Context, batchID string) ([]assess.Result, error)
}

// PageArchive keeps pages as JSON under a name: each batch as sent, so it
// can be judged again (a new rubric, a failed request) without reopening
// the tabs, and each judged page on its own (jdName), so a judgement can be
// read next to the posting it came from.
type PageArchive interface {
	Save(ctx context.Context, name string, v any) error
}

// jdName is where a judged page is kept: jd/104/<code>.json for a 104 job,
// jd/web/<hash>.json for a page elsewhere, whose key is its URL. A page
// judged again replaces the older copy.
func jdName(key string) string {
	if code, ok := strings.CutPrefix(key, alertATS+"#"); ok {
		return "jd/104/" + code + ".json"
	}
	sum := sha256.Sum256([]byte(key))
	return "jd/web/" + hex.EncodeToString(sum[:8]) + ".json"
}

// PageRules are the cheap checks a page from the batch button goes through
// before the model sees it. They live in an SSM parameter as JSON, so the
// user changes them without a deploy. A page they exclude is still listed
// in the summary, with the reason.
type PageRules struct {
	ExcludeTitleWords    []string `json:"exclude_title_words"`
	ExcludeLocationWords []string `json:"exclude_location_words"`
	// Zero turns the pay rule off.
	MinAnnualSalary  int `json:"min_annual_salary"`
	MinMonthlyTopPay int `json:"min_monthly_top_pay"`
}

// ParsePageRules reads the rules; an unknown field is an error, so a typo
// in the parameter does not silently turn a rule off.
func ParsePageRules(s string) (PageRules, error) {
	var r PageRules
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return PageRules{}, fmt.Errorf("page rules: %w", err)
	}
	return r, nil
}

// check returns why the rules exclude the page, in words, or "". The title
// is the part of the browser tab's title before the first ｜ (104 writes
// "職稱｜公司｜…"); place and pay are read from the lines after 上班地點 and
// 工作待遇 on the page. A field the page does not have is not checked.
func (r PageRules) check(p assess.Page) string {
	floor := filter.SalaryFloor{Annual: r.MinAnnualSalary, MonthlyTop: r.MinMonthlyTopPay}
	pay := ""
	if floor.Annual > 0 || floor.MonthlyTop > 0 {
		pay = fieldAfter(p.Text, "工作待遇")
	}
	if v := filter.NewSpringboard(nil, r.ExcludeTitleWords, floor).Evaluate(pageTitle(p.Title), pay); !v.Pass {
		return reasonText(v.Reason)
	}
	place := fieldAfter(p.Text, "上班地點")
	for _, w := range r.ExcludeLocationWords {
		if w = strings.TrimSpace(w); w != "" && strings.Contains(place, w) {
			return "地點含「" + w + "」"
		}
	}
	return ""
}

func pageTitle(t string) string {
	if i := strings.IndexAny(t, "｜|"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// fieldAfter returns what follows label on its line, or else the next
// non-empty line: a page's text puts a field's name and value on one line
// or two, depending on the layout.
func fieldAfter(text, label string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		j := strings.Index(l, label)
		if j < 0 {
			continue
		}
		if rest := strings.TrimSpace(strings.TrimLeft(l[j+len(label):], "：: ")); rest != "" {
			return rest
		}
		for _, next := range lines[i+1:] {
			if next = strings.TrimSpace(next); next != "" {
				return next
			}
		}
		return ""
	}
	return ""
}

// reassessAfter is how long a judgement of a job is reused instead of
// paying for it again.
const reassessAfter = 14 * 24 * time.Hour

// BatchReceipt is what the batch button gets back at once.
type BatchReceipt struct {
	Received  int    `json:"received"`
	Submitted int    `json:"submitted"`
	Excluded  int    `json:"excluded"`
	Skipped   int    `json:"skipped"`
	BatchID   string `json:"batch_id,omitempty"`
}

// SubmitPages takes the pages the batch button read from the open tabs.
// Each is archived, then the rules run, a job judged in the last two weeks
// is skipped, and the rest go to the model as one Message Batch; each of
// those is also kept on its own (jdName). The batch
// is recorded open; CheckBatches sends the summary once it has ended. When
// nothing is left for the model the summary goes out at once; otherwise the
// caller says the batch went out (BuildBatchReceipt).
func SubmitPages(ctx context.Context, d Deps, a BatchAssessor, n notify.Notifier, archive PageArchive,
	rules PageRules, profile string, pages []assess.Page) (BatchReceipt, error) {
	now := d.Now()
	if err := archive.Save(ctx, "batches/"+now.Format("2006-01-02T15-04-05Z")+".json", pages); err != nil {
		d.Log.Error("archive pages", "err", err)
	}
	b := store.Batch{Pending: store.BatchPending, SubmittedAt: now, Model: d.Cfg.Assess.Model}
	var items []assess.BatchItem
	seen := map[string]bool{}
	for _, p := range pages {
		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			continue
		}
		key := clipKey(u)
		if seen[key] {
			continue // the same job open in two tabs
		}
		seen[key] = true
		it := store.BatchItem{Key: key, URL: u.String(), Title: pageTitle(p.Title)}
		if strings.TrimSpace(p.Text) == "" {
			it.Reason = "頁面沒有文字，可能還沒載完"
			b.Skipped = append(b.Skipped, it)
			continue
		}
		if it.Reason = rules.check(p); it.Reason != "" {
			b.Excluded = append(b.Excluded, it)
			continue
		}
		prev, ok, err := d.Store.AssessmentOf(ctx, "assess#"+key)
		if err != nil {
			return BatchReceipt{}, err
		}
		if ok && now.Sub(prev.AssessedAt) < reassessAfter {
			it.Reason = fmt.Sprintf("兩週內評過：%d 分", prev.Score)
			b.Skipped = append(b.Skipped, it)
			continue
		}
		it.ID = fmt.Sprintf("p%d", len(items))
		if err := archive.Save(ctx, jdName(key), p); err != nil {
			d.Log.Error("keep page", "key", key, "err", err)
		} else {
			it.JD = jdName(key)
		}
		items = append(items, assess.BatchItem{ID: it.ID, Page: p})
		b.Items = append(b.Items, it)
	}
	r := BatchReceipt{Received: len(pages), Submitted: len(items), Excluded: len(b.Excluded), Skipped: len(b.Skipped)}

	if len(items) == 0 {
		return r, n.Send(ctx, BuildBatchSummary(b, nil, assess.Usage{}))
	}
	id, err := a.Submit(ctx, profile, items)
	if err != nil {
		return r, err
	}
	r.BatchID, b.BatchID, b.Key = id, id, "batch#"+id
	if err := d.Store.SaveBatch(ctx, b); err != nil {
		// The batch runs and is paid for, but no summary will come.
		return r, fmt.Errorf("batch %s submitted but not recorded: %w", id, err)
	}
	d.Log.Info("batch submitted", "batch", id, "received", r.Received, "submitted", r.Submitted,
		"excluded", r.Excluded, "skipped", r.Skipped)
	return r, nil
}

// BuildBatchReceipt is the Telegram note the batch button gets once its
// batch went out (BatchID set). A Message Batch ends within 24 hours,
// usually minutes to a few hours.
func BuildBatchReceipt(r BatchReceipt) string {
	return fmt.Sprintf("📥 收到 %d 頁：送 AI 評分 %d｜規則排除 %d｜略過 %d\n分頁可以關了，總表最久 24 小時內傳來（通常幾分鐘到幾小時）。",
		r.Received, r.Submitted, r.Excluded, r.Skipped)
}

// CheckBatches sends the summary of every open batch that has ended, and
// stores its judgements. A batch whose summary could not be sent stays open
// and is tried again on the next run; storing a judgement twice is harmless.
func CheckBatches(ctx context.Context, d Deps, a BatchAssessor, n notify.Notifier, open []store.Batch, retry Retry) (int, error) {
	var errs []error
	closed := 0
	for _, b := range open {
		ended, err := a.Ended(ctx, b.BatchID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ended {
			errs = append(errs, d.warnIfStuck(ctx, n, b))
			continue
		}
		results, err := a.Results(ctx, b.BatchID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		done, total := d.storeResults(ctx, b, results)
		d.handleFailures(ctx, a, retry, b, done)
		if err := sendWithButtons(ctx, n, BuildBatchSummary(b, done, total), batchLabelRows(done)); err != nil {
			errs = append(errs, fmt.Errorf("send batch %s summary: %w", b.BatchID, err))
			continue
		}
		if err := d.Store.CloseBatch(ctx, b.Key, d.Now()); err != nil {
			errs = append(errs, err)
			continue
		}
		closed++
		d.Log.Info("batch closed", "batch", b.BatchID, "model", b.Model, "results", len(results),
			"input_tokens", total.InputTokens, "output_tokens", total.OutputTokens,
			"cache_read_tokens", total.CacheReadTokens, "cache_write_tokens", total.CacheWriteTokens)
	}
	return closed, errors.Join(errs...)
}

// batchResult is one batch item and what became of it.
type batchResult struct {
	item store.BatchItem
	a    assess.Assessment
	err  error
	fate string // for a failure: retried or given up on (handleFailures)
}

// storeResults pairs each result with its page and stores the judgements,
// as a single 送到雷達 does. An item with no result is reported as failed.
func (d Deps) storeResults(ctx context.Context, b store.Batch, results []assess.Result) ([]batchResult, assess.Usage) {
	byID := map[string]assess.Result{}
	var total assess.Usage
	for _, r := range results {
		byID[r.ID] = r
		total.InputTokens += r.Usage.InputTokens
		total.OutputTokens += r.Usage.OutputTokens
		total.CacheReadTokens += r.Usage.CacheReadTokens
		total.CacheWriteTokens += r.Usage.CacheWriteTokens
	}
	now := d.Now()
	out := make([]batchResult, 0, len(b.Items))
	for _, it := range b.Items {
		r, ok := byID[it.ID]
		if !ok {
			r.Err = errors.New("沒有結果")
		}
		out = append(out, batchResult{item: it, a: r.Assessment, err: r.Err})
		if r.Err != nil {
			continue
		}
		detail, _ := json.Marshal(r.Assessment)
		rec := store.Assessment{
			Key: "assess#" + it.Key, URL: it.URL, Title: r.Assessment.JobTitle, Company: r.Assessment.Company,
			Score: r.Assessment.Score, Verdict: r.Assessment.Verdict, Model: b.Model, AssessedAt: now, Detail: string(detail),
			JD: it.JD,
		}
		if err := d.Store.SaveAssessment(ctx, rec); err != nil {
			d.Log.Error("save assessment", "key", it.Key, "err", err)
		}
	}
	return out, total
}

// BuildBatchSummary renders one Telegram message for a batch (HTML parse
// mode): the jobs worth applying to in full, every other one on a line, the
// pages left out with why, and what the batch cost.
func BuildBatchSummary(b store.Batch, results []batchResult, u assess.Usage) string {
	e := html.EscapeString
	var ok, failed []batchResult
	for _, j := range results {
		if j.err != nil {
			failed = append(failed, j)
		} else {
			ok = append(ok, j)
		}
	}
	sort.SliceStable(ok, func(i, k int) bool { return ok[i].a.Score > ok[k].a.Score })
	byVerdict := map[string][]batchResult{}
	for _, j := range ok {
		byVerdict[j.a.Verdict] = append(byVerdict[j.a.Verdict], j)
	}

	var s strings.Builder
	// Jobs, not tabs: a job open in two tabs counts once.
	total := len(b.Items) + len(b.Excluded) + len(b.Skipped)
	fmt.Fprintf(&s, "<b>📦 批次評分完成：%d 個職缺</b>\nAI 評分 %d｜規則排除 %d｜略過 %d｜失敗 %d\n",
		total, len(ok), len(b.Excluded), len(b.Skipped), len(failed))
	if good := byVerdict["建議投"]; len(good) > 0 {
		fmt.Fprintf(&s, "\n<b>🟢 建議投：%d</b>\n", len(good))
		for _, j := range good {
			fmt.Fprintf(&s, "\n%s\n", BuildAssessment(j.item.URL, j.a))
		}
	}
	for _, v := range []struct{ verdict, head string }{{"可考慮", "🟡 可考慮"}, {"不建議", "🔴 不建議"}} {
		group := byVerdict[v.verdict]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&s, "\n<b>%s：%d</b>\n", v.head, len(group))
		for _, j := range group {
			fmt.Fprintf(&s, "[%d] <a href=\"%s\">%s</a>｜%s｜%s", j.a.Score, e(j.item.URL), e(titleOr(j.a.JobTitle, j.item.Title)),
				e(j.a.Company), e(truncate(j.a.Reason, 40)))
			if apply := ApplyURL(j.item.URL, j.a); apply != "" {
				fmt.Fprintf(&s, "｜<a href=\"%s\">帶入應徵</a>", e(apply))
			}
			s.WriteString("\n")
		}
	}
	for _, g := range []struct {
		head  string
		items []store.BatchItem
	}{{"🚫 規則排除（沒送 AI，請抽查）", b.Excluded}, {"⏭ 略過", b.Skipped}} {
		if len(g.items) == 0 {
			continue
		}
		fmt.Fprintf(&s, "\n<b>%s：%d</b>\n", g.head, len(g.items))
		for _, it := range g.items {
			fmt.Fprintf(&s, "<a href=\"%s\">%s</a>｜%s\n", e(it.URL), e(titleOr(it.Title, it.Key)), e(it.Reason))
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&s, "\n<b>⚠️ 失敗：%d</b>\n", len(failed))
		for _, j := range failed {
			fmt.Fprintf(&s, "<a href=\"%s\">%s</a>｜%s", e(j.item.URL), e(titleOr(j.item.Title, j.item.Key)), e(truncate(j.err.Error(), 80)))
			if j.fate != "" {
				fmt.Fprintf(&s, "｜%s", e(j.fate))
			}
			s.WriteString("\n")
		}
	}
	if u != (assess.Usage{}) {
		s.WriteString("\n" + usageLine(b.Model, u, true))
	}
	return strings.TrimRight(s.String(), "\n")
}

func titleOr(t, fallback string) string {
	if strings.TrimSpace(t) == "" {
		return fallback
	}
	return t
}

// Prices in US$ per million tokens: input, output, cache read, and cache
// write with the one-hour lifetime (twice the input price). A batch costs
// half of each. Check https://claude.com/pricing when adding a model.
var prices = map[string][4]float64{
	"claude-opus-5-5":   {4, 20, 0.20, 8},
	"claude-sonnet-5-5": {2, 10, 0.20, 4},
}

// cost is what the usage cost in US$, if the model's price is known.
func cost(model string, u assess.Usage, batch bool) (float64, bool) {
	p, ok := prices[model]
	if !ok {
		return 0, false
	}
	usd := (float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1] +
		float64(u.CacheReadTokens)*p[2] + float64(u.CacheWriteTokens)*p[3]) / 1e6
	if batch {
		usd /= 2
	}
	return usd, true
}

// usageLine is the last line of a reply: the tokens used and their cost.
func usageLine(model string, u assess.Usage, batch bool) string {
	line := fmt.Sprintf("💰 輸入 %d｜快取讀 %d｜快取寫 %d｜輸出 %d token", u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.OutputTokens)
	if usd, ok := cost(model, u, batch); ok {
		line += fmt.Sprintf("，約 US$%.3f", usd)
		if batch {
			line += "（批次半價）"
		}
	}
	return line
}
