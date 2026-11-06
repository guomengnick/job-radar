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
		{Reason: "應徵已讀", Board: "範例網路"}, {Reason: "面試邀約", Board: "範例數位"},
		{Reason: "應徵已讀", Board: "範例<智能>"}, {Reason: "應徵已讀", Board: "範例網路"},
	}
	applied := []store.JobRecord{{Board: "範例雲端", Title: "資深後端資料工程師"}, {Board: "範例數位", Title: "資深軟體工程師"}}
	msg := BuildWeekly(now, runs, alert, contacts, applied)
	for _, want := range []string{
		"<b>🗓 雷達週報 10/25–11/01</b>",
		"greenhouse：掃描 2 次（成功 1）｜新職缺 40｜送到日報 5",
		"lever：掃描 1 次（成功 1）｜新職缺 5｜送到日報 0",
		"<b>104 配對信：2 封、4 個職缺</b>\n日報有列 2｜台中以外 1｜規則排除 1",
		"<b>📮 這週投了 2 家</b>\n範例雲端｜資深後端資料工程師\n範例數位｜資深軟體工程師",
		"<b>👋 有人來找你：4 則</b>\n面試邀約 1：範例數位\n應徵已讀 3：範例網路、範例&lt;智能&gt;",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q:\n%s", want, msg)
		}
	}
	empty := BuildWeekly(now, nil, nil, nil, nil)
	if !strings.Contains(empty, "⚠️ 這週沒有掃描紀錄") || !strings.Contains(empty, "這週沒有。") {
		t.Errorf("empty week:\n%s", empty)
	}
}
