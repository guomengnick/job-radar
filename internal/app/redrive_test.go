package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/dlq"
)

// A record in the shape Lambda sends to a failure destination.
const runRecord = `{"version":"1.0","timestamp":"2026-10-28T00:40:05.123Z",
 "requestContext":{"requestId":"r1","functionArn":"arn:aws:lambda:ap-southeast-2:123456789012:function:job-radar-digest:$LATEST","condition":"RetriesExhausted","approximateInvokeCount":1},
 "requestPayload":{"task":"digest"},
 "responseContext":{"statusCode":200,"executedVersion":"$LATEST","functionError":"Unhandled"},
 "responsePayload":{"errorMessage":"telegram sendMessage: HTTP 502","errorType":"errorString"}}`

type invoked struct {
	calls []string
	fail  bool
}

func (i *invoked) InvokeAsync(_ context.Context, fn string, payload []byte) error {
	if i.fail {
		return errors.New("throttled")
	}
	i.calls = append(i.calls, fn+" "+string(payload))
	return nil
}

type box struct{ deleted []string }

func (b *box) Delete(_ context.Context, m dlq.Message) error {
	b.deleted = append(b.deleted, m.ID)
	return nil
}

func TestReadDeadLetter(t *testing.T) {
	run := ReadDeadLetter(dlq.Message{ID: "m1", Body: runRecord})
	if run.Kind != "run" || run.Function != "job-radar-digest" || string(run.Payload) != `{"task":"digest"}` ||
		run.Error != "errorString telegram sendMessage: HTTP 502" || run.At.IsZero() {
		t.Fatalf("run = %+v", run)
	}
	if !strings.Contains(run.Line(), "執行  job-radar-digest {\"task\":\"digest\"}") || !strings.HasPrefix(run.Line(), "10/28 08:40") {
		t.Errorf("line = %q", run.Line())
	}
	l, _ := json.Marshal(Letter{Kind: "judgement", Key: "104#a1", Title: "後端", JD: "jd/104/a1.json", Class: "transient", Attempts: 2, Error: "busy"})
	j := ReadDeadLetter(dlq.Message{ID: "m2", Body: string(l)})
	if j.Kind != "judgement" || j.Letter.JD != "jd/104/a1.json" || !strings.Contains(j.Line(), "評分  後端（transient，重試 2 次）  busy") {
		t.Errorf("judgement = %+v %q", j, j.Line())
	}
	if u := ReadDeadLetter(dlq.Message{Body: "hello"}); u.Kind != "unknown" {
		t.Errorf("unknown = %+v", u)
	}
}

func TestRedrive(t *testing.T) {
	ctx := context.Background()
	d, _, _, clk := setup(t)
	kept := &memArchive{}
	kept.Save(ctx, "jd/104/a1.json", assess.Page{URL: "https://www.104.com.tw/job/a1", Title: "後端", Text: "JD a1"})
	letter := func(id, jd string) DeadLetter {
		b, _ := json.Marshal(Letter{Kind: "judgement", Key: "104#" + id, JD: jd, At: clk.t})
		return ReadDeadLetter(dlq.Message{ID: id, Body: string(b)})
	}
	letters := []DeadLetter{
		ReadDeadLetter(dlq.Message{ID: "run1", Body: runRecord}),
		letter("a1", "jd/104/a1.json"),
		letter("a2", ""),                 // no kept page: stays
		letter("a3", "jd/104/gone.json"), // page expired: stays
		ReadDeadLetter(dlq.Message{ID: "x", Body: "hello"}),
	}
	inv, bx, fb := &invoked{}, &box{}, &fakeBatches{}
	report, err := Redrive(ctx, d, bx, inv, fb, &sent{}, kept, Profile{Text: "評分檔案"}, letters)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.calls) != 1 || inv.calls[0] != `job-radar-digest {"task":"digest"}` {
		t.Errorf("invoked %v", inv.calls)
	}
	if len(fb.submitted) != 1 || fb.submitted[0].Page.Text != "JD a1" || fb.profile != "評分檔案" {
		t.Errorf("resubmitted %+v", fb.submitted)
	}
	if strings.Join(bx.deleted, ",") != "run1,a1" {
		t.Errorf("deleted %v; only what went out again", bx.deleted)
	}
	joined := strings.Join(report, "\n")
	for _, want := range []string{"🔁 重跑 job-radar-digest", "🔁 1 個評分重新送出（批次 msgbatch_1", "⚠️ 讀不到 jd/104/gone.json，留在 DLQ", "⏸ 留在 DLQ"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %q:\n%s", want, joined)
		}
	}

	// A run that cannot be started again stays in the queue.
	bx2 := &box{}
	_, err = Redrive(ctx, d, bx2, &invoked{fail: true}, &fakeBatches{}, &sent{}, kept, Profile{Text: "p"}, letters[:1])
	if err == nil || len(bx2.deleted) != 0 {
		t.Errorf("err %v, deleted %v", err, bx2.deleted)
	}
	// Only the radar's own functions are started.
	other := ReadDeadLetter(dlq.Message{ID: "o", Body: strings.ReplaceAll(runRecord, "job-radar-digest", "someone-else")})
	inv3 := &invoked{}
	Redrive(ctx, d, &box{}, inv3, &fakeBatches{}, &sent{}, kept, Profile{Text: "p"}, []DeadLetter{other})
	if len(inv3.calls) != 0 {
		t.Errorf("started %v", inv3.calls)
	}
}
