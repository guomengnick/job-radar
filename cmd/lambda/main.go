// Command lambda is the AWS Lambda entry point. One binary serves every
// function. EventBridge Scheduler passes the task to three of them:
//
//	{"task":"scan","ats":"greenhouse"}
//	{"task":"digest"}
//	{"task":"sync-boards","ats":"lever"}
//
// ingest is invoked by S3 when SES stores a message, and clip through its
// function URL by the 送到雷達 browser extension: POST / judges a page,
// GET /jobs lists the 104 alert jobs for the extension to open, and
// POST /batch sends the open tabs as one Message Batch. The scheduler runs
// batch every ten minutes to send the summary of each batch that has ended:
//
//	{"task":"batch-check"}
//
// alarm is subscribed to the alerts SNS topic and forwards each CloudWatch
// alarm to Telegram; the topic's email subscription stays as the fallback.
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
	"github.com/guomengnick/job-radar/internal/pages"
	"github.com/guomengnick/job-radar/internal/store"
	"github.com/guomengnick/job-radar/internal/version"
)

type Event struct {
	Task       string `json:"task"`
	ATS        string `json:"ats"`
	Limit      int    `json:"limit"`
	Silent     bool   `json:"silent"`
	NoBaseline bool   `json:"no_baseline"`
	DryRun     bool   `json:"dry_run"`
}

// minClient is the oldest 送到雷達 extension this server answers. Raise it
// when a change to the endpoints breaks older extensions: they get 426 and
// a message to reload. An extension too old to send its version (before
// 1.4.0) is still answered.
const minClient = "1.4.0"

type handler struct {
	deps app.Deps
	s3   *s3.Client
	log  *slog.Logger
	// commit is the code this binary was built from, sent back to the
	// extension and written on every log line.
	commit string
	// secret reads a SecureString parameter. Each role may read only its own
	// parameters, so a bug in the scanner cannot leak the bot token.
	secret func(ctx context.Context, name string) (string, error)
}

func main() {
	// Every log line names the commit, so an alarm's log points at the code
	// that wrote it.
	commit := version.Commit()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("commit", commit)
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
	h := &handler{deps: app.NewDeps(cfg, st, log, os.Stdout), s3: s3.NewFromConfig(awsCfg), log: log, commit: commit,
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
		return h.serve(ctx, req), nil
	}
	if rec, ok := s3Records(raw); ok {
		return h.ingest(ctx, rec)
	}
	if rec, ok := snsRecords(raw); ok {
		return h.alarm(ctx, rec)
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
		_, err = app.Digest(ctx, h.deps, n, app.DigestOptions{ExpectedATS: expected, DryRun: e.DryRun, Location: taipei, Version: h.commit})
		return "digest sent", err
	case "batch-check":
		return h.checkBatches(ctx)
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

// snsRecords returns the records of an SNS notification.
func snsRecords(raw json.RawMessage) ([]events.SNSEventRecord, bool) {
	var e events.SNSEvent
	if json.Unmarshal(raw, &e) != nil || len(e.Records) == 0 || e.Records[0].EventSource != "aws:sns" {
		return nil, false
	}
	return e.Records, true
}

// alarm sends each SNS message to Telegram. An error makes Lambda retry the
// event (two retries), so a Telegram outage delays the message rather than
// dropping it; a duplicate after a partial failure is harmless.
func (h *handler) alarm(ctx context.Context, recs []events.SNSEventRecord) (string, error) {
	token, err := h.secret(ctx, mustEnv("TELEGRAM_TOKEN_PARAM"))
	if err != nil {
		return "", err
	}
	n := notify.Telegram{Token: token, ChatID: mustEnv("TELEGRAM_CHAT_ID")}
	taipei, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		taipei = time.FixedZone("UTC+8", 8*60*60)
	}
	for _, r := range recs {
		msg := app.BuildAlarm(r.SNS.Subject, r.SNS.Message, envOr("AWS_REGION", "ap-southeast-2"), taipei)
		if err := n.Send(ctx, msg); err != nil {
			return "", fmt.Errorf("send alarm %s: %w", r.SNS.MessageID, err)
		}
	}
	return fmt.Sprintf("alarms: %d sent", len(recs)), nil
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

// A page's text is tens of KB at most. A batch of pages must stay under the
// function URL's 6 MB request limit; the extension sends at most 5 MB.
const (
	maxClipBytes  = 256 << 10
	maxBatchBytes = 5 << 20
)

type urlHandler func(context.Context, events.LambdaFunctionURLRequest) events.LambdaFunctionURLResponse

// serve answers the browser extension. The function URL is public, so the
// shared secret in the x-radar-secret header is the gate: past the route
// check, nothing is read or called before it matches. Errors are logged in
// full and answered with a status only. Past the gate, an extension older
// than minClient is refused, and every answer names the server's commit in
// x-radar-server.
func (h *handler) serve(ctx context.Context, r events.LambdaFunctionURLRequest) events.LambdaFunctionURLResponse {
	routes := map[string]map[string]urlHandler{
		"/":      {http.MethodPost: h.clip},
		"/jobs":  {http.MethodGet: h.jobs},
		"/batch": {http.MethodPost: h.batch},
	}
	path := r.RawPath
	if path == "" {
		path = "/"
	}
	methods, ok := routes[path]
	if !ok {
		return reply(http.StatusNotFound, "not found")
	}
	next, ok := methods[r.RequestContext.HTTP.Method]
	if !ok {
		return reply(http.StatusMethodNotAllowed, "method not allowed")
	}
	want, err := h.secret(ctx, mustEnv("CLIP_SECRET_PARAM"))
	if err != nil {
		h.log.Error("clip secret", "err", err)
		return reply(http.StatusInternalServerError, "server error")
	}
	if got := header(r.Headers, "x-radar-secret"); want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		h.log.Warn("clip refused", "path", path, "source_ip", r.RequestContext.HTTP.SourceIP)
		return reply(http.StatusUnauthorized, "unauthorized")
	}
	var resp events.LambdaFunctionURLResponse
	if v := header(r.Headers, "x-radar-client"); v != "" && !version.AtLeast(v, minClient) {
		h.log.Warn("old extension", "client", v, "min", minClient)
		resp = reply(http.StatusUpgradeRequired, fmt.Sprintf("擴充功能 %s 太舊，需要 %s 以上：git pull 後到 chrome://extensions 重新載入", v, minClient))
	} else {
		resp = next(ctx, r)
	}
	if resp.Headers == nil {
		resp.Headers = map[string]string{}
	}
	resp.Headers["x-radar-server"] = h.commit
	return resp
}

// jobs lists the 104 alert jobs first seen since the time in the since
// query parameter (RFC 3339), or in the last day without one.
func (h *handler) jobs(ctx context.Context, r events.LambdaFunctionURLRequest) events.LambdaFunctionURLResponse {
	since := h.deps.Now().Add(-24 * time.Hour)
	if v := r.QueryStringParameters["since"]; v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return reply(http.StatusBadRequest, "since must be an RFC 3339 time")
		}
		since = t
	}
	jobs, err := app.ListAlertJobs(ctx, h.deps, since)
	switch {
	case errors.Is(err, app.ErrBadSince):
		return reply(http.StatusBadRequest, err.Error())
	case err != nil:
		h.log.Error("list jobs", "err", err)
		return reply(http.StatusInternalServerError, "server error")
	}
	out, _ := json.Marshal(map[string]any{"since": since.UTC().Format(time.RFC3339), "jobs": jobs})
	return events.LambdaFunctionURLResponse{StatusCode: http.StatusOK, Body: string(out),
		Headers: map[string]string{"content-type": "application/json"}}
}

// clip judges the page in the body and replies on Telegram.
func (h *handler) clip(ctx context.Context, r events.LambdaFunctionURLRequest) events.LambdaFunctionURLResponse {
	body, status := requestBody(r, maxClipBytes)
	if status != 0 {
		return reply(status, http.StatusText(status))
	}
	var page assess.Page
	if err := json.Unmarshal(body, &page); err != nil {
		return reply(http.StatusBadRequest, "bad json")
	}

	vals, err := h.params(ctx, "ANTHROPIC_KEY_PARAM", "ASSESS_PROFILE_PARAM", "TELEGRAM_TOKEN_PARAM")
	if err != nil {
		h.log.Error("clip parameter", "err", err)
		return reply(http.StatusInternalServerError, "server error")
	}
	model := h.model(vals[0])
	n := notify.Telegram{Token: vals[2], ChatID: mustEnv("TELEGRAM_CHAT_ID")}
	archive := pages.S3{Client: h.s3, Bucket: mustEnv("PAGES_BUCKET")}
	j, err := app.Clip(ctx, h.deps, model, n, archive, vals[1], page)
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

// batch takes the pages the extension read from the open tabs, submits
// them as one Message Batch and answers at once with what it did.
func (h *handler) batch(ctx context.Context, r events.LambdaFunctionURLRequest) events.LambdaFunctionURLResponse {
	body, status := requestBody(r, maxBatchBytes)
	if status != 0 {
		return reply(status, http.StatusText(status))
	}
	var req struct {
		Pages []assess.Page `json:"pages"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Pages) == 0 {
		return reply(http.StatusBadRequest, "want {\"pages\": [...]}")
	}
	vals, err := h.params(ctx, "ANTHROPIC_KEY_PARAM", "ASSESS_PROFILE_PARAM", "TELEGRAM_TOKEN_PARAM", "BATCH_RULES_PARAM")
	if err != nil {
		h.log.Error("batch parameter", "err", err)
		return reply(http.StatusInternalServerError, "server error")
	}
	rules, err := app.ParsePageRules(vals[3])
	if err != nil {
		h.log.Error("batch rules", "err", err)
		return reply(http.StatusInternalServerError, "batch rules are not valid JSON")
	}
	n := notify.Telegram{Token: vals[2], ChatID: mustEnv("TELEGRAM_CHAT_ID")}
	archive := pages.S3{Client: h.s3, Bucket: mustEnv("PAGES_BUCKET")}
	receipt, err := app.SubmitPages(ctx, h.deps, h.model(vals[0]), n, archive, rules, vals[1], req.Pages)
	if err != nil {
		h.log.Error("batch", "err", err)
		return reply(http.StatusBadGateway, "batch failed")
	}
	out, _ := json.Marshal(receipt)
	return events.LambdaFunctionURLResponse{StatusCode: http.StatusAccepted, Body: string(out),
		Headers: map[string]string{"content-type": "application/json"}}
}

// checkBatches sends the summary of each open batch that has ended. Most
// runs find none and read no secret.
func (h *handler) checkBatches(ctx context.Context) (string, error) {
	open, err := h.deps.Store.OpenBatches(ctx)
	if err != nil || len(open) == 0 {
		return "no open batches", err
	}
	vals, err := h.params(ctx, "ANTHROPIC_KEY_PARAM", "TELEGRAM_TOKEN_PARAM")
	if err != nil {
		return "", err
	}
	n := notify.Telegram{Token: vals[1], ChatID: mustEnv("TELEGRAM_CHAT_ID")}
	closed, err := app.CheckBatches(ctx, h.deps, h.model(vals[0]), n, open)
	return fmt.Sprintf("batches: %d open, %d closed", len(open), closed), err
}

func (h *handler) model(apiKey string) assess.Client {
	return assess.Client{APIKey: apiKey, Model: h.deps.Cfg.Assess.Model, Effort: h.deps.Cfg.Assess.Effort}
}

// params reads the parameters named by the given environment variables.
func (h *handler) params(ctx context.Context, envs ...string) ([]string, error) {
	vals := make([]string, len(envs))
	for i, env := range envs {
		v, err := h.secret(ctx, mustEnv(env))
		if err != nil {
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

// requestBody returns the decoded body, or the status to answer with.
func requestBody(r events.LambdaFunctionURLRequest, limit int) ([]byte, int) {
	body := []byte(r.Body)
	if r.IsBase64Encoded {
		var err error
		if body, err = base64.StdEncoding.DecodeString(r.Body); err != nil {
			return nil, http.StatusBadRequest
		}
	}
	if len(body) > limit {
		return nil, http.StatusRequestEntityTooLarge
	}
	return body, 0
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
