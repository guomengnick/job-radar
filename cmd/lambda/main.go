// Command lambda is the AWS Lambda entry point. One binary serves five
// functions. EventBridge Scheduler passes the task to three of them:
//
//	{"task":"scan","ats":"greenhouse"}
//	{"task":"digest"}
//	{"task":"sync-boards","ats":"lever"}
//
// ingest is invoked by S3 when SES stores a message, and clip through its
// function URL when the 送到雷達 browser extension sends a page.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/guomengnick/job-radar/internal/assess"
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
	s3   *s3.Client
	log  *slog.Logger
	// secret reads a SecureString parameter. Each role may read only its own
	// parameters, so a bug in the scanner cannot leak the bot token.
	secret func(ctx context.Context, name string) (string, error)
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
	params := ssm.NewFromConfig(awsCfg)
	h := &handler{deps: app.NewDeps(cfg, st, log, os.Stdout), s3: s3.NewFromConfig(awsCfg), log: log,
		secret: func(ctx context.Context, name string) (string, error) {
			out, err := params.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: ptr(true)})
			if err != nil {
				return "", fmt.Errorf("read parameter %s: %w", name, err)
			}
			return *out.Parameter.Value, nil
		}}
	lambda.Start(h.handle)
}

// handle returns a string for scheduled and S3 invocations, and an HTTP
// response for the function URL.
func (h *handler) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	if req, ok := urlRequest(raw); ok {
		return h.clip(ctx, req), nil
	}
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

// urlRequest returns the request when the function was invoked through its
// function URL; scheduler and S3 payloads have no requestContext.http.
func urlRequest(raw json.RawMessage) (events.LambdaFunctionURLRequest, bool) {
	var r events.LambdaFunctionURLRequest
	if json.Unmarshal(raw, &r) != nil || r.RequestContext.HTTP.Method == "" {
		return r, false
	}
	return r, true
}

// A page's text is tens of KB at most.
const maxClipBytes = 256 << 10

// clip serves POST from the browser extension. The function URL is public,
// so the shared secret in the x-radar-secret header is the gate: nothing is
// read or called before it matches. Errors are logged in full and answered
// with a status only.
func (h *handler) clip(ctx context.Context, r events.LambdaFunctionURLRequest) events.LambdaFunctionURLResponse {
	if r.RequestContext.HTTP.Method != http.MethodPost {
		return reply(http.StatusMethodNotAllowed, "method not allowed")
	}
	want, err := h.secret(ctx, mustEnv("CLIP_SECRET_PARAM"))
	if err != nil {
		h.log.Error("clip secret", "err", err)
		return reply(http.StatusInternalServerError, "server error")
	}
	if got := header(r.Headers, "x-radar-secret"); want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		h.log.Warn("clip refused", "source_ip", r.RequestContext.HTTP.SourceIP)
		return reply(http.StatusUnauthorized, "unauthorized")
	}
	body := []byte(r.Body)
	if r.IsBase64Encoded {
		if body, err = base64.StdEncoding.DecodeString(r.Body); err != nil {
			return reply(http.StatusBadRequest, "bad body")
		}
	}
	if len(body) > maxClipBytes {
		return reply(http.StatusRequestEntityTooLarge, "page too large")
	}
	var page assess.Page
	if err := json.Unmarshal(body, &page); err != nil {
		return reply(http.StatusBadRequest, "bad json")
	}

	var vals [3]string
	for i, env := range []string{"ANTHROPIC_KEY_PARAM", "ASSESS_PROFILE_PARAM", "TELEGRAM_TOKEN_PARAM"} {
		if vals[i], err = h.secret(ctx, mustEnv(env)); err != nil {
			h.log.Error("clip parameter", "err", err)
			return reply(http.StatusInternalServerError, "server error")
		}
	}
	model := assess.Client{APIKey: vals[0], Model: h.deps.Cfg.Assess.Model}
	n := notify.Telegram{Token: vals[2], ChatID: mustEnv("TELEGRAM_CHAT_ID")}
	j, err := app.Clip(ctx, h.deps, model, n, vals[1], page)
	switch {
	case errors.Is(err, app.ErrBadClip):
		return reply(http.StatusBadRequest, err.Error())
	case err != nil:
		h.log.Error("clip", "err", err)
		return reply(http.StatusBadGateway, "assessment failed")
	}
	out, _ := json.Marshal(map[string]any{"score": j.Score, "verdict": j.Verdict})
	return events.LambdaFunctionURLResponse{StatusCode: http.StatusOK, Body: string(out),
		Headers: map[string]string{"content-type": "application/json"}}
}

func reply(status int, msg string) events.LambdaFunctionURLResponse {
	out, _ := json.Marshal(map[string]string{"error": msg})
	return events.LambdaFunctionURLResponse{StatusCode: status, Body: string(out),
		Headers: map[string]string{"content-type": "application/json"}}
}

// header looks a name up regardless of case; function URLs lowercase names,
// but a test or a proxy might not.
func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
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
