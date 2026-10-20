package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/guomengnick/job-radar/internal/notify"
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
