package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecordApplication(t *testing.T) {
	ctx := context.Background()
	d, _, st, clk := setup(t)
	tg := &sent{}
	a := Application{Code: "95o88", Title: " 資深後端資料工程師 <Remote> ", Company: "美商山嵐發現有限公司台灣分公司"}
	first, err := RecordApplication(ctx, d, tg, a)
	if err != nil || !first {
		t.Fatalf("first = %v, %v", first, err)
	}
	if len(tg.msgs) != 1 || tg.msgs[0] != `📮 已投：<a href="https://www.104.com.tw/job/95o88">資深後端資料工程師 &lt;Remote&gt;</a>｜美商山嵐發現有限公司台灣分公司` {
		t.Fatalf("telegram %q", tg.msgs)
	}
	// Reopening the confirmation page records and sends nothing.
	clk.t = clk.t.Add(time.Hour)
	if again, err := RecordApplication(ctx, d, tg, a); err != nil || again || len(tg.msgs) != 1 {
		t.Fatalf("again = %v, %v, sent %d", again, err, len(tg.msgs))
	}
	recs, _ := st.JobsSince(ctx, appliedATS, clk.t.Add(-48*time.Hour))
	if len(recs) != 1 || recs[0].Key != "104-applied#95o88" || recs[0].Board != "美商山嵐發現有限公司台灣分公司" ||
		recs[0].Title != "資深後端資料工程師 <Remote>" || !recs[0].FirstSeen.Equal(clk.t.Add(-time.Hour)) {
		t.Fatalf("records %+v", recs)
	}
	// Not a 104 job: refused, nothing stored.
	for _, code := range []string{"", "../x", "ABC", "95o88?x=1"} {
		if _, err := RecordApplication(ctx, d, tg, Application{Code: code}); !errors.Is(err, ErrBadApplication) {
			t.Errorf("%q: err = %v", code, err)
		}
	}
}
