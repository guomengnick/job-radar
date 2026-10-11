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
	in := "&lt;p&gt;Build &lt;strong&gt;APIs&lt;/strong&gt;&amp;nbsp;in Go&lt;/p&gt;&lt;ul&gt;&lt;li&gt;Kafka&lt;/li&gt;&lt;/ul&gt;"
	got := HTMLToText(in)
	if !strings.Contains(got, "Build APIs in Go") || !strings.Contains(got, "Kafka") || strings.Contains(got, "<") {
		t.Fatalf("got %q", got)
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
