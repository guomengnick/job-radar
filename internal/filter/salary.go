package filter

import (
	"regexp"
	"strconv"
	"strings"
)

// SalaryPeriod is the unit a Taiwanese posting states pay in.
type SalaryPeriod string

const (
	PayUnknown    SalaryPeriod = ""
	PayMonthly    SalaryPeriod = "month"
	PayAnnual     SalaryPeriod = "year"
	PayDaily      SalaryPeriod = "day"
	PayHourly     SalaryPeriod = "hour"
	PayNegotiable SalaryPeriod = "negotiable" // 面議
)

// Salary is a posted pay range in NT$. Max is 0 for "or more" (以上).
type Salary struct {
	Period   SalaryPeriod
	Min, Max int
}

var (
	// "40,000", "4.5萬", "1,000,000"
	amountRE = regexp.MustCompile(`(\d[\d,]*(?:\.\d+)?)\s*(萬)?`)
	periods  = []struct {
		word   string
		period SalaryPeriod
	}{{"年薪", PayAnnual}, {"月薪", PayMonthly}, {"日薪", PayDaily}, {"時薪", PayHourly}}
)

// ParseSalary reads 104-style pay text: "月薪 40,000~60,000元",
// "年薪 1,000,000元以上", "月薪 5萬~8萬", "待遇面議". Text it cannot read
// returns PayUnknown, which the floor check treats like 面議.
func ParseSalary(text string) Salary {
	var s Salary
	for _, p := range periods {
		if strings.Contains(text, p.word) {
			s.Period = p.period
			break
		}
	}
	var amounts []int
	for _, m := range amountRE.FindAllStringSubmatch(text, 2) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			continue
		}
		if m[2] == "萬" {
			v *= 10000
		}
		amounts = append(amounts, int(v))
	}
	switch {
	// "待遇面議（經常性薪資達4萬元或以上）" states an amount but no period.
	case s.Period == PayUnknown && strings.Contains(text, "面議"):
		return Salary{Period: PayNegotiable}
	case len(amounts) == 0 || s.Period == PayUnknown:
		return Salary{}
	case len(amounts) == 1:
		s.Min = amounts[0]
	default:
		s.Min, s.Max = amounts[0], amounts[1]
	}
	return s
}

// SalaryFloor is the springboard's third condition: pay clearly above the
// current job. A posting passes on the top of its range.
type SalaryFloor struct {
	Annual     int // NT$ per year
	MonthlyTop int // NT$ per month, compared with the top of a monthly range
}

// Check returns "ok", "low", or "unknown" (面議 or unreadable text). 面議
// only implies at least NT$40k a month by law, so it cannot decide either way.
//
// An open-ended figure ("月薪 50,000元以上") states only where pay starts. It
// passes when the start reaches the floor, and is unknown when the start is
// at least two thirds of it (NT$47k a month against 70k), since the top can
// still get there; below that, as with 40,000元以上, it is low.
func (f SalaryFloor) Check(s Salary) string {
	var floor int
	switch s.Period {
	case PayAnnual:
		floor = f.Annual
	case PayMonthly:
		floor = f.MonthlyTop
	case PayDaily, PayHourly:
		return "low" // day and hourly rates are part-time or temporary work
	default:
		return "unknown"
	}
	if s.Max > 0 {
		return okOrLow(s.Max >= floor)
	}
	switch {
	case s.Min >= floor:
		return "ok"
	case 3*s.Min >= 2*floor:
		return "unknown"
	}
	return "low"
}

func okOrLow(ok bool) string {
	if ok {
		return "ok"
	}
	return "low"
}
