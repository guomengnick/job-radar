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

	"github.com/aws/aws-lambda-go/events"
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

func TestClipGate(t *testing.T) {
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
		r := events.LambdaFunctionURLRequest{Headers: map[string]string{}, Body: body, IsBase64Encoded: b64}
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
		{"GET", req("GET", "right-secret", "", false), http.StatusMethodNotAllowed},
		{"no secret", req("POST", "", `{}`, false), http.StatusUnauthorized},
		{"wrong secret", req("POST", "right-secreT", `{}`, false), http.StatusUnauthorized},
		{"too large", req("POST", "right-secret", strings.Repeat("x", maxClipBytes+1), false), http.StatusRequestEntityTooLarge},
		{"bad base64", req("POST", "right-secret", "%%%", true), http.StatusBadRequest},
		{"bad json", req("POST", "right-secret", "{", false), http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			resp := h.clip(context.Background(), tt.r)
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
