package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// Assessor judges one page; assess.Client is the real one.
type Assessor interface {
	Assess(ctx context.Context, profile string, p assess.Page) (assess.Assessment, assess.Usage, error)
}

// ErrBadClip means the request cannot be judged (not a web page, or empty).
var ErrBadClip = errors.New("bad clip")

var job104 = regexp.MustCompile(`^https?://www\.104\.com\.tw/job/([0-9a-z]+)`)

// Clip judges a page the user sent with the 送到雷達 button and replies on
// Telegram. The page is kept (jdName) and the judgement stored with a
// pointer to it, for later comparison with what the user applied to; a
// failure to keep either is logged, since the reply matters more.
func Clip(ctx context.Context, d Deps, a Assessor, n notify.Notifier, archive PageArchive, profile string, p assess.Page) (assess.Assessment, error) {
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return assess.Assessment{}, fmt.Errorf("%w: the url must be a web page", ErrBadClip)
	}
	if strings.TrimSpace(p.Text) == "" {
		return assess.Assessment{}, fmt.Errorf("%w: the page has no text", ErrBadClip)
	}
	now := d.Now()
	key := clipKey(u)
	jd := jdName(key)
	if err := archive.Save(ctx, jd, p); err != nil {
		d.Log.Error("keep page", "key", key, "err", err)
		jd = ""
	}
	j, usage, err := a.Assess(ctx, profile, p)
	if err != nil {
		d.Log.Error("assess failed", "key", key, "err", err)
		n.Send(ctx, fmt.Sprintf("📡 評分失敗：%s\n%s", html.EscapeString(p.Title), html.EscapeString(err.Error())))
		return assess.Assessment{}, err
	}
	d.Log.Info("page assessed", "key", key, "score", j.Score, "verdict", j.Verdict, "model", d.Cfg.Assess.Model,
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cache_read_tokens", usage.CacheReadTokens, "cache_write_tokens", usage.CacheWriteTokens)

	var rows [][]notify.Button
	if row := LabelButtons(key); row != nil {
		rows = [][]notify.Button{row}
	}
	if err := sendWithButtons(ctx, n, BuildAssessment(u.String(), j)+"\n"+usageLine(d.Cfg.Assess.Model, usage, false), rows); err != nil {
		return j, fmt.Errorf("send assessment: %w", err)
	}
	detail, _ := json.Marshal(j)
	rec := store.Assessment{
		Key: "assess#" + key, URL: u.String(), Title: j.JobTitle, Company: j.Company, Score: j.Score,
		Verdict: j.Verdict, Model: d.Cfg.Assess.Model, AssessedAt: now, Detail: string(detail), JD: jd,
	}
	if err := d.Store.SaveAssessment(ctx, rec); err != nil {
		d.Log.Error("save assessment", "key", key, "err", err)
	}
	return j, nil
}

// clipKey names the job a page shows: the 104 job key, matching the alert
// records, or the URL without its query and fragment.
func clipKey(u *url.URL) string {
	if m := job104.FindStringSubmatch(u.String()); m != nil {
		return alertATS + "#" + m[1]
	}
	c := *u
	c.RawQuery, c.Fragment = "", ""
	return c.String()
}

var verdictIcons = map[string]string{"建議投": "🟢", "可考慮": "🟡", "不建議": "🔴"}

// BuildAssessment renders the Telegram reply (HTML parse mode). Every field
// came from the model reading a third-party page, so all of it is escaped.
// The cover-letter sentence is in <code>, which Telegram copies on a tap.
func BuildAssessment(pageURL string, j assess.Assessment) string {
	e := html.EscapeString
	title := j.JobTitle
	if title == "" {
		title = "職缺"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%d 分｜%s</b>\n", verdictIcons[j.Verdict], j.Score, e(j.Verdict))
	fmt.Fprintf(&b, "<a href=\"%s\">%s</a>｜%s\n", e(pageURL), e(title), e(j.Company))
	fmt.Fprintf(&b, "方向 %d/10｜技能 %d/10｜%s｜薪資：%s\n", j.DirectionFit, j.SkillsFit, e(j.CompanyType), e(j.Salary))
	if len(j.RedFlags) > 0 {
		fmt.Fprintf(&b, "⚠️ %s\n", e(strings.Join(j.RedFlags, "；")))
	}
	if len(j.Evidence) > 0 {
		fmt.Fprintf(&b, "依據：「%s」\n", e(strings.Join(j.Evidence, "」「")))
	}
	resume := j.Resume + " 版"
	if j.AddMLLine {
		resume += "，加 ML 那條"
	}
	fmt.Fprintf(&b, "📄 履歷用 %s\n", e(resume))
	if j.LetterLine != "" {
		fmt.Fprintf(&b, "✉️ 推薦信對應句（點一下複製）：\n<code>%s</code>\n", e(j.LetterLine))
	}
	if j.Reason != "" {
		fmt.Fprintf(&b, "💬 %s\n", e(j.Reason))
	}
	if apply := ApplyURL(pageURL, j); apply != "" {
		fmt.Fprintf(&b, "👉 <a href=\"%s\">看職缺</a>｜<a href=\"%s\">帶入應徵</a>", e(pageURL), e(apply))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ApplyLinkMinScore is the lowest score whose reply carries the 帶入應徵
// link: 可考慮 and up. The user reads the form before sending it anyway.
const ApplyLinkMinScore = 55

// letterLineMax bounds the sentence carried in the link; the prompt asks for
// 40 characters, and a link stays well under what browsers and Telegram take.
const letterLineMax = 120

// ApplyURL is a 104 job page that opens its apply form (104's own
// ?apply=form) with a fragment the extension reads: which résumé version to
// choose and the cover-letter sentence. The fragment never reaches 104's
// server. It returns "" for other sites and for scores below
// ApplyLinkMinScore. The extension only fills the form; the user reviews it
// and presses 確認送出.
func ApplyURL(pageURL string, j assess.Assessment) string {
	m := job104.FindStringSubmatch(pageURL)
	if m == nil || j.Score < ApplyLinkMinScore {
		return ""
	}
	cv := j.Resume
	if j.AddMLLine {
		cv += "+ML"
	}
	v := url.Values{"cv": {cv}}
	if line := strings.TrimSpace(j.LetterLine); line != "" {
		v.Set("line", truncate(line, letterLineMax))
	}
	return "https://www.104.com.tw/job/" + m[1] + "?apply=form#radar-apply?" + v.Encode()
}
