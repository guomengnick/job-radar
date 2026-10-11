// Package notify delivers the daily digest. Telegram is the first channel
// because it is free, needs one HTTPS POST, and reaches the phone instantly.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Notifier interface {
	Send(ctx context.Context, text string) error
}

// Writer prints the message instead of sending it (local runs, dry runs).
type Writer struct{ W io.Writer }

func (w Writer) Send(_ context.Context, text string) error {
	_, err := fmt.Fprintln(w.W, text)
	return err
}

type Telegram struct {
	Token  string
	ChatID string
	HTTP   *http.Client
	// BaseURL is overridable for tests.
	BaseURL string
}

// Telegram rejects messages longer than 4096 characters.
const telegramLimit = 4000

func (t Telegram) Send(ctx context.Context, text string) error {
	for _, part := range Split(text, telegramLimit) {
		if err := t.send(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

func (t Telegram) send(ctx context.Context, text string) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	body, _ := json.Marshal(map[string]any{
		"chat_id": t.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// The URL carries the token; never let it reach a log line.
		return fmt.Errorf("telegram send failed: %s", strings.ReplaceAll(err.Error(), t.Token, "<token>"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram send: HTTP %d: %s", resp.StatusCode, msg)
	}
	return nil
}

// Split breaks text on line boundaries so each part fits limit runes.
func Split(text string, limit int) []string {
	var parts []string
	var cur strings.Builder
	n := 0
	for _, line := range strings.Split(text, "\n") {
		l := len([]rune(line)) + 1
		if n > 0 && n+l > limit {
			parts = append(parts, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
			n = 0
		}
		cur.WriteString(line + "\n")
		n += l
	}
	if n > 0 {
		parts = append(parts, strings.TrimRight(cur.String(), "\n"))
	}
	return parts
}
