package alert104

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Contact is a 104 mail about the user rather than a job list: a recruiter
// asking about interest or inviting to an interview, an employer's own
// recruiting mail, or a notice that an application was read.
type Contact struct {
	Kind     string // 詢問意願, 面試邀約, 求才通知 or 應徵已讀 (others as 104 names them)
	Company  string
	JobCode  string // empty when the mail names no job
	JobTitle string
	ReplyBy  string // the recruiter's requested reply time, when stated
}

// URL is the job's page, or "" when the mail names no job.
func (c Contact) URL() string {
	if c.JobCode == "" {
		return ""
	}
	return "https://www.104.com.tw/job/" + c.JobCode
}

var (
	// 【某公司】發給您的【面試邀約】
	messageSubject = regexp.MustCompile(`^【(.+)】發給您的【(.+)】$`)
	// 您主動應徵某公司某職稱職務的信函已被讀取
	readSubject = regexp.MustCompile(`^您主動應徵(.+)職務的信函已被讀取$`)
)

// ParseContact recognises a contact by its subject; false means the mail is
// something else (a login code, a newsletter).
func ParseContact(subject, body string) (Contact, bool) {
	subject = strings.TrimSpace(subject)
	switch {
	case messageSubject.MatchString(subject):
		m := messageSubject.FindStringSubmatch(subject)
		c := Contact{Company: strings.TrimSpace(m[1]), Kind: strings.TrimSpace(m[2])}
		c.JobCode, c.JobTitle, c.ReplyBy = messageDetails(body)
		return c, true
	case readSubject.MatchString(subject):
		// The subject runs company and title together; company names end
		// in 公司 (有限公司, 分公司), so split after the last one.
		rest := readSubject.FindStringSubmatch(subject)[1]
		c := Contact{Kind: "應徵已讀", Company: rest}
		if i := strings.LastIndex(rest, "公司"); i >= 0 {
			c.Company, c.JobTitle = rest[:i+len("公司")], strings.TrimSpace(rest[i+len("公司"):])
		}
		return c, true
	case strings.HasSuffix(subject, "求才通知"):
		// An employer's own mail through 104: free text, no job link.
		return Contact{Kind: "求才通知", Company: strings.TrimSpace(strings.TrimSuffix(subject, "求才通知"))}, true
	}
	return Contact{}, false
}

// messageDetails reads the job a recruiter's message is about, from
// td.update-announcement, and the requested reply time from
// td.resume-list_title.
func messageDetails(body string) (code, title, replyBy string) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return "", "", ""
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "td" {
			switch {
			case hasClass(n, "update-announcement") && code == "":
				code, title = firstJobLink(n)
			case hasClass(n, "resume-list_title") && replyBy == "":
				replyBy = text(n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return code, title, replyBy
}

func firstJobLink(n *html.Node) (code, title string) {
	if n.Type == html.ElementNode && n.Data == "a" {
		if m := jobLink.FindStringSubmatch(attr(n, "href")); m != nil {
			return m[1], text(n)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if code, title = firstJobLink(c); code != "" {
			return code, title
		}
	}
	return "", ""
}
