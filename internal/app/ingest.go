package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/alert104"
	"github.com/guomengnick/job-radar/internal/filter"
	"github.com/guomengnick/job-radar/internal/mailin"
	"github.com/guomengnick/job-radar/internal/metrics"
	"github.com/guomengnick/job-radar/internal/store"
)

// SES accepts messages up to 40 MB; a job alert is a few hundred KB.
const maxMailBytes = 20 << 20

// The ATS field of records from 104 mail: jobs from alerts, and contacts
// (a recruiter's message, an application read), which the digest lists first.
const (
	alertATS   = "104"
	contactATS = "104-contact"
)

// jobAlertSenders are the domains whose mail becomes jobs (subdomains count).
// Anything else, such as a test message or a mailbox's forwarding
// confirmation, is only logged.
var jobAlertSenders = []string{"104.com.tw"}

// forwarders are the mail services allowed to pass on a 104 alert. Outlook's
// redirect rule rewrites the message, which breaks 104's own DKIM signature
// on the way to SES.
var forwarders = []string{"outlook.com"}

// MailOutcome says what one stored message turned into.
type MailOutcome struct {
	Key    string // the object key in the mail bucket
	Sender string // the sender's domain only: logs keep no addresses
	Status string // "parsed" | "ignored" | "failed"
	Reason string // for a parsed message: "alert" or the contact's kind
	// For a parsed alert: jobs listed, first seen, and new ones to send.
	Jobs, New, Pending int
}

// IngestMail reads one message that SES stored and turns verified 104 mail
// into records: a match alert into its jobs, a contact into one entry. A message that cannot be parsed is reported in the
// outcome, not returned as an error: a retry would fail the same way, and the
// raw copy stays in the bucket for a later re-run. Read and store errors are
// returned, so the Lambda's asynchronous retry covers transient failures; the
// retry is safe because a job already stored is not inserted again.
func IngestMail(ctx context.Context, d Deps, key string, raw io.Reader) (MailOutcome, error) {
	o := MailOutcome{Key: key}
	b, err := io.ReadAll(io.LimitReader(raw, maxMailBytes+1))
	if err != nil {
		return o, fmt.Errorf("read %s: %w", key, err)
	}
	var m *mailin.Message
	m, o.Sender, o.Status, o.Reason = classifyMail(b)
	var storeErr error
	switch {
	case o.Status != "verified":
	case alert104.IsMatchAlert(m.Subject):
		o.Status, o.Reason = "parsed", "alert"
		jobs, err := alert104.Parse(m.HTML)
		if err != nil {
			o.Status, o.Reason = "failed", err.Error()
			break
		}
		run, err := d.saveAlertJobs(ctx, m, jobs)
		o.Jobs, o.New, o.Pending = run.JobsFetched, run.JobsNew, run.JobsPending
		storeErr = err
	default:
		c, ok := alert104.ParseContact(m.Subject, m.HTML)
		if !ok {
			o.Status, o.Reason = "ignored", "other_104_mail" // login codes, newsletters
			break
		}
		o.Status, o.Reason = "parsed", c.Kind
		storeErr = d.saveContact(ctx, m, c)
	}
	if storeErr != nil {
		o.Status, o.Reason = "failed", "store_error"
		storeErr = fmt.Errorf("store %s: %w", key, storeErr)
	}
	if d.Metrics != nil {
		metrics.EmitMail(d.Metrics, o.Status, o.Status == "parsed" && o.Reason == "alert", d.Now())
	}
	d.Log.Info("mail ingested", "key", o.Key, "sender", o.Sender, "status", o.Status, "reason", o.Reason,
		"jobs", o.Jobs, "new", o.New, "pending", o.Pending)
	return o, storeErr
}

// classifyMail returns status "verified" for mail from 104 that passed the
// sender check, which the caller then reads.
func classifyMail(b []byte) (m *mailin.Message, sender, status, reason string) {
	if len(b) > maxMailBytes {
		return nil, "", "failed", "too_large"
	}
	m, err := mailin.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, "", "failed", err.Error()
	}
	sender = m.FromDomain()
	switch {
	case !isJobAlertSender(sender):
		return m, sender, "ignored", "not_a_job_alert_sender"
	case !verified(m):
		return m, sender, "ignored", "unverified_sender"
	}
	return m, sender, "verified", ""
}

func isJobAlertSender(domain string) bool {
	for _, s := range jobAlertSenders {
		if domain == s || strings.HasSuffix(domain, "."+s) {
			return true
		}
	}
	return false
}

// verified reports whether a check nobody outside can fake passed for the
// From domain: SES's DMARC result when 104 mailed us directly, or, for mail a
// forwarder redirected, the forwarder's DKIM signature on the copy plus the
// DMARC result its server recorded when the alert first arrived there.
//
// Each server adds its header on top, so SES's is the first and the
// forwarder's is the one just below it; a sender's fake headers can only sit
// lower. Position rather than server name identifies the forwarder's header,
// because Microsoft sometimes leaves its name out.
func verified(m *mailin.Message) bool {
	h, from := m.AuthResults, m.FromDomain()
	if len(h) == 0 || serverOf(h[0]) != "amazonses.com" {
		return false
	}
	ses := entries(h[0])
	if ses.pass("dmarc", "header.from", from) {
		return true
	}
	for _, domain := range forwarders {
		if len(h) > 1 && ses.pass("dkim", "header.i", "@"+domain) && entries(h[1]).pass("dmarc", "header.from", from) {
			return true
		}
	}
	return false
}

// results are the "method=result prop=value ..." entries of one
// Authentication-Results header, comments removed.
type results []string

var comment = regexp.MustCompile(`\([^)]*\)`)

// serverOf returns the server name a header starts with, or "" if it starts
// with a result.
func serverOf(h string) string {
	first, _, _ := strings.Cut(h, ";")
	if f := strings.Fields(first); len(f) > 0 && !strings.Contains(first, "=") {
		return strings.ToLower(f[0])
	}
	return ""
}

func entries(h string) results {
	parts := strings.Split(comment.ReplaceAllString(h, ""), ";")
	if serverOf(h) != "" {
		parts = parts[1:]
	}
	return parts
}

func (r results) pass(method, prop, value string) bool {
	for _, e := range r {
		f := strings.Fields(e)
		if len(f) == 0 || !strings.EqualFold(f[0], method+"=pass") {
			continue
		}
		for _, kv := range f[1:] {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, prop) && strings.EqualFold(v, value) {
				return true
			}
		}
	}
	return false
}

// saveAlertJobs records each job of one alert. A job already stored, from an
// earlier alert or from this one on a retry, is left as it was, so no job is
// sent twice. The run record feeds the digest's 104 line.
func (d Deps) saveAlertJobs(ctx context.Context, m *mailin.Message, jobs []alert104.Job) (store.Run, error) {
	now := d.Now()
	run := store.Run{ATS: alertATS, Task: "mail", StartedAt: now, JobsFetched: len(jobs), NewByClass: map[string]int{}}
	var errs []error
	for _, j := range jobs {
		rec := d.alertJob(j, m.Date, now)
		if rec.State == store.JobNew {
			run.JobsMatched++
		}
		isNew, err := d.Store.InsertJobIfNew(ctx, rec)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if isNew {
			run.JobsNew++
			run.NewByClass[rec.Class]++
			if rec.State == store.JobNew {
				run.JobsPending++
			}
		}
	}
	err := errors.Join(errs...)
	run.Status, run.FinishedAt = "ok", d.Now()
	if err != nil {
		run.Status, run.Error = "partial", err.Error()
	}
	if serr := d.Store.SaveRun(ctx, run); serr != nil {
		err = errors.Join(err, fmt.Errorf("save run: %w", serr))
	}
	return run, err
}

// alertJob judges one 104 job. The springboard rules veto titles and pay
// below the floor; what passes is ranked by the skills score plus 10 points
// for each plus keyword in the title.
func (d Deps) alertJob(j alert104.Job, posted, now time.Time) store.JobRecord {
	class, reason, mode := placeAlertJob(j)
	v := d.Springboard.Evaluate(j.Title, j.Salary)
	score, skills := d.Scorer.Score(j.Title, j.Description)
	state := store.JobSeen
	switch {
	case !v.Pass:
		reason = v.Reason
	case class != filter.ClassD:
		state = store.JobNew
	}
	return store.JobRecord{
		Key: alertATS + "#" + j.Code, ATS: alertATS, Board: j.Company, Title: j.Title, URL: j.URL(),
		Location: j.Location, WorkMode: mode, Class: string(class), Reason: reason,
		Score: min(score+10*len(v.Hits), 100), Skills: skills, Salary: j.Salary,
		Notes: notes(d.Cfg.Springboard.Notes, j), PostedAt: posted, FirstSeen: now, State: state,
	}
}

// saveContact records one contact, keyed by kind, company and job: a retried
// event, or a recruiter sending the same message twice (it happens, seconds
// apart), lists it once. Its job title goes through the same
// title rules, so a recruiter's 業務 role says so at a glance; an
// application the user sent is not judged.
func (d Deps) saveContact(ctx context.Context, m *mailin.Message, c alert104.Contact) error {
	rec := store.JobRecord{
		Key: strings.Join([]string{contactATS, c.Kind, c.Company, c.JobCode}, "#"), ATS: contactATS, Board: c.Company, Title: c.JobTitle, URL: c.URL(),
		Reason: c.Kind, PostedAt: m.Date, FirstSeen: d.Now(), State: store.JobNew,
	}
	if c.JobTitle != "" && c.Kind != "應徵已讀" {
		rec.Score, rec.Skills = d.Scorer.Score(c.JobTitle, "")
		if v := d.Springboard.Evaluate(c.JobTitle, ""); !v.Pass {
			rec.Notes = append(rec.Notes, "職稱含「"+strings.TrimPrefix(v.Reason, "title_excluded:")+"」")
		}
	}
	if c.ReplyBy != "" {
		rec.Notes = append(rec.Notes, c.ReplyBy)
	}
	_, err := d.Store.InsertJobIfNew(ctx, rec)
	return err
}

// placeAlertJob classes a 104 job by where it was listed. The saved searches
// are Taichung ones plus one for remote work anywhere in Taiwan, named with
// 遠端: its jobs are class A. A Taichung job is class B, since the commute and
// pay need a person's judgement. Anything else is one of 104's suggestions
// outside the searches: class D, stored but not sent.
func placeAlertJob(j alert104.Job) (class filter.Class, reason, mode string) {
	switch {
	case strings.Contains(j.Section, "遠端"):
		return filter.ClassA, "taiwan_remote", "remote"
	case strings.HasPrefix(j.Location, "台中"):
		return filter.ClassB, "taichung_onsite_manual_review", ""
	}
	return filter.ClassD, "outside_taichung", ""
}

// notes tags a job with each rule whose words appear in its title,
// description, pay or tags.
func notes(rules []config.NoteRule, j alert104.Job) []string {
	text := strings.Join(append([]string{j.Title, j.Description, j.Salary}, j.Tags...), "\n")
	var out []string
	for _, r := range rules {
		for _, w := range r.Words {
			if strings.Contains(text, w) {
				out = append(out, r.Name)
				break
			}
		}
	}
	return out
}
