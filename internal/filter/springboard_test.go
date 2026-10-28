package filter

import (
	"slices"
	"testing"
)

func TestParseSalary(t *testing.T) {
	tests := []struct {
		text string
		want Salary
	}{
		{"月薪 40,000~60,000元", Salary{PayMonthly, 40000, 60000}},
		{"月薪70,000元以上", Salary{PayMonthly, 70000, 0}},
		{"月薪 5萬~8.5萬", Salary{PayMonthly, 50000, 85000}},
		{"年薪 1,000,000~1,500,000元", Salary{PayAnnual, 1000000, 1500000}},
		{"年薪 900,000元以上", Salary{PayAnnual, 900000, 0}},
		{"時薪 200元", Salary{PayHourly, 200, 0}},
		{"待遇面議", Salary{Period: PayNegotiable}},
		{"待遇面議（經常性薪資達4萬元或以上）", Salary{Period: PayNegotiable}},
		{"論件計酬", Salary{}},
		{"", Salary{}},
		{"60,000~80,000", Salary{}}, // no period: cannot tell month from year
	}
	for _, tt := range tests {
		if got := ParseSalary(tt.text); got != tt.want {
			t.Errorf("ParseSalary(%q) = %+v, want %+v", tt.text, got, tt.want)
		}
	}
}

func TestSalaryFloor(t *testing.T) {
	c := mustConfig(t)
	f := SalaryFloor{Annual: c.Springboard.MinAnnualSalary, MonthlyTop: c.Springboard.MinMonthlyTopPay}
	tests := map[string]string{
		"月薪 40,000~60,000元":     "low",     // top 60k < 70k
		"月薪 50,000~70,000元":     "ok",      // top reaches 70k
		"月薪 40,000元以上":          "low",     // starts far below the floor
		"月薪 46,000元以上":          "low",     // just under two thirds of 70k
		"月薪 47,000元以上":          "unknown", // the top may still reach 70k
		"月薪 50,000元以上":          "unknown",
		"月薪 70,000元以上":          "ok",
		"年薪 800,000~950,000元":   "low",
		"年薪 900,000~1,200,000元": "ok",
		"年薪 600,000元以上":         "low",
		"年薪 700,000元以上":         "unknown",
		"年薪 1,000,000元以上":       "ok",
		"時薪 500元":               "low",
		"待遇面議":                  "unknown",
		"論件計酬":                  "unknown",
	}
	for text, want := range tests {
		if got := f.Check(ParseSalary(text)); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}

func TestSpringboard(t *testing.T) {
	c := mustConfig(t)
	s := NewSpringboard(c.Springboard.TitlePlus, c.Springboard.TitleExclude,
		SalaryFloor{Annual: c.Springboard.MinAnnualSalary, MonthlyTop: c.Springboard.MinMonthlyTopPay})
	tests := []struct {
		title, salary string
		pass          bool
		reason        string
		hits          []string
	}{
		{"資深後端工程師(.NET/C#)", "月薪 60,000~90,000元", true, "ok", []string{"後端", ".net", "c#"}},
		{"ASP.NET 軟體工程師", "待遇面議", true, "ok", []string{"軟體工程師", ".net"}},
		{"AI工程師", "年薪 1,200,000元以上", true, "ok", []string{"ai"}},
		{"Go 後端工程師", "待遇面議", true, "ok", []string{"後端", "go"}},
		{"Golang Developer", "待遇面議", true, "ok", nil}, // "go" is not a word in "Golang"
		{"Email 系統維運", "待遇面議", true, "ok", nil},       // nor is "ai" in "Email"
		{"演算法工程師", "月薪 70,000元以上", true, "ok", nil},   // neutral: passes, no bonus
		{"PLC 軟體工程師", "月薪 80,000元以上", false, "title_excluded:plc", nil},
		{"軟體工程師（實習）", "時薪 200元", false, "title_excluded:實習", nil},
		{"業務工程師", "待遇面議", false, "title_excluded:業務", nil},
		{"韌體工程師 Firmware", "待遇面議", false, "title_excluded:韌體", nil},
		{"資深行銷", "待遇面議", false, "title_excluded:行銷", nil},
		{"行政類--人資流程優化管理師", "待遇面議", false, "title_excluded:行政", nil},
		{"2026校園徵才-軟體類_大數據工程師", "待遇面議", false, "title_excluded:校園徵才", nil},
		{"設備現場工程師（駐廠）", "月薪 80,000元以上", false, "title_excluded:現場工程師", nil},
		// Only the whole word: an on-site visit now and then is not 現場工程師.
		{"全端工程師 (全遠端｜需不定期現場支援)", "月薪 80,000元以上", true, "ok", []string{"全端"}},
		{"Python 資料工程師", "月薪 40,000~60,000元", false, "salary_below_floor", []string{"資料工程", "python"}},
	}
	for _, tt := range tests {
		v := s.Evaluate(tt.title, tt.salary)
		if v.Pass != tt.pass || v.Reason != tt.reason || !slices.Equal(v.Hits, tt.hits) {
			t.Errorf("%q: %+v", tt.title, v)
		}
	}
	if v := s.Evaluate("後端工程師", "待遇面議"); v.Salary != "unknown" {
		t.Errorf("面議 must be labelled unknown, got %q", v.Salary)
	}
}
