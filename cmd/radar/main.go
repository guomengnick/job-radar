// Command radar runs the job radar from a laptop: the same use cases the
// Lambda runs, against a local JSON state file or the real DynamoDB tables.
//
//	radar sync-boards --ats greenhouse [--from-file path]
//	radar scan --ats greenhouse [--limit 200] [--silent] [--no-baseline]
//	radar digest [--dry-run]
//	radar ingest-file message.eml ...
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/app"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

const usage = `usage: radar <command> [flags]

commands:
  sync-boards  add boards from the public dataset (or --from-file)
  scan         check due boards of one ATS and record new postings
  digest       send the daily summary (Telegram if TELEGRAM_BOT_TOKEN and
               TELEGRAM_CHAT_ID are set, otherwise print it)
  ingest-file  read saved .eml files the way the ingest Lambda reads stored
               mail (keep real messages outside the repository)

common flags:
  --state path   local state file (default .radar/state.json)
  --store s      "file" (default) or "dynamo" (uses TABLE_BOARDS/TABLE_JOBS/TABLE_RUNS)
  --config path  rules file (default: the embedded config/radar.yml)
  -v             debug logging`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	statePath := fs.String("state", ".radar/state.json", "local state file")
	storeKind := fs.String("store", "file", "file | dynamo")
	cfgPath := fs.String("config", "", "rules file")
	verbose := fs.Bool("v", false, "debug logging")
	atsName := fs.String("ats", "", "greenhouse | lever | ashby (scan, sync-boards); all if empty")
	limit := fs.Int("limit", 0, "scan: max boards this run (0 = all due)")
	silent := fs.Bool("silent", false, "scan: record new postings without notifying")
	noBaseline := fs.Bool("no-baseline", false, "scan: notify postings found on a board's first fetch")
	fromFile := fs.String("from-file", "", "sync-boards: read slugs from a local JSON array")
	dryRun := fs.Bool("dry-run", false, "digest: print only, keep jobs pending")
	fs.Parse(os.Args[2:])

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, log, cmd, fs.Args(), *statePath, *storeKind, *cfgPath, *atsName, *limit, *silent, *noBaseline, *fromFile, *dryRun); err != nil {
		log.Error(cmd+" failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, cmd string, args []string, statePath, storeKind, cfgPath, atsName string,
	limit int, silent, noBaseline bool, fromFile string, dryRun bool) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	st, flush, err := openStore(ctx, storeKind, statePath)
	if err != nil {
		return err
	}
	defer flush()
	d := app.NewDeps(cfg, st, log, nil)
	names := []string{"greenhouse", "lever", "ashby"}
	if atsName != "" {
		names = strings.Split(atsName, ",")
	}

	switch cmd {
	case "sync-boards":
		for _, a := range names {
			added, total, err := app.SyncBoards(ctx, d, a, fromFile)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %d boards listed, %d new\n", a, total, added)
		}
	case "scan":
		for _, a := range names {
			r, err := app.Scan(ctx, d, app.ScanOptions{ATS: a, Limit: limit, Silent: silent, NoBaseline: noBaseline})
			if err := flush(); err != nil {
				return err
			}
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s in %s — boards %d/%d checked, outcomes %v, matched %d, new %d, to notify %d\n",
				a, r.Status, r.Duration().Round(time.Second), r.BoardsChecked, r.BoardsDue, r.Outcomes,
				r.JobsMatched, r.JobsNew, r.JobsPending)
		}
	case "digest":
		var n notify.Notifier = notify.Writer{W: os.Stdout}
		if tok, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID"); tok != "" && chat != "" {
			n = notify.Telegram{Token: tok, ChatID: chat}
		}
		taipei, _ := time.LoadLocation("Asia/Taipei")
		_, err := app.Digest(ctx, d, n, app.DigestOptions{ExpectedATS: names, DryRun: dryRun, Location: taipei})
		return err
	case "ingest-file":
		if len(args) == 0 {
			return fmt.Errorf("ingest-file needs at least one .eml path")
		}
		for _, path := range args {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			o, err := app.IngestMail(ctx, d, path, f)
			f.Close()
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s (%s), sender %s\n", path, o.Status, o.Reason, o.Sender)
		}
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
	return nil
}

func openStore(ctx context.Context, kind, path string) (store.Store, func() error, error) {
	switch kind {
	case "file":
		m, err := store.OpenFile(path)
		if err != nil {
			return nil, nil, err
		}
		return m, m.Flush, nil
	case "dynamo":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, nil, err
		}
		st := store.NewDynamo(dynamodb.NewFromConfig(awsCfg),
			os.Getenv("TABLE_BOARDS"), os.Getenv("TABLE_JOBS"), os.Getenv("TABLE_RUNS"))
		return st, func() error { return nil }, nil
	}
	return nil, nil, fmt.Errorf("unknown store %q", kind)
}
