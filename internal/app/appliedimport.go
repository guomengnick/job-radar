package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/guomengnick/job-radar/internal/store"
)

// PastApplication is an application made before the extension recorded
// them, copied from 104's 應徵紀錄 list: when (Taiwan time), the job code
// if known, the company and the title.
type PastApplication struct {
	At      time.Time
	Code    string
	Company string
	Title   string
}

// ParseApplications reads tab-separated lines: time ("2006-01-02 15:04",
// Taiwan), job code (may be empty), company, title. Blank lines and lines
// starting with # are skipped.
func ParseApplications(r io.Reader) ([]PastApplication, error) {
	var out []PastApplication
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			return nil, fmt.Errorf("line %d: want 4 tab-separated fields, got %d", n, len(f))
		}
		at, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(f[0]), taiwanTime)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		a := PastApplication{At: at, Code: strings.TrimSpace(f[1]), Company: strings.TrimSpace(f[2]), Title: strings.TrimSpace(f[3])}
		if a.Code != "" && !jobCode.MatchString(a.Code) {
			return nil, fmt.Errorf("line %d: job code %q", n, a.Code)
		}
		if a.Company == "" {
			return nil, fmt.Errorf("line %d: no company", n)
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

// ImportApplications stores past applications as RecordApplication would,
// dated when they were made and without a Telegram note. One with no job
// code gets a key from its company and title, so it still marks its
// company and a second import adds nothing. It returns how many were new.
func ImportApplications(ctx context.Context, d Deps, apps []PastApplication) (int, error) {
	added := 0
	for _, a := range apps {
		code, url := a.Code, "https://www.104.com.tw/job/"+a.Code
		if code == "" {
			sum := sha256.Sum256([]byte(a.Company + "\n" + a.Title))
			code, url = "nocode-"+hex.EncodeToString(sum[:5]), ""
		}
		rec := store.JobRecord{
			Key: appliedATS + "#" + code, ATS: appliedATS, Board: truncate(a.Company, 60), Title: truncate(a.Title, 100),
			URL: url, FirstSeen: a.At.UTC(), State: store.JobSeen,
		}
		isNew, err := d.Store.InsertJobIfNew(ctx, rec)
		if err != nil {
			return added, err
		}
		if isNew {
			added++
		}
	}
	return added, nil
}
