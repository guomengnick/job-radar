package mailin

import (
	"bytes"
	"encoding/base64"
	"errors"
	"mime"
	"mime/quotedprintable"
	"strings"
	"testing"

	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// The fixtures are synthetic: built here with the same encodings real mail
// uses, so no real message or address ever enters the repository.

func qp(s string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.String()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// crlf turns the readable LF fixtures into wire format; qp output already has
// CRLF soft breaks, so normalize first.
func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

const subject = "104 配對日報：R1 台中 軟體"

var (
	html = `<html><body><table><tr><td><a href="https://www.104.com.tw/job/abc12">後端工程師</a></td></tr></table></body></html>`
	text = "後端工程師 https://www.104.com.tw/job/abc12"
)

func alternative() string {
	return crlf(`From: =?UTF-8?B?` + b64("104人力銀行") + `?= <NoReply@Example.104.com.tw>
To: jobs@example.com
Subject: ` + mime.BEncoding.Encode("UTF-8", subject) + `
Date: Tue, 20 Oct 2026 06:30:00 +0800
Message-ID: <abc@example.test>
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b1"

--b1
Content-Type: text/plain; charset="UTF-8"
Content-Transfer-Encoding: quoted-printable

` + qp(text) + `
--b1
Content-Type: text/html; charset=utf-8
Content-Transfer-Encoding: base64

` + b64(html) + `
--b1--
`)
}

func TestParseAlternative(t *testing.T) {
	m, err := Parse(strings.NewReader(alternative()))
	if err != nil {
		t.Fatal(err)
	}
	if m.From != "noreply@example.104.com.tw" || m.FromDomain() != "example.104.com.tw" {
		t.Errorf("from = %q (%q)", m.From, m.FromDomain())
	}
	if m.Subject != subject {
		t.Errorf("subject = %q", m.Subject)
	}
	if m.MessageID != "abc@example.test" || m.Date.IsZero() {
		t.Errorf("id = %q, date = %v", m.MessageID, m.Date)
	}
	if m.HTML != html {
		t.Errorf("html = %q", m.HTML)
	}
	if strings.TrimSpace(m.Text) != text {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParseShapes(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantHTML bool
		wantText bool
		wantErr  error // nil: must parse; errAny: any error
	}{
		{
			name: "single part html, quoted-printable",
			raw: crlf("From: a@example.com\nSubject: hi\nContent-Type: text/html; charset=UTF-8\n" +
				"Content-Transfer-Encoding: quoted-printable\n\n" + qp(html)),
			wantHTML: true,
		},
		{
			name:     "no content type defaults to ascii text",
			raw:      crlf("From: a@example.com\nSubject: hi\n\nplain body\n"),
			wantText: true,
		},
		{
			name: "an html attachment before the body is skipped",
			raw: crlf(`From: a@example.com
Subject: hi
Content-Type: multipart/mixed; boundary=outer

--outer
Content-Type: text/html; charset=utf-8
Content-Disposition: attachment; filename="old.html"

<p>attached</p>
--outer
Content-Type: multipart/alternative; boundary=inner

--inner
Content-Type: text/html; charset=utf-8

<p>inline</p>
--inner--
--outer--
`),
			wantHTML: true,
		},
		{
			name: "only an attachment",
			raw: crlf(`From: a@example.com
Subject: hi
Content-Type: multipart/mixed; boundary=x

--x
Content-Type: application/pdf
Content-Transfer-Encoding: base64

` + b64("%PDF") + `
--x--
`),
			wantErr: ErrNoBody,
		},
		{
			name: "an unknown charset fails loudly instead of garbling",
			raw: crlf("From: a@example.com\nSubject: hi\nContent-Type: text/html; charset=x-unknown\n\n" +
				"\xab\xe1\xba\xdd"),
			wantErr: ErrCharset,
		},
		{
			name:    "not a message",
			raw:     "this is not an email",
			wantErr: errAny,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Parse(strings.NewReader(tt.raw))
			switch {
			case tt.wantErr == errAny:
				if err == nil {
					t.Fatal("want an error")
				}
				return
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if (m.HTML != "") != tt.wantHTML || (m.Text != "") != tt.wantText {
				t.Errorf("html=%q text=%q", m.HTML, m.Text)
			}
			if strings.Contains(m.HTML, "attached") {
				t.Error("attachment content leaked into the body")
			}
		})
	}
}

var errAny = errors.New("any error")

// big5 encodes s the way a Traditional Chinese Outlook does.
func big5(t *testing.T, s string) string {
	t.Helper()
	b, _, err := transform.String(traditionalchinese.Big5.NewEncoder(), s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseBig5 mirrors the first message forwarded from Outlook: a Big5
// B-encoded display name in From, a Big5 subject, and Big5 bodies.
func TestParseBig5(t *testing.T) {
	name, subj, body := "測試寄件者", "測試信", "<p>後端工程師</p>"
	raw := crlf(`From: =?big5?B?` + b64(big5(t, name)) + `?= <Someone@Outlook.example>
Subject: =?big5?B?` + b64(big5(t, subj)) + `?=
Content-Type: multipart/alternative; boundary=b

--b
Content-Type: text/plain; charset="big5"
Content-Transfer-Encoding: base64

` + b64(big5(t, "後端工程師")) + `
--b
Content-Type: text/html; charset="big5"
Content-Transfer-Encoding: quoted-printable

` + qp(big5(t, body)) + `
--b--
`)
	m, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.From != "someone@outlook.example" || m.Subject != subj || m.HTML != body || m.Text != "後端工程師" {
		t.Errorf("got %+v", m)
	}
}

func TestParseAuthResults(t *testing.T) {
	raw := crlf(`Authentication-Results: amazonses.com;
 spf=pass client-ip=192.0.2.1; dkim=pass header.i=@outlook.example
From: a@example.com
Authentication-Results: mx.example.net 1; dmarc=pass header.from=example.com
Subject: hi

body
`)
	m, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"amazonses.com; spf=pass client-ip=192.0.2.1; dkim=pass header.i=@outlook.example",
		"mx.example.net 1; dmarc=pass header.from=example.com",
	}
	if len(m.AuthResults) != 2 || m.AuthResults[0] != want[0] || m.AuthResults[1] != want[1] {
		t.Errorf("AuthResults = %q", m.AuthResults)
	}
}
