// Command lambda is the AWS Lambda entry point. One binary serves four
// functions. EventBridge Scheduler passes the task to three of them:
//
//	{"task":"scan","ats":"greenhouse"}
//	{"task":"digest"}
//	{"task":"sync-boards","ats":"lever"}
//
// The fourth, ingest, is invoked by S3 when SES stores a message.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/app"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

type Event struct {
	Task       string `json:"task"`
	ATS        string `json:"ats"`
	Limit      int    `json:"limit"`
	Silent     bool   `json:"silent"`
	NoBaseline bool   `json:"no_baseline"`
	DryRun     bool   `json:"dry_run"`
}

type handler struct {
	deps app.Deps
	ssm  *ssm.Client
	s3   *s3.Client
	log  *slog.Logger
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.Default()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Error("aws config", "err", err)
		os.Exit(1)
	}
	st := store.NewDynamo(dynamodb.NewFromConfig(awsCfg),
		mustEnv("TABLE_BOARDS"), mustEnv("TABLE_JOBS"), mustEnv("TABLE_RUNS"))
	// EMF metric lines go to stdout, where CloudWatch Logs extracts them.
	h := &handler{deps: app.NewDeps(cfg, st, log, os.Stdout),
		ssm: ssm.NewFromConfig(awsCfg), s3: s3.NewFromConfig(awsCfg), log: log}
	lambda.Start(h.handle)
}

func (h *handler) handle(ctx context.Context, raw json.RawMessage) (string, error) {
	if rec, ok := s3Records(raw); ok {
		return h.ingest(ctx, rec)
	}
	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		return "", fmt.Errorf("decode event: %w", err)
	}
	switch e.Task {
	case "scan":
		deadline, _ := ctx.Deadline()
		r, err := app.Scan(ctx, h.deps, app.ScanOptions{
			ATS: e.ATS, Limit: e.Limit, Silent: e.Silent, NoBaseline: e.NoBaseline, Deadline: deadline,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s %s: %d/%d boards, %d new", r.ATS, r.Status, r.BoardsChecked, r.BoardsDue, r.JobsNew), nil
	case "digest":
		token, err := h.secret(ctx, mustEnv("TELEGRAM_TOKEN_PARAM"))
		if err != nil {
			return "", err
		}
		n := notify.Telegram{Token: token, ChatID: mustEnv("TELEGRAM_CHAT_ID")}
		taipei, _ := time.LoadLocation("Asia/Taipei")
		expected := strings.Split(envOr("EXPECTED_ATS", "greenhouse,lever,ashby"), ",")
		_, err = app.Digest(ctx, h.deps, n, app.DigestOptions{ExpectedATS: expected, DryRun: e.DryRun, Location: taipei})
		return "digest sent", err
	case "sync-boards":
		added, total, err := app.SyncBoards(ctx, h.deps, e.ATS, "")
		return fmt.Sprintf("%s: %d listed, %d new", e.ATS, total, added), err
	}
	return "", fmt.Errorf("unknown task %q", e.Task)
}

// s3Records returns the records of an S3 notification. Scheduler payloads have
// no Records field, so they fall through to the task switch.
func s3Records(raw json.RawMessage) ([]events.S3EventRecord, bool) {
	var e events.S3Event
	if json.Unmarshal(raw, &e) != nil || len(e.Records) == 0 || e.Records[0].EventSource != "aws:s3" {
		return nil, false
	}
	return e.Records, true
}

// ingest reads each stored message. Errors are collected rather than stopping
// at the first, and a retried event re-reads every object; that is safe
// because ingest only records outcomes (and later, conditional writes).
func (h *handler) ingest(ctx context.Context, recs []events.S3EventRecord) (string, error) {
	var errs []error
	counts := map[string]int{}
	for _, r := range recs {
		bucket, key := r.S3.Bucket.Name, r.S3.Object.URLDecodedKey
		out, err := h.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
		if err != nil {
			errs = append(errs, fmt.Errorf("get %s: %w", key, err))
			continue
		}
		o, err := app.IngestMail(ctx, h.deps, key, out.Body)
		out.Body.Close()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		counts[o.Status]++
	}
	return fmt.Sprintf("mail: %v", counts), errors.Join(errs...)
}

// secret reads a SecureString parameter. Only the digest function's role may
// read it, so a bug in the scanner cannot leak the bot token.
func (h *handler) secret(ctx context.Context, name string) (string, error) {
	out, err := h.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: ptr(true)})
	if err != nil {
		return "", fmt.Errorf("read parameter %s: %w", name, err)
	}
	return *out.Parameter.Value, nil
}

func ptr[T any](v T) *T { return &v }

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		panic("missing environment variable " + k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
