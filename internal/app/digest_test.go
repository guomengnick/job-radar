package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

func TestDigestShowsAlertJobs(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	var out bytes.Buffer
	msg, err := Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{DryRun: true})
	if err != nil || !strings.Contains(msg, "📭 104 配對信") || strings.Contains(msg, "<b>104 配對信") {
		t.Fatalf("before any alert: %v\n%s", err, msg)
	}

	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	msg, err = Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"📬 104 配對信 1 封｜職缺 4｜第一次出現 4｜值得看 2",
		"<b>104 配對信：2 筆</b>",
		`<a href="https://www.104.com.tw/job/aaa01">資深後端工程師（.NET）</a>`,
		"範例科技股份有限公司｜台中市西屯區｜月薪 60,000~90,000元",
		"104 的 A＝遠端，B＝台中到班。",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("digest missing %q:\n%s", want, msg)
		}
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 0 {
		t.Error("digest should mark the 104 jobs notified")
	}
}

func TestDigestListsEveryAlertJob(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	d.Cfg.Notify.MaxItems = 1
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	msg, err := Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{})
	if err != nil || !strings.Contains(msg, "<b>104 配對信：2 筆</b>") || strings.Contains(msg, "留到下次") {
		t.Fatalf("%v\n%s", err, msg)
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 0 {
		t.Errorf("no 104 job waits for the next digest: %d pending", len(p))
	}
}

func TestDigestOpensWithContacts(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	const from = "jobbank@ms1.104.com.tw"
	for key, subject := range map[string]string{
		"inbound/read":   "您主動應徵範例數據有限公司資料工程師職務的信函已被讀取",
		"inbound/invite": "【範例科技股份有限公司】發給您的【面試邀約】",
		"inbound/steel":  "台中範例鋼鐵求才通知",
	} {
		if _, err := IngestMail(ctx, d, key, strings.NewReader(mailWith(sesDMARCPass, from, subject, contactHTML(t)))); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	msg, err := Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"<b>👋 有人來找你：3 則</b>",
		"面試邀約｜範例科技股份有限公司\n" + `<a href="https://www.104.com.tw/job/aaa01">後端工程師（.NET）</a>` +
			"\n注意：期待 1 天 (2026-10-21) 內能收到您的回覆。",
		"求才通知｜台中範例鋼鐵\n",
		"應徵已讀｜範例數據有限公司\n資料工程師\n",
		"<b>掃描狀況</b>",
	}
	last := -1
	for _, w := range want {
		i := strings.Index(msg, w)
		if i < 0 || i < last {
			t.Fatalf("digest missing %q, or out of order:\n%s", w, msg)
		}
		last = i
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 0 {
		t.Error("digest should mark contacts notified")
	}
}

func TestDigestListsLeftOutAlertJobs(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	st.InsertJobIfNew(ctx, store.JobRecord{Key: "104#ddd01", ATS: alertATS, Title: "後端工程師", Board: "台北範例",
		URL: "https://www.104.com.tw/job/ddd01", Class: "D", Reason: "outside_taichung", State: store.JobSeen, FirstSeen: clk.t})
	// Left out yesterday, so already listed by yesterday's digest.
	st.InsertJobIfNew(ctx, store.JobRecord{Key: "104#eee01", ATS: alertATS, Title: "業務專員", Reason: "title_excluded:業務",
		State: store.JobSeen, FirstSeen: clk.t.Add(-25 * time.Hour)})
	clk.t = clk.t.Add(time.Hour)

	msg, err := Digest(ctx, d, notify.Writer{W: &bytes.Buffer{}}, DigestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"<b>104 配對信：2 筆</b>",
		"<b>🚫 規則排除（沒送出）：2 筆</b>\n請抽查有沒有誤刪。\n" +
			`<a href="https://www.104.com.tw/job/aaa02">PLC 控制工程師</a>｜範例機械有限公司｜職稱含「plc」` + "\n" +
			`<a href="https://www.104.com.tw/job/bbb01">資料工程師（完全遠端）</a>｜示範數據有限公司｜薪資低於門檻` + "\n",
		"<b>📍 台中以外、非遠端（104 推薦的）：1 筆</b>\n" +
			`<a href="https://www.104.com.tw/job/ddd01">後端工程師</a>｜台北範例｜台中以外、非遠端` + "\n",
	}
	last := -1
	for _, w := range want {
		i := strings.Index(msg, w)
		if i < 0 || i < last {
			t.Fatalf("digest missing %q, or out of order:\n%s", w, msg)
		}
		last = i
	}
	if strings.Contains(msg, "業務專員") {
		t.Errorf("a job left out before the window is not listed again:\n%s", msg)
	}
}

// failingJobsSince is a store whose JobsSince always fails.
type failingJobsSince struct{ store.Store }

func (failingJobsSince) JobsSince(context.Context, string, time.Time) ([]store.JobRecord, error) {
	return nil, errors.New("index not ready")
}

func TestDigestSurvivesLeftOutError(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	d.Store = failingJobsSince{st}
	var out bytes.Buffer
	msg, err := Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{})
	if err != nil || !strings.Contains(msg, "<b>104 配對信：2 筆</b>") || !strings.Contains(msg, "⚠️ 沒列出的 104 職缺讀取失敗") {
		t.Fatalf("the digest goes out with a warning: %v\n%s", err, msg)
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 0 {
		t.Error("the sent jobs are still marked notified")
	}
}
