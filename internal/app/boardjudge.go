package app

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/filter"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// boardATS are the job boards the scan reads, whose postings it keeps.
var boardATS = []string{"greenhouse", "lever", "ashby"}

// KeptPages is where the scan kept the board pages and where a batch keeps
// what it judged.
type KeptPages interface {
	PageSource
	PageArchive
}

// JudgeBoardJobs sends the class A board jobs (remote, open to Taiwan)
// first seen since the given time to the model as one batch: the best
// keyword scores first, at most max. Each is judged from the page the scan
// kept; a job without one is left out. A job judged in the last two weeks
// is skipped as in any batch, and the summary arrives the same way. With
// nothing to judge, nothing is sent.
func JudgeBoardJobs(ctx context.Context, d Deps, a BatchAssessor, n notify.Notifier, kept KeptPages,
	profile Profile, since time.Time, max int) (BatchReceipt, error) {
	var picked []store.JobRecord
	for _, ats := range boardATS {
		recs, err := d.Store.JobsSince(ctx, ats, since)
		if err != nil {
			return BatchReceipt{}, err
		}
		for _, r := range recs {
			if r.Class == string(filter.ClassA) && (r.State == store.JobNew || r.State == store.JobNotified) {
				picked = append(picked, r)
			}
		}
	}
	sort.SliceStable(picked, func(i, k int) bool { return picked[i].Score > picked[k].Score })
	var pages []assess.Page
	for _, r := range picked {
		if len(pages) == max {
			break
		}
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" {
			continue
		}
		var p assess.Page
		if err := kept.Load(ctx, jdName(clipKey(u)), &p); err != nil {
			d.Log.Warn("board job has no kept page", "key", r.Key, "err", err)
			continue
		}
		pages = append(pages, p)
	}
	d.Log.Info("board jobs to judge", "class_a", len(picked), "sent", len(pages), "max", max)
	if len(pages) == 0 {
		return BatchReceipt{}, nil
	}
	r, err := SubmitPages(ctx, d, a, n, kept, PageRules{}, profile, pages)
	if err == nil && r.BatchID != "" {
		if err := n.Send(ctx, BuildBoardReceipt(len(picked), r.Submitted, max)); err != nil {
			d.Log.Error("send board receipt", "err", err)
		}
	}
	return r, err
}

// BuildBoardReceipt tells the user the day's board jobs went to the model.
func BuildBoardReceipt(classA, sent, max int) string {
	return fmt.Sprintf("🌏 國外職缺：今天新的 A 類 %d 個，送 AI 評分 %d 個（每天上限 %d）。總表最久 24 小時內傳來。", classA, sent, max)
}
