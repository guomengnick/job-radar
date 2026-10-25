// Package app holds the radar's use cases — Scan, Digest, SyncBoards — wired
// from the lower packages. The CLI and the Lambda handler both call these, so
// a run that works on the laptop exercises the same logic as the cloud.
package app

import (
	"io"
	"log/slog"
	"time"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/ats"
	"github.com/guomengnick/job-radar/internal/fetch"
	"github.com/guomengnick/job-radar/internal/filter"
	"github.com/guomengnick/job-radar/internal/store"
)

type Deps struct {
	Cfg     *config.Config
	Store   store.Store
	Getter  ats.Getter
	Sources map[string]ats.Source
	Title   *filter.TitleFilter
	Loc     *filter.LocationClassifier
	Scorer  *filter.Scorer
	// Springboard judges Taichung office jobs from 104 alerts.
	Springboard *filter.Springboard
	Log         *slog.Logger
	Metrics     io.Writer // EMF lines; nil disables
	// Pages keeps the page of each board job the scan will notify, for the
	// model to judge after the digest; nil skips it.
	Pages PageArchive
	Now   func() time.Time
}

// NewDeps builds the standard dependency set around a store.
func NewDeps(cfg *config.Config, st store.Store, log *slog.Logger, metrics io.Writer) Deps {
	sources := ats.All()
	rps := map[string]float64{}
	allow := []string{"raw.githubusercontent.com"}
	for name, src := range sources {
		allow = append(allow, src.Host())
		if v, ok := cfg.Scan.RPS[name]; ok {
			rps[src.Host()] = v
		}
	}
	rps["raw.githubusercontent.com"] = 2
	client := fetch.New(fetch.Options{
		RPS: rps, Retries: 2, Timeout: 20 * time.Second, AllowHosts: allow,
	})
	return Deps{
		Cfg: cfg, Store: st, Getter: client, Sources: sources,
		Title:  filter.NewTitleFilter(cfg.Title.Positive, cfg.Title.Negative),
		Loc:    filter.NewLocationClassifier(cfg.Location),
		Scorer: filter.NewScorer(cfg.Skills),
		Springboard: filter.NewSpringboard(cfg.Springboard.TitlePlus, cfg.Springboard.TitleExclude,
			filter.SalaryFloor{Annual: cfg.Springboard.MinAnnualSalary, MonthlyTop: cfg.Springboard.MinMonthlyTopPay}),
		Log: log, Metrics: metrics, Now: func() time.Time { return time.Now().UTC() },
	}
}
