package filter

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/guomengnick/job-radar/config"
	"github.com/guomengnick/job-radar/internal/ats"
)

// Class buckets a posting by whether the user can take it from Taiwan.
// Rules port career-ops local-tools/linkedin-email-intake.mjs evaluateMetadata().
type Class string

const (
	ClassA Class = "A" // explicitly open to Taiwan (remote, or Taipei/Hsinchu hybrid within the day limit)
	ClassB Class = "B" // could be: remote without a country, or a Taiwan role whose terms need checking
	ClassC Class = "C" // restricted to another country or region
	ClassD Class = "D" // office-bound outside the user's limits
)

type Verdict struct {
	Class    Class
	WorkMode string
	Reason   string
	// InTaiwan marks postings located in Taiwan; they are rare enough that
	// notify rules treat them more generously than generic remote roles.
	InTaiwan bool
}

type LocationClassifier struct {
	taipeiMax  float64
	hsinchuMax float64
}

func NewLocationClassifier(r config.LocationRules) *LocationClassifier {
	return &LocationClassifier{taipeiMax: r.TaipeiHybridMaxDays, hsinchuMax: r.HsinchuHybridMaxDays}
}

var (
	taiwanRE   = regexp.MustCompile(`(?i)taiwan|taipei|hsinchu|zhubei|taichung|kaohsiung|tainan|taoyuan|台灣|臺灣|台北|臺北|新北|新竹|竹北|竹科|台中|臺中|高雄|台南|臺南|桃園`)
	taipeiRE   = regexp.MustCompile(`(?i)taipei|台北|臺北|新北`)
	hsinchuRE  = regexp.MustCompile(`(?i)hsinchu|zhubei|新竹|竹北|竹科`)
	taichungRE = regexp.MustCompile(`(?i)taichung|台中|臺中`)

	remoteRE = regexp.MustCompile(`(?i)\bremote\b|work from home|\bwfh\b|遠端`)
	hybridRE = regexp.MustCompile(`(?i)\bhybrid\b|混合`)
	onsiteRE = regexp.MustCompile(`(?i)\bon-?site\b|\bin[- ]office\b|office[- ]based`)

	broadRE = regexp.MustCompile(`(?i)\bapac\b|asia[- ]pacific|east asia|world ?wide|anywhere|\bglobal\b|any time ?zone|timezone independent`)
	// "Anywhere in France" and "SF Global HQ" are not worldwide; these are
	// removed before broadRE runs. "Anywhere in the world" is kept.
	notBroadRE = regexp.MustCompile(`(?i)anywhere in (?:the )?(?:[a-z]+)|global (?:hq|headquarters|office)`)
	worldRE    = regexp.MustCompile(`(?i)anywhere in (?:the )?world`)
	// "Remote - Global - Eastern Time Zone": open to anyone who keeps US/EU hours.
	tzBoundRE = regexp.MustCompile(`(?i)(?:eastern|pacific|central|mountain|european|americas?|us|uk|cet|est|pst|gmt) time ?zones?|\b(?:est|pst|cet|edt|pdt)\b`)
	// Remote roles pinned to a city abroad are almost always remote within that country.
	foreignCityRE = regexp.MustCompile(`(?i)\b(?:new york|nyc|san francisco|bay area|seattle|boston|austin|chicago|los angeles|denver|atlanta|miami|washington|portland|philadelphia|dallas|houston|san diego|san jose|palo alto|mountain view|menlo park|toronto|vancouver|montreal|london|berlin|paris|amsterdam|dublin|munich|zurich|stockholm|lisbon|madrid|barcelona|tel aviv|warsaw|krakow|bucharest)\b`)

	foreignLocREs = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:us|u\.s\.?|usa|united states|america|americas|amer|north america|latam|latin america|south america|canada|mexico|brazil|argentina|colombia|chile|peru)\b`),
		regexp.MustCompile(`(?i)\b(?:emea|europe|european union|eu|uk|united kingdom|england|scotland|ireland|germany|france|spain|portugal|italy|netherlands|belgium|switzerland|austria|poland|czech|romania|bulgaria|greece|sweden|norway|denmark|finland|estonia|lithuania|latvia|ukraine|serbia|croatia|hungary|israel|turkey|egypt|nigeria|kenya|south africa|uae|united arab emirates|dubai|saudi)\b`),
		regexp.MustCompile(`(?i)\b(?:japan|tokyo|osaka|india|bangalore|bengaluru|mumbai|pune|hyderabad|chennai|delhi|noida|gurgaon|singapore|australia|sydney|melbourne|brisbane|new zealand|auckland|vietnam|hanoi|ho chi minh|china|beijing|shanghai|shenzhen|hong kong|korea|seoul|philippines|manila|cebu|malaysia|kuala lumpur|indonesia|jakarta|thailand|bangkok|pakistan)\b`),
		regexp.MustCompile(`(?:東京|大阪|日本|印度|新加坡|澳洲|澳大利亞|雪梨|墨爾本|紐西蘭|越南|中國|北京|上海|深圳|香港|韓國|首爾|菲律賓|馬尼拉|馬來西亞|吉隆坡|印尼|泰國|曼谷|美國|英國|德國|法國|荷蘭|加拿大|波蘭|以色列)`),
	}
	// Two-letter US state codes after a comma ("Boston, MA"). Case-sensitive on purpose.
	usStateRE = regexp.MustCompile(`,\s*(?:AL|AK|AZ|AR|CA|CO|CT|DE|DC|FL|GA|HI|ID|IL|IN|IA|KS|KY|LA|ME|MD|MA|MI|MN|MS|MO|MT|NE|NV|NH|NJ|NM|NY|NC|ND|OH|OK|OR|PA|RI|SC|SD|TN|TX|UT|VT|VA|WA|WV|WI|WY)\b`)

	// A JD body is only decisive when it states a hard restriction; companies
	// often describe a US headquarters even for global roles.
	jdRestrictREs = []*regexp.Regexp{
		regexp.MustCompile(`(?i)must (?:be (?:located|based)|reside|live|be authorized to work) in (?:the )?(?:united states|u\.s\.|us|usa|uk|eu|europe|canada|india|japan|singapore|australia|latam|latin america)\b`),
		regexp.MustCompile(`(?i)(?:only available to|applications? (?:are )?limited to) candidates? (?:based|located|residing) in\b`),
		regexp.MustCompile(`(?i)(?:right to work|work authorization) (?:is )?required in (?:the )?(?:united states|u\.s\.|us|uk|eu|europe|canada|india|japan|singapore|australia)\b`),
		regexp.MustCompile(`(?i)(?:legally )?(?:eligible|authorized) to work in (?:the )?(?:united states|u\.s\.|us|usa|uk|canada|eu|europe)\b`),
		regexp.MustCompile(`(?i)remote (?:within|only in) (?:the )?(?:united states|u\.s\.|us|usa|uk|eu|europe|canada|india|japan|singapore|australia)\b`),
		regexp.MustCompile(`(?i)\b(?:itar|ts/sci|public trust|security clearance|u\.s\. person)\b`),
	}
	jdBroadRE = regexp.MustCompile(`(?i)(?:hire|hiring|work|recruit) (?:from )?anywhere|anywhere in the world|hire globally|hiring globally|any time ?zone|asia[- ]pacific|\bapac\b|utc ?\+ ?8|gmt ?\+ ?8|\btaiwan\b|\btaipei\b`)

	vagueLocRE = regexp.MustCompile(`(?i)^(?:\d+ locations?|multiple locations|various|flexible|tbd|n/?a)$`)
)

func (c *LocationClassifier) Classify(j ats.Job) Verdict {
	head := j.Title + " ; " + j.Location
	mode := j.WorkplaceType
	if mode == "" {
		mode = inferMode(head)
	}

	if taiwanRE.MatchString(head) {
		v := c.taiwan(mode, head, j.Description)
		v.InTaiwan = true
		return v
	}
	// Geography rules read the location field only: titles like "Help Us
	// Build Global Payments" would otherwise look restricted or worldwide.
	if isBroad(j.Location) {
		if jdRestricted(j.Description) {
			return Verdict{Class: ClassC, WorkMode: mode, Reason: "jd_states_foreign_restriction"}
		}
		if tzBoundRE.MatchString(j.Location) {
			return Verdict{Class: ClassB, WorkMode: mode, Reason: "global_but_timezone_bound"}
		}
		return Verdict{Class: ClassA, WorkMode: mode, Reason: "broad_remote_eligibility"}
	}
	if foreignLocation(j.Location) || foreignCityRE.MatchString(j.Location) {
		return Verdict{Class: ClassC, WorkMode: mode, Reason: "foreign_location"}
	}
	switch mode {
	case "remote":
		if jdRestricted(j.Description) {
			return Verdict{Class: ClassC, WorkMode: mode, Reason: "jd_states_foreign_restriction"}
		}
		if jdBroadRE.MatchString(j.Description) {
			return Verdict{Class: ClassA, WorkMode: mode, Reason: "jd_states_broad_eligibility"}
		}
		return Verdict{Class: ClassB, WorkMode: mode, Reason: "remote_country_not_stated"}
	case "hybrid", "onsite":
		return Verdict{Class: ClassD, WorkMode: mode, Reason: "office_outside_taiwan"}
	}
	loc := strings.TrimSpace(j.Location)
	if loc == "" || vagueLocRE.MatchString(loc) {
		return Verdict{Class: ClassB, WorkMode: mode, Reason: "location_not_stated"}
	}
	// A named place that is not remote and not in Taiwan is an office job elsewhere.
	return Verdict{Class: ClassD, WorkMode: mode, Reason: "office_location_assumed"}
}

func (c *LocationClassifier) taiwan(mode, head, jd string) Verdict {
	switch mode {
	case "remote":
		return Verdict{Class: ClassA, WorkMode: mode, Reason: "taiwan_remote"}
	case "onsite":
		if taichungRE.MatchString(head) {
			return Verdict{Class: ClassB, WorkMode: mode, Reason: "taichung_onsite_manual_review"}
		}
		return Verdict{Class: ClassD, WorkMode: mode, Reason: "onsite_not_accepted"}
	case "hybrid":
		if taichungRE.MatchString(head) {
			return Verdict{Class: ClassB, WorkMode: mode, Reason: "taichung_hybrid_manual_review"}
		}
		var maxDays float64
		switch {
		case taipeiRE.MatchString(head):
			maxDays = c.taipeiMax
		case hsinchuRE.MatchString(head):
			maxDays = c.hsinchuMax
		default:
			return Verdict{Class: ClassB, WorkMode: mode, Reason: "taiwan_city_unclear"}
		}
		days, ok := HybridDaysPerWeek(jd)
		if !ok {
			return Verdict{Class: ClassB, WorkMode: mode, Reason: "hybrid_days_unknown"}
		}
		if days > maxDays {
			return Verdict{Class: ClassD, WorkMode: mode, Reason: "hybrid_too_many_office_days"}
		}
		return Verdict{Class: ClassA, WorkMode: mode, Reason: "hybrid_within_limit"}
	}
	return Verdict{Class: ClassB, WorkMode: mode, Reason: "taiwan_work_mode_unknown"}
}

func inferMode(s string) string {
	switch {
	case remoteRE.MatchString(s):
		return "remote"
	case hybridRE.MatchString(s):
		return "hybrid"
	case onsiteRE.MatchString(s):
		return "onsite"
	}
	return ""
}

func isBroad(loc string) bool {
	loc = worldRE.ReplaceAllString(loc, "worldwide")
	loc = notBroadRE.ReplaceAllString(loc, "")
	return broadRE.MatchString(loc)
}

func foreignLocation(s string) bool {
	if usStateRE.MatchString(s) {
		return true
	}
	for _, re := range foreignLocREs {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func jdRestricted(jd string) bool {
	for _, re := range jdRestrictREs {
		if re.MatchString(jd) {
			return true
		}
	}
	return false
}

var (
	monthlyRE   = regexp.MustCompile(`(?i)monthly|per month|once a month|每月`)
	weeklyNumRE = regexp.MustCompile(`(?i)\b(\d(?:\.\d)?|one|two|three|four|five)\s*(?:\(\d\)\s*)?days?\s*(?:a|per|each|/)\s*week`)
	daysInRE    = regexp.MustCompile(`(?i)\b(\d|one|two|three|four|five)\s*(?:\(\d\)\s*)?days?\s*(?:in|at)\s*(?:the|our)?\s*office`)
	onceWeekRE  = regexp.MustCompile(`(?i)once (?:a|per) week|每週\s*(?:一次|1\s*天)`)
	zhDaysRE    = regexp.MustCompile(`每週\s*(\d(?:\.\d)?)\s*天`)
	// Read right after a "N days in the office" match.
	perMonthRE = regexp.MustCompile(`(?i)^\W*(?:(?:per|a|each|every)\s+month|monthly)`)
	wordNums   = map[string]float64{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5}
)

// HybridDaysPerWeek reads the in-office frequency a JD states, if any.
func HybridDaysPerWeek(text string) (float64, bool) {
	if m := zhDaysRE.FindStringSubmatch(text); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v, true
	}
	if m := weeklyNumRE.FindStringSubmatch(text); m != nil {
		return dayCount(m[1]), true
	}
	if m := daysInRE.FindStringSubmatchIndex(text); m != nil {
		v := dayCount(text[m[2]:m[3]])
		// "3 days in the office per month" is a monthly figure.
		if perMonthRE.MatchString(text[m[1]:]) {
			return v / 4, true
		}
		return v, true
	}
	if onceWeekRE.MatchString(text) {
		return 1, true
	}
	if monthlyRE.MatchString(text) {
		return 0.25, true
	}
	return 0, false
}

func dayCount(s string) float64 {
	if v, ok := wordNums[strings.ToLower(s)]; ok {
		return v
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
