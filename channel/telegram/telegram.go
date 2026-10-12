// Package telegram delivers pigeon messages with the Telegram Bot API.
//
// Create a bot with @BotFather to get a token, add it to the chat, and use the
// chat's ID: a number such as -1001234567890 for groups and channels, or
// "@channelname" for public channels.
//
// Messages are sent as plain text by default, so event data can never break
// formatting. With WithParseMode, escape data in templates using EscapeHTML
// or EscapeMarkdownV2, registered through pigeon.WithTemplateFuncs.
// A title becomes the first paragraph. Text beyond Telegram's 4096-character
// limit is truncated with "…"; with a parse mode, truncation may cut through
// markup and make Telegram reject the message.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/httpx"
)

const (
	// DefaultBaseURL is the public Bot API endpoint.
	DefaultBaseURL = "https://api.telegram.org"

	// MaxTextLen is Telegram's message length limit, in UTF-16 code units.
	MaxTextLen = 4096
)

// ParseMode selects how Telegram interprets message text.
type ParseMode string

// Parse modes.
const (
	ParseModeNone       ParseMode = ""
	ParseModeHTML       ParseMode = "HTML"
	ParseModeMarkdownV2 ParseMode = "MarkdownV2"
)

// Channel sends messages to one Telegram chat. Create it with New.
type Channel struct {
	token     string
	chatID    string
	baseURL   string
	client    *http.Client
	parseMode ParseMode
	silent    bool
	noPreview bool
	threadID  int64
}

// Option configures a Channel.
type Option func(*Channel)

// New returns a Channel posting to chatID as the bot identified by token.
func New(token, chatID string, opts ...Option) *Channel {
	c := &Channel{
		token:   token,
		chatID:  chatID,
		baseURL: DefaultBaseURL,
		client:  http.DefaultClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithParseMode enables HTML or MarkdownV2 formatting.
func WithParseMode(m ParseMode) Option { return func(c *Channel) { c.parseMode = m } }

// WithSilent delivers messages without a notification sound.
func WithSilent() Option { return func(c *Channel) { c.silent = true } }

// WithoutLinkPreview disables link previews.
func WithoutLinkPreview() Option { return func(c *Channel) { c.noPreview = true } }

// WithThreadID posts into a forum topic.
func WithThreadID(id int64) Option { return func(c *Channel) { c.threadID = id } }

// WithHTTPClient replaces http.DefaultClient.
func WithHTTPClient(client *http.Client) Option { return func(c *Channel) { c.client = client } }

// WithBaseURL points the channel at a self-hosted Bot API server or a test server.
func WithBaseURL(url string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(url, "/") }
}

// Validate reports configuration that can never work.
func (c *Channel) Validate() error {
	var problems []string
	if c.token == "" {
		problems = append(problems, "empty bot token")
	}
	if c.chatID == "" {
		problems = append(problems, "empty chat ID")
	}
	if c.client == nil {
		problems = append(problems, "nil HTTP client")
	}
	switch c.parseMode {
	case ParseModeNone, ParseModeHTML, ParseModeMarkdownV2:
	default:
		problems = append(problems, fmt.Sprintf("unknown parse mode %q", c.parseMode))
	}
	if len(problems) > 0 {
		return errors.New("telegram: " + strings.Join(problems, "; "))
	}
	return nil
}

// DefaultRate is Telegram's documented limit for a single chat.
func (c *Channel) DefaultRate() pigeon.Rate { return pigeon.Rate{N: 1, Per: time.Second} }

type sendMessageRequest struct {
	ChatID              string              `json:"chat_id"`
	Text                string              `json:"text"`
	ParseMode           ParseMode           `json:"parse_mode,omitempty"`
	DisableNotification bool                `json:"disable_notification,omitempty"`
	MessageThreadID     int64               `json:"message_thread_id,omitempty"`
	LinkPreviewOptions  *linkPreviewOptions `json:"link_preview_options,omitempty"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter      int   `json:"retry_after"`
		MigrateToChatID int64 `json:"migrate_to_chat_id"`
	} `json:"parameters"`
}

// Send posts msg with sendMessage.
func (c *Channel) Send(ctx context.Context, msg *pigeon.Message) error {
	text := msg.Text
	if msg.Title != "" {
		text = msg.Title + "\n\n" + text
	}
	req := sendMessageRequest{
		ChatID:              c.chatID,
		Text:                truncate(text, MaxTextLen),
		ParseMode:           c.parseMode,
		DisableNotification: c.silent,
		MessageThreadID:     c.threadID,
	}
	if c.noPreview {
		req.LinkPreviewOptions = &linkPreviewOptions{IsDisabled: true}
	}

	res, err := httpx.PostJSON(ctx, c.client, c.baseURL+"/bot"+c.token+"/sendMessage", req, c.token)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	if res.StatusCode == http.StatusOK {
		return nil
	}

	var body apiResponse
	_ = json.Unmarshal(res.Body, &body)
	desc := body.Description
	if desc == "" {
		desc = http.StatusText(res.StatusCode)
	}
	err = fmt.Errorf("telegram: %d %s", res.StatusCode, desc)

	switch {
	case body.Parameters.MigrateToChatID != 0:
		return pigeon.Permanent(fmt.Errorf("%w (group became supergroup %d; use that chat ID)", err, body.Parameters.MigrateToChatID))
	case res.StatusCode == http.StatusTooManyRequests && body.Parameters.RetryAfter > 0:
		return pigeon.RetryAfter(err, time.Duration(body.Parameters.RetryAfter)*time.Second)
	}
	return httpx.Classify(res.StatusCode, res.Header, err)
}

func truncate(s string, limit int) string {
	if utf16Len(s) <= limit {
		return s
	}
	n := 0
	for i, r := range s {
		w := max(utf16.RuneLen(r), 1)
		if n+w > limit-1 { // leave room for the ellipsis
			return s[:i] + "…"
		}
		n += w
	}
	return s
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += max(utf16.RuneLen(r), 1)
	}
	return n
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// EscapeHTML escapes s for ParseModeHTML.
func EscapeHTML(s string) string { return htmlEscaper.Replace(s) }

// EscapeMarkdownV2 escapes s for ParseModeMarkdownV2.
func EscapeMarkdownV2(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(`\_*[]()~`+"`"+`>#+-=|{}.!`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
