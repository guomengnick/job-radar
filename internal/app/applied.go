package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

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
