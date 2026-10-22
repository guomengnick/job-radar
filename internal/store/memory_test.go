package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMemoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	m, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := m.AddBoards(ctx, "lever", []string{"a", "b", "a"}); n != 2 {
		t.Fatalf("added %d, want 2", n)
	}
	if n, _ := m.AddBoards(ctx, "lever", []string{"b", "c"}); n != 1 {
		t.Fatalf("second add %d, want 1", n)
	}
	now := time.Now().UTC()
	isNew, _ := m.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#1", State: JobNew, FirstSeen: now})
	again, _ := m.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#1", State: JobNew, FirstSeen: now})
	if !isNew || again {
		t.Fatalf("insert new=%v again=%v", isNew, again)
	}
	m.InsertJobIfNew(ctx, JobRecord{Key: "lever#a#2", State: JobSeen, FirstSeen: now})
	m.SaveRun(ctx, Run{ATS: "lever", Task: "scan", StartedAt: now})
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}

	m2, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boards, _ := m2.ListBoards(ctx, "lever")
	pending, _ := m2.PendingJobs(ctx)
	runs, _ := m2.RunsSince(ctx, now.Add(-time.Minute))
	if len(boards) != 3 || len(pending) != 1 || len(runs) != 1 {
		t.Fatalf("after reload: boards=%d pending=%d runs=%d", len(boards), len(pending), len(runs))
	}
	m2.MarkNotified(ctx, []string{"lever#a#1"}, now)
	if p, _ := m2.PendingJobs(ctx); len(p) != 0 {
		t.Fatal("notified job still pending")
	}
}

func TestMemorySaveAssessment(t *testing.T) {
	m := NewMemory()
	for _, score := range []int{40, 82} {
		if err := m.SaveAssessment(context.Background(), Assessment{Key: "assess#x", Score: score}); err != nil {
			t.Fatal(err)
		}
	}
	if a, ok := m.Assessment("assess#x"); !ok || a.Score != 82 {
		t.Errorf("a later judgement replaces the earlier one: %+v %v", a, ok)
	}
}

func TestMemoryJobsSince(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	at := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for key, j := range map[string]JobRecord{
		"104#old":   {ATS: "104", FirstSeen: at.Add(-time.Second)},
		"104#later": {ATS: "104", FirstSeen: at.Add(time.Hour), State: JobSeen},
		"104#first": {ATS: "104", FirstSeen: at, State: JobNew},
		"lever#a#1": {ATS: "lever", FirstSeen: at},
	} {
		j.Key = key
		m.InsertJobIfNew(ctx, j)
	}
	jobs, err := m.JobsSince(ctx, "104", at)
	if err != nil || len(jobs) != 2 || jobs[0].Key != "104#first" || jobs[1].Key != "104#later" {
		t.Fatalf("want the two 104 jobs from the bound on, oldest first: %+v %v", jobs, err)
	}
}

func TestMemoryBatches(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	at := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	m.SaveBatch(ctx, Batch{Key: "batch#2", Pending: BatchPending, SubmittedAt: at.Add(time.Minute)})
	m.SaveBatch(ctx, Batch{Key: "batch#1", Pending: BatchPending, SubmittedAt: at})
	m.CloseBatch(ctx, "batch#2", at)
	open, _ := m.OpenBatches(ctx)
	if len(open) != 1 || open[0].Key != "batch#1" {
		t.Fatalf("open = %+v", open)
	}
}
