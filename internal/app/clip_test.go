package app

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/notify"
)

type fakeAssessor struct {
	out     assess.Assessment
	err     error
	profile string
	page    assess.Page
}

func (f *fakeAssessor) Assess(_ context.Context, profile string, p assess.Page) (assess.Assessment, assess.Usage, error) {
	f.profile, f.page = profile, p
	return f.out, assess.Usage{InputTokens: 3000, OutputTokens: 400}, f.err
}

var judged = assess.Assessment{
	JobTitle: "資深後端工程師 <.NET>", Company: "範例科技", Score: 82, Verdict: "建議投",
	DirectionFit: 8, SkillsFit: 7, CompanyType: "自有產品", Salary: "月薪 60,000~90,000元",
	RedFlags: []string{"要求 Kubernetes"}, Evidence: []string{"自家 SaaS", "C# 與 PostgreSQL"},
	Resume: "C", AddMLLine: true, LetterLine: "這個職缺需要 C# 後端，跟我的經驗直接相關。", Reason: "自家產品，技能對得上",
}

func TestClip(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	a := &fakeAssessor{out: judged}
	var out bytes.Buffer
	page := assess.Page{URL: "https://www.104.com.tw/job/aaa01?jobsource=match_mail", Title: "t", Text: "工作內容"}
	j, err := Clip(ctx, d, a, notify.Writer{W: &out}, "我的背景", page)
	if err != nil || j.Score != 82 {
		t.Fatalf("%+v %v", j, err)
	}
	if a.profile != "我的背景" || a.page.Text != "工作內容" {
		t.Errorf("assessor got %q %+v", a.profile, a.page)
	}
	for _, want := range []string{
		"🟢 <b>82 分｜建議投</b>",
		`<a href="https://www.104.com.tw/job/aaa01?jobsource=match_mail">資深後端工程師 &lt;.NET&gt;</a>｜範例科技`,
		"方向 8/10｜技能 7/10｜自有產品｜薪資：月薪 60,000~90,000元",
		"⚠️ 要求 Kubernetes",
		"依據：「自家 SaaS」「C# 與 PostgreSQL」",
		"📄 履歷用 C 版，加 ML 那條",
		"<code>這個職缺需要 C# 後端，跟我的經驗直接相關。</code>",
		"💰 輸入 3000｜快取讀 0｜快取寫 0｜輸出 400 token，約 US$0.020",
		`👉 <a href="https://www.104.com.tw/job/aaa01?jobsource=match_mail">看職缺</a>｜<a href="https://www.104.com.tw/job/aaa01?apply=form#radar-apply?cv=C%2BML&amp;line=`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("reply missing %q:\n%s", want, out.String())
		}
	}
	rec, ok := st.Assessment("assess#104#aaa01")
	if !ok || rec.Score != 82 || rec.Model != d.Cfg.Assess.Model || !strings.Contains(rec.Detail, `"resume":"C"`) {
		t.Errorf("stored = %+v %v", rec, ok)
	}
}

func TestApplyURL(t *testing.T) {
	const job = "https://www.104.com.tw/job/aaa01?jobsource=match_mail"
	for _, c := range []struct {
		name, url string
		a         assess.Assessment
		cv, line  string // empty cv: no link
	}{
		{"ML line", job, judged, "C+ML", judged.LetterLine},
		{"at the threshold, no sentence", job, assess.Assessment{Score: ApplyLinkMinScore, Resume: "A", LetterLine: "  "}, "A", ""},
		{"below the threshold", job, assess.Assessment{Score: ApplyLinkMinScore - 1, Resume: "A"}, "", ""},
		{"not 104", "https://jobs.example.com/p/42", judged, "", ""},
		{"long sentence", job, assess.Assessment{Score: 90, Resume: "B", LetterLine: strings.Repeat("字", 200)}, "B", strings.Repeat("字", letterLineMax) + "…"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ApplyURL(c.url, c.a)
			if c.cv == "" {
				if got != "" {
					t.Fatalf("want no link, got %q", got)
				}
				return
			}
			u, err := url.Parse(got)
			if err != nil || u.Host != "www.104.com.tw" || u.Path != "/job/aaa01" || u.RawQuery != "apply=form" {
				t.Fatalf("link %q: %v", got, err)
			}
			frag, ok := strings.CutPrefix(u.EscapedFragment(), "radar-apply?")
			if !ok {
				t.Fatalf("fragment %q", u.Fragment)
			}
			v, err := url.ParseQuery(frag)
			if err != nil || v.Get("cv") != c.cv || v.Get("line") != c.line || v.Has("line") != (c.line != "") {
				t.Errorf("fragment %v %v, want cv %q line %q", v, err, c.cv, c.line)
			}
		})
	}
}

func TestClipKeysOtherSitesByURL(t *testing.T) {
	d, _, st, _ := setup(t)
	page := assess.Page{URL: "https://jobs.example.com/p/42?utm=x#apply", Text: "x"}
	if _, err := Clip(context.Background(), d, &fakeAssessor{out: judged}, notify.Writer{W: &bytes.Buffer{}}, "p", page); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Assessment("assess#https://jobs.example.com/p/42"); !ok {
		t.Error("a page outside 104 is keyed by its URL without query or fragment")
	}
}

func TestClipRejectsAndReportsFailures(t *testing.T) {
	ctx := context.Background()
	d, _, _, _ := setup(t)
	for _, p := range []assess.Page{
		{URL: "javascript:alert(1)", Text: "x"},
		{URL: "chrome://extensions", Text: "x"},
		{URL: "https://www.104.com.tw/job/a1", Text: "  "},
	} {
		a := &fakeAssessor{out: judged}
		if _, err := Clip(ctx, d, a, notify.Writer{W: &bytes.Buffer{}}, "p", p); !errors.Is(err, ErrBadClip) || a.page.URL != "" {
			t.Errorf("%+v: err = %v; the model must not be called", p, err)
		}
	}

	var out bytes.Buffer
	a := &fakeAssessor{err: errors.New("anthropic: HTTP 529: overloaded_error")}
	_, err := Clip(ctx, d, a, notify.Writer{W: &out}, "p", assess.Page{URL: "https://www.104.com.tw/job/a1", Title: "後端", Text: "x"})
	if err == nil || !strings.Contains(out.String(), "評分失敗：後端") {
		t.Errorf("a failed call is returned and reported: %v\n%s", err, out.String())
	}
}
