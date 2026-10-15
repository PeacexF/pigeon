package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/fakehttp"
)

const token = "s3cr3t-webhook-token"

func newChannel(srv *fakehttp.Server, opts ...Option) *Channel {
	return New(srv.URL+"/api/webhooks/123/"+token, append([]Option{WithHTTPClient(srv.Client())}, opts...)...)
}

func TestSendContent(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{"id":"1"}`)
	if err := newChannel(srv).Send(t.Context(), &pigeon.Message{Text: "95% full"}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last(t)
	if req.Path != "/api/webhooks/123/"+token || req.Query != "wait=true" {
		t.Errorf("request = %s?%s", req.Path, req.Query)
	}
	body := srv.LastJSON(t)
	if body["content"] != "95% full" || body["embeds"] != nil {
		t.Errorf("body = %v", body)
	}
	if got := mustJSON(body["allowed_mentions"]); got != `{"parse":[]}` {
		t.Errorf("allowed_mentions = %s, want mentions disabled", got)
	}
}

func TestSendEmbed(t *testing.T) {
	srv := fakehttp.New(t, http.StatusNoContent, "")
	ch := newChannel(srv, WithUsername("pigeon"), WithAvatarURL("https://example.com/a.png"),
		WithThreadID("999"), WithColor(0xFF0000), WithSilent(), WithMentions())
	if err := ch.Send(t.Context(), &pigeon.Message{Title: "Disk", Text: "95% full"}); err != nil {
		t.Fatal(err)
	}
	if q := srv.Last(t).Query; q != "thread_id=999&wait=true" {
		t.Errorf("query = %q", q)
	}
	body := srv.LastJSON(t)
	want := map[string]string{
		"content":          "null",
		"username":         `"pigeon"`,
		"avatar_url":       `"https://example.com/a.png"`,
		"flags":            "4096",
		"allowed_mentions": "null",
		"embeds":           `[{"color":16711680,"description":"95% full","title":"Disk"}]`,
	}
	for k, v := range want {
		if got := mustJSON(body[k]); got != v {
			t.Errorf("%s = %s, want %s", k, got, v)
		}
	}
}

func TestSendTruncates(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{}`)
	ch := newChannel(srv)
	long := strings.Repeat("😀", 5000)

	if err := ch.Send(t.Context(), &pigeon.Message{Text: long}); err != nil {
		t.Fatal(err)
	}
	if n := utf16Len(srv.LastJSON(t)["content"]); n > MaxContentLen {
		t.Errorf("content is %d UTF-16 units", n)
	}

	if err := ch.Send(t.Context(), &pigeon.Message{Title: long, Text: long}); err != nil {
		t.Fatal(err)
	}
	e := srv.LastJSON(t)["embeds"].([]any)[0].(map[string]any)
	if utf16Len(e["title"]) > MaxTitleLen || utf16Len(e["description"]) > MaxDescriptionLen {
		t.Errorf("embed title %d, description %d UTF-16 units", utf16Len(e["title"]), utf16Len(e["description"]))
	}
}

func utf16Len(v any) int {
	s, _ := v.(string)
	return len(utf16.Encode([]rune(s)))
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
		{"rate limited", 429, `{"message":"You are being rate limited.","retry_after":1.5,"global":false}`, nil, false, 1500 * time.Millisecond, "discord: 429 You are being rate limited."},
		{"retry-after header", 429, `{}`, []string{"Retry-After", "2"}, false, 2 * time.Second, "429 Too Many Requests"},
		{"empty message", 400, `{"message":"Cannot send an empty message","code":50006}`, nil, true, 0, "discord: 400 Cannot send an empty message (code 50006)"},
		{"unknown webhook", 404, `{"message":"Unknown Webhook","code":10015}`, nil, true, 0, "Unknown Webhook"},
		{"invalid token", 401, `{"message":"Invalid Webhook Token","code":50027}`, nil, true, 0, "Invalid Webhook Token"},
		{"server error", 502, `<html>`, nil, false, 0, "discord: 502 Bad Gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fakehttp.New(t, tt.status, tt.reply)
			srv.Reply(tt.status, tt.reply, tt.header...)
			err := newChannel(srv).Send(t.Context(), &pigeon.Message{Text: "x"})
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

func TestSendNetworkErrorHidesToken(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, "")
	ch := newChannel(srv)
	srv.Close()
	err := ch.Send(t.Context(), &pigeon.Message{Text: "x"})
	if err == nil || pigeon.IsPermanent(err) {
		t.Fatalf("err = %v, want a retryable error", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("err leaks the token: %q", err)
	}
}

func TestSendWaitsForBucketReset(t *testing.T) {
	srv := fakehttp.New(t, http.StatusOK, `{}`)
	srv.Reply(http.StatusOK, `{}`, "X-RateLimit-Remaining", "0", "X-RateLimit-Reset-After", "0.2")
	ch := newChannel(srv)
	if err := ch.Send(t.Context(), &pigeon.Message{Text: "1"}); err != nil {
		t.Fatal(err)
	}
	srv.Reply(http.StatusOK, `{}`, "X-RateLimit-Remaining", "4")
	start := time.Now()
	if err := ch.Send(t.Context(), &pigeon.Message{Text: "2"}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Errorf("second send after an exhausted bucket took %v", took)
	}

	ch.resetAt = time.Now().Add(time.Hour)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := ch.Send(ctx, &pigeon.Message{Text: "3"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("%d requests, want 2", n)
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
		{"ok", New("https://discord.com/api/webhooks/1/abc"), ""},
		{"versioned", New("https://discord.com/api/v10/webhooks/1/abc?thread_id=5"), ""},
		{"empty", New(""), "http or https URL"},
		{"not a webhook", New("https://discord.com/channels/1/2"), "/webhooks/<id>/<token>"},
		{"no token", New("https://discord.com/api/webhooks/1/"), "/webhooks/<id>/<token>"},
		{"nil client", New("https://discord.com/api/webhooks/1/abc", WithHTTPClient(nil)), "nil HTTP client"},
		{"bad color", New("https://discord.com/api/webhooks/1/abc", WithColor(-1)), "color"},
	}
	for _, tt := range tests {
		err := tt.ch.Validate()
		if (err == nil) != (tt.want == "") || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: Validate() = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestSendInvalidURLIsPermanent(t *testing.T) {
	if err := New("nope").Send(t.Context(), &pigeon.Message{Text: "x"}); !pigeon.IsPermanent(err) {
		t.Errorf("err = %v, want permanent", err)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	got := EscapeMarkdown("**bold** _it_ [link](http://x) # h - a > q `c` ~s~ |sp| <@1> a\\b")
	want := `\*\*bold\*\* \_it\_ \[link\]\(http://x\) \# h \- a \> q \` + "`c\\`" + ` \~s\~ \|sp\| \<@1\> a\\b`
	if got != want {
		t.Errorf("EscapeMarkdown =\n%s\nwant\n%s", got, want)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
