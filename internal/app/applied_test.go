package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

func TestRecordApplication(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	tg := &sent{}
	a := Application{Code: "9bbb1", Title: " 資深後端資料工程師 <Remote> ", Company: "美商範例雲端有限公司台灣分公司"}
	first, err := RecordApplication(ctx, d, tg, a)
	if err != nil || !first {
		t.Fatalf("first = %v, %v", first, err)
	}
	if len(tg.msgs) != 1 || tg.msgs[0] != `📮 已投：<a href="https://www.104.com.tw/job/9bbb1">資深後端資料工程師 &lt;Remote&gt;</a>｜美商範例雲端有限公司台灣分公司` {
		t.Fatalf("telegram %q", tg.msgs)
	}
	// Reopening the confirmation page records and sends nothing.
	clk.t = clk.t.Add(time.Hour)
	if again, err := RecordApplication(ctx, d, tg, a); err != nil || again || len(tg.msgs) != 1 {
		t.Fatalf("again = %v, %v, sent %d", again, err, len(tg.msgs))
	}
	recs, _ := st.JobsSince(ctx, appliedATS, clk.t.Add(-48*time.Hour))
	if len(recs) != 1 || recs[0].Key != "104-applied#9bbb1" || recs[0].Board != "美商範例雲端有限公司台灣分公司" ||
		recs[0].Title != "資深後端資料工程師 <Remote>" || !recs[0].FirstSeen.Equal(clk.t.Add(-time.Hour)) {
		t.Fatalf("records %+v", recs)
	}
	// Not a 104 job: refused, nothing stored.
	for _, code := range []string{"", "../x", "ABC", "9bbb1?x=1"} {
		if _, err := RecordApplication(ctx, d, tg, Application{Code: code}); !errors.Is(err, ErrBadApplication) {
			t.Errorf("%q: err = %v", code, err)
		}
	}
}

func applied(code, company, title string, at time.Time) store.JobRecord {
	return store.JobRecord{Key: appliedATS + "#" + code, ATS: appliedATS, Board: company, Title: title,
		URL: "https://www.104.com.tw/job/" + code, FirstSeen: at, State: store.JobSeen}
}

func TestAppliedNote(t *testing.T) {
	day := time.Date(2026, 10, 20, 14, 37, 0, 0, time.UTC) // 22:37 in Taiwan
	a := NewApplied([]store.JobRecord{
		applied("8abc1", "範例精密有限公司", "資深軟體工程師(C#)", day),
		applied("9aaa1", "範例金控_範例金融控股股份有限公司", "Sr. Data R&D Engineer", day),
		// 00:30 the next day in Taiwan: the date is Taiwan's.
		applied("9aaa2", "範例金控_範例金融控股股份有限公司", "Senior ML Engineer", day.Add(2*time.Hour)),
		applied("9bbb1", "美商範例雲端有限公司台灣分公司", "Senior Backend Data Engineer", day),
		applied("9ccc1", "範例光電股份有限公司台中分公司", "AI工程師", day),
		applied("8ddd1", "甲乙股份有限公司", "Backend Engineer", day),
	})
	for _, c := range []struct{ key, company, want string }{
		{"104#8abc1", "範例精密", "⛔ 這個職缺 10/20 已投過"},
		{"104#8zzz9", "範例精密有限公司", "⚠️ 此公司 10/20 投過：資深軟體工程師(C#)"},
		{"", "範例金控", "⚠️ 此公司 10/21 投過：Senior ML Engineer（共 2 個職缺）"},
		{"", "範例雲端", "⚠️ 此公司 10/20 投過：Senior Backend Data Engineer"},
		{"", "範例光電股份有限公司", "⚠️ 此公司 10/20 投過：AI工程師"},
		{"", "ＥＸＡＭＰＬＥ", ""}, // names across sites do not match
		{"", "甲乙股份有限公司", "⚠️ 此公司 10/20 投過：Backend Engineer"}, // equal names match at any length
		{"", "甲乙", "⚠️ 此公司 10/20 投過：Backend Engineer"},
		{"", "甲乙科技", ""}, // contains 甲乙, too short to match by containment
		{"", "科技", ""},
		{"104#zzz01", "", ""},
	} {
		if got := a.Note(c.key, c.company); got != c.want {
			t.Errorf("Note(%q, %q) = %q, want %q", c.key, c.company, got, c.want)
		}
	}
	if got := (Applied{}).Note("104#8abc1", "範例精密"); got != "" {
		t.Errorf("no applications: %q", got)
	}
}

func TestCompanyKey(t *testing.T) {
	for in, want := range map[string]string{
		"範例精密有限公司":                              "範例精密",
		"範例電子企業股份有限公司":                          "範例電子企業",
		"範例光電股份有限公司台中分公司":                       "範例光電",
		"Ｅｘａｍｐｌｅ　Ｉｎｃ．":                          "exampleinc.",
		"Example Consulting_印度商範例顧問服務有限公司台灣分公司": "exampleconsulting_印度商範例顧問服務",
	} {
		if got := companyKey(in); got != want {
			t.Errorf("companyKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClipNotesAnApplication(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	st.InsertJobIfNew(ctx, applied("bbb02", "範例科技有限公司", "韌體工程師", clk.t.Add(-48*time.Hour)))
	var out bytes.Buffer
	page := assess.Page{URL: "https://www.104.com.tw/job/aaa01", Title: "t", Text: "工作內容"}
	if _, err := Clip(ctx, d, &fakeAssessor{out: judged}, notify.Writer{W: &out}, &memArchive{}, Profile{}, page); err != nil {
		t.Fatal(err)
	}
	want := "｜範例科技\n⚠️ 此公司 10/10 投過：韌體工程師\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("reply missing %q:\n%s", want, out.String())
	}
}

func TestDigestNotesApplications(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	st.InsertJobIfNew(ctx, applied("aaa01", "範例科技股份有限公司", "資深後端工程師（.NET）", clk.t.Add(-time.Hour)))
	msg, err := Digest(ctx, d, notify.Writer{W: &bytes.Buffer{}}, DigestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "範例科技股份有限公司｜台中市西屯區｜月薪 60,000~90,000元\n⛔ 這個職缺 10/12 已投過\n"
	if !strings.Contains(msg, want) {
		t.Errorf("digest missing %q:\n%s", want, msg)
	}
}
