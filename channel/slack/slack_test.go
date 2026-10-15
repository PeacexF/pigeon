package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/fakehttp"
)

const (
	hookPath = "/services/T000/B000/s3cr3t"
	botToken = "xoxb-s3cr3t"
)

func webhook(srv *fakehttp.Server) *Channel {
	return New(srv.URL+hookPath, WithHTTPClient(srv.Client()))
}

func bot(srv *fakehttp.Server) *Channel {
	return NewBot(botToken, "C123", WithBaseURL(srv.URL+"/api/"), WithHTTPClient(srv.Client()))
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestWebhookSend(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "ok")
	if err := webhook(srv).Send(t.Context(), &pigeon.Message{Text: "*db1* is down"}); err != nil {
		t.Fatal(err)
	}
	if p := srv.Last(t).Path; p != hookPath {
		t.Errorf("path = %q", p)
	}
	if got := string(srv.Last(t).Body); got != `{"text":"*db1* is down"}` {
		t.Errorf("body = %s", got)
	}
}

func TestSendWithTitle(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "ok")
	if err := webhook(srv).Send(t.Context(), &pigeon.Message{Title: "Disk", Text: "95% full"}); err != nil {
		t.Fatal(err)
	}
	want := `{"text":"Disk\n95% full","blocks":[` +
		`{"type":"header","text":{"type":"plain_text","text":"Disk"}},` +
		`{"type":"section","text":{"type":"mrkdwn","text":"95% full"}}]}`
	if got := string(srv.Last(t).Body); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}

	if err := webhook(srv).Send(t.Context(), &pigeon.Message{Title: "Only a title"}); err != nil {
		t.Fatal(err)
	}
	if blocks := srv.LastJSON(t)["blocks"].([]any); len(blocks) != 1 {
		t.Errorf("blocks = %v, want only the header", blocks)
	}
}

func TestSendTruncates(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "ok")
	long := strings.Repeat("a", MaxTextLen+10)
	if err := webhook(srv).Send(t.Context(), &pigeon.Message{Title: long, Text: long}); err != nil {
		t.Fatal(err)
	}
	var body postRequest
	if err := json.Unmarshal(srv.Last(t).Body, &body); err != nil {
		t.Fatal(err)
	}
	if len([]rune(body.Text)) != MaxTextLen || len([]rune(body.Blocks[0].Text.Text)) != MaxHeaderLen || len([]rune(body.Blocks[1].Text.Text)) != MaxSectionLen {
		t.Errorf("lengths: text %d, header %d, section %d", len(body.Text), len(body.Blocks[0].Text.Text), len(body.Blocks[1].Text.Text))
	}
}

func TestWebhookErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		reply     string
		header    []string
		permanent bool
		delay     time.Duration
		msg       string
	}{
		{"rate limited", 429, "rate_limited", []string{"Retry-After", "30"}, false, 30 * time.Second, "slack: 429 rate_limited"},
		{"bad payload", 400, "invalid_payload", nil, true, 0, "slack: 400 invalid_payload"},
		{"forbidden", 403, "action_prohibited", nil, true, 0, "action_prohibited"},
		{"gone", 404, "no_service", nil, true, 0, "no_service"},
		{"archived", 410, "channel_is_archived", nil, true, 0, "channel_is_archived"},
		{"server error", 500, "rollup_error", nil, false, 0, "slack: 500 rollup_error"},
		{"html error", 503, "<html>" + strings.Repeat("x", 300), nil, false, 0, "slack: 503 Service Unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakehttp.New(t, tt.status, tt.reply)
			srv.Reply(tt.status, tt.reply, tt.header...)
			err := webhook(srv).Send(t.Context(), &pigeon.Message{Text: "x"})
			checkErr(t, err, tt.permanent, tt.delay, tt.msg)
		})
	}
}

func TestBotSend(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{"ok":true,"ts":"1.2"}`)
	if err := bot(srv).Send(t.Context(), &pigeon.Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last(t)
	if req.Path != "/api/chat.postMessage" || req.Header.Get("Authorization") != "Bearer "+botToken {
		t.Errorf("request = %s, Authorization %q", req.Path, req.Header.Get("Authorization"))
	}
	if got := string(req.Body); got != `{"channel":"C123","text":"hi"}` {
		t.Errorf("body = %s", got)
	}
}

func TestBotErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		reply     string
		header    []string
		permanent bool
		delay     time.Duration
		msg       string
	}{
		{"channel not found", 200, `{"ok":false,"error":"channel_not_found"}`, nil, true, 0, "slack: channel_not_found"},
		{"bad auth", 200, `{"ok":false,"error":"invalid_auth"}`, nil, true, 0, "slack: invalid_auth"},
		{"internal", 200, `{"ok":false,"error":"internal_error"}`, nil, false, 0, "slack: internal_error"},
		{"ratelimited in body", 200, `{"ok":false,"error":"ratelimited"}`, []string{"Retry-After", "3"}, false, 3 * time.Second, "ratelimited"},
		{"http 429", 429, `{"ok":false,"error":"ratelimited"}`, []string{"Retry-After", "5"}, false, 5 * time.Second, "slack: 429"},
		{"not json", 200, `<html>`, nil, false, 0, "slack: 200"},
		{"gateway", 502, ``, nil, false, 0, "slack: 502 Bad Gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakehttp.New(t, tt.status, tt.reply)
			srv.Reply(tt.status, tt.reply, tt.header...)
			checkErr(t, bot(srv).Send(t.Context(), &pigeon.Message{Text: "x"}), tt.permanent, tt.delay, tt.msg)
		})
	}
}

func checkErr(t *testing.T, err error, permanent bool, wantDelay time.Duration, msg string) {
	t.Helper()
	if err == nil {
		t.Fatal("Send succeeded")
	}
	delay, _ := pigeon.RetryAfterDelay(err)
	if pigeon.IsPermanent(err) != permanent || delay != wantDelay {
		t.Errorf("permanent = %v, delay = %v; want %v, %v", pigeon.IsPermanent(err), delay, permanent, wantDelay)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Errorf("err = %q, want it to contain %q", err, msg)
	}
}

func TestNetworkErrorHidesSecrets(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "ok")
	hook, b := webhook(srv), bot(srv)
	srv.Close()
	for _, ch := range []*Channel{hook, b} {
		err := ch.Send(t.Context(), &pigeon.Message{Text: "x"})
		if err == nil || pigeon.IsPermanent(err) {
			t.Fatalf("err = %v, want a retryable error", err)
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("err leaks a secret: %q", err)
		}
	}
}

func TestSendHonorsContext(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "ok")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := webhook(srv).Send(ctx, &pigeon.Message{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		ch   *Channel
		want string
	}{
		{"webhook", New("https://hooks.slack.com/services/T/B/X"), ""},
		{"bot", NewBot("xoxb-1", "#ops"), ""},
		{"bad webhook", New("hooks.slack.com/x"), "webhook URL"},
		{"empty webhook", New(""), "webhook URL"},
		{"no token", NewBot("", "#ops"), "empty bot token"},
		{"no channel", NewBot("xoxb-1", ""), "empty channel"},
		{"bad base URL", NewBot("xoxb-1", "#ops", WithBaseURL("nope")), "base URL"},
		{"nil client", New("https://hooks.slack.com/x", WithHTTPClient(nil)), "nil HTTP client"},
	}
	for _, tt := range tests {
		err := tt.ch.Validate()
		if (err == nil) != (tt.want == "") || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: Validate() = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestDefaultRate(t *testing.T) {
	if r := New("https://x").DefaultRate(); mustJSON(r) != `{"N":1,"Per":1000000000}` {
		t.Errorf("DefaultRate = %+v", r)
	}
}

func TestEscape(t *testing.T) {
	if got, want := Escape("<!channel> & <https://evil|click>"), "&lt;!channel&gt; &amp; &lt;https://evil|click&gt;"; got != want {
		t.Errorf("Escape = %q, want %q", got, want)
	}
}
