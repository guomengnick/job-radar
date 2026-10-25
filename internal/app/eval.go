package app

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/guomengnick/job-radar/internal/assess"
)

// PageSource reads back what a PageArchive kept: a judged page under jd/,
// or a batch as sent under batches/.
type PageSource interface {
	Load(ctx context.Context, name string, v any) error
	// List returns the names under a prefix, oldest first.
	List(ctx context.Context, prefix string) ([]string, error)
}

// EvalCase is one row of the user's labels: a job and how they judge it,
// either a verdict (投, 看, 不, or 建議投, 可考慮, 不建議) or a score.
type EvalCase struct {
	Key  string // the job's key, like 104#abc, or its URL
	Want string // 建議投 | 可考慮 | 不建議, or a score 0–100
	Note string
}

// EvalRow is a case judged again with the profile under test, next to the
// stored judgement.
type EvalRow struct {
	Case                   EvalCase
	Title                  string
	OldScore, NewScore     int
	OldVerdict, NewVerdict string
	Usage                  assess.Usage
	Err                    error
}

var wantVerdicts = map[string]string{"投": "建議投", "建議投": "建議投", "看": "可考慮", "可考慮": "可考慮", "不": "不建議", "不建議": "不建議"}

// ParseLabels reads a CSV with the header key,want,note. A row whose want is
// neither a verdict nor a score from 0 to 100 is an error, so a typo does not
// quietly count as a disagreement.
func ParseLabels(r io.Reader) ([]EvalCase, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // the note may be left off
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || strings.TrimSpace(strings.TrimPrefix(rows[0][0], "\ufeff")) != "key" {
		return nil, errors.New("the first row must be the header key,want,note")
	}
	var out []EvalCase
	for i, row := range rows[1:] {
		if len(row) < 2 || strings.TrimSpace(row[0]) == "" {
			continue
		}
		c := EvalCase{Key: jobKey(strings.TrimSpace(row[0])), Want: strings.TrimSpace(row[1])}
		if len(row) > 2 {
			c.Note = strings.TrimSpace(row[2])
		}
		if v, ok := wantVerdicts[c.Want]; ok {
			c.Want = v
		} else if n, err := strconv.Atoi(c.Want); err != nil || n < 0 || n > 100 {
			return nil, fmt.Errorf("row %d: want %q is not 投/看/不 or a score", i+2, c.Want)
		}
		out = append(out, c)
	}
	return out, nil
}

// jobKey accepts a key as stored (104#abc) or the job's URL.
func jobKey(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return clipKey(u)
	}
	return s
}

// Agrees reports whether a judgement matches the user's label: the same
// verdict, or a score within 10 points.
func (c EvalCase) Agrees(score int, verdict string) bool {
	if n, err := strconv.Atoi(c.Want); err == nil {
		return score-n <= 10 && n-score <= 10
	}
	return verdict == c.Want
}

// Evaluate judges each case's kept page again with the given profile, one at
// a time. A case whose page was not kept (judged before 2026-10-25 and not
// backfilled) is reported, not judged.
func Evaluate(ctx context.Context, d Deps, a Assessor, pages PageSource, profile string, cases []EvalCase) []EvalRow {
	out := make([]EvalRow, 0, len(cases))
	for _, c := range cases {
		row := EvalRow{Case: c}
		rec, ok, err := d.Store.AssessmentOf(ctx, "assess#"+c.Key)
		switch {
		case err != nil:
			row.Err = err
		case !ok:
			row.Err = errors.New("沒有評分紀錄")
		case rec.JD == "":
			row.Err = errors.New("沒有存 JD，先跑 radar backfill-jd")
		}
		if row.Err != nil {
			out = append(out, row)
			continue
		}
		row.Title, row.OldScore, row.OldVerdict = titleOr(rec.Title, c.Key), rec.Score, rec.Verdict
		var p assess.Page
		if err := pages.Load(ctx, rec.JD, &p); err != nil {
			row.Err = fmt.Errorf("讀 %s：%w", rec.JD, err)
			out = append(out, row)
			continue
		}
		j, u, err := a.Assess(ctx, profile, p)
		row.NewScore, row.NewVerdict, row.Usage, row.Err = j.Score, j.Verdict, u, err
		d.Log.Info("evaluated", "key", c.Key, "old", rec.Score, "new", j.Score, "err", err)
		out = append(out, row)
	}
	return out
}

// BuildEvalReport is the table the radar eval command prints: each case
// with the user's label, the stored score and the new one, then how many
// agree before and after, and what the run cost.
func BuildEvalReport(rows []EvalRow, model string) string {
	var b strings.Builder
	var total assess.Usage
	judged, oldAgree, newAgree := 0, 0, 0
	for _, r := range rows {
		if r.Err != nil {
			fmt.Fprintf(&b, "⚠️  %-8s %s：%v\n", r.Case.Want, r.Case.Key, r.Err)
			continue
		}
		judged++
		mark := "❌"
		if r.Case.Agrees(r.NewScore, r.NewVerdict) {
			mark = "✅"
			newAgree++
		}
		if r.Case.Agrees(r.OldScore, r.OldVerdict) {
			oldAgree++
		}
		fmt.Fprintf(&b, "%s %-8s %3d %s → %3d %s  %s\n", mark, r.Case.Want, r.OldScore, r.OldVerdict, r.NewScore, r.NewVerdict, truncate(r.Title, 40))
		total.InputTokens += r.Usage.InputTokens
		total.OutputTokens += r.Usage.OutputTokens
		total.CacheReadTokens += r.Usage.CacheReadTokens
		total.CacheWriteTokens += r.Usage.CacheWriteTokens
	}
	fmt.Fprintf(&b, "\n跟你的判斷一致：原本 %d/%d → 這次 %d/%d\n", oldAgree, judged, newAgree, judged)
	b.WriteString(strings.TrimPrefix(usageLine(model, total, false), "💰 ") + "\n")
	return b.String()
}
