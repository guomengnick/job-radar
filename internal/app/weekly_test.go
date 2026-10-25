package app

import (
	"strings"
	"testing"
	"time"

	"github.com/guomengnick/job-radar/internal/store"
)

func TestBuildWeekly(t *testing.T) {
	now := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	runs := []store.Run{
		{ATS: "greenhouse", Task: "scan", Status: "ok", JobsNew: 30, JobsPending: 4},
		{ATS: "greenhouse", Task: "scan", Status: "partial", JobsNew: 10, JobsPending: 1},
		{ATS: "lever", Task: "scan", Status: "ok", JobsNew: 5},
		{ATS: "104", Task: "mail"}, {ATS: "104", Task: "mail"},
		{ATS: "greenhouse", Task: "sync"},
	}
	alert := []ListedJob{{Group: GroupDigest}, {Group: GroupDigest}, {Group: GroupElsewhere}, {Group: GroupExcluded}}
	contacts := []store.JobRecord{
		{Reason: "應徵已讀", Board: "沛思坦"}, {Reason: "面試邀約", Board: "艾創點"},
		{Reason: "應徵已讀", Board: "長佳<智能>"}, {Reason: "應徵已讀", Board: "沛思坦"},
	}
	msg := BuildWeekly(now, runs, alert, contacts)
	for _, want := range []string{
		"<b>🗓 雷達週報 10/25–11/01</b>",
		"greenhouse：掃描 2 次（成功 1）｜新職缺 40｜送到日報 5",
		"lever：掃描 1 次（成功 1）｜新職缺 5｜送到日報 0",
		"<b>104 配對信：2 封、4 個職缺</b>\n日報有列 2｜台中以外 1｜規則排除 1",
		"<b>👋 有人來找你：4 則</b>\n面試邀約 1：艾創點\n應徵已讀 3：沛思坦、長佳&lt;智能&gt;",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q:\n%s", want, msg)
		}
	}
	empty := BuildWeekly(now, nil, nil, nil)
	if !strings.Contains(empty, "⚠️ 這週沒有掃描紀錄") || !strings.Contains(empty, "這週沒有。") {
		t.Errorf("empty week:\n%s", empty)
	}
}
