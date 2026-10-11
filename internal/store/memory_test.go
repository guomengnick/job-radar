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
