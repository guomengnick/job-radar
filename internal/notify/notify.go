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

// Button is one button under a message: Text is shown, Data comes back in
// the callback when it is pressed (Telegram allows 64 bytes).
type Button struct{ Text, Data string }

// ButtonSender is a Notifier that can put rows of buttons under a message.
// A caller holding a plain Notifier sends the text without them.
type ButtonSender interface {
	SendWithButtons(ctx context.Context, text string, rows [][]Button) error
}

// Writer prints the message instead of sending it (local runs, dry runs).
type Writer struct{ W io.Writer }

func (w Writer) Send(_ context.Context, text string) error {
	_, err := fmt.Fprintln(w.W, text)
	return err
}

// SendWithButtons prints the buttons as a line each, after the text.
func (w Writer) SendWithButtons(ctx context.Context, text string, rows [][]Button) error {
	if err := w.Send(ctx, text); err != nil {
		return err
	}
	for _, row := range rows {
		var labels []string
		for _, b := range row {
			labels = append(labels, "["+b.Text+"]")
		}
		if _, err := fmt.Fprintln(w.W, strings.Join(labels, " ")); err != nil {
			return err
		}
	}
	return nil
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
	return t.SendWithButtons(ctx, text, nil)
}

// SendWithButtons sends the text, split as Send does, with the buttons
// under its last part.
func (t Telegram) SendWithButtons(ctx context.Context, text string, rows [][]Button) error {
	parts := Split(text, telegramLimit)
	for i, part := range parts {
		msg := map[string]any{
			"chat_id": t.ChatID, "text": part, "parse_mode": "HTML", "disable_web_page_preview": true,
		}
		if i == len(parts)-1 && len(rows) > 0 {
			msg["reply_markup"] = inlineKeyboard(rows)
		}
		if err := t.call(ctx, "sendMessage", msg); err != nil {
			return err
		}
	}
	return nil
}

// AnswerCallback acknowledges a pressed button; Telegram shows the text
// briefly at the top of the chat.
func (t Telegram) AnswerCallback(ctx context.Context, id, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text})
}

func inlineKeyboard(rows [][]Button) map[string]any {
	keyboard := make([][]map[string]string, len(rows))
	for i, row := range rows {
		for _, b := range row {
			keyboard[i] = append(keyboard[i], map[string]string{"text": b.Text, "callback_data": b.Data})
		}
	}
	return map[string]any{"inline_keyboard": keyboard}
}

func (t Telegram) call(ctx context.Context, method string, payload map[string]any) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// The URL carries the token; never let it reach a log line.
		return fmt.Errorf("telegram %s failed: %s", method, strings.ReplaceAll(err.Error(), t.Token, "<token>"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram %s: HTTP %d: %s", method, resp.StatusCode, msg)
	}
	return nil
}

// Split breaks text on line boundaries so each part fits limit runes. A
// part left with only blank lines is dropped: Telegram rejects an empty
// message.
func Split(text string, limit int) []string {
	var parts []string
	var cur strings.Builder
	n := 0
	flush := func() {
		if part := strings.TrimRight(cur.String(), "\n"); strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
		cur.Reset()
		n = 0
	}
	for _, line := range strings.Split(text, "\n") {
		l := len([]rune(line)) + 1
		if n > 0 && n+l > limit {
			flush()
		}
		cur.WriteString(line + "\n")
		n += l
	}
	flush()
	return parts
}
