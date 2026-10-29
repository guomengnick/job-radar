// Command radar runs the job radar from a laptop: the same use cases the
// Lambda runs, against a local JSON state file or the real DynamoDB tables.
//
//	radar sync-boards --ats greenhouse [--from-file path]
//	radar scan --ats greenhouse [--limit 200] [--silent] [--no-baseline]
//	radar digest [--dry-run]
//	radar ingest-file message.eml ...
//	radar eval --labels labels.csv|telegram [--profile draft.md]
//	radar backfill-jd
//	radar labels > labels.csv
//	radar dlq
//	radar redrive
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/app"
	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/dlq"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/pages"
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
  eval         judge the kept pages of the jobs in --labels again, with the
               profile in SSM or a draft (--profile), and compare with your
               labels (CSV: key,want,note; want is 投/看/不 or a score), or
               with the Telegram buttons you pressed (--labels telegram).
               Each job is one model call, about US$0.03.
  backfill-jd  point judgements made before pages were kept at their page,
               found in the batches still archived (30 days)
  labels       print a labels file for eval: the jobs judged from the
               archived batches, best first, with want left for you to fill
  dlq          list what is in the dead-letter queue
  redrive      send the dead letters again, after a yes: failed runs are
               started again, judgements go out as one new batch

common flags:
  --state path   local state file (default .radar/state.json)
  --store s      "file" (default) or "dynamo" (TABLE_BOARDS/TABLE_JOBS/TABLE_RUNS,
                 default job-radar-*); eval and backfill-jd always use dynamo
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
	labels := fs.String("labels", "", "eval: your labels, a CSV with key,want,note")
	profile := fs.String("profile", "", "eval: a draft profile to test instead of the one in SSM")
	fs.Parse(os.Args[2:])

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if cmd == "eval" || cmd == "backfill-jd" || cmd == "labels" || cmd == "dlq" || cmd == "redrive" {
		*storeKind = "dynamo"
	}
	if err := run(ctx, log, cmd, fs.Args(), *statePath, *storeKind, *cfgPath, *atsName, *limit, *silent, *noBaseline, *fromFile, *dryRun, *labels, *profile); err != nil {
		log.Error(cmd+" failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, cmd string, args []string, statePath, storeKind, cfgPath, atsName string,
	limit int, silent, noBaseline bool, fromFile string, dryRun bool, labels, profilePath string) error {
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
			fmt.Printf("%s: %s (%s), sender %s, jobs %d, new %d, to send %d\n",
				path, o.Status, o.Reason, o.Sender, o.Jobs, o.New, o.Pending)
		}
	case "eval":
		return evaluate(ctx, d, labels, profilePath)
	case "dlq", "redrive":
		return deadLetters(ctx, d, cmd == "redrive")
	case "labels":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return err
		}
		kept, err := pages.ForAccount(ctx, awsCfg)
		if err != nil {
			return err
		}
		rows, err := app.LabelRows(ctx, d, kept)
		if err != nil {
			return err
		}
		w := csv.NewWriter(os.Stdout)
		w.WriteAll(rows)
		return w.Error()
	case "backfill-jd":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return err
		}
		kept, err := pages.ForAccount(ctx, awsCfg)
		if err != nil {
			return err
		}
		n, err := app.BackfillJD(ctx, d, kept, kept)
		fmt.Printf("%d judgements now point at their page in %s\n", n, kept.Bucket)
		return err
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
	return nil
}

// evaluate runs radar eval. The API key and, unless a draft is given, the
// profile are read from SSM into memory, never into the environment.
func evaluate(ctx context.Context, d app.Deps, labelsPath, profilePath string) error {
	var cases []app.EvalCase
	switch labelsPath {
	case "":
		return fmt.Errorf("eval needs --labels path.csv, or --labels telegram for the buttons pressed")
	case "telegram":
		ls, err := d.Store.Labels(ctx)
		if err != nil {
			return err
		}
		cases = app.LabelCases(ls)
	default:
		f, err := os.Open(labelsPath)
		if err != nil {
			return err
		}
		cases, err = app.ParseLabels(f)
		f.Close()
		if err != nil {
			return err
		}
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	kept, err := pages.ForAccount(ctx, awsCfg)
	if err != nil {
		return err
	}
	params := ssm.NewFromConfig(awsCfg)
	param := func(name string) (string, error) {
		out, err := params.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: aws.Bool(true)})
		if err != nil {
			return "", fmt.Errorf("read parameter %s: %w", name, err)
		}
		return aws.ToString(out.Parameter.Value), nil
	}
	key, err := param("/job-radar/anthropic-api-key")
	if err != nil {
		return err
	}
	var profile string
	if profilePath != "" {
		b, err := os.ReadFile(profilePath)
		if err != nil {
			return err
		}
		profile = string(b)
	} else if profile, err = param("/job-radar/assess-profile"); err != nil {
		return err
	}
	source := "SSM"
	if profilePath != "" {
		source = profilePath
	}
	fmt.Printf("評估 %d 個職缺，模型 %s，評分檔案：%s\n\n", len(cases), d.Cfg.Assess.Model, source)
	client := &assess.Client{APIKey: key, Model: d.Cfg.Assess.Model, Effort: d.Cfg.Assess.Effort}
	rows := app.Evaluate(ctx, d, client, kept, profile, cases)
	fmt.Print(app.BuildEvalReport(rows, d.Cfg.Assess.Model))
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
			envOr("TABLE_BOARDS", "job-radar-boards"), envOr("TABLE_JOBS", "job-radar-jobs"), envOr("TABLE_RUNS", "job-radar-runs"))
		return st, func() error { return nil }, nil
	}
	return nil, nil, fmt.Errorf("unknown store %q", kind)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// deadLetters lists the dead-letter queue and, for redrive, sends it again
// after the user types yes. Reading hides the messages for two minutes,
// which redrive needs to delete them; a plain list puts them back at once.
func deadLetters(ctx context.Context, d app.Deps, redrive bool) error {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}
	client := sqs.NewFromConfig(awsCfg)
	url, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("job-radar-dlq")})
	if err != nil {
		return fmt.Errorf("find the dead-letter queue: %w", err)
	}
	q := dlq.Queue{Client: client, URL: aws.ToString(url.QueueUrl)}
	msgs, err := q.Peek(ctx, 100, 120)
	if err != nil {
		return err
	}
	letters := make([]app.DeadLetter, len(msgs))
	for i, m := range msgs {
		letters[i] = app.ReadDeadLetter(m)
		fmt.Println(letters[i].Line())
	}
	if len(msgs) == 0 {
		fmt.Println("DLQ 是空的。")
		return nil
	}
	fmt.Printf("\n共 %d 則。\n", len(msgs))
	if !redrive {
		for _, m := range msgs {
			q.Release(ctx, m)
		}
		fmt.Println("修好原因之後，用 make redrive 重送。")
		return nil
	}
	fmt.Print("要全部重送嗎？輸入 yes：")
	var answer string
	fmt.Scanln(&answer)
	if answer != "yes" {
		fmt.Println("沒有重送。兩分鐘後它們會回到佇列。")
		return nil
	}
	kept, err := pages.ForAccount(ctx, awsCfg)
	if err != nil {
		return err
	}
	params := ssm.NewFromConfig(awsCfg)
	param := func(name string) (string, int64, error) {
		out, err := params.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: aws.Bool(true)})
		if err != nil {
			return "", 0, fmt.Errorf("read parameter %s: %w", name, err)
		}
		return aws.ToString(out.Parameter.Value), out.Parameter.Version, nil
	}
	key, _, err := param("/job-radar/anthropic-api-key")
	if err != nil {
		return err
	}
	text, version, err := param("/job-radar/assess-profile")
	if err != nil {
		return err
	}
	profile := app.Profile{Text: text, Version: version}
	model := &assess.Client{APIKey: key, Model: d.Cfg.Assess.Model, Effort: d.Cfg.Assess.Effort}
	report, err := app.Redrive(ctx, d, q, lambdaInvoker{lambda.NewFromConfig(awsCfg)}, model,
		notify.Writer{W: os.Stdout}, kept, profile, letters)
	for _, line := range report {
		fmt.Println(line)
	}
	return err
}

// lambdaInvoker starts a function the way the scheduler does: asynchronously.
type lambdaInvoker struct{ c *lambda.Client }

func (l lambdaInvoker) InvokeAsync(ctx context.Context, function string, payload []byte) error {
	_, err := l.c.Invoke(ctx, &lambda.InvokeInput{FunctionName: &function, Payload: payload, InvocationType: lambdatypes.InvocationTypeEvent})
	return err
}
