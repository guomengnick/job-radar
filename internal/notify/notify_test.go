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
