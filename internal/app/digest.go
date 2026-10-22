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
	// Job-board roles are capped at MaxItems; every 104 alert job and contact
	// is listed (the user's choice), and they do not count toward the cap, so
	// a busy alert day cannot crowd out the remote roles. Telegram gets the
	// long message in parts.
	boardJobs, alertJobs, contacts := splitBySource(pending)
	shown := append(append(boardJobs[:min(len(boardJobs), d.Cfg.Notify.MaxItems)], alertJobs...), contacts...)

	// The 104 jobs the digest does not send are listed too, one line each,
	// so the user can check what the rules drop. Failing to read them only
	// costs that part of the message.
	left, leftErr := leftOutAlertJobs(ctx, d, now.Add(-o.Window))
	msg := BuildDigest(now.In(o.Location), runs, shown, left, len(pending)-len(shown), o.ExpectedATS)
	if leftErr != nil {
		d.Log.Error("digest: list left-out 104 jobs", "err", leftErr)
		msg += "\n⚠️ 沒列出的 104 職缺讀取失敗，原因見 CloudWatch。"
	}
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

// leftOutAlertJobs returns the 104 alert jobs first seen since the given
// time that the digest does not send: those a rule excluded, then those
// outside the searched places.
func leftOutAlertJobs(ctx context.Context, d Deps, since time.Time) ([]store.JobRecord, error) {
	recs, err := d.Store.JobsSince(ctx, alertATS, since)
	if err != nil {
		return nil, err
	}
	var excluded, elsewhere []store.JobRecord
	for _, r := range recs {
		switch alertGroup(r) {
		case GroupExcluded:
			excluded = append(excluded, r)
		case GroupElsewhere:
			elsewhere = append(elsewhere, r)
		}
	}
	return append(excluded, elsewhere...), nil
}

// BuildDigest renders the Telegram message (HTML parse mode). left holds the
// 104 alert jobs the digest does not send, listed one line each.
func BuildDigest(now time.Time, runs []store.Run, jobs, left []store.JobRecord, more int, expected []string) string {
	boardJobs, alertJobs, contacts := splitBySource(jobs)
	var b strings.Builder
	fmt.Fprintf(&b, "<b>📡 職缺雷達 %s</b>\n\n", now.Format("01/02 15:04"))
	writeContacts(&b, contacts)
	b.WriteString("<b>掃描狀況</b>\n")

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
	writeLeftOut(&b, left)
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

// splitBySource separates job-board roles, 104 alert jobs and 104 contacts,
// keeping each group's order.
func splitBySource(jobs []store.JobRecord) (board, alert, contacts []store.JobRecord) {
	for _, j := range jobs {
		switch j.ATS {
		case alertATS:
			alert = append(alert, j)
		case contactATS:
			contacts = append(contacts, j)
		default:
			board = append(board, j)
		}
	}
	return board, alert, contacts
}

// writeLeftOut lists, one line each, the 104 jobs a rule excluded and those
// outside the searched places, so a rule that drops good jobs shows.
func writeLeftOut(b *strings.Builder, left []store.JobRecord) {
	sections := []struct{ group, title, hint string }{
		{GroupExcluded, "🚫 規則排除（沒送出）", "請抽查有沒有誤刪。\n"},
		{GroupElsewhere, "📍 台中以外、非遠端（104 推薦的）", ""},
	}
	for _, sec := range sections {
		var group []store.JobRecord
		for _, j := range left {
			if alertGroup(j) == sec.group {
				group = append(group, j)
			}
		}
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(b, "\n<b>%s：%d 筆</b>\n%s", sec.title, len(group), sec.hint)
		for _, j := range group {
			fmt.Fprintf(b, "<a href=\"%s\">%s</a>｜%s｜%s\n", html.EscapeString(j.URL), html.EscapeString(j.Title),
				html.EscapeString(j.Board), html.EscapeString(reasonText(j.Reason)))
		}
	}
}

// contactOrder puts what needs a reply first.
var contactOrder = map[string]int{"面試邀約": 0, "詢問意願": 1, "求才通知": 2, "應徵已讀": 3}

// writeContacts opens the digest with the people who reached out, since an
// invitation may ask for a reply within a day.
func writeContacts(b *strings.Builder, contacts []store.JobRecord) {
	if len(contacts) == 0 {
		return
	}
	rank := func(kind string) int {
		if r, ok := contactOrder[kind]; ok {
			return r
		}
		return len(contactOrder)
	}
	sort.SliceStable(contacts, func(i, j int) bool { return rank(contacts[i].Reason) < rank(contacts[j].Reason) })
	fmt.Fprintf(b, "<b>👋 有人來找你：%d 則</b>\n", len(contacts))
	for _, c := range contacts {
		fmt.Fprintf(b, "\n%s｜%s\n", html.EscapeString(c.Reason), html.EscapeString(c.Board))
		switch {
		case c.URL != "":
			fmt.Fprintf(b, "<a href=\"%s\">%s</a>\n", html.EscapeString(c.URL), html.EscapeString(c.Title))
		case c.Title != "":
			fmt.Fprintf(b, "%s\n", html.EscapeString(c.Title))
		}
		if len(c.Notes) > 0 {
			fmt.Fprintf(b, "注意：%s\n", html.EscapeString(strings.Join(c.Notes, "｜")))
		}
	}
	b.WriteString("\n")
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
	"outside_taichung":              "台中以外、非遠端",
	"salary_below_floor":            "薪資低於門檻",
}

func reasonText(r string) string {
	if w, ok := strings.CutPrefix(r, "title_excluded:"); ok {
		return "職稱含「" + w + "」"
	}
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
