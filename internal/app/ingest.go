package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/guomengnick/job-radar/internal/mailin"
	"github.com/guomengnick/job-radar/internal/metrics"
)

// SES accepts messages up to 40 MB; a job alert is a few hundred KB.
const maxMailBytes = 20 << 20

// jobAlertSenders are the domains whose mail becomes jobs (subdomains count).
// Anything else, such as a test message or a mailbox's forwarding
// confirmation, is only logged.
var jobAlertSenders = []string{"104.com.tw"}

// MailOutcome says what one stored message turned into.
type MailOutcome struct {
	Key    string // the object key in the mail bucket
	Sender string // the sender's domain only: logs keep no addresses
	Status string // "ignored" | "unparsed" | "failed"
	Reason string
}

// IngestMail reads one message that SES stored. A message that cannot be
// parsed is reported in the outcome, not returned as an error: a retry would
// fail the same way, and the raw copy stays in the bucket for a later re-run.
// Only an error reading the object is returned, so the Lambda's asynchronous
// retry covers transient failures.
func IngestMail(ctx context.Context, d Deps, key string, raw io.Reader) (MailOutcome, error) {
	o := MailOutcome{Key: key}
	b, err := io.ReadAll(io.LimitReader(raw, maxMailBytes+1))
	if err != nil {
		return o, fmt.Errorf("read %s: %w", key, err)
	}
	o.Sender, o.Status, o.Reason = classifyMail(b)
	if d.Metrics != nil {
		metrics.EmitMail(d.Metrics, o.Status, d.Now())
	}
	d.Log.Info("mail ingested", "key", o.Key, "sender", o.Sender, "status", o.Status, "reason", o.Reason)
	return o, nil
}

func classifyMail(b []byte) (sender, status, reason string) {
	if len(b) > maxMailBytes {
		return "", "failed", "too_large"
	}
	m, err := mailin.Parse(bytes.NewReader(b))
	if err != nil {
		return "", "failed", err.Error()
	}
	sender = m.FromDomain()
	if !isJobAlertSender(sender) {
		return sender, "ignored", "not_a_job_alert_sender"
	}
	// The 104 layout parser comes with the first real alert; until then the
	// message waits in the bucket, which keeps it for 30 days.
	return sender, "unparsed", "no_parser_yet"
}

func isJobAlertSender(domain string) bool {
	for _, s := range jobAlertSenders {
		if domain == s || strings.HasSuffix(domain, "."+s) {
			return true
		}
	}
	return false
}
