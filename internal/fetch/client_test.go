package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func noSleep(context.Context, time.Duration) error { return nil }

func newTestClient(srv *httptest.Server, retries int) *Client {
	u, _ := url.Parse(srv.URL)
	return New(Options{Retries: retries, DefaultRPS: 1000, Sleep: noSleep,
		AllowHosts: []string{u.Hostname()}, Timeout: 2 * time.Second})
}

func TestOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    Outcome
		retries int
		calls   int32
	}{
		{"ok", 200, `{"a":1}`, OK, 2, 1},
		{"not found is not retried", 404, ``, NotFound, 2, 1},
		{"throttled is retried", 429, ``, Throttled, 2, 3},
		{"5xx is retried", 503, ``, ServerError, 1, 2},
		{"bad json", 200, `{`, ParseError, 2, 1},
		{"other 4xx", 403, ``, ClientError, 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var v map[string]any
			err := newTestClient(srv, tc.retries).GetJSON(context.Background(), srv.URL, &v)
			if got := Classify(err); got != tc.want {
				t.Fatalf("outcome = %s, want %s (err %v)", got, tc.want, err)
			}
			if calls.Load() != tc.calls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.calls)
			}
		})
	}
}

func TestRecoversAfterThrottle(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	var v []any
	if err := newTestClient(srv, 2).GetJSON(context.Background(), srv.URL, &v); err != nil {
		t.Fatalf("want success after retry, got %v", err)
	}
}

func TestStalledBodyTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"a":`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	c := newTestClient(srv, 0)
	c.http.Timeout = 300 * time.Millisecond
	var v map[string]any
	err := c.GetJSON(context.Background(), srv.URL, &v)
	if got := Classify(err); got != Timeout {
		t.Fatalf("outcome = %s, want timeout (err %v)", got, err)
	}
}

func TestBlocksUnlistedHost(t *testing.T) {
	c := New(Options{AllowHosts: []string{"boards-api.greenhouse.io"}, Sleep: noSleep})
	var v any
	err := c.GetJSON(context.Background(), "https://example.com/x", &v)
	if Classify(err) != Blocked {
		t.Fatalf("want blocked, got %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	if d, ok := ParseRetryAfter("7", now); !ok || d != 7*time.Second {
		t.Fatalf("seconds form: %v %v", d, ok)
	}
	if d, ok := ParseRetryAfter("Sun, 11 Oct 2026 00:00:30 GMT", now); !ok || d != 30*time.Second {
		t.Fatalf("date form: %v %v", d, ok)
	}
	if _, ok := ParseRetryAfter("soon", now); ok {
		t.Fatal("garbage should not parse")
	}
}
