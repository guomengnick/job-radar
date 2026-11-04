package alertlinkedin

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	b, err := os.ReadFile("testdata/alert.html")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	want := []Job{
		{ID: "1000000001", Title: "Senior Software Engineer", Company: "範例 AI", Location: "台灣", WorkMode: "remote"},
		{ID: "1000000002", Title: "Optical Software Engineer & Tools - Hsinchu", Company: "範例半導體", Location: "新竹地區", WorkMode: "hybrid"},
		{ID: "1000000003", Title: "Principal Software Engineer", Company: "範例電腦", Location: "Zhubei City"},
		{ID: "1000000004", Title: ".NET Backend Developer", Company: "Example Outsourcing", Location: "海得拉巴德", WorkMode: "remote"},
	}
	if len(jobs) != len(want) {
		t.Fatalf("got %d jobs: %+v", len(jobs), jobs)
	}
	for i := range want {
		if jobs[i] != want[i] {
			t.Errorf("job %d = %+v, want %+v", i, jobs[i], want[i])
		}
	}
	// The sign-in token in every link stays out of what is kept.
	if u := jobs[0].URL(); u != "https://www.linkedin.com/jobs/view/1000000001/" || strings.Contains(u, "Token") {
		t.Errorf("URL = %s", u)
	}
}

func TestParseNoJobs(t *testing.T) {
	if _, err := Parse(`<p>新版面</p><a href="https://www.linkedin.com/feed/">x</a>`); !errors.Is(err, ErrNoJobs) {
		t.Errorf("err = %v", err)
	}
}

func TestWorkModes(t *testing.T) {
	for place, want := range map[string][2]string{
		"台灣 (遠距)":              {"台灣", "remote"},
		"新竹地區 (混合型)":           {"新竹地區", "hybrid"},
		"新加坡 (現場)":             {"新加坡", "onsite"},
		"Taipei City (Hybrid)": {"Taipei City", "hybrid"},
		"Hsinchu City":         {"Hsinchu City", ""},
		"台中-彰化地區":              {"台中-彰化地區", ""},
	} {
		jobs, err := Parse(`<a href="https://www.linkedin.com/jobs/view/1/">t</a><p>c · ` + place + `</p>`)
		if err != nil || len(jobs) != 1 || jobs[0].Location != want[0] || jobs[0].WorkMode != want[1] {
			t.Errorf("%s: %+v %v", place, jobs, err)
		}
	}
}
