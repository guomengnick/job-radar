package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// appliedATS is the source of the records of jobs the user applied to on
// 104, kept in the jobs table like any job so source-index lists them by
// time.
const appliedATS = "104-applied"

// ErrBadApplication means a report the extension sent is not a 104 job.
var ErrBadApplication = errors.New("bad application")

// Application is what the extension read off 104's 應徵成功 page
// (www.104.com.tw/job/apply/done/?jobNo=…).
type Application struct {
	Code    string `json:"code"`
	Title   string `json:"title"`
	Company string `json:"company"`
}

// RecordApplication stores an application 104 confirmed. The confirmation
// page can be opened again later, so the first record stays and a repeat
// changes and sends nothing; the first sends a short note on Telegram. It
// reports whether this was the first.
func RecordApplication(ctx context.Context, d Deps, n notify.Notifier, a Application) (bool, error) {
	if !jobCode.MatchString(a.Code) {
		return false, fmt.Errorf("%w: job code %q", ErrBadApplication, a.Code)
	}
	title := truncate(strings.TrimSpace(a.Title), 100)
	company := truncate(strings.TrimSpace(a.Company), 60)
	url := "https://www.104.com.tw/job/" + a.Code
	rec := store.JobRecord{
		Key: appliedATS + "#" + a.Code, ATS: appliedATS, Board: company, Title: title, URL: url,
		FirstSeen: d.Now(), State: store.JobSeen,
	}
	isNew, err := d.Store.InsertJobIfNew(ctx, rec)
	if err != nil || !isNew {
		return false, err
	}
	d.Log.Info("applied", "code", a.Code, "company", company)
	msg := fmt.Sprintf("📮 已投：<a href=\"%s\">%s</a>｜%s", url, html.EscapeString(titleOr(title, a.Code)), html.EscapeString(company))
	if err := n.Send(ctx, msg); err != nil {
		d.Log.Error("send applied note", "err", err)
	}
	return true, nil
}

// Applied is what the user applied to, for marking a job, or another job at
// the same company, wherever one is shown: a judgement, a batch summary, the
// digest. It holds every application record; there are a few hundred at
// most, so they are read whole and matched in memory rather than indexed by
// company.
type Applied struct {
	byJob     map[string]store.JobRecord   // by the 104 job key (alertATS#code)
	byCompany map[string][]store.JobRecord // by companyKey
}

// minContainedRunes is the shortest company name matched by containment: a
// shorter one (a bare 科技) would match too many. Equal names match at any
// length, so 凱納股份有限公司 still finds 凱納.
const minContainedRunes = 3

// LoadApplied reads every application on record.
func LoadApplied(ctx context.Context, d Deps) (Applied, error) {
	recs, err := d.Store.JobsSince(ctx, appliedATS, time.Time{})
	if err != nil {
		return Applied{}, fmt.Errorf("applications: %w", err)
	}
	return NewApplied(recs), nil
}

// NewApplied indexes application records; the zero Applied knows none.
func NewApplied(recs []store.JobRecord) Applied {
	a := Applied{byJob: map[string]store.JobRecord{}, byCompany: map[string][]store.JobRecord{}}
	for _, r := range recs {
		a.byJob[alertATS+"#"+strings.TrimPrefix(r.Key, appliedATS+"#")] = r
		if c := companyKey(r.Board); c != "" {
			a.byCompany[c] = append(a.byCompany[c], r)
		}
	}
	return a
}

// Note says when this job (by its job key) or the latest job at the same
// company was applied to, or "" if neither was. Companies match when their
// normalized names are equal, or when one contains the other and the
// shorter has minContainedRunes: 104 writes 準進科技有限公司 where a
// judgement reads 準進科技. Names across sites (凱納, Hyena) do not match.
func (a Applied) Note(jobKey, company string) string {
	if r, ok := a.byJob[jobKey]; ok {
		return fmt.Sprintf("⛔ 這個職缺 %s 已投過", appliedDay(r.FirstSeen))
	}
	c := companyKey(company)
	if c == "" {
		return ""
	}
	var same []store.JobRecord
	for k, rs := range a.byCompany {
		if k == c || sameCompany(k, c) {
			same = append(same, rs...)
		}
	}
	if len(same) == 0 {
		return ""
	}
	last := same[0]
	for _, r := range same[1:] {
		if r.FirstSeen.After(last.FirstSeen) {
			last = r
		}
	}
	note := fmt.Sprintf("⚠️ 此公司 %s 投過：%s", appliedDay(last.FirstSeen), truncate(titleOr(last.Title, last.URL), 40))
	if len(same) > 1 {
		note += fmt.Sprintf("（共 %d 個職缺）", len(same))
	}
	return note
}

// sameCompany reports whether one name contains the other, the shorter
// being long enough to mean something.
func sameCompany(a, b string) bool {
	if utf8.RuneCountInString(a) > utf8.RuneCountInString(b) {
		a, b = b, a
	}
	return utf8.RuneCountInString(a) >= minContainedRunes && strings.Contains(b, a)
}

// taiwanTime shows dates as the user saw them on 104. Taiwan keeps no
// daylight saving, so a fixed zone needs no time zone database.
var taiwanTime = time.FixedZone("CST", 8*60*60)

func appliedDay(t time.Time) string { return t.In(taiwanTime).Format("01/02") }

// legalForms end a registered company name; what follows them (台中分公司,
// 台灣分公司) names a branch of the same company.
var legalForms = []string{"股份有限公司", "有限公司"}

// companyKey normalizes a company name for matching: full-width letters made
// half-width, spaces dropped, lower case, and the legal form and anything
// after it cut. What is left before it (美商、an English name and "_")
// stays: matching by containment already sees past it.
func companyKey(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= '！' && r <= '～':
			return unicode.ToLower(r - 0xFEE0)
		case unicode.IsSpace(r):
			return -1
		}
		return unicode.ToLower(r)
	}, s)
	for _, f := range legalForms {
		if i := strings.Index(s, f); i > 0 {
			s = s[:i]
		}
	}
	return s
}
