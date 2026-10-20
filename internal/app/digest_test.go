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

func TestDigestCapsAlertJobsSeparately(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	d.Cfg.Notify.MaxItems = 1
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	msg, err := Digest(ctx, d, notify.Writer{W: &out}, DigestOptions{})
	if err != nil || !strings.Contains(msg, "<b>104 配對信：1 筆</b>") || !strings.Contains(msg, "另有 1 筆留到下次") {
		t.Fatalf("%v\n%s", err, msg)
	}
	if p, _ := st.PendingJobs(ctx); len(p) != 1 {
		t.Errorf("the job over the cap stays pending: %d", len(p))
	}
}
