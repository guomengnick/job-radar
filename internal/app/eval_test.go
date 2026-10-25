package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/store"
)

func TestParseLabels(t *testing.T) {
	// A CSV saved by Excel starts with a byte order mark.
	in := "\ufeffkey,want,note\n" +
		"104#aaa01,投,自家產品\n" +
		"https://www.104.com.tw/job/bbb01?jobsource=match_mail,看,\n" +
		"104#ccc01,不\n" +
		"104#ddd01,72,差不多這個分數\n" +
		"104#eee01,,還沒標\n" +
		",,\n"
	got, err := ParseLabels(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []EvalCase{
		{Key: "104#aaa01", Want: "建議投", Note: "自家產品"},
		{Key: "104#bbb01", Want: "可考慮"},
		{Key: "104#ccc01", Want: "不建議"},
		{Key: "104#ddd01", Want: "72", Note: "差不多這個分數"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"key,want\n104#a,也許\n", "key,want\n104#a,120\n", "job,want\n104#a,投\n"} {
		if _, err := ParseLabels(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestAgrees(t *testing.T) {
	for _, c := range []struct {
		want    string
		score   int
		verdict string
		agree   bool
	}{
		{"建議投", 80, "建議投", true}, {"建議投", 70, "可考慮", false},
		{"72", 80, "建議投", true}, {"72", 83, "建議投", false}, {"72", 62, "可考慮", true},
	} {
		if got := (EvalCase{Want: c.want}).Agrees(c.score, c.verdict); got != c.agree {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestEvaluate(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	pages := &memArchive{}
	pages.Save(ctx, "jd/104/aaa01.json", assess.Page{URL: "https://www.104.com.tw/job/aaa01", Text: "後端 API"})
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#aaa01", Title: "資深後端工程師", Score: 60, Verdict: "可考慮", AssessedAt: clk.t, JD: "jd/104/aaa01.json"})
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#old01", Score: 40, Verdict: "不建議", AssessedAt: clk.t.Add(-time.Hour)})
	a := &fakeAssessor{out: judged} // 82 建議投
	rows := Evaluate(ctx, d, a, pages, "草稿評分檔案", []EvalCase{
		{Key: "104#aaa01", Want: "建議投"}, {Key: "104#old01", Want: "不建議"}, {Key: "104#none1", Want: "可考慮"},
	})
	if a.profile != "草稿評分檔案" || a.page.Text != "後端 API" {
		t.Errorf("judged %q with %q", a.page.Text, a.profile)
	}
	if rows[0].Err != nil || rows[0].OldScore != 60 || rows[0].NewScore != 82 ||
		rows[1].Err == nil || !strings.Contains(rows[1].Err.Error(), "backfill-jd") || rows[2].Err == nil {
		t.Fatalf("rows = %+v", rows)
	}
	report := BuildEvalReport(rows, d.Cfg.Assess.Model)
	for _, want := range []string{
		"✅ 建議投       60 可考慮 →  82 建議投  資深後端工程師",
		"⚠️  不建議      104#old01：沒有存 JD，先跑 radar backfill-jd",
		"跟你的判斷一致：原本 0/1 → 這次 1/1",
		"輸入 3000",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestBackfillJD(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	pages := &memArchive{}
	pages.Save(ctx, "batches/2026-10-22T16-32-29Z.json", []assess.Page{
		{URL: "https://www.104.com.tw/job/aaa01?jobsource=x", Title: "舊", Text: "舊版內容"},
		{URL: "https://www.104.com.tw/job/bbb01", Text: "有了"},
	})
	pages.Save(ctx, "batches/2026-10-23T09-00-00Z.json", []assess.Page{
		{URL: "https://www.104.com.tw/job/aaa01", Title: "新", Text: "新版內容"},
		{URL: "https://www.104.com.tw/job/ccc01", Text: "沒評過分"},
		{URL: "https://www.104.com.tw/job/ddd01", Text: ""},
	})
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#aaa01", Score: 64, AssessedAt: clk.t})
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#bbb01", Score: 50, AssessedAt: clk.t, JD: "jd/104/bbb01.json"})
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#ddd01", Score: 30, AssessedAt: clk.t})

	n, err := BackfillJD(ctx, d, pages, pages)
	if err != nil || n != 1 {
		t.Fatalf("backfilled %d, %v", n, err)
	}
	rec, _ := st.Assessment("assess#104#aaa01")
	var p assess.Page
	if rec.JD != "jd/104/aaa01.json" || pages.Load(ctx, rec.JD, &p) != nil || p.Text != "新版內容" || rec.Score != 64 {
		t.Errorf("record %+v, page %+v; want the newest copy and the score kept", rec, p)
	}
	if rec, _ := st.Assessment("assess#104#ddd01"); rec.JD != "" {
		t.Error("an empty page is not a copy of what was judged")
	}
	if n, _ := BackfillJD(ctx, d, pages, pages); n != 0 {
		t.Errorf("a second run backfilled %d", n)
	}
}
