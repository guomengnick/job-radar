package app

import (
	"strings"
	"testing"
	"time"
)

func TestBuildAlarm(t *testing.T) {
	taipei, _ := time.LoadLocation("Asia/Taipei")
	msg := `{"AlarmName":"job-radar-clip-errors","AlarmDescription":"clip 有錯誤 <看 logs>","NewStateValue":"ALARM",
		"NewStateReason":"Threshold Crossed: 1 datapoint [2.0] was greater than the threshold (0.0).",
		"StateChangeTime":"2026-10-23T15:04:05.000+0000","Region":"Asia Pacific (Sydney)"}`
	got := BuildAlarm("ALARM: \"job-radar-clip-errors\"", msg, "ap-southeast-2", taipei)
	for _, want := range []string{
		"<b>🚨 告警：job-radar-clip-errors</b>",
		"說明：clip 有錯誤 &lt;看 logs&gt;",
		"原因：Threshold Crossed: 1 datapoint [2.0]",
		"時間：10/23 23:04",
		`<a href="https://ap-southeast-2.console.aws.amazon.com/cloudwatch/home?region=ap-southeast-2#alarmsV2:alarm/job-radar-clip-errors">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	ok := BuildAlarm("OK", `{"AlarmName":"job-radar-no-104-alert-48h","NewStateValue":"OK"}`, "ap-southeast-2", taipei)
	if !strings.HasPrefix(ok, "<b>✅ 恢復正常：job-radar-no-104-alert-48h</b>") || strings.Contains(ok, "說明") {
		t.Errorf("ok message:\n%s", ok)
	}
	other := BuildAlarm("Budget <80%>", "You have spent 80% of your budget.", "ap-southeast-2", taipei)
	if other != "📣 <b>Budget &lt;80%&gt;</b>\nYou have spent 80% of your budget." {
		t.Errorf("plain message: %q", other)
	}
}
