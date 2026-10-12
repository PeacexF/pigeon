package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
)

const token = "123456:test-token"

type fakeAPI struct {
	*httptest.Server
	path   string
	body   map[string]any
	status int
	reply  string
	header http.Header
}

func newFakeAPI(t *testing.T, status int, reply string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{status: status, reply: reply, header: http.Header{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &f.body); err != nil {
			t.Errorf("request body is not JSON: %s", raw)
		}
		maps.Copy(w.Header(), f.header)
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) channel(opts ...Option) *Channel {
	return New(token, "-1001234", append([]Option{WithBaseURL(f.URL + "/"), WithHTTPClient(f.Client())}, opts...)...)
}

func TestSendRequest(t *testing.T) {
	api := newFakeAPI(t, http.StatusOK, `{"ok":true}`)
	ch := api.channel(WithParseMode(ParseModeHTML), WithSilent(), WithThreadID(42), WithoutLinkPreview())

	if err := ch.Send(t.Context(), &pigeon.Message{Title: "Disk", Text: "95% full"}); err != nil {
		t.Fatal(err)
	}
	if api.path != "/bot"+token+"/sendMessage" {
		t.Errorf("path = %q", api.path)
	}
	want := map[string]any{
		"chat_id":              "-1001234",
		"text":                 "Disk\n\n95% full",
		"parse_mode":           "HTML",
		"disable_notification": true,
		"message_thread_id":    float64(42),
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	for k, v := range want {
		if got, _ := json.Marshal(api.body[k]); string(got) != mustJSON(v) {
			t.Errorf("%s = %s, want %s", k, got, mustJSON(v))
		}
	}
}

func TestSendMinimalRequest(t *testing.T) {
	api := newFakeAPI(t, http.StatusOK, `{"ok":true}`)
	if err := api.channel().Send(t.Context(), &pigeon.Message{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(api.body) != 2 || api.body["text"] != "hi" {
		t.Errorf("body = %v, want only chat_id and text", api.body)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSendErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		reply     string
		header    string
		permanent bool
		delay     time.Duration
		msg       string
	}{
		{"flood control", 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`, "", false, 7 * time.Second, "telegram: 429 Too Many Requests: retry after 7"},
		{"429 with header only", 429, `{}`, "3", false, 3 * time.Second, "telegram: 429 Too Many Requests"},
		{"chat not found", 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, "", true, 0, "telegram: 400 Bad Request: chat not found"},
		{"bot kicked", 403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the group chat"}`, "", true, 0, "Forbidden"},
		{"bad token", 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, "", true, 0, "401 Unauthorized"},
		{"migrated", 400, `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1009999}}`, "", true, 0, "use that chat ID"},
		{"server error", 502, `<html>bad gateway</html>`, "", false, 0, "telegram: 502 Bad Gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t, tt.status, tt.reply)
			if tt.header != "" {
				api.header.Set("Retry-After", tt.header)
			}
			err := api.channel().Send(t.Context(), &pigeon.Message{Text: "x"})
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
			if strings.Contains(err.Error(), token) {
				t.Errorf("err leaks the token: %q", err)
			}
		})
	}
}

func TestSendNetworkErrorHidesToken(t *testing.T) {
	api := newFakeAPI(t, http.StatusOK, "")
	ch := api.channel()
	api.Close()

	err := ch.Send(t.Context(), &pigeon.Message{Text: "x"})
	if err == nil {
		t.Fatal("Send to a closed server succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("err leaks the token: %q", err)
	}
	if pigeon.IsPermanent(err) {
		t.Error("network error marked permanent")
	}
}

func TestSendHonorsContext(t *testing.T) {
	api := newFakeAPI(t, http.StatusOK, `{"ok":true}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := api.channel().Send(ctx, &pigeon.Message{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		ch   *Channel
		want string
	}{
		{"ok", New("t", "c"), ""},
		{"no token", New("", "c"), "empty bot token"},
		{"no chat", New("t", ""), "empty chat ID"},
		{"nil client", New("t", "c", WithHTTPClient(nil)), "nil HTTP client"},
		{"bad parse mode", New("t", "c", WithParseMode("Markdown")), `unknown parse mode "Markdown"`},
	}
	for _, tt := range tests {
		err := tt.ch.Validate()
		if (err == nil) != (tt.want == "") || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%s: Validate() = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestDefaultRate(t *testing.T) {
	if r := New("t", "c").DefaultRate(); r.N != 1 || r.Per != time.Second {
		t.Errorf("DefaultRate = %+v", r)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", MaxTextLen); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
	exact := strings.Repeat("a", MaxTextLen)
	if got := truncate(exact, MaxTextLen); got != exact {
		t.Error("text at the limit was truncated")
	}

	long := truncate(strings.Repeat("a", 5000), MaxTextLen)
	if utf16Len(long) != MaxTextLen || !strings.HasSuffix(long, "…") {
		t.Errorf("long text: %d UTF-16 units, suffix %q", utf16Len(long), long[len(long)-3:])
	}

	// 😀 is two UTF-16 units: an odd limit must not split it.
	emoji := truncate(strings.Repeat("😀", 10), 6)
	if emoji != "😀😀…" {
		t.Errorf("emoji truncation = %q", emoji)
	}
}

func TestEscapeHTML(t *testing.T) {
	if got, want := EscapeHTML(`<b>Tom & "Jerry"</b>`), `&lt;b&gt;Tom &amp; "Jerry"&lt;/b&gt;`; got != want {
		t.Errorf("EscapeHTML = %q, want %q", got, want)
	}
}

func TestEscapeMarkdownV2(t *testing.T) {
	if got, want := EscapeMarkdownV2(`v1.2 (beta)! *50%* _off_ a\b`), `v1\.2 \(beta\)\! \*50%\* \_off\_ a\\b`; got != want {
		t.Errorf("EscapeMarkdownV2 = %q, want %q", got, want)
	}
}
