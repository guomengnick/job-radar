package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/store"
)

// Synthetic headers in the shapes SES and Microsoft write.
const (
	sesDMARCPass = "Authentication-Results: amazonses.com; spf=pass client-ip=192.0.2.1; dkim=pass header.i=@ms1.104.com.tw; dmarc=pass header.from=ms1.104.com.tw;\r\n"
	// After an Outlook redirect: Outlook's signature verifies, 104's does not.
	sesViaOutlook = "Authentication-Results: amazonses.com; spf=pass (spfCheck: domain of outlook.com designates 192.0.2.2 as permitted sender) client-ip=192.0.2.2; dkim=pass header.i=@outlook.com; dmarc=fail header.from=ms1.104.com.tw;\r\n"
	msPass        = "Authentication-Results: mx.microsoft.com 1; spf=pass (sender IP is 192.0.2.3) smtp.mailfrom=ms1.104.com.tw; dkim=pass (signature was verified) header.d=ms1.104.com.tw;dmarc=pass action=none header.from=ms1.104.com.tw;compauth=pass reason=100\r\n"
	// Microsoft sometimes leaves its server name out.
	msPassNoName = "Authentication-Results: spf=pass (sender IP is 192.0.2.3) smtp.mailfrom=104.com.tw; dkim=pass (signature was verified) header.d=104.com.tw;dmarc=pass action=none header.from=104.com.tw;compauth=pass reason=100\r\n"
	msFail       = "Authentication-Results: mx.microsoft.com 1; dkim=none; dmarc=fail (dmarc=pass header.from=ms1.104.com.tw) header.from=ms1.104.com.tw\r\n"
)

func mail(from, body string) string { return mailWith("", from, "s", body) }

func mailWith(headers, from, subject, body string) string {
	return headers + "From: " + from + "\r\nSubject: " + subject +
		"\r\nDate: Tue, 20 Oct 2026 02:26:00 +0800\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + body + "\r\n"
}

func alertHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../alert104/testdata/alert.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// recorder keeps every job inserted, including those not sent.
type recorder struct {
	store.Store
	jobs map[string]store.JobRecord
}

func (r *recorder) InsertJobIfNew(ctx context.Context, j store.JobRecord) (bool, error) {
	ok, err := r.Store.InsertJobIfNew(ctx, j)
	if ok {
		r.jobs[j.Key] = j
	}
	return ok, err
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestIngestMail(t *testing.T) {
	const from = "104人力銀行 <jobbank@ms1.104.com.tw>"
	alert := alertHTML(t)
	tests := []struct {
		name, raw, status, reason, sender string
	}{
		{"a direct alert is parsed", mailWith(sesDMARCPass, from, "小明10/20的配對日報", alert), "parsed", "alert", "ms1.104.com.tw"},
		{"an alert Outlook redirected is parsed", mailWith(sesViaOutlook+msPass, from, "小明10/20的配對日報", alert), "parsed", "alert", "ms1.104.com.tw"},
		{"a redirect of a forged alert is not", mailWith(sesViaOutlook+msFail, from, "配對日報", alert), "ignored", "unverified_sender", "ms1.104.com.tw"},
		{"Microsoft's header may lack its name", mailWith(strings.ReplaceAll(sesViaOutlook, "ms1.104", "104")+msPassNoName, "jobbank@104.com.tw", "配對週報", alert), "parsed", "alert", "104.com.tw"},
		{"only the header just below SES's counts", mailWith(sesViaOutlook+msFail+msPass, from, "配對日報", alert), "ignored", "unverified_sender", "ms1.104.com.tw"},
		{"SES's header must come first", mailWith(msPass+sesViaOutlook, from, "配對日報", alert), "ignored", "unverified_sender", "ms1.104.com.tw"},
		{"a Microsoft header without Outlook's signature does not count", mailWith(msPass, from, "配對日報", alert), "ignored", "unverified_sender", "ms1.104.com.tw"},
		{"no checks at all", mail("noreply@104.com.tw", alert), "ignored", "unverified_sender", "104.com.tw"},
		{"a recruiter's message is a contact", mailWith(sesDMARCPass, from, "【某公司】發給您的【面試邀約】", "<p>hi</p>"), "parsed", "面試邀約", "ms1.104.com.tw"},
		{"a forged recruiter's message is not", mailWith(sesViaOutlook+msFail, from, "【某公司】發給您的【面試邀約】", "<p>hi</p>"), "ignored", "unverified_sender", "ms1.104.com.tw"},
		{"a login code is ignored", mailWith(sesDMARCPass, from, "【123456】 是你的104登入驗證碼", "<p>123456</p>"), "ignored", "other_104_mail", "ms1.104.com.tw"},
		{"an alert with no jobs fails", mailWith(sesDMARCPass, from, "配對週報", "<p>新版面</p>"), "failed", "no jobs found: the 104 layout may have changed", "ms1.104.com.tw"},
		{"a lookalike domain is ignored", mail("noreply@fake104.com.tw", alert), "ignored", "not_a_job_alert_sender", "fake104.com.tw"},
		{"a test message is ignored", mail("someone@example.com", "<p>hi</p>"), "ignored", "not_a_job_alert_sender", "example.com"},
		{"garbage is reported, not returned", "not a message", "failed", "", ""},
		{"too large", mail("noreply@104.com.tw", strings.Repeat("x", maxMailBytes)), "failed", "too_large", ""},
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
			if o.Status != tt.status || o.Sender != tt.sender || o.Key != "inbound/k" ||
				(tt.reason != "" && o.Reason != tt.reason) {
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

func TestIngestMailStoresAlertJobs(t *testing.T) {
	d, _, mem, _ := setup(t)
	st := &recorder{Store: mem, jobs: map[string]store.JobRecord{}}
	d.Store = st
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	o, err := IngestMail(context.Background(), d, "inbound/k", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if o.Jobs != 4 || o.New != 4 || o.Pending != 2 {
		t.Fatalf("outcome = %+v", o)
	}
	pending, err := st.PendingJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.JobRecord{}
	for _, j := range pending {
		got[j.Key] = j
	}
	a := got["104#aaa01"]
	if a.Class != "B" || a.Board != "範例科技股份有限公司" || a.Salary != "月薪 60,000~90,000元" ||
		a.URL != "https://www.104.com.tw/job/aaa01" || a.Score < 20 || a.PostedAt.IsZero() {
		t.Errorf("Taichung job = %+v", a)
	}
	if _, ok := got["104#ccc01"]; !ok || len(got) != 2 {
		t.Errorf("pending = %v; want aaa01 and the Taichung suggestion ccc01", got)
	}

	// The PLC title and the low part-time pay are kept but not sent; the
	// part-time job is tagged.
	all := st.jobs
	if r := all["104#aaa02"]; r.State != store.JobSeen || r.Reason != "title_excluded:plc" {
		t.Errorf("PLC job = %+v", r)
	}
	if r := all["104#bbb01"]; r.State != store.JobSeen || r.Reason != "salary_below_floor" ||
		r.Class != "A" || len(r.Notes) != 1 || r.Notes[0] != "兼職" {
		t.Errorf("part-time remote job = %+v", r)
	}

	// The same mail again (a retry, or the job listed in a later alert)
	// inserts nothing.
	o, err = IngestMail(context.Background(), d, "inbound/k", strings.NewReader(raw))
	if err != nil || o.New != 0 || o.Pending != 0 {
		t.Errorf("second ingest = %+v, %v", o, err)
	}
	runs, _ := st.RunsSince(context.Background(), d.Now().Add(-24*time.Hour))
	if len(runs) != 2 || runs[0].ATS != "104" || runs[0].Task != "mail" || runs[0].JobsPending != 2 {
		t.Errorf("runs = %+v", runs)
	}
}

func TestIngestMailReturnsReadErrors(t *testing.T) {
	d, _, _, _ := setup(t)
	if _, err := IngestMail(context.Background(), d, "inbound/k", failingReader{}); err == nil {
		t.Fatal("a read error must be returned so the invocation is retried")
	}
}

func contactHTML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../alert104/testdata/contact.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestIngestMailStoresContacts(t *testing.T) {
	ctx := context.Background()
	d, _, mem, _ := setup(t)
	st := &recorder{Store: mem, jobs: map[string]store.JobRecord{}}
	d.Store = st
	const from = "jobbank@ms1.104.com.tw"
	mails := map[string]string{
		"inbound/invite": mailWith(sesDMARCPass, from, "【範例科技股份有限公司】發給您的【面試邀約】", contactHTML(t)),
		"inbound/sales": mailWith(sesDMARCPass, from, "【範例旅行社股份有限公司】發給您的【詢問意願】",
			`<table><tr><td class="update-announcement"><a href="https://www.104.com.tw/job/bbb02">業務專員</a></td></tr></table>`),
		"inbound/read": mailWith(sesDMARCPass, from, "您主動應徵範例數據有限公司資料工程師職務的信函已被讀取", "<p>x</p>"),
	}
	for key, raw := range mails {
		o, err := IngestMail(ctx, d, key, strings.NewReader(raw))
		if err != nil || o.Status != "parsed" {
			t.Fatalf("%s: %+v %v", key, o, err)
		}
	}
	invite := st.jobs["104-contact#面試邀約#範例科技股份有限公司#aaa01"]
	if invite.Reason != "面試邀約" || invite.Board != "範例科技股份有限公司" || invite.URL != "https://www.104.com.tw/job/aaa01" ||
		invite.State != store.JobNew || len(invite.Notes) != 1 || invite.Notes[0] != "期待 1 天 (2026-10-21) 內能收到您的回覆。" {
		t.Errorf("invitation = %+v", invite)
	}
	if n := st.jobs["104-contact#詢問意願#範例旅行社股份有限公司#bbb02"].Notes; len(n) != 1 || n[0] != "職稱含「業務」" {
		t.Errorf("a 業務 role is flagged: %v", n)
	}
	if r := st.jobs["104-contact#應徵已讀#範例數據有限公司#"]; r.Reason != "應徵已讀" || r.Title != "資料工程師" || len(r.Notes) != 0 {
		t.Errorf("read notice = %+v", r)
	}

	// A retried event, or the same message sent again, lists it once.
	for _, key := range []string{"inbound/invite", "inbound/invite-again"} {
		if _, err := IngestMail(ctx, d, key, strings.NewReader(mails["inbound/invite"])); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 3 {
		t.Errorf("pending = %d, want 3", len(p))
	}
}
