// Package discord delivers pigeon messages through a Discord webhook.
//
// Create a webhook in the channel's settings (Integrations → Webhooks) and
// pass its URL to New. The URL is a secret: anyone who has it can post.
//
// A message without a title is sent as plain message content, cut to
// Discord's 2000-character limit. A message with a title is sent as an embed,
// with the title (up to 256 characters) above the text (up to 4096). Cut text
// ends with "…". Discord renders Markdown in both; escape event data with
// EscapeMarkdown, registered through pigeon.WithTemplateFuncs. Mentions such
// as @everyone are not notified unless WithMentions is set.
//
// Besides the default rate limit, the channel follows the X-RateLimit headers
// Discord sends: once a webhook's bucket is used up, sends wait for it to
// reset.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/httpx"
	"github.com/PeacexF/pigeon/internal/truncate"
)

// Length limits. Text is measured in UTF-16 code units, which is never less
// than the character count Discord checks.
const (
	MaxContentLen     = 2000
	MaxTitleLen       = 256
	MaxDescriptionLen = 4096
)

const suppressNotifications = 1 << 12

// Channel posts to one Discord webhook. Create it with New.
type Channel struct {
	webhookURL string
	client     *http.Client
	username   string
	avatarURL  string
	threadID   string
	mentions   bool
	color      *int
	silent     bool

	mu      sync.Mutex
	resetAt time.Time // the webhook's rate-limit bucket is empty until then
}

// Option configures a Channel.
type Option func(*Channel)

// New returns a Channel posting to webhookURL, which looks like
// https://discord.com/api/webhooks/<id>/<token>.
func New(webhookURL string, opts ...Option) *Channel {
	c := &Channel{webhookURL: webhookURL, client: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithUsername overrides the webhook's display name.
func WithUsername(name string) Option { return func(c *Channel) { c.username = name } }

// WithAvatarURL overrides the webhook's avatar.
func WithAvatarURL(u string) Option { return func(c *Channel) { c.avatarURL = u } }

// WithThreadID posts into a thread of the webhook's channel.
func WithThreadID(id string) Option { return func(c *Channel) { c.threadID = id } }

// WithMentions lets @everyone, @here, and user and role mentions in the text notify people.
func WithMentions() Option { return func(c *Channel) { c.mentions = true } }

// WithColor sets the embed's side color as 0xRRGGBB. It applies to messages with a title.
func WithColor(rgb int) Option { return func(c *Channel) { c.color = &rgb } }

// WithSilent delivers messages without push and desktop notifications.
func WithSilent() Option { return func(c *Channel) { c.silent = true } }

// WithHTTPClient replaces http.DefaultClient.
func WithHTTPClient(client *http.Client) Option { return func(c *Channel) { c.client = client } }

// Validate reports configuration that can never work.
func (c *Channel) Validate() error {
	var problems []string
	if _, err := c.endpoint(); err != nil {
		problems = append(problems, err.Error())
	}
	if c.client == nil {
		problems = append(problems, "nil HTTP client")
	}
	if c.color != nil && (*c.color < 0 || *c.color > 0xFFFFFF) {
		problems = append(problems, fmt.Sprintf("color %#x is not 0xRRGGBB", *c.color))
	}
	if len(problems) > 0 {
		return errors.New("discord: " + strings.Join(problems, "; "))
	}
	return nil
}

// DefaultRate is Discord's limit of 30 webhook messages per minute in a
// channel. Shorter bursts are governed by the rate-limit headers.
func (c *Channel) DefaultRate() pigeon.Rate { return pigeon.Rate{N: 30, Per: time.Minute} }

func (c *Channel) endpoint() (string, error) {
	u, err := url.Parse(c.webhookURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("webhook URL must be an http or https URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[len(parts)-3] != "webhooks" || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", errors.New("webhook URL must end in /webhooks/<id>/<token>")
	}
	q := u.Query()
	q.Set("wait", "true") // report failures instead of accepting and dropping the message
	if c.threadID != "" {
		q.Set("thread_id", c.threadID)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// secrets are what to redact from errors: the URL in any form, and the token.
func (c *Channel) secrets() []string {
	s := []string{c.webhookURL}
	if u, err := url.Parse(c.webhookURL); err == nil {
		s = append(s, u.Path[strings.LastIndex(u.Path, "/")+1:])
	}
	return s
}

type executeRequest struct {
	Content         string           `json:"content,omitempty"`
	Username        string           `json:"username,omitempty"`
	AvatarURL       string           `json:"avatar_url,omitempty"`
	Embeds          []embed          `json:"embeds,omitempty"`
	AllowedMentions *allowedMentions `json:"allowed_mentions,omitempty"`
	Flags           int              `json:"flags,omitempty"`
}

type embed struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Color       *int   `json:"color,omitempty"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

type errorResponse struct {
	Message    string  `json:"message"`
	Code       int     `json:"code"`
	RetryAfter float64 `json:"retry_after"`
}

// Send executes the webhook with msg.
func (c *Channel) Send(ctx context.Context, msg *pigeon.Message) error {
	endpoint, err := c.endpoint()
	if err != nil {
		return pigeon.Permanent(fmt.Errorf("discord: %w", err))
	}
	req := executeRequest{Username: c.username, AvatarURL: c.avatarURL}
	if msg.Title != "" {
		req.Embeds = []embed{{
			Title:       truncate.UTF16(msg.Title, MaxTitleLen),
			Description: truncate.UTF16(msg.Text, MaxDescriptionLen),
			Color:       c.color,
		}}
	} else {
		req.Content = truncate.UTF16(msg.Text, MaxContentLen)
	}
	if !c.mentions {
		req.AllowedMentions = &allowedMentions{Parse: []string{}}
	}
	if c.silent {
		req.Flags = suppressNotifications
	}

	if err := c.waitForBucket(ctx); err != nil {
		return err
	}
	res, err := httpx.PostJSON(ctx, c.client, endpoint, nil, req, c.secrets()...)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	c.trackBucket(res.Header)
	if res.StatusCode/100 == 2 {
		return nil
	}

	var body errorResponse
	_ = json.Unmarshal(res.Body, &body)
	desc := body.Message
	if desc == "" {
		desc = http.StatusText(res.StatusCode)
	}
	if body.Code != 0 {
		desc += fmt.Sprintf(" (code %d)", body.Code)
	}
	err = fmt.Errorf("discord: %d %s", res.StatusCode, desc)
	if res.StatusCode == http.StatusTooManyRequests && body.RetryAfter > 0 {
		return pigeon.RetryAfter(err, time.Duration(body.RetryAfter*float64(time.Second)))
	}
	return httpx.Classify(res.StatusCode, res.Header, err)
}

func (c *Channel) waitForBucket(ctx context.Context) error {
	c.mu.Lock()
	wait := time.Until(c.resetAt)
	c.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("discord: waiting for rate limit reset: %w", ctx.Err())
	}
}

func (c *Channel) trackBucket(h http.Header) {
	if h.Get("X-RateLimit-Remaining") != "0" {
		return
	}
	secs, err := strconv.ParseFloat(h.Get("X-RateLimit-Reset-After"), 64)
	if err != nil || secs <= 0 || secs > 3600 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetAt = time.Now().Add(time.Duration(secs * float64(time.Second)))
}

var markdownEscaper = func() *strings.Replacer {
	var pairs []string
	for _, r := range "\\*_~`|>#-[]()<" {
		pairs = append(pairs, string(r), "\\"+string(r))
	}
	return strings.NewReplacer(pairs...)
}()

// EscapeMarkdown escapes s so Discord shows it literally instead of as
// formatting, links or headings.
func EscapeMarkdown(s string) string { return markdownEscaper.Replace(s) }
