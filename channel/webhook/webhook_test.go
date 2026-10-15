package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/fakehttp"
)

var msg = &pigeon.Message{
	ID: "ev1:hook", EventID: "ev1", EventType: "order.created", Channel: "hook",
	Title: "New order", Text: "#42", Data: map[string]any{"total": 9.5}, Attempt: 2,
}

// local allows the loopback test server through the address check.
func local(srv *fakehttp.Server, opts ...Option) *Channel {
	return New(srv.URL+"/hook?token=s3cr3t", append([]Option{WithAllowPrivate()}, opts...)...)
}

func TestSendRequest(t *testing.T) {
	srv := fakehttp.New(t, http.StatusNoContent, "")
	ch := local(srv, WithHeader("Authorization", "Bearer abc"), WithSecret("shh"))
	if err := ch.Send(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	req := srv.Last(t)
	if req.Method != http.MethodPost || req.Path != "/hook" || req.Query != "token=s3cr3t" {
		t.Errorf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	for k, v := range map[string]string{
		"Content-Type":      "application/json",
		"Idempotency-Key":   "ev1:hook",
		"Pigeon-Event-Type": "order.created",
		"Authorization":     "Bearer abc",
	} {
		if got := req.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	want := `{"id":"ev1:hook","event_id":"ev1","event_type":"order.created","channel":"hook","title":"New order","text":"#42","data":{"total":9.5},"attempt":2}`
	if string(req.Body) != want {
		t.Errorf("body =\n%s\nwant\n%s", req.Body, want)
	}
	if err := Verify("shh", req.Header.Get(HeaderSignature), req.Body, 0); err != nil {
		t.Errorf("signature doesn't verify: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(req.Body, &p); err != nil || p.ID != msg.ID || p.Attempt != 2 {
		t.Errorf("Payload round trip = %+v, %v", p, err)
	}
}

func TestSendUnsigned(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	if err := local(srv).Send(t.Context(), &pigeon.Message{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if s := srv.Last(t).Header.Get(HeaderSignature); s != "" {
		t.Errorf("unsigned channel sent signature %q", s)
	}
}

func TestSendErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		header    []string
		permanent bool
		delay     time.Duration
		msg       string
	}{
		{"rate limited", 429, []string{"Retry-After", "9"}, false, 9 * time.Second, "429 Too Many Requests"},
		{"bad request", 400, nil, true, 0, "400 Bad Request"},
		{"gone", 410, nil, true, 0, "410 Gone"},
		{"timeout", 408, nil, false, 0, "408"},
		{"server error", 500, nil, false, 0, "500 Internal Server Error"},
		{"unavailable", 503, []string{"Retry-After", "2"}, false, 2 * time.Second, "503"},
		{"redirect", 302, []string{"Location", "http://169.254.169.254/"}, true, 0, "302 redirect not followed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakehttp.New(t, tt.status, "")
			srv.Reply(tt.status, "", tt.header...)
			err := local(srv).Send(t.Context(), msg)
			if err == nil {
				t.Fatal("Send succeeded")
			}
			delay, _ := pigeon.RetryAfterDelay(err)
			if pigeon.IsPermanent(err) != tt.permanent || delay != tt.delay {
				t.Errorf("permanent = %v, delay = %v; want %v, %v", pigeon.IsPermanent(err), delay, tt.permanent, tt.delay)
			}
			if !strings.Contains(err.Error(), tt.msg) || strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("err = %q, want it to contain %q and no secret", err, tt.msg)
			}
			if n := len(srv.Requests()); n != 1 {
				t.Errorf("%d requests, want 1", n)
			}
		})
	}
}

func TestBlocksInternalAddresses(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	err := New(srv.URL+"/hook?token=s3cr3t").Send(t.Context(), msg)
	if err == nil {
		t.Fatal("delivered to a loopback address")
	}
	if !pigeon.IsPermanent(err) || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("err = %v, want permanent without the URL's secret", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("server received %d requests", n)
	}
}

func TestBlocksInternalAddressesAfterResolution(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	// localhost passes Validate, which only sees the name; the dialer sees 127.0.0.1.
	ch := New(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
	if err := ch.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(t.Context(), msg); err == nil || !pigeon.IsPermanent(err) {
		t.Errorf("err = %v, want a permanent refusal", err)
	}
}

func TestCustomClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	defer srv.Close()
	ch := New(srv.URL, WithHTTPClient(srv.Client()))
	if err := ch.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(t.Context(), msg); err != nil {
		t.Errorf("custom client: %v", err)
	}
	_ = ch.Close()
}

func TestSendNetworkError(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	ch := local(srv)
	srv.Close()
	err := ch.Send(t.Context(), msg)
	if err == nil || pigeon.IsPermanent(err) || strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("err = %v, want retryable without the secret", err)
	}
	_ = ch.Close()
}

func TestSendHonorsContext(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := local(srv).Send(ctx, msg); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSendUnencodableData(t *testing.T) {
	err := New("https://example.com").Send(t.Context(), &pigeon.Message{Data: map[string]any{"c": make(chan int)}})
	if !pigeon.IsPermanent(err) {
		t.Errorf("err = %v, want permanent", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		ch   *Channel
		want string
	}{
		{"ok", New("https://example.com/hook"), ""},
		{"private allowed", New("http://10.0.0.5:8080/hook", WithAllowPrivate()), ""},
		{"private", New("http://10.0.0.5:8080/hook"), "10.0.0.5 is not a public address"},
		{"metadata", New("http://169.254.169.254/latest"), "not a public address"},
		{"ipv6 loopback", New("http://[::1]/x"), "::1 is not a public address"},
		{"no scheme", New("example.com/hook"), "http or https URL"},
		{"ftp", New("ftp://example.com"), "http or https URL"},
		{"nil client", New("https://example.com", WithHTTPClient(nil)), "nil HTTP client"},
		{"reserved header", New("https://example.com", WithHeader("content-type", "text/plain")), "is set by the channel"},
	}
	for _, tt := range tests {
		err := tt.ch.Validate()
		if (err == nil) != (tt.want == "") || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: Validate() = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestVerify(t *testing.T) {
	body := []byte(`{"id":"1"}`)
	now := time.Now()
	good := sign([]byte("k"), now, body)

	tests := []struct {
		name, secret, sig string
		body              []byte
		ok                bool
	}{
		{"valid", "k", good, body, true},
		{"rotated", "k", good + ",v1=" + strings.Repeat("0", 64), body, true},
		{"spaces", "k", strings.ReplaceAll(good, ",", ", "), body, true},
		{"wrong secret", "other", good, body, false},
		{"tampered body", "k", good, []byte(`{"id":"2"}`), false},
		{"old", "k", sign([]byte("k"), now.Add(-time.Hour), body), body, false},
		{"future", "k", sign([]byte("k"), now.Add(time.Hour), body), body, false},
		{"empty", "k", "", body, false},
		{"no v1", "k", "t=" + good[2:12], body, false},
		{"bad t", "k", "t=abc,v1=00", body, false},
	}
	for _, tt := range tests {
		err := Verify(tt.secret, tt.sig, tt.body, 0)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrInvalidSignature)) {
			t.Errorf("%s: Verify = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
	if err := Verify("k", sign([]byte("k"), now.Add(-time.Hour), body), body, 2*time.Hour); err != nil {
		t.Errorf("custom tolerance: %v", err)
	}
}

func FuzzVerify(f *testing.F) {
	f.Add("t=1,v1=00", []byte("{}"))
	f.Add(sign([]byte("k"), time.Now(), []byte("{}")), []byte("{}"))
	f.Fuzz(func(t *testing.T, sig string, body []byte) {
		if Verify("k", sig, body, 0) != nil {
			return
		}
		var ts string
		for part := range strings.SplitSeq(sig, ",") {
			if k, v, _ := strings.Cut(strings.TrimSpace(part), "="); k == "t" {
				ts = v
			}
		}
		if !strings.Contains(sig, "v1="+mac([]byte("k"), ts, body)) {
			t.Errorf("Verify accepted %q without the right MAC", sig)
		}
	})
}
