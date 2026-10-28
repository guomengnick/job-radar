package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/dlq"
	"github.com/guomengnick/job-radar/internal/notify"
)

// The dead-letter queue holds two kinds of message:
//
//   - a Letter (kind "judgement"), sent by the batch function for a
//     judgement it gave up on;
//   - a Lambda destination record (kind "run"), sent by Lambda for an
//     asynchronous run that failed after its retries: the original event
//     and the error.
//
// radar dlq lists them; radar redrive sends them again.

// DeadLetter is one message of the queue, read.
type DeadLetter struct {
	Message  dlq.Message
	Kind     string          // "judgement", "run", or "unknown"
	Function string          // run: the function's name
	Payload  json.RawMessage // run: the event to send again
	Letter   Letter          // judgement
	Error    string
	At       time.Time
}

// lambdaRecord is the part of a Lambda failure-destination record we use.
type lambdaRecord struct {
	Timestamp      time.Time `json:"timestamp"`
	RequestContext struct {
		FunctionArn string `json:"functionArn"`
		Condition   string `json:"condition"`
	} `json:"requestContext"`
	RequestPayload  json.RawMessage `json:"requestPayload"`
	ResponsePayload struct {
		ErrorMessage string `json:"errorMessage"`
		ErrorType    string `json:"errorType"`
	} `json:"responsePayload"`
}

// ReadDeadLetter tells what a message is. One it cannot read is "unknown"
// and is left in the queue for a person.
func ReadDeadLetter(m dlq.Message) DeadLetter {
	dl := DeadLetter{Message: m, Kind: "unknown"}
	var l Letter
	if json.Unmarshal([]byte(m.Body), &l) == nil && l.Kind == "judgement" {
		dl.Kind, dl.Letter, dl.Error, dl.At = "judgement", l, l.Error, l.At
		return dl
	}
	var r lambdaRecord
	if json.Unmarshal([]byte(m.Body), &r) == nil && r.RequestContext.FunctionArn != "" {
		dl.Kind, dl.Payload, dl.At = "run", r.RequestPayload, r.Timestamp
		dl.Function = functionName(r.RequestContext.FunctionArn)
		dl.Error = strings.TrimSpace(r.ResponsePayload.ErrorType + " " + r.ResponsePayload.ErrorMessage)
		if dl.Error == "" {
			dl.Error = r.RequestContext.Condition
		}
	}
	return dl
}

// functionName takes the name out of arn:aws:lambda:…:function:NAME[:qualifier].
func functionName(arn string) string {
	_, rest, ok := strings.Cut(arn, ":function:")
	if !ok {
		return arn
	}
	name, _, _ := strings.Cut(rest, ":")
	return name
}

// Line is how radar dlq shows a dead letter.
func (dl DeadLetter) Line() string {
	at := dl.At.In(time.FixedZone("UTC+8", 8*60*60)).Format("01/02 15:04")
	switch dl.Kind {
	case "judgement":
		return fmt.Sprintf("%s  評分  %s（%s，重試 %d 次）  %s", at, titleOr(dl.Letter.Title, dl.Letter.Key), dl.Letter.Class, dl.Letter.Attempts, truncate(dl.Error, 80))
	case "run":
		return fmt.Sprintf("%s  執行  %s %s  %s", at, dl.Function, truncate(string(dl.Payload), 60), truncate(dl.Error, 80))
	}
	return "?  看不懂的訊息  " + truncate(dl.Message.Body, 80)
}

// Invoker starts a function asynchronously with an event.
type Invoker interface {
	InvokeAsync(ctx context.Context, function string, payload []byte) error
}

// LetterBox is the queue as redrive uses it.
type LetterBox interface {
	Delete(ctx context.Context, m dlq.Message) error
}

// Redrive sends the dead letters again and deletes each one that went out:
// a failed run is started again with its event; the judgements go to the
// model as one new batch from their kept pages (its summary comes through
// batch-check as usual). A judgement without a kept page, or a message it
// cannot read, stays in the queue. It returns one line per message.
func Redrive(ctx context.Context, d Deps, box LetterBox, inv Invoker, a BatchAssessor, n notify.Notifier,
	kept KeptPages, profile string, letters []DeadLetter) ([]string, error) {
	var report []string
	var errs []error
	var pages []assess.Page
	var judged []DeadLetter
	for _, dl := range letters {
		switch {
		case dl.Kind == "run" && strings.HasPrefix(dl.Function, "job-radar-"):
			if err := inv.InvokeAsync(ctx, dl.Function, dl.Payload); err != nil {
				errs = append(errs, err)
				report = append(report, "⚠️ 重跑失敗："+dl.Function+"："+err.Error())
				continue
			}
			report = append(report, "🔁 重跑 "+dl.Function+" "+truncate(string(dl.Payload), 60))
			errs = append(errs, box.Delete(ctx, dl.Message))
		case dl.Kind == "judgement" && dl.Letter.JD != "":
			var p assess.Page
			if err := kept.Load(ctx, dl.Letter.JD, &p); err != nil {
				report = append(report, "⚠️ 讀不到 "+dl.Letter.JD+"，留在 DLQ")
				continue
			}
			pages, judged = append(pages, p), append(judged, dl)
		default:
			report = append(report, "⏸ 留在 DLQ（沒辦法自動重送）："+dl.Line())
		}
	}
	if len(pages) > 0 {
		r, err := SubmitPages(ctx, d, a, n, kept, PageRules{}, profile, pages)
		switch {
		case err != nil:
			errs = append(errs, err)
			report = append(report, fmt.Sprintf("⚠️ %d 個評分重送失敗，留在 DLQ：%v", len(pages), err))
		default:
			report = append(report, fmt.Sprintf("🔁 %d 個評分重新送出（批次 %s；兩週內評過的會略過）", len(pages), r.BatchID))
			for _, dl := range judged {
				errs = append(errs, box.Delete(ctx, dl.Message))
			}
		}
	}
	return report, errors.Join(errs...)
}
