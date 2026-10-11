package app

import (
	"context"
	"fmt"

	"github.com/guomengnick/job-radar/internal/boards"
)

// SyncBoards adds slugs from the public dataset (or a local file) plus the
// config's extra boards. Existing boards keep their state.
func SyncBoards(ctx context.Context, d Deps, atsName, fromFile string) (added, total int, err error) {
	if _, ok := d.Sources[atsName]; !ok {
		return 0, 0, fmt.Errorf("unknown ATS %q", atsName)
	}
	var slugs []string
	if fromFile != "" {
		slugs, err = boards.ReadSlugsFile(fromFile)
	} else {
		slugs, err = boards.FetchSlugs(ctx, d.Getter, d.Cfg.Boards.DatasetBase, atsName)
	}
	if err != nil {
		return 0, 0, err
	}
	extra := make([]any, 0)
	for _, s := range d.Cfg.Boards.Extra[atsName] {
		extra = append(extra, s)
	}
	slugs = append(slugs, boards.Clean(extra)...)
	added, err = d.Store.AddBoards(ctx, atsName, slugs)
	if err != nil {
		return 0, 0, err
	}
	d.Log.Info("boards synced", "ats", atsName, "listed", len(slugs), "added", added)
	return added, len(slugs), nil
}
