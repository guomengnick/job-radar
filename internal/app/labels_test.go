package app

import (
	"context"
	"strings"
	"testing"

	"github.com/guomengnick/job-radar/internal/assess"
	"github.com/guomengnick/job-radar/internal/notify"
	"github.com/guomengnick/job-radar/internal/store"
)

// buttons records what a ButtonSender was asked to send.
type buttons struct {
	texts []string
	rows  [][][]notify.Button
}

func (b *buttons) Send(_ context.Context, text string) error {
	b.texts, b.rows = append(b.texts, text), append(b.rows, nil)
	return nil
}

func (b *buttons) SendWithButtons(_ context.Context, text string, rows [][]notify.Button) error {
	b.texts, b.rows = append(b.texts, text), append(b.rows, rows)
	return nil
}

type answers struct{ id, text string }

func (a *answers) AnswerCallback(_ context.Context, id, text string) error {
	a.id, a.text = id, text
	return nil
}

func TestLabelButtons(t *testing.T) {
	row := LabelButtons("104#7umpg")
	if len(row) != 3 || row[0].Data != "lbl:7umpg:y" || row[1].Data != "lbl:7umpg:m" || row[2].Data != "lbl:7umpg:n" {
		t.Fatalf("row = %+v", row)
	}
	for _, key := range []string{"https://jobs.example.com/p/42", "104#", "104#BAD", "greenhouse#acme#1"} {
		if LabelButtons(key) != nil {
			t.Errorf("%s: want no buttons", key)
		}
	}
}

func TestClipSendsLabelButtons(t *testing.T) {
	d, _, _, _ := setup(t)
	n := &buttons{}
	page := assess.Page{URL: "https://www.104.com.tw/job/aaa01", Text: "x"}
	if _, err := Clip(context.Background(), d, &fakeAssessor{out: judged}, n, &memArchive{}, Profile{Text: "p"}, page); err != nil {
		t.Fatal(err)
	}
	if len(n.rows) != 1 || len(n.rows[0]) != 1 || n.rows[0][0][0].Data != "lbl:aaa01:y" {
		t.Fatalf("rows = %+v", n.rows)
	}
}

func TestBatchLabelRows(t *testing.T) {
	var results []batchResult
	for i, code := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9"} {
		results = append(results, batchResult{item: store.BatchItem{Key: "104#" + code},
			a: assess.Assessment{Score: 50 + i, Company: "公司" + code, JobTitle: "職缺"}})
	}
	results = append(results,
		batchResult{item: store.BatchItem{Key: "https://jobs.example.com/x"}, a: assess.Assessment{Score: 99}},
		batchResult{item: store.BatchItem{Key: "104#fail1"}, err: context.Canceled})
	rows := batchLabelRows(results)
	if len(rows) != maxLabelRows || rows[0][0].Text != "58 公司a9" || rows[0][0].Data != "lbl:a9:i" || rows[0][1].Data != "lbl:a9:y" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[7][0].Data != "lbl:a2:i" {
		t.Errorf("the eighth is the eighth best: %+v", rows[7])
	}
}

func TestHandleLabel(t *testing.T) {
	ctx := context.Background()
	d, _, st, _ := setup(t)
	st.SaveAssessment(ctx, store.Assessment{Key: "assess#104#7umpg", Title: "資深軟體工程師", Company: "艾創點", Score: 64, Verdict: "可考慮"})
	update := func(chat int64, data string) TelegramUpdate {
		var u TelegramUpdate
		u.CallbackQuery = &struct {
			ID      string `json:"id"`
			Data    string `json:"data"`
			Message *struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		}{ID: "cb1", Data: data}
		u.CallbackQuery.Message = &struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		}{}
		u.CallbackQuery.Message.Chat.ID = chat
		return u
	}
	a := &answers{}
	if err := HandleLabel(ctx, d, a, "42", update(42, "lbl:7umpg:y")); err != nil {
		t.Fatal(err)
	}
	labels, _ := st.Labels(ctx)
	if len(labels) != 1 || labels[0].Key != "label#104#7umpg" || labels[0].Want != "建議投" ||
		a.id != "cb1" || a.text != "已記下：你覺得「投」｜艾創點｜AI 64 可考慮" {
		t.Fatalf("labels %+v, answer %q", labels, a.text)
	}
	// Pressing again changes the verdict.
	HandleLabel(ctx, d, a, "42", update(42, "lbl:7umpg:n"))
	if labels, _ := st.Labels(ctx); len(labels) != 1 || labels[0].Want != "不建議" {
		t.Errorf("relabelled: %+v", labels)
	}
	// The job's own button only says what it is.
	a = &answers{}
	HandleLabel(ctx, d, a, "42", update(42, "lbl:7umpg:i"))
	if a.text != "艾創點｜AI 64 可考慮" {
		t.Errorf("info = %q", a.text)
	}
	// Another chat, or something that is not a label button, stores nothing.
	a = &answers{}
	HandleLabel(ctx, d, a, "42", update(7, "lbl:aaa01:y"))
	HandleLabel(ctx, d, a, "42", TelegramUpdate{})
	if labels, _ := st.Labels(ctx); len(labels) != 1 || a.id != "" {
		t.Errorf("stored %+v, answered %q", labels, a.id)
	}
	HandleLabel(ctx, d, a, "42", update(42, "lbl:../x:y"))
	if !strings.Contains(a.text, "看不懂") {
		t.Errorf("bad data answered %q", a.text)
	}
	labels, _ = st.Labels(ctx)
	cases := LabelCases(labels)
	if len(cases) != 1 || cases[0].Key != "104#7umpg" || cases[0].Want != "不建議" {
		t.Errorf("cases = %+v", cases)
	}
}
