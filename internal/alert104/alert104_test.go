package alert104

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// testdata/alert.html is synthetic: the real layout with made-up jobs.
func TestParse(t *testing.T) {
	b, err := os.ReadFile("testdata/alert.html")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := Parse(string(b))
	if err != nil {
		t.Fatal(err)
	}
	want := []Job{
		{
			Code: "aaa01", Title: "資深後端工程師（.NET）", Company: "範例科技股份有限公司",
			Location: "台中市西屯區", Experience: "5年以上", Education: "大學以上", Industry: "電腦軟體服務業",
			Description: "負責自家 SaaS 產品的後端 API，使用 C#、PostgreSQL 與 Kafka...",
			Salary:      "月薪 60,000~90,000元", Tags: []string{"月薪 60,000~90,000元", "遠端工作"},
			Section: "R1 台中 後端",
		},
		{
			Code: "aaa02", Title: "PLC 控制工程師", Company: "範例機械有限公司",
			Location: "台中市大里區", Experience: "經歷不拘", Education: "專科以上", Industry: "機械設備製造修配業",
			Description: "設備控制程式撰寫。", Salary: "待遇面議", Tags: []string{"待遇面議"},
			Section: "R1 台中 後端",
		},
		// aaa01 again under R2 is dropped, and its details do not leak.
		{
			Code: "bbb01", Title: "資料工程師（完全遠端）", Company: "示範數據有限公司",
			Location: "高雄市前鎮區", Experience: "3年以上", Education: "大學", Industry: "資訊服務業",
			Salary: "部分工時(月薪) 30,000元", Tags: []string{"部分工時(月薪) 30,000元"},
			Section: "R2 全台 遠端",
		},
		{
			Code: "ccc01", Title: "全端工程師", Company: "樣本生技股份有限公司",
			Location: "台中市南屯區", Experience: "經歷不拘", Education: "大學以上",
			Salary: "待遇面議", Section: "適合你大展身手的工作",
		},
		// zzz99 has no title span, so it is not a job entry.
	}
	if len(jobs) != len(want) {
		t.Fatalf("got %d jobs: %+v", len(jobs), jobs)
	}
	for i := range want {
		if !reflect.DeepEqual(jobs[i], want[i]) {
			t.Errorf("job %d\n got %+v\nwant %+v", i, jobs[i], want[i])
		}
	}
	if u := jobs[0].URL(); u != "https://www.104.com.tw/job/aaa01" {
		t.Errorf("URL = %s", u)
	}
}

func TestParseNoJobs(t *testing.T) {
	_, err := Parse(`<html><body><p>本週沒有新的配對職缺</p><a href="https://www.104.com.tw/job/x1">x</a></body></html>`)
	if !errors.Is(err, ErrNoJobs) {
		t.Errorf("err = %v, want ErrNoJobs", err)
	}
}

func TestIsMatchAlert(t *testing.T) {
	for subject, want := range map[string]bool{
		"小明10/20的配對日報，符合職缺59筆": true,
		"小明的配對週報":              true,
		"【123456】 是你的104登入驗證碼": false,
		"您主動應徵某公司某職務的信函已被讀取":   false,
		"【某公司】發給您的【面試邀約】":      false,
	} {
		if got := IsMatchAlert(subject); got != want {
			t.Errorf("IsMatchAlert(%q) = %v", subject, got)
		}
	}
}
