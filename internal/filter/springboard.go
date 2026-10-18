package filter

import (
	"regexp"
	"strings"
)

// Springboard judges Taichung office jobs from 104 alerts (master plan §3,
// category 5). 104 has already narrowed them to software job categories, so a
// title is only vetoed here; plus keywords rank what remains. Words in
// neither list (演算法, MES, 測試) pass without a bonus.
type Springboard struct {
	plus, exclude []keyword
	floor         SalaryFloor
}

type keyword struct {
	word  string
	match func(string) bool
}

// SpringboardVerdict is the outcome for one posting.
type SpringboardVerdict struct {
	Pass   bool
	Reason string   // why it failed, or "ok"
	Hits   []string // plus keywords found in the title
	Salary string   // "ok" | "low" | "unknown"
}

func NewSpringboard(plus, exclude []string, floor SalaryFloor) *Springboard {
	return &Springboard{plus: compileZh(plus), exclude: compileZh(exclude), floor: floor}
}

// Evaluate checks the title, then the pay. Unknown pay (面議) passes and is
// labelled; pay below the floor fails, since all three springboard conditions
// must hold before applying.
func (s *Springboard) Evaluate(title, salaryText string) SpringboardVerdict {
	lower := strings.ToLower(title)
	for _, k := range s.exclude {
		if k.match(lower) {
			return SpringboardVerdict{Reason: "title_excluded:" + k.word}
		}
	}
	v := SpringboardVerdict{Salary: s.floor.Check(ParseSalary(salaryText))}
	for _, k := range s.plus {
		if k.match(lower) {
			v.Hits = append(v.Hits, k.word)
		}
	}
	if v.Salary == "low" {
		v.Reason = "salary_below_floor"
		return v
	}
	v.Pass, v.Reason = true, "ok"
	return v
}

// compileZh matches keywords inside Chinese titles. Chinese words match as
// substrings. ASCII words must not touch other ASCII letters or digits, but
// may touch Chinese: "AI工程師" contains "ai", "Golang" does not contain "go",
// and "Email" does not contain "ai".
func compileZh(words []string) []keyword {
	var out []keyword
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		k := keyword{word: w}
		if isASCII(w) {
			// Only an alphanumeric end needs a boundary: ".net" matches "ASP.NET".
			pattern := regexp.QuoteMeta(w)
			if isAlnum(w[0]) {
				pattern = `(?:^|[^a-z0-9])` + pattern
			}
			if isAlnum(w[len(w)-1]) {
				pattern += `(?:$|[^a-z0-9])`
			}
			k.match = regexp.MustCompile(pattern).MatchString
		} else {
			k.match = func(s string) bool { return strings.Contains(s, w) }
		}
		out = append(out, k)
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
