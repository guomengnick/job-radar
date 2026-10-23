package app

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"
)

// cloudWatchAlarm is the part of the JSON CloudWatch publishes to SNS when an
// alarm changes state.
type cloudWatchAlarm struct {
	AlarmName        string `json:"AlarmName"`
	AlarmDescription string `json:"AlarmDescription"`
	NewStateValue    string `json:"NewStateValue"`
	NewStateReason   string `json:"NewStateReason"`
	StateChangeTime  string `json:"StateChangeTime"`
}

var alarmStates = map[string]string{"ALARM": "🚨 告警", "OK": "✅ 恢復正常", "INSUFFICIENT_DATA": "⚪ 資料不足"}

// BuildAlarm renders an SNS message for Telegram (HTML parse mode). A
// CloudWatch alarm becomes its state, name, description, reason, time in
// Taipei and a link to the alarm in the console; any other message is
// passed on as text, so nothing sent to the topic is lost.
func BuildAlarm(subject, message, region string, loc *time.Location) string {
	e := html.EscapeString
	var a cloudWatchAlarm
	if json.Unmarshal([]byte(message), &a) != nil || a.AlarmName == "" {
		return fmt.Sprintf("📣 <b>%s</b>\n%s", e(subject), e(truncate(message, 1000)))
	}
	state, ok := alarmStates[a.NewStateValue]
	if !ok {
		state = a.NewStateValue
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s：%s</b>\n", state, e(a.AlarmName))
	if a.AlarmDescription != "" {
		fmt.Fprintf(&b, "說明：%s\n", e(a.AlarmDescription))
	}
	if a.NewStateReason != "" {
		fmt.Fprintf(&b, "原因：%s\n", e(truncate(a.NewStateReason, 300)))
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000-0700", a.StateChangeTime); err == nil {
		fmt.Fprintf(&b, "時間：%s\n", t.In(loc).Format("01/02 15:04"))
	}
	link := fmt.Sprintf("https://%s.console.aws.amazon.com/cloudwatch/home?region=%s#alarmsV2:alarm/%s",
		region, region, url.PathEscape(a.AlarmName))
	fmt.Fprintf(&b, "<a href=\"%s\">在 CloudWatch 看這個告警</a>", e(link))
	return b.String()
}
