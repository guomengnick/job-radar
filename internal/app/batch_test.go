package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/store"
)

// fakeBatches records what was submitted and answers with scripted results.
type fakeBatches struct {
	submitted []assess.BatchItem
	profile   string
	ended     bool
	results   []assess.Result
}

func (f *fakeBatches) Submit(_ context.Context, profile string, items []assess.BatchItem) (string, error) {
	f.profile, f.submitted = profile, items
	return "msgbatch_1", nil
}
func (f *fakeBatches) Ended(context.Context, string) (bool, error) { return f.ended, nil }
func (f *fakeBatches) Results(context.Context, string) ([]assess.Result, error) {
	return f.results, nil
}

type memArchive struct {
	names []string
	saved map[string]any
	fail  bool
}

// Load and List make it a PageSource too, reading back what Save kept.
func (m *memArchive) Load(_ context.Context, name string, v any) error {
	saved, ok := m.saved[name]
	if !ok {
		return errors.New("no such page")
	}
	b, _ := json.Marshal(saved)
	return json.Unmarshal(b, v)
}

func (m *memArchive) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for _, n := range m.names {
		if strings.HasPrefix(n, prefix) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *memArchive) Save(_ context.Context, name string, v any) error {
	if m.fail {
		return errors.New("s3 down")
	}
	m.names = append(m.names, name)
	if m.saved == nil {
		m.saved = map[string]any{}
	}
	m.saved[name] = v
	return nil
}

type sent struct{ msgs []string }

func (s *sent) Send(_ context.Context, msg string) error {
	s.msgs = append(s.msgs, msg)
	return nil
}

func page(code, title, text string) assess.Page {
	return assess.Page{URL: "https://www.104.com.tw/job/" + code, Title: title, Text: text}
}

func TestPageRules(t *testing.T) {
	r, err := ParsePageRules(`{"exclude_title_words":["業務","現場工程師"],"exclude_location_words":["后里"],"min_monthly_top_pay":70000}`)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		p    assess.Page
		want string
	}{
		{page("a", "資深後端工程師｜範例科技｜台中市", "上班地點\n台中市西屯區\n工作待遇\n月薪 60,000~90,000元"), ""},
		{page("b", "業務工程師｜範例｜台中市", ""), "職稱含「業務」"},
		{page("c", "後端工程師｜範例業務顧問有限公司｜台中市", ""), ""}, // the company is not the title
		{page("d", "軟體工程師｜範例", "上班地點：台中市后里區三豐路"), "地點含「后里」"},
		{page("e", "軟體工程師｜範例", "工作待遇\n\n月薪 35,000~45,000元"), "薪資低於門檻"},
		{page("f", "軟體工程師｜範例", "工作待遇\n待遇面議"), ""},
		{page("g", "軟體工程師", "沒有欄位的頁面"), ""},
	}
	for _, tt := range tests {
		if got := r.check(tt.p); got != tt.want {
			t.Errorf("%s %q: got %q, want %q", tt.p.URL, tt.p.Title, got, tt.want)
		}
	}
	if (PageRules{}).check(page("h", "x", "工作待遇\n時薪 200元")) != "" {
		t.Error("with no floor set, pay is not checked")
	}
	if _, err := ParsePageRules(`{"exclude_titles":["業務"]}`); err == nil {
		t.Error("a misspelled field must be an error, not a rule silently off")
	}
}

func TestSubmitAndCheckBatch(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#old01", Score: 64, AssessedAt: clk.t.Add(-24 * time.Hour)})
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#old02", Score: 30, AssessedAt: clk.t.Add(-20 * 24 * time.Hour)})
	rules := PageRules{ExcludeTitleWords: []string{"業務"}}
	pages := []assess.Page{
		page("aaa01", "資深後端工程師｜範例科技", "工作內容：後端 API"),
		page("bbb01", "業務專員｜範例", "賣軟體"),
		{URL: "https://www.104.com.tw/job/aaa01?jobsource=x", Title: "資深後端工程師", Text: "同一個職缺"},
		page("ccc01", "資料工程師｜範例", "  "),
		page("old01", "後端工程師｜舊的", "評過了"),
		page("old02", "平台工程師｜很久以前", "再評一次"),
	}
	fb := &fakeBatches{}
	tg := &sent{}
	arch := &memArchive{}
	r, err := SubmitPages(ctx, d, fb, tg, arch, rules, "我的評分檔案", pages)
	if err != nil {
		t.Fatal(err)
	}
	if r != (BatchReceipt{Received: 6, Submitted: 2, Excluded: 1, Skipped: 2, BatchID: "msgbatch_1"}) {
		t.Fatalf("receipt = %+v", r)
	}
	if len(fb.submitted) != 2 || fb.submitted[0].ID != "p0" || fb.submitted[1].Page.URL != "https://www.104.com.tw/job/old02" || fb.profile != "我的評分檔案" {
		t.Fatalf("submitted = %+v", fb.submitted)
	}
	// The batch as sent, then each page that went to the model on its own.
	if len(arch.names) != 3 || !strings.HasPrefix(arch.names[0], "batches/") ||
		arch.names[1] != "jd/104/aaa01.json" || arch.names[2] != "jd/104/old02.json" {
		t.Fatalf("archive %v", arch.names)
	}
	if p, ok := arch.saved["jd/104/aaa01.json"].(assess.Page); !ok || p.Text != "工作內容：後端 API" {
		t.Fatalf("kept page = %#v", arch.saved["jd/104/aaa01.json"])
	}
	if len(tg.msgs) != 1 || !strings.Contains(tg.msgs[0], "📥 收到 6 頁：送 AI 評分 2｜規則排除 1｜略過 2") {
		t.Fatalf("telegram %q", tg.msgs)
	}
	open, _ := st.OpenBatches(ctx)
	if len(open) != 1 || open[0].BatchID != "msgbatch_1" || open[0].Items[1].Key != "104#old02" || open[0].Skipped[1].Reason != "兩週內評過：64 分" {
		t.Fatalf("open = %+v", open)
	}

	// Not ended yet: nothing is sent, the batch stays open.
	if n, err := CheckBatches(ctx, d, fb, tg, open); n != 0 || err != nil || len(tg.msgs) != 1 {
		t.Fatalf("closed %d, err %v, sent %d", n, err, len(tg.msgs))
	}

	fb.ended = true
	good := assess.Assessment{JobTitle: "資深後端工程師", Company: "範例科技", Score: 82, Verdict: "建議投", Resume: "C", Reason: "技能對得上"}
	fb.results = []assess.Result{
		{ID: "p1", Err: errors.New("anthropic: prompt is too long")},
		{ID: "p0", Assessment: good, Usage: assess.Usage{InputTokens: 1000, OutputTokens: 2000, CacheReadTokens: 3000}},
	}
	clk.t = clk.t.Add(time.Hour)
	if n, err := CheckBatches(ctx, d, fb, tg, open); n != 1 || err != nil {
		t.Fatalf("closed %d, err %v", n, err)
	}
	msg := tg.msgs[1]
	for _, want := range []string{
		"<b>📦 批次評分完成：5 個職缺</b>\nAI 評分 1｜規則排除 1｜略過 2｜失敗 1",
		"<b>🟢 建議投：1</b>",
		`<a href="https://www.104.com.tw/job/aaa01">資深後端工程師</a>｜範例科技`,
		"<b>🚫 規則排除（沒送 AI，請抽查）：1</b>\n" + `<a href="https://www.104.com.tw/job/bbb01">業務專員</a>｜職稱含「業務」`,
		"<b>⏭ 略過：2</b>",
		"頁面沒有文字，可能還沒載完",
		"<b>⚠️ 失敗：1</b>\n" + `<a href="https://www.104.com.tw/job/old02">平台工程師</a>｜anthropic: prompt is too long`,
		"💰 輸入 1000｜快取讀 3000｜快取寫 0｜輸出 2000 token，約 US$0.022（批次半價）",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary missing %q:\n%s", want, msg)
		}
	}
	if a, ok := st.Assessment("assess#104#aaa01"); !ok || a.Score != 82 || a.Model != d.Cfg.Assess.Model || a.JD != "jd/104/aaa01.json" {
		t.Errorf("judgement stored: %+v %v", a, ok)
	}
	if open, _ := st.OpenBatches(ctx); len(open) != 0 {
		t.Error("the batch is closed after its summary")
	}
}

func TestBatchSummaryApplyLinks(t *testing.T) {
	b := store.Batch{Model: "claude-opus-5-5", Items: []store.BatchItem{{Key: "104#aaa01"}, {Key: "104#bbb01"}, {Key: "104#ccc01"}}}
	item := func(code string) store.BatchItem {
		return store.BatchItem{Key: "104#" + code, URL: "https://www.104.com.tw/job/" + code}
	}
	msg := BuildBatchSummary(b, []batchResult{
		{item: item("aaa01"), a: assess.Assessment{JobTitle: "後端", Score: 82, Verdict: "建議投", Resume: "A"}},
		{item: item("bbb01"), a: assess.Assessment{JobTitle: "平台", Score: 72, Verdict: "可考慮", Resume: "B", AddMLLine: true}},
		{item: item("ccc01"), a: assess.Assessment{JobTitle: "資料", Score: 54, Verdict: "不建議", Resume: "B"}},
	}, assess.Usage{})
	for _, want := range []string{
		`<a href="https://www.104.com.tw/job/aaa01?apply=form#radar-apply?cv=A">帶入應徵</a>`,
		`[72] <a href="https://www.104.com.tw/job/bbb01">平台</a>｜｜｜<a href="https://www.104.com.tw/job/bbb01?apply=form#radar-apply?cv=B%2BML">帶入應徵</a>` + "\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary missing %q:\n%s", want, msg)
		}
	}
	if !strings.HasSuffix(msg, `[54] <a href="https://www.104.com.tw/job/ccc01">資料</a>｜｜`) {
		t.Errorf("below the threshold, no link:\n%s", msg)
	}
}

func TestSubmitPagesAllLeftOut(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	fb, tg := &fakeBatches{}, &sent{}
	r, err := SubmitPages(ctx, d, fb, tg, &memArchive{}, PageRules{ExcludeTitleWords: []string{"業務"}}, "p",
		[]assess.Page{page("bbb01", "業務專員｜範例", "賣軟體")})
	if err != nil || r.Submitted != 0 || fb.submitted != nil {
		t.Fatalf("r=%+v err=%v submitted=%v", r, err, fb.submitted)
	}
	if len(tg.msgs) != 1 || !strings.Contains(tg.msgs[0], "規則排除（沒送 AI，請抽查）：1") {
		t.Fatalf("the summary goes out at once: %q", tg.msgs)
	}
	if open, _ := st.OpenBatches(ctx); len(open) != 0 {
		t.Error("nothing to wait for, nothing recorded")
	}
}

func TestCost(t *testing.T) {
	// Opus 5.5: 1M input $4, 1M output $20, 1M cache read $0.20, 1M one-hour write $8; a batch half.
	u := assess.Usage{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 1e6, CacheWriteTokens: 1e6}
	if usd, ok := cost("claude-opus-5-5", u, false); !ok || usd < 32.19 || usd > 32.21 {
		t.Errorf("single = %v %v", usd, ok)
	}
	if usd, ok := cost("claude-opus-5-5", u, true); !ok || usd < 16.09 || usd > 16.11 {
		t.Errorf("batch = %v %v", usd, ok)
	}
	if _, ok := cost("unknown", assess.Usage{}, false); ok {
		t.Error("an unknown model has no price")
	}
	if got := usageLine("unknown", assess.Usage{InputTokens: 5}, false); got != "💰 輸入 5｜快取讀 0｜快取寫 0｜輸出 0 token" {
		t.Errorf("no price, no cost: %q", got)
	}
}
