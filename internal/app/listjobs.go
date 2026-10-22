package app

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/guomengnick/job-radar/internal/store"
)

// ListedJob is one 104 alert job as the browser extension shows it before
// opening the pages.
type ListedJob struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Company  string   `json:"company"`
	Location string   `json:"location"`
	Salary   string   `json:"salary,omitempty"`
	Class    string   `json:"class"`
	Score    int      `json:"score"`
	Group    string   `json:"group"`  // see the Group constants
	Reason   string   `json:"reason"` // why it is in its group, in words
	Notes    []string `json:"notes,omitempty"`
}

// The groups of 104 alert jobs, in the order the extension opens them.
const (
	// GroupDigest jobs are the ones the digest lists: remote, or in
	// Taichung, and passed the title and pay rules.
	GroupDigest = "digest"
	// GroupElsewhere jobs are 104's own suggestions outside the saved
	// searches: neither remote nor in Taichung.
	GroupElsewhere = "elsewhere"
	// GroupExcluded jobs failed a title or pay rule.
	GroupExcluded = "excluded"
)

var groupOrder = map[string]int{GroupDigest: 0, GroupElsewhere: 1, GroupExcluded: 2}

// MaxListWindow is how far back the extension may ask for jobs.
const MaxListWindow = 14 * 24 * time.Hour

// ErrBadSince means the requested start time is in the future or too old.
var ErrBadSince = errors.New("since must be within the last 14 days")

// ListAlertJobs returns every 104 alert job first seen since the given time,
// for the extension to open. Nothing is left out: each job carries its group
// and the reason, and the user decides which groups to open. Jobs are ordered
// by group, then class and score, as the digest orders them.
func ListAlertJobs(ctx context.Context, d Deps, since time.Time) ([]ListedJob, error) {
	now := d.Now()
	if since.After(now) || since.Before(now.Add(-MaxListWindow)) {
		return nil, ErrBadSince
	}
	recs, err := d.Store.JobsSince(ctx, alertATS, since)
	if err != nil {
		return nil, err
	}
	out := make([]ListedJob, 0, len(recs))
	for _, r := range recs {
		out = append(out, ListedJob{
			URL: r.URL, Title: r.Title, Company: r.Board, Location: r.Location, Salary: r.Salary,
			Class: r.Class, Score: r.Score, Group: alertGroup(r), Reason: reasonText(r.Reason), Notes: r.Notes,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if groupOrder[a.Group] != groupOrder[b.Group] {
			return groupOrder[a.Group] < groupOrder[b.Group]
		}
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		return a.Score > b.Score
	})
	return out, nil
}

// alertGroup reads a stored 104 job's group from what alertJob decided: a
// job the digest sends is pending or already sent; of the rest, a job that
// passed the rules was only outside the searched places.
func alertGroup(r store.JobRecord) string {
	switch {
	case r.State == store.JobNew || r.State == store.JobNotified:
		return GroupDigest
	case r.Reason == "outside_taichung":
		return GroupElsewhere
	}
	return GroupExcluded
}
