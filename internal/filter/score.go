package filter

import (
	"regexp"

	"github.com/guomengnick/job-radar/config"
)

// Scorer gives a rough 0–100 fit score from weighted skill keywords. It only
// ranks what reaches the phone; the real 0–100 review stays a human/Claude job.
type Scorer struct {
	skills []compiledSkill
}

type compiledSkill struct {
	name   string
	re     *regexp.Regexp
	weight int
}

func NewScorer(skills []config.Skill) *Scorer {
	s := &Scorer{}
	for _, k := range skills {
		s.skills = append(s.skills, compiledSkill{k.Name, regexp.MustCompile(k.Pattern), k.Weight})
	}
	return s
}

// Score returns the capped score and the names of the skills that matched.
func (s *Scorer) Score(title, description string) (int, []string) {
	text := title + "\n" + description
	total := 0
	var matched []string
	for _, k := range s.skills {
		if k.re.MatchString(text) {
			total += k.weight
			matched = append(matched, k.name)
		}
	}
	return min(total, 100), matched
}

// ShouldNotify applies the per-class thresholds from config. Taiwan-located
// postings are rare, so a Taiwan remote role is always sent and other Taiwan
// roles that need checking use the class A bar instead of the class B one.
func ShouldNotify(v Verdict, score int, r config.NotifyRules) bool {
	switch {
	case v.Class == ClassA && v.InTaiwan && v.WorkMode == "remote":
		return true
	case v.Class == ClassA:
		return score >= r.ClassAMinScore
	case v.Class == ClassB && v.InTaiwan:
		return score >= r.ClassAMinScore
	case v.Class == ClassB:
		return score >= r.ClassBMinScore
	}
	return false
}
