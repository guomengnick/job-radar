package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestImportApplications(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	in := "# time\tcode\tcompany\ttitle\n" +
		"2026-10-28 11:20\t9bbb1\t美商範例雲端有限公司台灣分公司\t資深後端資料工程師\n" +
		"\n" +
		"2026-10-20 22:37\t\t範例精密有限公司\t資深軟體工程師(C#)\r\n"
	apps, err := ParseApplications(strings.NewReader(in))
	if err != nil || len(apps) != 2 {
		t.Fatalf("%+v %v", apps, err)
	}
	if added, err := ImportApplications(ctx, d, apps); err != nil || added != 2 {
		t.Fatalf("added %d, %v", added, err)
	}
	// A second import adds nothing.
	if added, err := ImportApplications(ctx, d, apps); err != nil || added != 0 {
		t.Fatalf("again: added %d, %v", added, err)
	}
	recs, _ := st.JobsSince(ctx, appliedATS, time.Time{})
	if len(recs) != 2 {
		t.Fatalf("records %+v", recs)
	}
	nocode, coded := recs[0], recs[1] // oldest first
	if !nocode.FirstSeen.Equal(time.Date(2026, 10, 20, 14, 37, 0, 0, time.UTC)) || nocode.URL != "" ||
		!strings.HasPrefix(nocode.Key, "104-applied#nocode-") || nocode.Board != "範例精密有限公司" {
		t.Errorf("no code: %+v", nocode)
	}
	if coded.Key != "104-applied#9bbb1" || coded.URL != "https://www.104.com.tw/job/9bbb1" {
		t.Errorf("coded: %+v", coded)
	}
	// Imported, the company is marked: the case this was for.
	a, err := LoadApplied(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Note("104#8zzz9", "範例精密"); got != "⚠️ 此公司 10/20 投過：資深軟體工程師(C#)" {
		t.Errorf("note %q", got)
	}
}

func TestParseApplicationsRejects(t *testing.T) {
	for _, in := range []string{
		"2026-10-20 22:37\t範例精密有限公司\t資深軟體工程師\n", // 3 fields
		"10/20 22:37\t\t範例精密有限公司\t資深軟體工程師\n",
		"2026-10-20 22:37\t../x\t範例精密有限公司\t資深軟體工程師\n",
		"2026-10-20 22:37\t\t\t資深軟體工程師\n",
	} {
		if _, err := ParseApplications(strings.NewReader(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}
