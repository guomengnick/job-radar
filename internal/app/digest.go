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

type DigestOptions struct {
	Window      time.Duration // how far back to report runs
	ExpectedATS []string      // an ATS with no run in the window is reported as missing
	DryRun      bool          // build and send the message but leave jobs pending
	Location    *time.Location
}

// Digest sends one message: what each scan did (the "dashboard" on the
// phone) followed by the new postings above the notify threshold.
func Digest(ctx context.Context, d Deps, n notify.Notifier, o DigestOptions) (string, error) {
	now := d.Now()
	if o.Window == 0 {
		o.Window = 24 * time.Hour
	}
	if o.Location == nil {
		o.Location = time.UTC
	}
	runs, err := d.Store.RunsSince(ctx, now.Add(-o.Window))
	if err != nil {
		return "", err
	}
	pending, err := d.Store.PendingJobs(ctx)
	if err != nil {
		return "", err
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].Class != pending[j].Class {
			return pending[i].Class < pending[j].Class
		}
		return pending[i].Score > pending[j].Score
	})
	// Job-board roles are capped at MaxItems; every 104 alert job is listed
	// (the user's choice), and they do not count toward the cap, so a busy
	// alert day cannot crowd out the remote roles. Telegram gets the long
	// message in parts.
	var boardJobs, alertJobs []store.JobRecord
	for _, j := range pending {
		if j.ATS == alertATS {
			alertJobs = append(alertJobs, j)
		} else {
			boardJobs = append(boardJobs, j)
		}
	}
	shown := append(boardJobs[:min(len(boardJobs), d.Cfg.Notify.MaxItems)], alertJobs...)

	msg := BuildDigest(now.In(o.Location), runs, shown, len(pending)-len(shown), o.ExpectedATS)
	if err := n.Send(ctx, msg); err != nil {
		return msg, err
	}
	if o.DryRun {
		return msg, nil
	}
	keys := make([]string, len(shown))
	for i, j := range shown {
		keys[i] = j.Key
	}
	return msg, d.Store.MarkNotified(ctx, keys, now)
}

// BuildDigest renders the Telegram message (HTML parse mode).
func BuildDigest(now time.Time, runs []store.Run, jobs []store.JobRecord, more int, expected []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>📡 職缺雷達 %s</b>\n\n<b>掃描狀況</b>\n", now.Format("01/02 15:04"))

	latest := map[string]store.Run{}
	for _, r := range runs {
		if r.Task != "scan" {
			continue
		}
		if cur, ok := latest[r.ATS]; !ok || r.StartedAt.After(cur.StartedAt) {
			latest[r.ATS] = r
		}
	}
	names := append([]string{}, expected...)
	for a := range latest {
		if !contains(names, a) {
			names = append(names, a)
		}
	}
	sort.Strings(names)
	newByClass := map[string]int{}
	for _, a := range names {
		r, ok := latest[a]
		if !ok {
			fmt.Fprintf(&b, "⚠️ %s：這段時間沒有執行紀錄\n", a)
			continue
		}
		icon := map[string]string{"ok": "✅", "partial": "🟡", "failed": "❌"}[r.Status]
		failed := 0
		for k, v := range r.Outcomes {
			if k != "ok" && k != "not_found" && k != "throttled" {
				failed += v
			}
		}
		fmt.Fprintf(&b, "%s %s %s｜看板 %d/%d｜404 %d｜限速 %d｜其他失敗 %d｜新職缺 %d\n",
			icon, a, fmtDuration(r.Duration()), r.BoardsChecked, r.BoardsDue,
			r.Outcomes["not_found"], r.Outcomes["throttled"], failed, r.JobsNew)
		if r.NewlyDead > 0 || r.Revived > 0 {
			fmt.Fprintf(&b, "   看板新判定死亡 %d、復活 %d\n", r.NewlyDead, r.Revived)
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "   錯誤：%s\n", html.EscapeString(truncate(r.Error, 200)))
		}
		for c, v := range r.NewByClass {
			newByClass[c] += v
		}
	}
	if len(newByClass) > 0 {
		fmt.Fprintf(&b, "新出現（含未達門檻）：A %d｜B %d｜C %d｜D %d\n",
			newByClass["A"], newByClass["B"], newByClass["C"], newByClass["D"])
	}
	writeMailLine(&b, runs)

	var boardJobs, alertJobs []store.JobRecord
	for _, j := range jobs {
		if j.ATS == alertATS {
			alertJobs = append(alertJobs, j)
		} else {
			boardJobs = append(boardJobs, j)
		}
	}
	fmt.Fprintf(&b, "\n<b>值得看的新職缺：%d 筆</b>\n", len(boardJobs))
	if len(boardJobs) == 0 {
		b.WriteString("今天沒有。\n")
	}
	for _, j := range boardJobs {
		fmt.Fprintf(&b, "\n[%s %d] <a href=\"%s\">%s</a>\n%s｜%s\n",
			j.Class, j.Score, html.EscapeString(j.URL), html.EscapeString(j.Title),
			html.EscapeString(j.Board), html.EscapeString(truncate(j.Location, 80)))
		if len(j.Skills) > 0 {
			fmt.Fprintf(&b, "技能：%s\n", html.EscapeString(strings.Join(j.Skills, "、")))
		}
		fmt.Fprintf(&b, "判斷：%s\n", reasonText(j.Reason))
	}
	if len(alertJobs) > 0 {
		fmt.Fprintf(&b, "\n<b>104 配對信：%d 筆</b>\n", len(alertJobs))
	}
	for _, j := range alertJobs {
		fmt.Fprintf(&b, "\n[%s %d] <a href=\"%s\">%s</a>\n%s｜%s｜%s\n",
			j.Class, j.Score, html.EscapeString(j.URL), html.EscapeString(j.Title),
			html.EscapeString(j.Board), html.EscapeString(j.Location), html.EscapeString(j.Salary))
		if len(j.Skills) > 0 {
			fmt.Fprintf(&b, "技能：%s\n", html.EscapeString(strings.Join(j.Skills, "、")))
		}
		if len(j.Notes) > 0 {
			fmt.Fprintf(&b, "注意：%s\n", html.EscapeString(strings.Join(j.Notes, "、")))
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n另有 %d 筆留到下次。\n", more)
	}
	b.WriteString("\nA＝明確可從台灣應徵；B＝待確認。")
	if len(alertJobs) > 0 {
		b.WriteString("104 的 A＝遠端，B＝台中到班。")
	}
	b.WriteString("最後投不投，請貼網址做完整評分。")
	return b.String()
}

// writeMailLine sums the 104 alerts received in the window. 104 sends the
// daily alert Tuesday to Saturday and the weekly one on Sunday, so a Monday
// without mail is normal.
func writeMailLine(b *strings.Builder, runs []store.Run) {
	var mails, listed, fresh, pending int
	var errs []string
	for _, r := range runs {
		if r.Task != "mail" {
			continue
		}
		mails++
		listed += r.JobsFetched
		fresh += r.JobsNew
		pending += r.JobsPending
		if r.Error != "" {
			errs = append(errs, r.Error)
		}
	}
	if mails == 0 {
		b.WriteString("📭 104 配對信：這段時間沒有收到\n")
		return
	}
	fmt.Fprintf(b, "📬 104 配對信 %d 封｜職缺 %d｜第一次出現 %d｜值得看 %d\n", mails, listed, fresh, pending)
	for _, e := range errs {
		fmt.Fprintf(b, "   寫入錯誤：%s\n", html.EscapeString(truncate(e, 200)))
	}
}

var reasons = map[string]string{
	"taiwan_remote":                 "台灣遠端",
	"hybrid_within_limit":           "台北／新竹混合，到班天數在 2 天內",
	"broad_remote_eligibility":      "地點寫全球／亞太／Anywhere",
	"jd_states_broad_eligibility":   "JD 寫可全球或亞太應徵",
	"remote_country_not_stated":     "Remote 但沒寫國家，需確認",
	"location_not_stated":           "地點沒寫，需確認",
	"hybrid_days_unknown":           "混合辦公，到班天數需確認",
	"taiwan_work_mode_unknown":      "台灣職缺，工作型態需確認",
	"taiwan_city_unclear":           "台灣職缺，城市需確認",
	"taichung_onsite_manual_review": "台中到班，只在待遇與契合度特別好時考慮",
	"taichung_hybrid_manual_review": "台中混合辦公，需人工判斷",
}

func reasonText(r string) string {
	if t, ok := reasons[r]; ok {
		return t
	}
	return r
}

func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
