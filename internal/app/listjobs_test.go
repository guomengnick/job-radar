package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

func TestListAlertJobs(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	since := clk.t
	st.InsertJobIfNew(ctx, store.JobRecord{Key: "104#old01", ATS: alertATS, FirstSeen: since.Add(-time.Second), State: store.JobNew})
	clk.t = since.Add(time.Hour)
	raw := mailWith(sesDMARCPass, "jobbank@ms1.104.com.tw", "配對日報", alertHTML(t))
	if _, err := IngestMail(ctx, d, "inbound/k", strings.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	// One of 104's suggestions in Taipei, and a contact, which is not an alert job.
	st.InsertJobIfNew(ctx, store.JobRecord{Key: "104#ddd01", ATS: alertATS, Title: "後端工程師", Board: "台北範例",
		Class: "D", Reason: "outside_taichung", State: store.JobSeen, FirstSeen: clk.t})
	st.InsertJobIfNew(ctx, store.JobRecord{Key: "104-contact#面試邀約#範例#aaa01", ATS: contactATS, State: store.JobNew, FirstSeen: clk.t})
	// The digest marks its jobs sent; they stay in the digest group.
	if _, err := Digest(ctx, d, notify.Writer{W: &bytes.Buffer{}}, DigestOptions{}); err != nil {
		t.Fatal(err)
	}

	jobs, err := ListAlertJobs(ctx, d, since)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, j := range jobs {
		got = append(got, j.Group+" "+j.Class+" "+strings.TrimPrefix(j.URL, "https://www.104.com.tw/job/")+" "+j.Reason)
	}
	want := []string{
		"digest B aaa01 台中到班，只在待遇與契合度特別好時考慮",
		"digest B ccc01 台中到班，只在待遇與契合度特別好時考慮",
		"elsewhere D  台中以外、非遠端",
		"excluded A bbb01 薪資低於門檻",
		"excluded B aaa02 職稱含「plc」",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if j := jobs[0]; j.Title != "資深後端工程師（.NET）" || j.Company != "範例科技股份有限公司" || j.Salary != "月薪 60,000~90,000元" {
		t.Errorf("first job fields: %+v", j)
	}
}

func TestListAlertJobsBounds(t *testing.T) {
	d, _, _, clk := setup(t)
	for _, since := range []time.Time{clk.t.Add(time.Minute), clk.t.Add(-MaxListWindow - time.Second)} {
		if _, err := ListAlertJobs(context.Background(), d, since); !errors.Is(err, ErrBadSince) {
			t.Errorf("since %v: err = %v, want ErrBadSince", since, err)
		}
	}
	if _, err := ListAlertJobs(context.Background(), d, clk.t.Add(-MaxListWindow)); err != nil {
		t.Errorf("the window's edge is allowed: %v", err)
	}
}
