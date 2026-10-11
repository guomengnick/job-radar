// Package filter decides which postings deserve the user's attention:
// title first (cheap), then location class, then a rough skill score.
package filter

import (
	"regexp"
	"strings"
)

// TitleFilter mirrors career-ops title-keywords.mjs semantics so both systems
// agree: lowercase substring match, 2–3 letter alphabetic keywords match whole
// words only, and "a + b" requires both terms.
type TitleFilter struct {
	positive []func(string) bool
	negative []func(string) bool
}

var shortWordRE = regexp.MustCompile(`^[a-z]{2,3}$`)
var andSepRE = regexp.MustCompile(`\s+\+\s+`)

func compileKeyword(kw string) func(string) bool {
	if shortWordRE.MatchString(kw) {
		re := regexp.MustCompile(`(?:^|[^\p{L}\p{M}\p{N}_])` + regexp.QuoteMeta(kw) + `(?:$|[^\p{L}\p{M}\p{N}_])`)
		return re.MatchString
	}
	return func(s string) bool { return strings.Contains(s, kw) }
}

func compilePositive(kw string) func(string) bool {
	if !andSepRE.MatchString(kw) {
		return compileKeyword(kw)
	}
	var ms []func(string) bool
	for _, t := range andSepRE.Split(kw, -1) {
		if t = strings.TrimSpace(t); t != "" {
			ms = append(ms, compileKeyword(t))
		}
	}
	return func(s string) bool {
		for _, m := range ms {
			if !m(s) {
				return false
			}
		}
		return true
	}
}

func NewTitleFilter(positive, negative []string) *TitleFilter {
	f := &TitleFilter{}
	for _, k := range positive {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			f.positive = append(f.positive, compilePositive(k))
		}
	}
	for _, k := range negative {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			f.negative = append(f.negative, compileKeyword(k))
		}
	}
	return f
}

func (f *TitleFilter) Match(title string) bool {
	lower := strings.ToLower(title)
	hit := len(f.positive) == 0
	for _, m := range f.positive {
		if m(lower) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	for _, m := range f.negative {
		if m(lower) {
			return false
		}
	}
	return true
}
