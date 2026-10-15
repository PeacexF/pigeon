package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
)

func TestPostJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("got %s with Content-Type %q", r.Method, r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Custom") != "v" || r.Header.Get("User-Agent") != UserAgent {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"a":1}` {
			t.Errorf("body = %s", body)
		}
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "done")
	}))
	defer srv.Close()

	res, err := PostJSON(t.Context(), srv.Client(), srv.URL, http.Header{"X-Custom": {"v"}}, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusAccepted || string(res.Body) != "done" || res.Header.Get("X-Test") != "yes" {
		t.Errorf("response = %d %q %v", res.StatusCode, res.Body, res.Header)
	}
}

func TestPostJSONCapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", MaxBody+100))
	}))
	defer srv.Close()

	res, err := PostJSON(t.Context(), srv.Client(), srv.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Body) != MaxBody {
		t.Errorf("read %d bytes, want %d", len(res.Body), MaxBody)
	}
}

func TestPostJSONRedactsSecrets(t *testing.T) {
	const secret = "123456:SECRET-token"
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/bot" + secret + "/send"
	srv.Close()

	_, err := PostJSON(t.Context(), http.DefaultClient, url, nil, nil, secret)
	if err == nil {
		t.Fatal("request to a closed server succeeded")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("err = %q", err)
	}

	_, err = PostJSON(t.Context(), http.DefaultClient, "http://bad host/"+secret, nil, nil, secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("invalid URL err = %v", err)
	}
}

func TestPostJSONEncodeError(t *testing.T) {
	if _, err := PostJSON(t.Context(), http.DefaultClient, "http://example.invalid", nil, make(chan int)); err == nil {
		t.Error("unencodable payload accepted")
	}
}

func TestPostJSONHonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := PostJSON(ctx, srv.Client(), srv.URL, nil, nil, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRedact(t *testing.T) {
	if Redact(nil, "s") != nil {
		t.Error("Redact(nil) != nil")
	}
	plain := errors.New("no secret here")
	if _, rewrapped := errors.AsType[*redactedError](Redact(plain, "s3cret", "")); rewrapped {
		t.Error("error without secrets was rewrapped")
	}
	err := Redact(&wrappedError{"call s3cret failed", context.DeadlineExceeded}, "s3cret")
	if err.Error() != "call REDACTED failed" {
		t.Errorf("message = %q", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("redaction broke errors.Is")
	}
	if errors.Unwrap(err) != nil {
		t.Error("redacted error unwraps to the unredacted one")
	}
}

type wrappedError struct {
	msg string
	err error
}

func (e *wrappedError) Error() string { return e.msg }
func (e *wrappedError) Unwrap() error { return e.err }

func TestClassify(t *testing.T) {
	base := errors.New("x")
	retryHeader := http.Header{"Retry-After": {"7"}}
	tests := []struct {
		status    int
		header    http.Header
		permanent bool
		delay     time.Duration
	}{
		{http.StatusTooManyRequests, retryHeader, false, 7 * time.Second},
		{http.StatusTooManyRequests, http.Header{}, false, 0},
		{http.StatusServiceUnavailable, retryHeader, false, 7 * time.Second},
		{http.StatusRequestTimeout, nil, false, 0},
		{http.StatusInternalServerError, nil, false, 0},
		{http.StatusBadRequest, nil, true, 0},
		{http.StatusUnauthorized, nil, true, 0},
		{http.StatusNotFound, nil, true, 0},
		{http.StatusFound, nil, false, 0},
	}
	for _, tt := range tests {
		err := Classify(tt.status, tt.header, base)
		delay, _ := pigeon.RetryAfterDelay(err)
		if pigeon.IsPermanent(err) != tt.permanent || delay != tt.delay || !errors.Is(err, base) {
			t.Errorf("Classify(%d) = permanent %v, delay %v", tt.status, pigeon.IsPermanent(err), delay)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"0", 0, true},
		{" 120 ", 2 * time.Minute, true},
		{"Thu, 01 Jan 2026 00:00:30 GMT", 30 * time.Second, true},
		{"Wed, 31 Dec 2025 23:00:00 GMT", 0, true},
		{"", 0, false},
		{"-1", 0, false},
		{"999999999", 0, false},
		{"soon", 0, false},
	}
	for _, tt := range tests {
		got, ok := ParseRetryAfter(tt.in, now)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseRetryAfter(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func FuzzParseRetryAfter(f *testing.F) {
	for _, s := range []string{"5", "Thu, 01 Jan 2026 00:00:30 GMT", "-3", ""} {
		f.Add(s)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, s string) {
		if d, ok := ParseRetryAfter(s, now); ok && (d < 0 || (d > 24*time.Hour && !strings.Contains(s, " "))) {
			t.Errorf("ParseRetryAfter(%q) = %v", s, d)
		}
	})
}
