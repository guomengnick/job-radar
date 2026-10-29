package app

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/ats"
	"github.com/guomengnick/job-radar/internal/store"
)

func TestScanKeepsThePagesItWillNotify(t *testing.T) {
	ctx := context.Background()
	d, src, st, clk := setup(t)
	kept := &memArchive{}
	d.Pages = kept
	st.AddBoards(ctx, "fake", []string{"acme"})
	src.jobs["acme"] = []ats.Job{job("acme", "1", "Senior Backend Engineer", "Remote, Global", strongJD)}
	Scan(ctx, d, ScanOptions{ATS: "fake"}) // the baseline: nothing to notify, nothing kept
	if len(kept.names) != 0 {
		t.Fatalf("baseline kept %v", kept.names)
	}
	clk.t = clk.t.Add(24 * time.Hour)
	src.jobs["acme"] = append(src.jobs["acme"],
		job("acme", "2", "Senior Backend Engineer", "Remote, Global", strongJD),
		job("acme", "3", "Senior Backend Engineer", "Remote - United States", strongJD))
	Scan(ctx, d, ScanOptions{ATS: "fake"})
	u, _ := url.Parse("https://x/2")
	var p assess.Page
	if len(kept.names) != 1 || kept.Load(ctx, jdName(clipKey(u)), &p) != nil {
		t.Fatalf("kept %v; want only the job to notify, under its URL's name", kept.names)
	}
	if p.URL != "https://x/2" || p.Title != "Senior Backend Engineer｜acme" ||
		!strings.Contains(p.Text, "地點：Remote, Global") || !strings.Contains(p.Text, strongJD) {
		t.Errorf("page = %+v", p)
	}
}

func TestJudgeBoardJobs(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	kept := &memArchive{}
	add := func(ats, id, class string, state store.JobState, score int, seen time.Time, page bool) {
		u := "https://jobs.example.com/" + ats + "/" + id
		st.InsertJobIfNew(ctx, store.JobRecord{Key: ats + "#acme#" + id, ATS: ats, URL: u, Title: id,
			Class: class, State: state, Score: score, FirstSeen: seen})
		if page {
			pu, _ := url.Parse(u)
			kept.Save(ctx, jdName(clipKey(pu)), assess.Page{URL: u, Title: id, Text: "JD " + id})
		}
	}
	now, old := clk.t, clk.t.Add(-48*time.Hour)
	add("greenhouse", "a50", "A", store.JobNew, 50, now, true)
	add("greenhouse", "a70", "A", store.JobNotified, 70, now, true)
	add("lever", "a60", "A", store.JobNew, 60, now, true)
	add("ashby", "a90-nopage", "A", store.JobNew, 90, now, false)
	add("greenhouse", "b80", "B", store.JobNew, 80, now, true)
	add("greenhouse", "a85-seen", "A", store.JobSeen, 85, now, true)
	add("lever", "a95-old", "A", store.JobNotified, 95, old, true)

	fb, tg := &fakeBatches{}, &sent{}
	r, err := JudgeBoardJobs(ctx, d, fb, tg, kept, Profile{Text: "評分檔案"}, now.Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range fb.submitted {
		got = append(got, it.Page.Title)
	}
	// Class A, new or notified, seen in the window, with a kept page; best first, two of them.
	if strings.Join(got, ",") != "a70,a60" || r.Submitted != 2 || fb.profile != "評分檔案" || len(tg.msgs) != 1 ||
		tg.msgs[0] != "🌏 國外職缺：今天新的 A 類 4 個，送 AI 評分 2 個（每天上限 2）。總表最久 24 小時內傳來。" {
		t.Fatalf("submitted %v, receipt %+v, telegram %q", got, r, tg.msgs)
	}

	fb2, tg2 := &fakeBatches{}, &sent{}
	if r, err := JudgeBoardJobs(ctx, d, fb2, tg2, kept, Profile{Text: "p"}, now.Add(time.Minute), 10); err != nil || r.Submitted != 0 || len(tg2.msgs) != 0 {
		t.Errorf("nothing new: receipt %+v, err %v, telegram %q", r, err, tg2.msgs)
	}
}
