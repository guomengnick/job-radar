package ats

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fileGetter serves a recorded API response, so parser tests never hit the network.
type fileGetter struct {
	path    string
	gotURL  string
	failErr error
}

func (f *fileGetter) GetJSON(_ context.Context, url string, v any) error {
	f.gotURL = url
	if f.failErr != nil {
		return f.failErr
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// Fixtures copy the keys, key order, types and encodings of real responses
// recorded 2026-10-11 (double-escaped Greenhouse HTML, Lever lists, Ashby nulls
// and omitted keys), but every value describes a fictional company, so the repo
// redistributes no employer's job text.
func TestParsers(t *testing.T) {
	cases := []struct {
		src       Source
		slug      string
		fixture   string
		wantURL   string
		wantTitle string
		wantLoc   string
		wantWP    string
	}{
		{Greenhouse{}, "acme", "testdata/greenhouse.json",
			"https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true",
			"Backend Engineer, Fleet Data - Taiwan", "Taipei, Taiwan", ""},
		{Lever{}, "AcmeRobotics", "testdata/lever.json",
			"https://api.lever.co/v0/postings/AcmeRobotics?mode=json",
			"Backend Engineer - Data Ingestion (Platform Team)", "Taipei", "hybrid"},
		{Ashby{}, "acme", "testdata/ashby.json",
			"https://api.ashbyhq.com/posting-api/job-board/acme",
			"Platform Engineer", "Remote, Global", ""},
	}
	for _, tc := range cases {
		t.Run(tc.src.Name(), func(t *testing.T) {
			g := &fileGetter{path: tc.fixture}
			jobs, err := tc.src.Fetch(context.Background(), g, tc.slug)
			if err != nil {
				t.Fatal(err)
			}
			if g.gotURL != tc.wantURL {
				t.Fatalf("url = %s", g.gotURL)
			}
			if len(jobs) != 3 {
				t.Fatalf("got %d jobs, want 3", len(jobs))
			}
			var found *Job
			for i := range jobs {
				j := jobs[i]
				if j.ID == "" || j.URL == "" || j.ATS != tc.src.Name() || j.Board != tc.slug {
					t.Fatalf("incomplete job: %+v", j)
				}
				if j.Title == tc.wantTitle {
					found = &jobs[i]
				}
			}
			if found == nil {
				t.Fatalf("title %q not found", tc.wantTitle)
			}
			if !strings.Contains(found.Location, tc.wantLoc) {
				t.Fatalf("location = %q, want it to contain %q", found.Location, tc.wantLoc)
			}
			if found.WorkplaceType != tc.wantWP {
				t.Fatalf("workplace = %q, want %q", found.WorkplaceType, tc.wantWP)
			}
			if len(found.Description) < 100 || strings.Contains(found.Description, "<p>") {
				t.Fatalf("description not plain text: %.120q", found.Description)
			}
			if found.PostedAt.IsZero() {
				t.Fatal("posted date missing")
			}
		})
	}
}

func TestHTMLToText(t *testing.T) {
	in := "<p>Build <strong>APIs</strong>&nbsp;in Go</p><ul><li>Kafka</li></ul>"
	got := HTMLToText(in)
	if !strings.Contains(got, "Build APIs in Go") || !strings.Contains(got, "Kafka") || strings.Contains(got, "<") {
		t.Fatalf("got %q", got)
	}
}

// jsonGetter answers every request with the same JSON body.
type jsonGetter string

func (g jsonGetter) GetJSON(_ context.Context, _ string, v any) error {
	return json.Unmarshal([]byte(g), v)
}

// "<5 years" in a JD is text, not markup. It arrives as &lt; in Lever's HTML
// and as &amp;lt; in Greenhouse's extra-escaped HTML; both must keep it.
func TestEscapedTextSurvives(t *testing.T) {
	cases := []struct {
		src Source
		g   Getter
	}{
		{Greenhouse{}, jsonGetter(`{"jobs":[{"id":1,"title":"T","absolute_url":"https://x/1",
			"content":"&lt;li&gt;&amp;lt;5 years of &lt;b&gt;Go&lt;/b&gt;&lt;/li&gt;&lt;li&gt;Latency &amp;lt;10ms&lt;/li&gt;"}]}`)},
		{Lever{}, jsonGetter(`[{"id":"a","text":"T","hostedUrl":"https://x/a",
			"lists":[{"text":"Requirements","content":"<li>&lt;5 years of <b>Go</b></li><li>Latency &lt;10ms</li>"}]}]`)},
	}
	for _, tc := range cases {
		jobs, err := tc.src.Fetch(context.Background(), tc.g, "acme")
		if err != nil || len(jobs) != 1 {
			t.Fatalf("%s: jobs=%d err=%v", tc.src.Name(), len(jobs), err)
		}
		d := jobs[0].Description
		if !strings.Contains(d, "<5 years of Go") || !strings.Contains(d, "Latency <10ms") {
			t.Errorf("%s lost escaped text: %q", tc.src.Name(), d)
		}
	}
}

func TestSlugRE(t *testing.T) {
	for _, ok := range []string{"appier", "Gogolook", "m-kopa", "foo.bar_1"} {
		if !SlugRE.MatchString(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "a?b=c", "-x"} {
		if SlugRE.MatchString(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
