// Package httpx is the HTTP plumbing shared by channel adapters.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PeacexF/pigeon"
)

// MaxBody caps how much of a response is read; providers' replies are small,
// and an unbounded read lets a misbehaving endpoint exhaust memory.
const MaxBody = 1 << 20

// UserAgent is sent unless the caller sets its own.
const UserAgent = "pigeon (+https://github.com/PeacexF/pigeon)"

// Response is a fully read HTTP response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// PostJSON sends payload as JSON with the extra header, which may be nil.
// See Post.
func PostJSON(ctx context.Context, client *http.Client, url string, header http.Header, payload any, secrets ...string) (*Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	return Post(ctx, client, url, h, body, secrets...)
}

// Post sends body with header and reads up to MaxBody of the response.
// Every secret is redacted from returned errors, since request errors
// include the URL and adapters often carry tokens in it.
func Post(ctx context.Context, client *http.Client, url string, header http.Header, body []byte, secrets ...string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, Redact(err, secrets...)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, Redact(err, secrets...)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return nil, Redact(fmt.Errorf("read response: %w", err), secrets...)
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: data}, nil
}

// Classify attaches pigeon's retry semantics to err for a non-2xx status:
// 429 and 503 honor Retry-After, 408 and 5xx are retryable, and other 4xx are permanent.
func Classify(status int, header http.Header, err error) error {
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		if d, ok := ParseRetryAfter(header.Get("Retry-After"), time.Now()); ok {
			return pigeon.RetryAfter(err, d)
		}
		return err
	case status == http.StatusRequestTimeout || status >= 500:
		return err
	case status >= 400:
		return pigeon.Permanent(err)
	}
	return err
}

// ParseRetryAfter parses a Retry-After value in either delay-seconds or HTTP-date form.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 || secs > int64(24*time.Hour/time.Second) {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// Redact replaces every secret in err's message with "REDACTED". The result
// still matches the original's sentinels (context.DeadlineExceeded, ...) with
// errors.Is, but deliberately doesn't unwrap: the wrapped error's message
// would leak the secret.
func Redact(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := msg
	for _, s := range secrets {
		if s != "" {
			redacted = strings.ReplaceAll(redacted, s, "REDACTED")
		}
	}
	if redacted == msg {
		return err
	}
	return &redactedError{msg: redacted, orig: err}
}

type redactedError struct {
	msg  string
	orig error
}

func (e *redactedError) Error() string        { return e.msg }
func (e *redactedError) Is(target error) bool { return errors.Is(e.orig, target) }
