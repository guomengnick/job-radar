package app

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// week is how far back the weekly report looks.
const week = 7 * 24 * time.Hour

// Weekly sends Sunday's report: what the week brought (new board jobs and
// 104 jobs, by where they ended up), who reached out, and how the runs went.
// It reads only what the digest reads, so it needs no new permissions.
func Weekly(ctx context.Context, d Deps, n notify.Notifier, loc *time.Location) (string, error) {
	now := d.Now()
	since := now.Add(-week)
	runs, err := d.Store.RunsSince(ctx, since)
	if err != nil {
		return "", err
	}
	alert, err := ListAlertJobs(ctx, d, since)
	if err != nil {
		return "", err
	}
	contacts, err := d.Store.JobsSince(ctx, contactATS, since)
	if err != nil {
		return "", err
	}
	msg := BuildWeekly(now.In(loc), runs, alert, contacts)
	return msg, n.Send(ctx, msg)
}

// BuildWeekly renders the weekly report (Telegram HTML).
func BuildWeekly(now time.Time, runs []store.Run, alert []ListedJob, contacts []store.JobRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>🗓 雷達週報 %s–%s</b>\n", now.Add(-week).Format("01/02"), now.Format("01/02"))

	// Board scans: new jobs and how many were worth sending, per ATS.
	type tally struct{ runs, ok, fresh, pending int }
	scans := map[string]*tally{}
	mail := 0
	for _, r := range runs {
		switch {
		case r.Task == "scan":
			t := scans[r.ATS]
			if t == nil {
				t = &tally{}
				scans[r.ATS] = t
			}
			t.runs++
			if r.Status == "ok" {
				t.ok++
			}
			t.fresh += r.JobsNew
			t.pending += r.JobsPending
		case r.Task == "mail" && r.ATS == alertATS:
			mail++
		}
	}
	b.WriteString("\n<b>國外職缺看板</b>\n")
	if len(scans) == 0 {
		b.WriteString("⚠️ 這週沒有掃描紀錄\n")
	}
	names := make([]string, 0, len(scans))
	for a := range scans {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		t := scans[a]
		fmt.Fprintf(&b, "%s：掃描 %d 次（成功 %d）｜新職缺 %d｜送到日報 %d\n", a, t.runs, t.ok, t.fresh, t.pending)
	}

	groups := map[string]int{}
	for _, j := range alert {
		groups[j.Group]++
	}
	fmt.Fprintf(&b, "\n<b>104 配對信：%d 封、%d 個職缺</b>\n", mail, len(alert))
	fmt.Fprintf(&b, "日報有列 %d｜台中以外 %d｜規則排除 %d\n", groups[GroupDigest], groups[GroupElsewhere], groups[GroupExcluded])

	kinds := map[string][]string{}
	for _, c := range contacts {
		kinds[c.Reason] = append(kinds[c.Reason], c.Board)
	}
	fmt.Fprintf(&b, "\n<b>👋 有人來找你：%d 則</b>\n", len(contacts))
	kindNames := make([]string, 0, len(kinds))
	for k := range kinds {
		kindNames = append(kindNames, k)
	}
	sort.SliceStable(kindNames, func(i, k int) bool { return rankContact(kindNames[i]) < rankContact(kindNames[k]) })
	for _, k := range kindNames {
		fmt.Fprintf(&b, "%s %d：%s\n", html.EscapeString(k), len(kinds[k]), html.EscapeString(strings.Join(dedupe(kinds[k]), "、")))
	}
	if len(contacts) == 0 {
		b.WriteString("這週沒有。\n")
	}
	b.WriteString("\n投了哪些、進度如何，在投遞清單裡更新。")
	return b.String()
}

func rankContact(kind string) int {
	if r, ok := contactOrder[kind]; ok {
		return r
	}
	return len(contactOrder)
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
