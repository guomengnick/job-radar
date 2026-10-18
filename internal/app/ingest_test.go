package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func mail(from, body string) string {
	return "From: " + from + "\r\nSubject: s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + body + "\r\n"
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestIngestMail(t *testing.T) {
	tests := []struct {
		name, raw, status, sender string
	}{
		{"104 alert waits for the parser", mail("104 <noreply@104.com.tw>", "<p>jobs</p>"), "unparsed", "104.com.tw"},
		{"a 104 subdomain counts", mail("noreply@mail.104.com.tw", "<p>jobs</p>"), "unparsed", "mail.104.com.tw"},
		{"a lookalike domain does not", mail("noreply@fake104.com.tw", "<p>jobs</p>"), "ignored", "fake104.com.tw"},
		{"a test message is ignored", mail("someone@example.com", "<p>hi</p>"), "ignored", "example.com"},
		{"garbage is reported, not returned", "not a message", "failed", ""},
		{"too large", mail("noreply@104.com.tw", strings.Repeat("x", maxMailBytes)), "failed", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _, _, _ := setup(t)
			var m bytes.Buffer
			d.Metrics = &m
			o, err := IngestMail(context.Background(), d, "inbound/k", strings.NewReader(tt.raw))
			if err != nil {
				t.Fatalf("a bad message must not be an error (it would be retried): %v", err)
			}
			if o.Status != tt.status || o.Sender != tt.sender || o.Key != "inbound/k" {
				t.Errorf("outcome = %+v", o)
			}
			wantFailed := `"MailFailed":0`
			if tt.status == "failed" {
				wantFailed = `"MailFailed":1`
			}
			if !strings.Contains(m.String(), `"MailReceived":1`) || !strings.Contains(m.String(), wantFailed) {
				t.Errorf("metrics = %s", m.String())
			}
		})
	}
}

func TestIngestMailReturnsReadErrors(t *testing.T) {
	d, _, _, _ := setup(t)
	if _, err := IngestMail(context.Background(), d, "inbound/k", failingReader{}); err == nil {
		t.Fatal("a read error must be returned so the invocation is retried")
	}
}
