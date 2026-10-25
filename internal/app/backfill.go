package app

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/guomengnick/job-radar/internal/assess"
)

// BackfillJD gives the judgements made before pages were kept on their own
// (2026-10-25) the page they judged, taken from the batches still archived
// under batches/ (30 days). A job in several batches gets its newest copy.
// A job judged only by a single 送到雷達 has no copy anywhere and is left
// as it is. It returns how many judgements now point at a page.
func BackfillJD(ctx context.Context, d Deps, pages PageSource, archive PageArchive) (int, error) {
	names, err := pages.List(ctx, "batches/")
	if err != nil {
		return 0, err
	}
	latest := map[string]assess.Page{}
	for _, name := range names { // oldest first, so a newer copy wins
		var batch []assess.Page
		if err := pages.Load(ctx, name, &batch); err != nil {
			return 0, fmt.Errorf("read %s: %w", name, err)
		}
		for _, p := range batch {
			u, err := url.Parse(p.URL)
			if err != nil || u.Host == "" || strings.TrimSpace(p.Text) == "" {
				continue
			}
			latest[clipKey(u)] = p
		}
	}
	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for _, key := range keys {
		rec, ok, err := d.Store.AssessmentOf(ctx, "assess#"+key)
		if err != nil {
			return n, err
		}
		if !ok || rec.JD != "" {
			continue
		}
		rec.JD = jdName(key)
		if err := archive.Save(ctx, rec.JD, latest[key]); err != nil {
			return n, err
		}
		if err := d.Store.SaveAssessment(ctx, rec); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
