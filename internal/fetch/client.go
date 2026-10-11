// Package fetch is the only place the radar talks HTTP. It owns the four
// problems every scraper of public APIs meets: per-host rate limits, requests
// that hang, transient failures, and telling a dead board from a bad day.
package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Outcome classifies one request so a run can report *why* boards failed,
// instead of lumping 404s, 429s and timeouts into one "unreachable" count.
type Outcome string

const (
	OK           Outcome = "ok"
	NotFound     Outcome = "not_found" // 404/410: the board no longer exists
	Throttled    Outcome = "throttled" // 429 after all retries
	Timeout      Outcome = "timeout"
	ServerError  Outcome = "server_error"
	ClientError  Outcome = "client_error" // other 4xx: our request is wrong
	NetworkError Outcome = "network_error"
	ParseError   Outcome = "parse_error"
	Blocked      Outcome = "blocked" // host not on the allowlist
)

// Error carries the classified outcome of a failed request.
type Error struct {
	Outcome Outcome
	Status  int
	URL     string
	Err     error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: HTTP %d (%s)", e.URL, e.Status, e.Outcome)
	}
	return fmt.Sprintf("%s: %s: %v", e.URL, e.Outcome, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Classify maps any error from GetJSON to an Outcome; nil means OK.
func Classify(err error) Outcome {
	if err == nil {
		return OK
	}
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Outcome
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	return NetworkError
}

type Options struct {
	// Requests per second per host; hosts not listed use DefaultRPS.
	RPS        map[string]float64
	DefaultRPS float64
	// Per-attempt timeout, including reading the body.
	Timeout    time.Duration
	Retries    int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	UserAgent  string
	MaxBody    int64
	AllowHosts []string
	// Sleep is replaceable so tests never wait on the wall clock.
	Sleep func(ctx context.Context, d time.Duration) error
}

type Client struct {
	http     *http.Client
	opts     Options
	allowed  map[string]bool
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func New(opts Options) *Client {
	if opts.Timeout == 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.DefaultRPS == 0 {
		opts.DefaultRPS = 5
	}
	if opts.BaseDelay == 0 {
		opts.BaseDelay = 500 * time.Millisecond
	}
	if opts.MaxDelay == 0 {
		opts.MaxDelay = 10 * time.Second
	}
	if opts.MaxBody == 0 {
		opts.MaxBody = 32 << 20
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "job-radar/0.1 (personal job alerts; once per day)"
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	allowed := map[string]bool{}
	for _, h := range opts.AllowHosts {
		allowed[h] = true
	}
	return &Client{
		// http.Client.Timeout covers the whole exchange, body read included,
		// so a server that sends headers and then stalls cannot hang a worker.
		http: &http.Client{
			Timeout: opts.Timeout,
			// Never follow redirects: an API answering with a redirect is not
			// an answer we trust, and it could point off the allowlist.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		opts:     opts,
		allowed:  allowed,
		limiters: map[string]*rate.Limiter{},
	}
}

func (c *Client) limiter(host string) *rate.Limiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.limiters[host]
	if !ok {
		rps := c.opts.DefaultRPS
		if v, ok := c.opts.RPS[host]; ok && v > 0 {
			rps = v
		}
		l = rate.NewLimiter(rate.Limit(rps), 1)
		c.limiters[host] = l
	}
	return l
}

// GetJSON fetches rawURL and decodes the JSON body into v, retrying
// throttling, 5xx and network failures with backoff and jitter.
func (c *Client) GetJSON(ctx context.Context, rawURL string, v any) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &Error{Outcome: ClientError, URL: rawURL, Err: err}
	}
	if len(c.allowed) > 0 && !c.allowed[u.Hostname()] {
		return &Error{Outcome: Blocked, URL: rawURL, Err: errors.New("host not allowlisted")}
	}
	var last *Error
	for attempt := 0; attempt <= c.opts.Retries; attempt++ {
		if attempt > 0 {
			if err := c.opts.Sleep(ctx, c.backoff(attempt, last)); err != nil {
				return last
			}
		}
		if err := c.limiter(u.Hostname()).Wait(ctx); err != nil {
			if last != nil {
				return last
			}
			return &Error{Outcome: Timeout, URL: rawURL, Err: err}
		}
		last = c.once(ctx, rawURL, v)
		if last == nil {
			return nil
		}
		if !retryable(last.Outcome) {
			return last
		}
	}
	return last
}

func (c *Client) once(ctx context.Context, rawURL string, v any) *Error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return &Error{Outcome: ClientError, URL: rawURL, Err: err}
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Outcome: transportOutcome(err), URL: rawURL, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &Error{Outcome: statusOutcome(resp.StatusCode), Status: resp.StatusCode, URL: rawURL,
			Err: retryAfterError{resp.Header.Get("Retry-After")}}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, c.opts.MaxBody)).Decode(v); err != nil {
		if isTimeout(err) {
			return &Error{Outcome: Timeout, URL: rawURL, Err: err}
		}
		return &Error{Outcome: ParseError, URL: rawURL, Err: err}
	}
	return nil
}

// retryAfterError carries the Retry-After header from a non-200 response.
type retryAfterError struct{ value string }

func (e retryAfterError) Error() string { return "retry-after=" + e.value }

func (c *Client) backoff(attempt int, last *Error) time.Duration {
	if last != nil {
		var ra retryAfterError
		if errors.As(last.Err, &ra) {
			if d, ok := ParseRetryAfter(ra.value, time.Now()); ok {
				return min(d, c.opts.MaxDelay)
			}
		}
	}
	d := c.opts.BaseDelay << (attempt - 1)
	d += time.Duration(rand.Int64N(int64(250 * time.Millisecond)))
	return min(d, c.opts.MaxDelay)
}

// ParseRetryAfter accepts both forms the header allows: seconds or an HTTP date.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, t.Sub(now)), true
	}
	return 0, false
}

func retryable(o Outcome) bool {
	switch o {
	case Throttled, ServerError, Timeout, NetworkError:
		return true
	}
	return false
}

func statusOutcome(code int) Outcome {
	switch {
	case code == http.StatusNotFound || code == http.StatusGone:
		return NotFound
	case code == http.StatusTooManyRequests:
		return Throttled
	case code >= 500:
		return ServerError
	default:
		return ClientError
	}
}

func transportOutcome(err error) Outcome {
	if isTimeout(err) {
		return Timeout
	}
	return NetworkError
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
