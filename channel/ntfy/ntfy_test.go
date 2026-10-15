package ntfy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/fakehttp"
)

func newChannel(srv *fakehttp.Server, opts ...Option) *Channel {
	return New("alerts", append([]Option{WithBaseURL(srv.URL + "/"), WithHTTPClient(srv.Client())}, opts...)...)
}

func TestSendRequest(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{"id":"abc"}`)
	ch := newChannel(srv, WithToken("tk_secret"), WithPriority(PriorityHigh), WithTags("warning", "db"),
		WithClick("https://example.com/x"), WithIcon("https://example.com/i.png"), WithMarkdown())

	if err := ch.Send(t.Context(), &pigeon.Message{Title: "Disk", Text: "95% full"}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last(t)
	if req.Method != http.MethodPost || req.Path != "/" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tk_secret" {
		t.Errorf("Authorization = %q", got)
	}
	body := srv.LastJSON(t)
	want := map[string]any{
		"topic": "alerts", "message": "95% full", "title": "Disk", "priority": float64(4),
		"click": "https://example.com/x", "icon": "https://example.com/i.png", "markdown": true,
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if tags, _ := body["tags"].([]any); len(tags) != 2 || tags[0] != "warning" {
		t.Errorf("tags = %v", body["tags"])
	}
}

func TestSendMinimalRequest(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{}`)
	if err := newChannel(srv).Send(t.Context(), &pigeon.Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if body := srv.LastJSON(t); len(body) != 2 {
		t.Errorf("body = %v, want only topic and message", body)
	}
	if h := srv.Last(t).Header.Get("Authorization"); h != "" {
		t.Errorf("unexpected Authorization %q", h)
	}
}

func TestSendBasicAuth(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{}`)
	if err := newChannel(srv, WithBasicAuth("phil", "pw")).Send(t.Context(), &pigeon.Message{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	r := &http.Request{Header: srv.Last(t).Header}
	if user, pass, ok := r.BasicAuth(); !ok || user != "phil" || pass != "pw" {
		t.Errorf("basic auth = %q %q %v", user, pass, ok)
	}
}

func TestSendTruncates(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{}`)
	if err := newChannel(srv).Send(t.Context(), &pigeon.Message{Text: strings.Repeat("é", MaxMessageBytes)}); err != nil {
		t.Fatal(err)
	}
	msg, _ := srv.LastJSON(t)["message"].(string)
	if len(msg) > MaxMessageBytes || !strings.HasSuffix(msg, "…") {
		t.Errorf("message is %d bytes", len(msg))
	}
}

func TestSendErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		reply     string
		header    []string
		permanent bool
		delay     time.Duration
		msg       string
	}{
		{"rate limited", 429, `{"code":42901,"http":429,"error":"limit reached: too many requests"}`, nil, false, 0, "ntfy: 429 limit reached"},
		{"retry after", 429, `{}`, []string{"Retry-After", "4"}, false, 4 * time.Second, "ntfy: 429 Too Many Requests"},
		{"forbidden", 403, `{"code":40301,"http":403,"error":"forbidden"}`, nil, true, 0, "ntfy: 403 forbidden"},
		{"unauthorized", 401, `{"code":40101,"http":401,"error":"unauthorized"}`, nil, true, 0, "401 unauthorized"},
		{"server error", 500, `oops`, nil, false, 0, "ntfy: 500 Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakehttp.New(t, tt.status, tt.reply)
			srv.Reply(tt.status, tt.reply, tt.header...)
			err := newChannel(srv, WithToken("tk_secret")).Send(t.Context(), &pigeon.Message{Text: "x"})
			if err == nil {
				t.Fatal("Send succeeded")
			}
			delay, _ := pigeon.RetryAfterDelay(err)
			if pigeon.IsPermanent(err) != tt.permanent || delay != tt.delay {
				t.Errorf("permanent = %v, delay = %v; want %v, %v", pigeon.IsPermanent(err), delay, tt.permanent, tt.delay)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("err = %q, want it to contain %q", err, tt.msg)
			}
		})
	}
}

func TestSendNetworkError(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	ch := newChannel(srv)
	srv.Close()
	err := ch.Send(t.Context(), &pigeon.Message{Text: "x"})
	if err == nil || pigeon.IsPermanent(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestSendHonorsContext(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := newChannel(srv).Send(ctx, &pigeon.Message{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		ch   *Channel
		want string
	}{
		{"ok", New("alerts"), ""},
		{"self-hosted", New("alerts", WithBaseURL("http://ntfy.local:8080")), ""},
		{"no topic", New(""), "topic"},
		{"bad topic", New("a/b"), "topic"},
		{"long topic", New(strings.Repeat("a", 65)), "topic"},
		{"bad base URL", New("t", WithBaseURL("ntfy.local")), "base URL"},
		{"nil client", New("t", WithHTTPClient(nil)), "nil HTTP client"},
		{"bad priority", New("t", WithPriority(6)), "priority 6"},
		{"two auths", New("t", WithToken("tk"), WithBasicAuth("u", "p")), "both token and basic auth"},
	}
	for _, tt := range tests {
		err := tt.ch.Validate()
		if (err == nil) != (tt.want == "") || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: Validate() = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestDefaultRate(t *testing.T) {
	if r := New("t").DefaultRate(); r.N != 60 || r.Per != 5*time.Minute {
		t.Errorf("ntfy.sh DefaultRate = %+v", r)
	}
	if r := New("t", WithBaseURL("https://ntfy.example.com")).DefaultRate(); r.N != 0 {
		t.Errorf("self-hosted DefaultRate = %+v, want none", r)
	}
}
