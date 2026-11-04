// Package alertlinkedin reads LinkedIn's job alert mail (職缺配對通知), which
// the user's Gmail forwards to the radar. It knows the layout only; what a
// job is worth is decided by the filter package.
//
// Every link in the mail carries tracking and a one-time sign-in token for
// the user's account (otpToken). Only a job's number is kept, and its URL is
// built again from it, so the token never reaches a record, a log line or
// Telegram.
package alertlinkedin

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Sender is the address of the alerts. LinkedIn sends other mail from
// linkedin.com too (newsletters, messages); those are not job lists.
const Sender = "jobalerts-noreply@linkedin.com"

// ErrNoJobs means an alert had no job this parser recognises. A real alert
// always lists some, so it usually means LinkedIn changed the layout.
var ErrNoJobs = errors.New("no jobs found: the LinkedIn layout may have changed")

type Job struct {
	ID       string // the number in https://www.linkedin.com/jobs/view/<id>/
	Title    string
	Company  string
	Location string // e.g. 新竹地區, Hsinchu City, 台灣
	// WorkMode is "remote", "hybrid" or "onsite" when the card says so
	// ("台灣 (遠距)"), else "".
	WorkMode string
}

func (j Job) URL() string { return "https://www.linkedin.com/jobs/view/" + j.ID + "/" }

var (
	jobLink = regexp.MustCompile(`^https://www\.linkedin\.com/(?:comm/)?jobs/view/(\d+)`)
	// The work mode closes the place: "新竹地區 (混合型)", "Taiwan (Remote)".
	modeSuffix = regexp.MustCompile(`^(.*?)\s*[(（](遠距|遠端|混合型|混合|現場|Remote|Hybrid|On-site)[)）]$`)
	modes      = map[string]string{"遠距": "remote", "遠端": "remote", "Remote": "remote",
		"混合型": "hybrid", "混合": "hybrid", "Hybrid": "hybrid", "現場": "onsite", "On-site": "onsite"}
)

// Parse lists the jobs in an alert's HTML body in mail order, each once.
//
// Each job is a card: a link to /jobs/view/<id> around the logo (no text),
// and a link around the whole card holding another around the title alone,
// then a paragraph "company · place (work mode)". The title is the text of
// the innermost link; the paragraph is the first with a "·" after it.
func Parse(body string) ([]Job, error) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	p := parser{seen: map[string]bool{}}
	p.walk(doc)
	if len(p.jobs) == 0 {
		return nil, ErrNoJobs
	}
	return p.jobs, nil
}

type parser struct {
	cur  *Job // the job whose company line is still to come
	jobs []Job
	seen map[string]bool
}

func (p *parser) walk(n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "a":
			m := jobLink.FindStringSubmatch(attr(n, "href"))
			if m == nil {
				return
			}
			if !p.seen[m[1]] {
				p.seen[m[1]] = true
				p.jobs = append(p.jobs, Job{ID: m[1]})
				p.cur = &p.jobs[len(p.jobs)-1]
			}
			if p.cur != nil && p.cur.ID == m[1] && p.cur.Title == "" && !hasLink(n) {
				p.cur.Title = text(n)
			}
		case "p":
			if company, place, ok := strings.Cut(text(n), " · "); ok && p.cur != nil {
				p.cur.Company = company
				p.cur.Location = place
				if m := modeSuffix.FindStringSubmatch(place); m != nil {
					p.cur.Location, p.cur.WorkMode = m[1], modes[m[2]]
				}
				p.cur = nil
			}
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		p.walk(c)
	}
}

// hasLink reports whether n holds another link.
func hasLink(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "a" || hasLink(c) {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// text returns n's text with runs of white space (including &nbsp;) folded
// to one space.
func text(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return strings.Join(strings.Fields(strings.ReplaceAll(b.String(), " ", " ")), " ")
}
