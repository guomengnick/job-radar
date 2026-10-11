package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/ats"
	"github.com/guomengnick/job-radar/internal/fetch"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// fakeSource returns scripted jobs or errors per board slug.
type fakeSource struct {
	mu    sync.Mutex
	jobs  map[string][]ats.Job
	errs  map[string]error
	calls map[string]int
}

func (f *fakeSource) Name() string                { return "fake" }
func (f *fakeSource) Host() string                { return "fake.test" }
func (f *fakeSource) BoardURL(slug string) string { return "https://fake.test/" + slug }
func (f *fakeSource) Fetch(_ context.Context, _ ats.Getter, slug string) ([]ats.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[slug]++
	if err := f.errs[slug]; err != nil {
		return nil, err
	}
	return f.jobs[slug], nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T) (Deps, *fakeSource, *store.Memory, *clock) {
	t.Helper()
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory()
	src := &fakeSource{jobs: map[string][]ats.Job{}, errs: map[string]error{}, calls: map[string]int{}}
	clk := &clock{t: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}
	d := NewDeps(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	d.Sources = map[string]ats.Source{"fake": src}
	d.Now = clk.now
	return d, src, st, clk
}

func job(board, id, title, loc, desc string) ats.Job {
	return ats.Job{ATS: "fake", Board: board, ID: id, Title: title, URL: "https://x/" + id, Location: loc, Description: desc}
}

const strongJD = "Build event-driven data pipelines with Kafka and PostgreSQL in C# / .NET for factory equipment."

func TestScanBaselineThenNewJob(t *testing.T) {
	ctx := context.Background()
	d, src, st, clk := setup(t)
	st.AddBoards(ctx, "fake", []string{"acme"})
	src.jobs["acme"] = []ats.Job{job("acme", "1", "Senior Backend Engineer", "Remote, Global", strongJD)}

	run, err := Scan(ctx, d, ScanOptions{ATS: "fake"})
	if err != nil || run.Status != "ok" {
		t.Fatalf("first scan: %v %+v", err, run)
	}
	if run.JobsNew != 1 || run.JobsPending != 0 {
		t.Fatalf("first fetch of a board is a baseline: new=%d pending=%d", run.JobsNew, run.JobsPending)
	}

	clk.t = clk.t.Add(24 * time.Hour)
	src.jobs["acme"] = append(src.jobs["acme"],
		job("acme", "2", "Senior Backend Engineer", "Remote, Global", strongJD),
		job("acme", "3", "Senior Backend Engineer", "Remote - United States", strongJD),
		job("acme", "4", "Account Executive", "Remote, Global", strongJD))
	run, _ = Scan(ctx, d, ScanOptions{ATS: "fake"})
	if run.JobsFetched != 4 || run.JobsMatched != 3 || run.JobsNew != 2 || run.JobsPending != 1 {
		t.Fatalf("second scan: %+v", run)
	}
	if run.NewByClass["A"] != 1 || run.NewByClass["C"] != 1 {
		t.Fatalf("classes: %v", run.NewByClass)
	}

	var out bytes.Buffer
	msg, err := Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{ExpectedATS: []string{"fake", "lever"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✅ fake", "⚠️ lever", "值得看的新職缺：1 筆", "https://x/2", "[A "} {
		if !strings.Contains(msg, want) {
			t.Errorf("digest missing %q:\n%s", want, msg)
		}
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 0 {
		t.Fatal("digest should mark sent jobs notified")
	}
}

func TestDeadBoardLifecycle(t *testing.T) {
	ctx := context.Background()
	d, src, st, clk := setup(t)
	st.AddBoards(ctx, "fake", []string{"gone"})
	src.errs["gone"] = &fetch.Error{Outcome: fetch.NotFound, Status: 404}

	for i := 0; i < 3; i++ {
		run, _ := Scan(ctx, d, ScanOptions{ATS: "fake"})
		if run.Outcomes["not_found"] != 1 {
			t.Fatalf("day %d outcomes %v", i, run.Outcomes)
		}
		clk.t = clk.t.Add(24 * time.Hour)
	}
	boards, _ := st.ListBoards(ctx, "fake")
	if boards[0].Status != store.BoardDead {
		t.Fatalf("after 3 404s status = %s", boards[0].Status)
	}
	run, _ := Scan(ctx, d, ScanOptions{ATS: "fake"})
	if run.BoardsDue != 0 || src.calls["gone"] != 3 {
		t.Fatalf("dead board should be skipped: due=%d calls=%d", run.BoardsDue, src.calls["gone"])
	}
	clk.t = clk.t.Add(8 * 24 * time.Hour)
	delete(src.errs, "gone")
	run, _ = Scan(ctx, d, ScanOptions{ATS: "fake"})
	if run.Revived != 1 {
		t.Fatalf("weekly recheck should revive: %+v", run)
	}
}

func TestDeadlineMakesRunPartial(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	st.AddBoards(ctx, "fake", []string{"a", "b"})
	run, _ := Scan(ctx, d, ScanOptions{ATS: "fake", Deadline: clk.t.Add(30 * time.Second)})
	if run.Status != "partial" || run.BoardsChecked != 0 {
		t.Fatalf("inside the buffer nothing should start: %+v", run)
	}
}

func TestDueOrderOldestFirst(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	got := dueBoards([]store.Board{
		{Slug: "recent", LastCheckedAt: now.Add(-time.Hour)},
		{Slug: "never"},
		{Slug: "old", LastCheckedAt: now.Add(-48 * time.Hour)},
		{Slug: "dead", Status: store.BoardDead, LastCheckedAt: now.Add(-24 * time.Hour)},
	}, now, 7*24*time.Hour, 0)
	var slugs []string
	for _, b := range got {
		slugs = append(slugs, b.Slug)
	}
	if strings.Join(slugs, ",") != "never,old,recent" {
		t.Fatalf("order = %v", slugs)
	}
}
