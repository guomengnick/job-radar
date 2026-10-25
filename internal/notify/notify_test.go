package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSend(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tg := Telegram{Token: "123:abc", ChatID: "42", BaseURL: srv.URL}
	if err := tg.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:abc/sendMessage" || got["chat_id"] != "42" || got["text"] != "hello" {
		t.Fatalf("path=%s body=%v", path, got)
	}
}

func TestTelegramButtonsUnderTheLastPart(t *testing.T) {
	var calls []map[string]any
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		calls, paths = append(calls, got), append(paths, r.URL.Path)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tg := Telegram{Token: "123:abc", ChatID: "42", BaseURL: srv.URL}
	long := strings.Repeat("一行字\n", 2000) // two parts
	rows := [][]Button{{{Text: "👍 投", Data: "lbl:aaa01:y"}, {Text: "👎 不", Data: "lbl:aaa01:n"}}}
	if err := tg.SendWithButtons(context.Background(), long, rows); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0]["reply_markup"] != nil {
		t.Fatalf("calls = %d, first has markup %v", len(calls), calls[0]["reply_markup"])
	}
	kb, _ := json.Marshal(calls[1]["reply_markup"])
	if string(kb) != `{"inline_keyboard":[[{"callback_data":"lbl:aaa01:y","text":"👍 投"},{"callback_data":"lbl:aaa01:n","text":"👎 不"}]]}` {
		t.Errorf("keyboard = %s", kb)
	}
	if err := tg.AnswerCallback(context.Background(), "cb1", "已記下"); err != nil {
		t.Fatal(err)
	}
	if paths[2] != "/bot123:abc/answerCallbackQuery" || calls[2]["callback_query_id"] != "cb1" || calls[2]["text"] != "已記下" {
		t.Errorf("answer = %s %v", paths[2], calls[2])
	}
}

func TestSplitDropsEmptyParts(t *testing.T) {
	// 1000 lines of 4 runes fill a 4000-rune part exactly; the final newline
	// must not become a third, empty message.
	parts := Split(strings.Repeat("一行字\n", 2000), 4000)
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if len(Split("\n\n", 10)) != 0 {
		t.Error("blank text is no message")
	}
}

func TestTelegramErrorHidesToken(t *testing.T) {
	tg := Telegram{Token: "secret-token", ChatID: "1", BaseURL: "http://127.0.0.1:1"}
	err := tg.Send(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
}

func TestSplit(t *testing.T) {
	text := strings.Repeat("0123456789\n", 50)
	parts := Split(text, 100)
	if len(parts) < 5 {
		t.Fatalf("got %d parts", len(parts))
	}
	for _, p := range parts {
		if len([]rune(p)) > 100 {
			t.Fatalf("part too long: %d", len(p))
		}
	}
	if strings.Join(parts, "\n") != strings.TrimRight(text, "\n") {
		t.Fatal("split lost content")
	}
}
