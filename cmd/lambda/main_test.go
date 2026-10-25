package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/guomengnick/job-radar/internal/app"
	"github.com/guomengnick/job-radar/internal/store"
)

func TestS3Records(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantKey string // "" means: not an S3 notification
	}{
		{
			name: "S3 notification, key is URL-decoded",
			payload: `{"Records":[{"eventSource":"aws:s3","eventName":"ObjectCreated:Put",
				"s3":{"bucket":{"name":"job-radar-mail-1"},"object":{"key":"inbound/a%2Bb+c"}}}]}`,
			wantKey: "inbound/a+b c",
		},
		{name: "scheduler scan task", payload: `{"task":"scan","ats":"greenhouse"}`},
		{name: "digest task", payload: `{"task":"digest"}`},
		{name: "records from another source", payload: `{"Records":[{"eventSource":"aws:sqs"}]}`},
		{name: "empty records", payload: `{"Records":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs, ok := s3Records(json.RawMessage(tt.payload))
			if ok != (tt.wantKey != "") {
				t.Fatalf("ok = %v", ok)
			}
			if ok && recs[0].S3.Object.URLDecodedKey != tt.wantKey {
				t.Errorf("key = %q", recs[0].S3.Object.URLDecodedKey)
			}
		})
	}
}

func TestSNSRecords(t *testing.T) {
	sns := `{"Records":[{"EventSource":"aws:sns","EventVersion":"1.0","Sns":{"MessageId":"m1","Subject":"ALARM: \"job-radar-clip-errors\"",
		"Message":"{\"AlarmName\":\"job-radar-clip-errors\",\"NewStateValue\":\"ALARM\"}"}}]}`
	recs, ok := snsRecords(json.RawMessage(sns))
	if !ok || recs[0].SNS.MessageID != "m1" || !strings.Contains(recs[0].SNS.Message, "job-radar-clip-errors") {
		t.Fatalf("sns = %+v %v", recs, ok)
	}
	for _, other := range []string{`{"task":"digest"}`, `{"Records":[{"eventSource":"aws:s3"}]}`, `{"Records":[]}`} {
		if _, ok := snsRecords(json.RawMessage(other)); ok {
			t.Errorf("%s read as SNS", other)
		}
	}
	if _, ok := s3Records(json.RawMessage(sns)); ok {
		t.Error("an SNS event read as S3")
	}
}

func TestURLRequest(t *testing.T) {
	for payload, want := range map[string]bool{
		`{"version":"2.0","rawPath":"/","headers":{"x-radar-secret":"s"},"requestContext":{"http":{"method":"POST","sourceIp":"192.0.2.1"}},"body":"{}"}`: true,
		`{"task":"digest"}`:                      false,
		`{"Records":[{"eventSource":"aws:s3"}]}`: false,
	} {
		if _, ok := urlRequest(json.RawMessage(payload)); ok != want {
			t.Errorf("%s: %v", payload, ok)
		}
	}
}

func TestServeGate(t *testing.T) {
	for k, v := range map[string]string{"CLIP_SECRET_PARAM": "/test/clip-secret", "ANTHROPIC_KEY_PARAM": "/test/key",
		"ASSESS_PROFILE_PARAM": "/test/profile", "TELEGRAM_TOKEN_PARAM": "/test/token", "TELEGRAM_CHAT_ID": "1"} {
		t.Setenv(k, v)
	}
	var asked []string
	h := &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		secret: func(_ context.Context, name string) (string, error) {
			asked = append(asked, name)
			if name == "/test/clip-secret" {
				return "right-secret", nil
			}
			return "", errors.New("not allowed in this test")
		}}
	req := func(method, secret, body string, b64 bool) events.LambdaFunctionURLRequest {
		path, method, _ := strings.Cut(method, " ")
		r := events.LambdaFunctionURLRequest{RawPath: path, Headers: map[string]string{}, Body: body, IsBase64Encoded: b64}
		r.RequestContext.HTTP.Method = method
		if secret != "" {
			r.Headers["X-Radar-Secret"] = secret
		}
		return r
	}
	tests := []struct {
		name string
		r    events.LambdaFunctionURLRequest
		want int
	}{
		{"GET a page", req("/ GET", "right-secret", "", false), http.StatusMethodNotAllowed},
		{"POST the list", req("/jobs POST", "right-secret", "", false), http.StatusMethodNotAllowed},
		{"unknown path", req("/admin GET", "right-secret", "", false), http.StatusNotFound},
		{"no secret", req("/ POST", "", `{}`, false), http.StatusUnauthorized},
		{"wrong secret", req("/ POST", "right-secreT", `{}`, false), http.StatusUnauthorized},
		{"list without secret", req("/jobs GET", "", "", false), http.StatusUnauthorized},
		{"too large", req("/ POST", "right-secret", strings.Repeat("x", maxClipBytes+1), false), http.StatusRequestEntityTooLarge},
		{"bad base64", req("/ POST", "right-secret", "%%%", true), http.StatusBadRequest},
		{"bad json", req("/ POST", "right-secret", "{", false), http.StatusBadRequest},
		{"GET the batch", req("/batch GET", "right-secret", "", false), http.StatusMethodNotAllowed},
		{"batch without secret", req("/batch POST", "", `{"pages":[]}`, false), http.StatusUnauthorized},
		{"batch too large", req("/batch POST", "right-secret", strings.Repeat("x", maxBatchBytes+1), false), http.StatusRequestEntityTooLarge},
		{"batch with no pages", req("/batch POST", "right-secret", `{"pages":[]}`, false), http.StatusBadRequest},
		{"batch not JSON", req("/batch POST", "right-secret", `[`, false), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			resp := h.serve(context.Background(), tt.r)
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d (%s)", resp.StatusCode, tt.want, resp.Body)
			}
			for _, n := range asked {
				if n != "/test/clip-secret" {
					t.Errorf("read %s before the request was accepted", n)
				}
			}
		})
	}
}

func TestServeVersions(t *testing.T) {
	t.Setenv("CLIP_SECRET_PARAM", "/test/clip-secret")
	h := &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), commit: "3a9a26c",
		secret: func(context.Context, string) (string, error) { return "right-secret", nil }}
	req := func(secret, client string) events.LambdaFunctionURLRequest {
		r := events.LambdaFunctionURLRequest{RawPath: "/batch", Headers: map[string]string{"x-radar-secret": secret}, Body: "["}
		r.RequestContext.HTTP.Method = http.MethodPost
		if client != "" {
			r.Headers["x-radar-client"] = client
		}
		return r
	}
	for _, c := range []struct {
		name, secret, client string
		want                 int
		names                bool // the answer carries x-radar-server
	}{
		{"current extension", "right-secret", minClient, http.StatusBadRequest, true},
		{"before versions were sent", "right-secret", "", http.StatusBadRequest, true},
		{"too old", "right-secret", "1.3.9", http.StatusUpgradeRequired, true},
		{"no secret, no version given away", "wrong", minClient, http.StatusUnauthorized, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := h.serve(context.Background(), req(c.secret, c.client))
			if resp.StatusCode != c.want || (resp.Headers["x-radar-server"] == "3a9a26c") != c.names {
				t.Errorf("status %d headers %v body %s", resp.StatusCode, resp.Headers, resp.Body)
			}
		})
	}
}

func TestServeTelegramGate(t *testing.T) {
	t.Setenv("CLIP_SECRET_PARAM", "/test/clip-secret")
	h := &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		secret: func(_ context.Context, name string) (string, error) {
			if name == "/test/clip-secret" {
				return "right-secret", nil
			}
			return "", errors.New("not allowed in this test")
		}}
	req := func(header, value, body string) events.LambdaFunctionURLRequest {
		r := events.LambdaFunctionURLRequest{RawPath: "/telegram", Headers: map[string]string{header: value}, Body: body}
		r.RequestContext.HTTP.Method = http.MethodPost
		return r
	}
	for _, c := range []struct {
		name          string
		header, value string
		body          string
		want          int
	}{
		{"telegram's header", "x-telegram-bot-api-secret-token", "right-secret", `{"update_id":1,"message":{"text":"hi"}}`, http.StatusOK},
		{"wrong secret", "x-telegram-bot-api-secret-token", "nope", `{}`, http.StatusUnauthorized},
		{"the extension's header does not open it", "x-radar-secret", "right-secret", `{}`, http.StatusUnauthorized},
		{"not JSON is acknowledged", "x-telegram-bot-api-secret-token", "right-secret", `[`, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := h.serve(context.Background(), req(c.header, c.value, c.body)).StatusCode; got != c.want {
				t.Errorf("status = %d, want %d", got, c.want)
			}
		})
	}
}

func TestJobsList(t *testing.T) {
	t.Setenv("CLIP_SECRET_PARAM", "/test/clip-secret")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	st := store.NewMemory()
	st.InsertJobIfNew(context.Background(), store.JobRecord{Key: "104#aaa01", ATS: "104", URL: "https://www.104.com.tw/job/aaa01",
		Title: "後端工程師", Class: "A", Reason: "taiwan_remote", State: store.JobNew, FirstSeen: now.Add(-time.Hour)})
	h := &handler{deps: app.Deps{Store: st, Now: func() time.Time { return now }},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		secret: func(context.Context, string) (string, error) { return "right-secret", nil }}
	get := func(since string) events.LambdaFunctionURLResponse {
		r := events.LambdaFunctionURLRequest{RawPath: "/jobs", Headers: map[string]string{"x-radar-secret": "right-secret"}}
		if since != "" {
			r.QueryStringParameters = map[string]string{"since": since}
		}
		r.RequestContext.HTTP.Method = http.MethodGet
		return h.serve(context.Background(), r)
	}

	for _, since := range []string{"", "2026-10-21T16:00:00.000Z"} {
		resp := get(since)
		var body struct {
			Since string          `json:"since"`
			Jobs  []app.ListedJob `json:"jobs"`
		}
		if err := json.Unmarshal([]byte(resp.Body), &body); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("since %q: %d %s %v", since, resp.StatusCode, resp.Body, err)
		}
		if len(body.Jobs) != 1 || body.Jobs[0].Group != app.GroupDigest || body.Jobs[0].Reason != "台灣遠端" {
			t.Errorf("since %q: jobs = %+v", since, body.Jobs)
		}
	}
	if resp := get("2026-10-21T16:00:00Z"); !strings.Contains(resp.Body, `"since":"2026-10-21T16:00:00Z"`) {
		t.Errorf("the reply says which time it used: %s", resp.Body)
	}
	for _, since := range []string{"yesterday", "2026-09-01T00:00:00Z", "2026-10-23T00:00:00Z"} {
		if resp := get(since); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("since %q: status %d, want 400", since, resp.StatusCode)
		}
	}
}

func TestCheckBatchesReadsNoSecretWhenIdle(t *testing.T) {
	h := &handler{deps: app.Deps{Store: store.NewMemory()},
		secret: func(context.Context, string) (string, error) {
			t.Error("no open batch: no secret is read")
			return "", nil
		}}
	if out, err := h.handle(context.Background(), json.RawMessage(`{"task":"batch-check"}`)); err != nil || out != "no open batches" {
		t.Fatalf("out=%v err=%v", out, err)
	}
}
