package filter

import (
	"testing"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/ats"
)

func mustConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTitleFilter(t *testing.T) {
	c := mustConfig(t)
	f := NewTitleFilter(c.Title.Positive, c.Title.Negative)
	cases := map[string]bool{
		"Senior Software Engineer":                                true,
		"Backend Engineer - Data Acquisition (Anti-Scam 2B Team)": true,
		"Senior Golang Backend Engineer":                          true,
		"Data Engineer, Streaming":                                true,
		"IoT Platform Engineer":                                   true,
		"Senior Full Stack Engineer":                              false, // negative wins
		"Principal Backend Engineer":                              false,
		"Account Manager, Advertising Solution - Taiwan":          false,
		"Senior Software Engineer, Customer Support Tools":        false,
		"Biotech Researcher":                                      false, // "iot" is a whole-word keyword
		"C# Coding Specialist - Freelance AI Trainer Project":     false,
		"Software Development Engineer in Test (Backend)":         false,
		// word: keywords (2026-10-11), titles seen in a 1,500-board sample.
		"Senior Software Engineer, Internal Systems": true, // not "intern"
		"Senior Software Engineer - Architecture":    true, // not "architect"
		"Software Engineer Internship":               false,
		"Solutions Architect":                        false,
		"Manager, Analytics (Trust & Safety)":        false, // not "rust"
		"Escalations Specialist":                     false, // not "scala"
		"Senior Rust Engineer":                       true,
	}
	for title, want := range cases {
		if got := f.Match(title); got != want {
			t.Errorf("%q: got %v, want %v", title, got, want)
		}
	}
}

// The word: and stem: prefixes mirror career-ops title-keywords.mjs; the
// examples come from its comments.
func TestTitleKeywordPrefixes(t *testing.T) {
	cases := []struct {
		keyword, title string
		want           bool
	}{
		{"word:intern", "Operations Intern", true},
		{"word:intern", "Senior Software Engineer, Internal Systems", false},
		{"word:intern", "International Partnerships Manager", false},
		{"word:intern", "Préinternship", false}, // an accented letter is part of the word
		{"stem:agent", "Agentforce Developer", true},
		{"stem:agent", "Agentic Systems Engineer", true},
		{"stem:agent", "Reagents Specialist", false},
		{"agent", "Reagents Specialist", true}, // bare keywords stay substrings
		{"word:rust", "Rust Engineer", true},
		{"word:rust", "Trust & Safety Manager", false},
		{"word:", "Anything", false}, // a bare prefix matches nothing, never everything
		{"Word:Intern", "Operations Intern", true},
	}
	for _, tc := range cases {
		if got := NewTitleFilter([]string{tc.keyword}, nil).Match(tc.title); got != tc.want {
			t.Errorf("positive %q on %q: got %v, want %v", tc.keyword, tc.title, got, tc.want)
		}
	}
	// As a negative, a bare "word:" must not veto every title.
	if !NewTitleFilter(nil, []string{"word:"}).Match("Backend Engineer") {
		t.Error("a bare prefix in negative vetoed a title")
	}
	if NewTitleFilter([]string{"word:go + backend"}, nil).Match("Golang Backend Engineer") {
		t.Error("an AND term keeps its own prefix")
	}
}

// Location strings are real values seen in career-ops scan-history.tsv and
// in the recorded fixtures.
func TestLocationClassifier(t *testing.T) {
	c := NewLocationClassifier(mustConfig(t).Location)
	cases := []struct {
		name string
		job  ats.Job
		want Class
	}{
		{"remote, no country", ats.Job{Location: "Remote"}, ClassB},
		{"anywhere", ats.Job{Location: "Anywhere in the World"}, ClassA},
		{"global", ats.Job{Location: "Remote, Global"}, ClassA},
		{"apac", ats.Job{Location: "Remote - APAC"}, ClassA},
		{"us remote", ats.Job{Location: "Remote - United States"}, ClassC},
		{"us remote reversed", ats.Job{Location: "United States (Remote)"}, ClassC},
		{"us short", ats.Job{Location: "Remote, US"}, ClassC},
		{"us city", ats.Job{Location: "Boston, MA / Remote"}, ClassC},
		{"canada", ats.Job{Location: "Remote Canada"}, ClassC},
		{"germany", ats.Job{Location: "Germany (remote)"}, ClassC},
		{"amer", ats.Job{Location: "Remote, AMER", WorkplaceType: "remote"}, ClassC},
		{"emea", ats.Job{Location: "Remote, EMEA"}, ClassC},
		{"india", ats.Job{Location: "Remote, India"}, ClassC},
		{"vague", ats.Job{Location: "2 Locations"}, ClassB},
		{"empty", ats.Job{Location: ""}, ClassB},
		{"office abroad", ats.Job{Location: "Tokyo, Japan"}, ClassC},
		{"office city no mode", ats.Job{Location: "Berlin"}, ClassC},
		{"office city unknown country", ats.Job{Location: "Wroclaw"}, ClassD},
		{"taipei no mode", ats.Job{Location: "Taipei, Taiwan"}, ClassB},
		{"taiwan remote", ats.Job{Location: "Taipei, Taiwan", WorkplaceType: "remote"}, ClassA},
		{"taipei hybrid days unknown", ats.Job{Location: "Taipei", WorkplaceType: "hybrid"}, ClassB},
		{"taipei hybrid 2 days", ats.Job{Location: "Taipei", WorkplaceType: "hybrid",
			Description: "We work 2 days per week in the office."}, ClassA},
		{"taipei hybrid 3 days", ats.Job{Location: "Taipei", WorkplaceType: "hybrid",
			Description: "Hybrid: three days a week in office."}, ClassD},
		{"hsinchu hybrid zh", ats.Job{Location: "新竹", WorkplaceType: "hybrid",
			Description: "每週 1 天進辦公室"}, ClassA},
		{"taichung onsite", ats.Job{Location: "Taichung, Taiwan", WorkplaceType: "onsite"}, ClassB},
		{"taipei onsite", ats.Job{Location: "Taipei, Taiwan", WorkplaceType: "onsite"}, ClassD},
		{"taiwan in title", ats.Job{Title: "Backend Engineer - Taiwan (Remote)", Location: "Remote"}, ClassA},
		{"remote with jd restriction", ats.Job{Location: "Remote",
			Description: "You must be located in the United States."}, ClassC},
		{"remote with jd broad", ats.Job{Location: "Remote",
			Description: "We hire globally and work across time zones."}, ClassA},
		{"global but jd restricted", ats.Job{Location: "Remote, Global",
			Description: "Candidates must be authorized to work in the United States."}, ClassC},
		{"title words are not geography", ats.Job{Title: "Help Us Build Global Payments", Location: "Remote"}, ClassB},
		// Cases found in the first live run (2026-10-11).
		{"anywhere in a country", ats.Job{Location: "Anywhere in France, Belgium, Spain"}, ClassC},
		{"global hq is an office, not worldwide", ats.Job{Location: "SF Global HQ"}, ClassD},
		{"global but US hours", ats.Job{Location: "Remote - Global - Eastern Time Zone"}, ClassB},
		{"remote pinned to US city", ats.Job{Location: "NYC, New York", WorkplaceType: "remote"}, ClassC},
		{"worldwide plus a country", ats.Job{Location: "United States of America; Worldwide - Remote"}, ClassA},
		{"global anywhere", ats.Job{Location: "Global Anywhere; Remote - EMEA"}, ClassA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Classify(tc.job)
			if got.Class != tc.want {
				t.Fatalf("%+v → %s (%s), want %s", tc.job, got.Class, got.Reason, tc.want)
			}
		})
	}
}

func TestHybridDaysPerWeek(t *testing.T) {
	cases := map[string]float64{
		"2 days per week in office":     2,
		"three days a week":             3,
		"once a week":                   1,
		"come in two (2) days per week": 2,
		"每週 2 天進辦公室":                    2,
		"we meet monthly":               0.25,
		// A count followed by "per month" is monthly, not weekly (career-ops
		// checks "per month" first and reads this as monthly too).
		"3 days in the office per month": 0.75,
	}
	for in, want := range cases {
		got, ok := HybridDaysPerWeek(in)
		if !ok || got != want {
			t.Errorf("%q: got %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := HybridDaysPerWeek("flexible hybrid setup"); ok {
		t.Error("no number should mean unknown")
	}
}

func TestScore(t *testing.T) {
	c := mustConfig(t)
	s := NewScorer(c.Skills)
	score, matched := s.Score("Senior Backend Engineer",
		"Build event-driven data pipelines with Kafka and PostgreSQL in C# / .NET for factory equipment.")
	if score < 60 || len(matched) < 5 {
		t.Fatalf("strong match scored %d %v", score, matched)
	}
	low, _ := s.Score("Senior Backend Engineer", "Ruby on Rails and React.")
	if low >= 20 {
		t.Fatalf("weak match scored %d", low)
	}
	n := c.Notify
	if !ShouldNotify(Verdict{Class: ClassA}, 40, n) || ShouldNotify(Verdict{Class: ClassB}, 59, n) ||
		ShouldNotify(Verdict{Class: ClassC}, 100, n) {
		t.Fatal("thresholds wrong")
	}
	if !ShouldNotify(Verdict{Class: ClassA, InTaiwan: true, WorkMode: "remote"}, 0, n) {
		t.Fatal("Taiwan remote roles are always sent")
	}
	if !ShouldNotify(Verdict{Class: ClassB, InTaiwan: true}, 40, n) {
		t.Fatal("Taiwan roles needing a check use the class A bar")
	}
}
