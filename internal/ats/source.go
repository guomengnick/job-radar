// Package ats turns each vendor's public job-board API into one Job shape.
// Adding a vendor means adding one file that implements Source.
package ats

import (
	"context"
	"html"
	"regexp"
	"strings"
	"time"
)

// Job is the vendor-neutral posting the rest of the radar works with.
type Job struct {
	ATS           string
	Board         string // the board slug on that ATS
	ID            string // the vendor's posting id, stable across fetches
	Title         string
	URL           string
	Location      string
	WorkplaceType string // "remote" | "hybrid" | "onsite" | "" when unknown
	Description   string // plain text
	PostedAt      time.Time
}

// Key identifies a posting across runs and vendors.
func (j Job) Key() string { return j.ATS + "#" + j.Board + "#" + j.ID }

// Getter is the slice of fetch.Client a Source needs; tests can fake it.
type Getter interface {
	GetJSON(ctx context.Context, url string, v any) error
}

type Source interface {
	Name() string
	Host() string
	BoardURL(slug string) string
	Fetch(ctx context.Context, g Getter, slug string) ([]Job, error)
}

// All returns every supported source keyed by name.
func All() map[string]Source {
	return map[string]Source{
		"greenhouse": Greenhouse{},
		"lever":      Lever{},
		"ashby":      Ashby{},
	}
}

// SlugRE guards every slug before it is put into a URL.
var SlugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

var (
	tagRE   = regexp.MustCompile(`(?s)<[^>]*>`)
	blockRE = regexp.MustCompile(`(?i)</?(?:p|div|br|li|ul|ol|h[1-6]|tr)\b[^>]*>`)
	spaceRE = regexp.MustCompile(`[ \t\x{00a0}]+`)
	nlRE    = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText strips markup well enough for keyword matching.
// Greenhouse escapes its HTML once more, so unescape before and after.
func HTMLToText(s string) string {
	s = html.UnescapeString(s)
	s = blockRE.ReplaceAllString(s, "\n")
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaceRE.ReplaceAllString(s, " ")
	s = nlRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func normalizeWorkplace(v string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(v, "-", ""), "_", "")) {
	case "remote":
		return "remote"
	case "hybrid":
		return "hybrid"
	case "onsite", "inoffice", "office":
		return "onsite"
	}
	return ""
}

func joinUnique(parts ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return strings.Join(out, "; ")
}
