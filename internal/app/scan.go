package app

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/guomengnick/job-radar/internal/ats"
	"github.com/guomengnick/job-radar/internal/fetch"
	"github.com/guomengnick/job-radar/internal/filter"
	"github.com/guomengnick/job-radar/internal/metrics"
	"github.com/guomengnick/job-radar/internal/store"
)

type ScanOptions struct {
	ATS   string
	Limit int // 0 = every due board
	// Silent records new postings without queueing them for the digest,
	// e.g. the first run after a rule change.
	Silent bool
	// NoBaseline notifies postings found on a board's first successful fetch.
	// By default those are a baseline: they existed before the radar looked.
	NoBaseline bool
	// Deadline is when the caller must return (the Lambda deadline); zero = none.
	Deadline time.Time
}

// Scan checks the due boards of one ATS, oldest-checked first, and records
// what is new. Because the order is "least recently checked first", a run cut
// short by the deadline simply continues tomorrow — no checkpoint file needed.
func Scan(ctx context.Context, d Deps, o ScanOptions) (store.Run, error) {
	src, ok := d.Sources[o.ATS]
	if !ok {
		return store.Run{}, fmt.Errorf("unknown ATS %q", o.ATS)
	}
	now := d.Now()
	run := store.Run{ATS: o.ATS, Task: "scan", StartedAt: now, Silent: o.Silent,
		Outcomes: map[string]int{}, NewByClass: map[string]int{}}

	all, err := d.Store.ListBoards(ctx, o.ATS)
	if err != nil {
		return run, d.finish(ctx, &run, "failed", err)
	}
	if len(all) == 0 {
		return run, d.finish(ctx, &run, "failed", fmt.Errorf("no %s boards stored; run sync-boards first", o.ATS))
	}
	run.BoardsTotal = len(all)
	due := dueBoards(all, now, time.Duration(d.Cfg.Scan.DeadRecheckDays)*24*time.Hour, o.Limit)
	run.BoardsDue = len(due)

	var (
		mu      sync.Mutex
		touched []store.Board
		stopped bool
	)
	stopAt := time.Time{}
	if !o.Deadline.IsZero() {
		stopAt = o.Deadline.Add(-time.Duration(d.Cfg.Scan.DeadlineBufferSeconds) * time.Second)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(d.Cfg.Scan.Concurrency)
	for _, b := range due {
		if (!stopAt.IsZero() && d.Now().After(stopAt)) || ctx.Err() != nil {
			stopped = true
			break
		}
		g.Go(func() error {
			res := d.scanBoard(gctx, src, b, o)
			mu.Lock()
			defer mu.Unlock()
			touched = append(touched, res.board)
			run.BoardsChecked++
			run.Outcomes[string(res.outcome)]++
			run.JobsFetched += res.fetched
			run.JobsMatched += res.matched
			run.JobsNew += res.newJobs
			run.JobsPending += res.pending
			for c, n := range res.newByClass {
				run.NewByClass[c] += n
			}
			if res.newlyDead {
				run.NewlyDead++
			}
			if res.revived {
				run.Revived++
			}
			if res.storeErr != nil && run.Error == "" {
				run.Error = res.storeErr.Error()
			}
			return nil
		})
	}
	g.Wait()

	if err := d.Store.SaveBoards(ctx, touched); err != nil {
		return run, d.finish(ctx, &run, "failed", fmt.Errorf("save boards: %w", err))
	}
	status := "ok"
	switch {
	case stopped || (run.BoardsChecked > 0 && run.BoardsChecked < run.BoardsDue):
		status = "partial" // cut short by the deadline; the rest go first tomorrow
	case run.BoardsDue > 0 && run.BoardsChecked == 0:
		status = "failed"
	}
	return run, d.finish(ctx, &run, status, nil)
}

// dueBoards skips dead boards checked within recheck and orders the rest
// never-checked first, then oldest check first.
func dueBoards(all []store.Board, now time.Time, recheck time.Duration, limit int) []store.Board {
	var due []store.Board
	for _, b := range all {
		if b.Status == store.BoardDead && now.Sub(b.LastCheckedAt) < recheck {
			continue
		}
		due = append(due, b)
	}
	sort.SliceStable(due, func(i, j int) bool {
		if !due[i].LastCheckedAt.Equal(due[j].LastCheckedAt) {
			return due[i].LastCheckedAt.Before(due[j].LastCheckedAt)
		}
		return due[i].Slug < due[j].Slug
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due
}

type boardResult struct {
	board              store.Board
	outcome            fetch.Outcome
	fetched, matched   int
	newJobs, pending   int
	newByClass         map[string]int
	newlyDead, revived bool
	storeErr           error
}

func (d Deps) scanBoard(ctx context.Context, src ats.Source, b store.Board, o ScanOptions) boardResult {
	now := d.Now()
	jobs, err := src.Fetch(ctx, d.Getter, b.Slug)
	outcome := fetch.Classify(err)
	res := boardResult{outcome: outcome, newByClass: map[string]int{}}
	firstSuccess := b.LastOKAt.IsZero() || b.Status == store.BoardDead

	b.LastCheckedAt, b.LastOutcome = now, string(outcome)
	switch outcome {
	case fetch.OK:
		res.revived = b.Status == store.BoardDead
		b.Status, b.Consecutive404, b.LastOKAt, b.JobCount = store.BoardOK, 0, now, len(jobs)
	case fetch.NotFound:
		b.Consecutive404++
		if b.Consecutive404 >= d.Cfg.Scan.DeadAfter404 && b.Status != store.BoardDead {
			b.Status, res.newlyDead = store.BoardDead, true
		} else if b.Status != store.BoardDead {
			b.Status = store.BoardFailing
		}
	default:
		if b.Status != store.BoardDead {
			b.Status = store.BoardFailing
		}
		if err != nil {
			d.Log.Debug("board failed", "ats", b.ATS, "slug", b.Slug, "outcome", outcome, "err", err)
		}
	}
	res.board = b
	if outcome != fetch.OK {
		return res
	}

	baseline := firstSuccess && !o.NoBaseline
	for _, j := range jobs {
		res.fetched++
		if !d.Title.Match(j.Title) {
			continue
		}
		res.matched++
		v := d.Loc.Classify(j)
		score, skills := d.Scorer.Score(j.Title, j.Description)
		state := store.JobSeen
		switch {
		case o.Silent || baseline:
			state = store.JobSilent
		case filter.ShouldNotify(v, score, d.Cfg.Notify):
			state = store.JobNew
		}
		rec := store.JobRecord{
			Key: j.Key(), ATS: j.ATS, Board: j.Board, Title: j.Title, URL: j.URL,
			Location: j.Location, WorkMode: v.WorkMode, Class: string(v.Class), Reason: v.Reason,
			Score: score, Skills: skills, PostedAt: j.PostedAt, FirstSeen: now, State: state,
		}
		isNew, err := d.Store.InsertJobIfNew(ctx, rec)
		if err != nil {
			res.storeErr = err
			continue
		}
		if isNew {
			res.newJobs++
			res.newByClass[string(v.Class)]++
			if state == store.JobNew {
				res.pending++
			}
		}
	}
	return res
}

func (d Deps) finish(ctx context.Context, run *store.Run, status string, cause error) error {
	run.Status = status
	run.FinishedAt = d.Now()
	if cause != nil {
		run.Error = cause.Error()
	}
	if err := d.Store.SaveRun(ctx, *run); err != nil && cause == nil {
		cause = fmt.Errorf("save run: %w", err)
	}
	if d.Metrics != nil {
		metrics.EmitScan(d.Metrics, *run)
	}
	d.Log.Info("scan finished", "ats", run.ATS, "status", run.Status, "boards_due", run.BoardsDue,
		"boards_checked", run.BoardsChecked, "outcomes", run.Outcomes, "jobs_matched", run.JobsMatched,
		"jobs_new", run.JobsNew, "jobs_pending", run.JobsPending, "seconds", int(run.Duration().Seconds()))
	return cause
}
