package alert104

import (
	"os"
	"testing"
)

func TestParseContact(t *testing.T) {
	b, err := os.ReadFile("testdata/contact.html")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		subject, body string
		want          Contact
		ok            bool
	}{
		{"【範例科技股份有限公司】發給您的【面試邀約】", string(b), Contact{
			Kind: "面試邀約", Company: "範例科技股份有限公司", JobCode: "aaa01", JobTitle: "後端工程師（.NET）",
			ReplyBy: "期待 1 天 (2026-10-21) 內能收到您的回覆。",
		}, true},
		{"【範例資訊有限公司_軟體事業部】發給您的【詢問意願】", "<p>版面改了</p>", Contact{
			Kind: "詢問意願", Company: "範例資訊有限公司_軟體事業部",
		}, true},
		{"您主動應徵範例科技股份有限公司IoT 軟體開發工程師職務的信函已被讀取", "", Contact{
			Kind: "應徵已讀", Company: "範例科技股份有限公司", JobTitle: "IoT 軟體開發工程師",
		}, true},
		{"您主動應徵範例工作室資深工程師職務的信函已被讀取", "", Contact{
			Kind: "應徵已讀", Company: "範例工作室資深工程師", // no 公司 to split on
		}, true},
		{"台中範例鋼鐵求才通知", "<p>歡迎投遞履歷</p>", Contact{Kind: "求才通知", Company: "台中範例鋼鐵"}, true},
		{"【123456】 是你的104登入驗證碼", "", Contact{}, false},
		{"104帳戶登入成功通知", "", Contact{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseContact(tt.subject, tt.body)
		if ok != tt.ok || got != tt.want {
			t.Errorf("%q\n got %+v %v\nwant %+v %v", tt.subject, got, ok, tt.want, tt.ok)
		}
	}
	if c, _ := ParseContact("【範例科技股份有限公司】發給您的【面試邀約】", string(b)); c.URL() != "https://www.104.com.tw/job/aaa01" {
		t.Errorf("URL = %q", c.URL())
	}
	if (Contact{Kind: "求才通知"}).URL() != "" {
		t.Error("a contact without a job has no URL")
	}
}
