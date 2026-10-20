// Package alert104 reads 104's mail: the job lists in the 配對信 (the daily
// and weekly mail with the new jobs for each saved search), and the mail
// about the user, such as a recruiter's interview invitation (contact.go).
// It knows the layouts only; what a job is worth is decided by the filter
// package.
package alert104

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ErrNoJobs means a 配對信 had no job this parser recognises. A real alert
// always lists some, so it usually means 104 changed the layout.
var ErrNoJobs = errors.New("no jobs found: the 104 layout may have changed")

type Job struct {
	Code        string // the id in https://www.104.com.tw/job/<code>
	Title       string
	Company     string
	Location    string // e.g. 台中市西屯區
	Experience  string // e.g. 3年以上
	Education   string // e.g. 大學以上
	Industry    string // missing in the suggested-jobs list
	Description string // 104 cuts it to about 150 characters
	Salary      string // e.g. "月薪 40,000~60,000元" or "待遇面議"
	Tags        []string
	Section     string // the saved search's name, or the suggested-jobs heading
}

func (j Job) URL() string { return "https://www.104.com.tw/job/" + j.Code }

// IsMatchAlert reports whether a 104 subject is a 配對日報 or 配對週報, as
// opposed to an application notice, a recruiter's message or a login code.
func IsMatchAlert(subject string) bool { return strings.Contains(subject, "配對") }

var jobLink = regexp.MustCompile(`^https?://www\.104\.com\.tw/job/([0-9a-z]+)`)

// Parse lists the jobs in an alert's HTML body in mail order. A job listed
// under several saved searches is kept once, under the first.
//
// The mail has one table per saved search, headed by td.match_section__title,
// then 104's own suggestions under td.job_section__title. Each job is a link
// to /job/<code> around span.title (span.link in the suggestions), followed
// by the company link (span.subTitle), div.detail (place | experience |
// education | industry), div.desc, and the pay in span.tag or div.salary.
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
	section string
	cur     *Job // the job whose details are being read; nil before the first
	jobs    []Job
	seen    map[string]bool
}

func (p *parser) walk(n *html.Node) {
	if n.Type == html.ElementNode {
		switch {
		case n.Data == "td" && hasClass(n, "match_section__title"),
			n.Data == "td" && hasClass(n, "job_section__title"):
			p.section = text(n)
			return
		case n.Data == "a":
			if m := jobLink.FindStringSubmatch(attr(n, "href")); m != nil {
				if t := find(n, "span", "title", "link"); t != nil {
					p.start(m[1], text(t))
					return
				}
			}
			if p.cur != nil && strings.Contains(attr(n, "href"), "104.com.tw/company/") {
				if s := find(n, "span", "subTitle"); s != nil {
					p.cur.Company = text(s)
					return
				}
			}
		case p.cur == nil:
		case n.Data == "div" && hasClass(n, "detail"):
			p.detail(n)
			return
		case n.Data == "div" && hasClass(n, "desc"):
			p.cur.Description = text(n)
			return
		case n.Data == "div" && hasClass(n, "salary"):
			p.cur.Salary = text(n)
			return
		case n.Data == "span" && hasClass(n, "tag"):
			t := text(n)
			p.cur.Tags = append(p.cur.Tags, t)
			if p.cur.Salary == "" && isPay(t) {
				p.cur.Salary = t
			}
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		p.walk(c)
	}
}

// start begins a new job. A repeat of a job already listed gets a throwaway
// record, so its details do not overwrite the first listing's.
func (p *parser) start(code, title string) {
	j := Job{Code: code, Title: title, Section: p.section}
	if p.seen[code] {
		p.cur = &j
		return
	}
	p.seen[code] = true
	p.jobs = append(p.jobs, j)
	p.cur = &p.jobs[len(p.jobs)-1]
}

func (p *parser) detail(n *html.Node) {
	var fields []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "span" && !hasClass(c, "divide") {
			fields = append(fields, text(c))
		}
	}
	for i, dst := range []*string{&p.cur.Location, &p.cur.Experience, &p.cur.Education, &p.cur.Industry} {
		if i < len(fields) {
			*dst = fields[i]
		}
	}
}

func isPay(s string) bool {
	for _, w := range []string{"薪", "待遇", "計酬"} {
		if strings.Contains(s, w) {
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

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// find returns the first descendant <tag> carrying one of the classes.
func find(n *html.Node, tag string, classes ...string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			for _, cl := range classes {
				if hasClass(c, cl) {
					return c
				}
			}
		}
		if f := find(c, tag, classes...); f != nil {
			return f
		}
	}
	return nil
}

// text returns n's text with runs of white space (including &nbsp;) folded
// to one space. Conditional comments for Outlook are comments, so skipped.
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
