// Package mailin reads the raw messages SES stores in S3: the headers, plus the
// HTML and plain-text bodies decoded to UTF-8. It knows nothing about any
// sender's layout; per-sender parsers (104 first) build on Message.
package mailin

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

var (
	// ErrNoBody means the message has no inline text/html or text/plain part.
	ErrNoBody = errors.New("no text or html body")
	// ErrCharset means a body uses a charset this package cannot decode yet.
	ErrCharset = errors.New("unsupported charset")
)

// Limits keep a hostile or broken message from exhausting the function.
const (
	maxBodyBytes = 10 << 20
	maxDepth     = 8
	maxParts     = 64
)

type Message struct {
	From      string // the sender's address, lowercased
	Subject   string
	Date      time.Time // zero when the header is missing or malformed
	MessageID string
	HTML      string // the first inline text/html part
	Text      string // the first inline text/plain part
}

// FromDomain returns the part of From after the last "@".
func (m *Message) FromDomain() string {
	return m.From[strings.LastIndex(m.From, "@")+1:]
}

// Parse reads one RFC 5322 message.
func Parse(r io.Reader) (*Message, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	m := &Message{MessageID: strings.Trim(msg.Header.Get("Message-Id"), "<> ")}

	if raw := msg.Header.Get("From"); raw != "" {
		addr, err := (&mail.AddressParser{WordDecoder: dec}).Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse From: %w", err)
		}
		m.From = strings.ToLower(addr.Address)
	}
	if s, err := dec.DecodeHeader(msg.Header.Get("Subject")); err == nil {
		m.Subject = s
	} else {
		m.Subject = msg.Header.Get("Subject")
	}
	if d, err := msg.Header.Date(); err == nil {
		m.Date = d
	}

	parts := 0
	err = walk(m, msg.Header, io.LimitReader(msg.Body, maxBodyBytes), 0, &parts)
	if err != nil {
		return nil, err
	}
	if m.HTML == "" && m.Text == "" {
		return nil, ErrNoBody
	}
	return m, nil
}

// header is what walk needs from either a message or a MIME part header.
type header interface{ Get(string) string }

// walk descends multipart bodies and keeps the first inline HTML and text parts.
func walk(m *Message, h header, body io.Reader, depth int, parts *int) error {
	if depth > maxDepth {
		return errors.New("multipart nested too deep")
	}
	ctype := h.Get("Content-Type")
	if ctype == "" {
		ctype = "text/plain; charset=us-ascii" // RFC 2045 default
	}
	media, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		return fmt.Errorf("content type %q: %w", ctype, err)
	}
	if strings.HasPrefix(media, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("multipart: %w", err)
			}
			if *parts++; *parts > maxParts {
				return errors.New("too many parts")
			}
			if err := walk(m, p.Header, p, depth+1, parts); err != nil {
				return err
			}
		}
	}
	if media != "text/html" && media != "text/plain" {
		return nil
	}
	if d, _, _ := mime.ParseMediaType(h.Get("Content-Disposition")); d == "attachment" {
		return nil
	}
	if (media == "text/html" && m.HTML != "") || (media == "text/plain" && m.Text != "") {
		return nil
	}
	text, err := decodeBody(body, h.Get("Content-Transfer-Encoding"), params["charset"])
	if err != nil {
		return fmt.Errorf("%s body: %w", media, err)
	}
	if media == "text/html" {
		m.HTML = text
	} else {
		m.Text = text
	}
	return nil
}

func decodeBody(body io.Reader, encoding, charset string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, body) // ignores line breaks
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	r, err := charsetReader(charset, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	b, err = io.ReadAll(r)
	return string(b), err
}

// charsetReader passes UTF-8 and ASCII through. Other charsets fail loudly
// until a real sample shows one is needed, rather than guessing a decoder.
func charsetReader(charset string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.Trim(charset, `" `)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return r, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrCharset, charset)
}
