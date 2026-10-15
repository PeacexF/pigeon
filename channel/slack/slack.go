// Package slack delivers pigeon messages to Slack, through an incoming
// webhook (New) or as a bot with chat.postMessage (NewBot).
//
// A webhook is the simplest option: create an app, enable incoming webhooks
// and add one for the channel. Its URL is a secret. A bot token
// ("xoxb-...") with the chat:write scope can post to any channel the bot is
// a member of.
//
// Text is Slack mrkdwn, cut to 40,000 characters. A message with a title is
// sent as a header block (up to 150 characters) above a section block (up
// to 3000), and the plain text stays as the notification fallback. Cut text
// ends with "…". Escape event data with Escape, registered through
// pigeon.WithTemplateFuncs, so it can't inject links or mentions.
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/httpx"
	"github.com/PeacexF/pigeon/internal/truncate"
)

// DefaultBaseURL is the Slack Web API endpoint used by NewBot.
const DefaultBaseURL = "https://slack.com/api"

// Length limits, in UTF-16 code units, which is never less than the
// character count Slack checks.
const (
	MaxTextLen    = 40000
	MaxHeaderLen  = 150
	MaxSectionLen = 3000
)

// Channel posts to one Slack channel. Create it with New or NewBot.
type Channel struct {
	webhookURL string
	isBot      bool
	token      string
	channel    string
	baseURL    string
	client     *http.Client
}

// Option configures a Channel.
type Option func(*Channel)

// New returns a Channel posting to an incoming webhook URL, which looks like
// https://hooks.slack.com/services/T.../B.../....
func New(webhookURL string, opts ...Option) *Channel {
	return build(&Channel{webhookURL: webhookURL}, opts)
}

// NewBot returns a Channel posting to channel, an ID such as "C0123456789"
// or a name such as "#ops", with a bot token.
func NewBot(token, channel string, opts ...Option) *Channel {
	return build(&Channel{isBot: true, token: token, channel: channel}, opts)
}

func build(c *Channel, opts []Option) *Channel {
	c.baseURL = DefaultBaseURL
	c.client = http.DefaultClient
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithHTTPClient replaces http.DefaultClient.
func WithHTTPClient(client *http.Client) Option { return func(c *Channel) { c.client = client } }

// WithBaseURL points a bot channel at a different Web API endpoint, e.g. a test server.
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// Validate reports configuration that can never work.
func (c *Channel) Validate() error {
	var problems []string
	if c.isBot {
		if c.token == "" {
			problems = append(problems, "empty bot token")
		}
		if c.channel == "" {
			problems = append(problems, "empty channel")
		}
		if !httpURL(c.baseURL) {
			problems = append(problems, fmt.Sprintf("base URL %q must be an http or https URL", c.baseURL))
		}
	} else if !httpURL(c.webhookURL) {
		problems = append(problems, "webhook URL must be an http or https URL")
	}
	if c.client == nil {
		problems = append(problems, "nil HTTP client")
	}
	if len(problems) > 0 {
		return errors.New("slack: " + strings.Join(problems, "; "))
	}
	return nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// DefaultRate is Slack's limit of about one message per second per channel.
func (c *Channel) DefaultRate() pigeon.Rate { return pigeon.Rate{N: 1, Per: time.Second} }

type postRequest struct {
	Channel string  `json:"channel,omitempty"`
	Text    string  `json:"text"`
	Blocks  []block `json:"blocks,omitempty"`
}

type block struct {
	Type string     `json:"type"`
	Text textObject `json:"text"`
}

type textObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type apiResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// Send posts msg.
func (c *Channel) Send(ctx context.Context, msg *pigeon.Message) error {
	req := postRequest{Channel: c.channel, Text: truncate.UTF16(msg.Text, MaxTextLen)}
	if msg.Title != "" {
		req.Text = truncate.UTF16(msg.Title+"\n"+msg.Text, MaxTextLen)
		req.Blocks = []block{{Type: "header", Text: textObject{Type: "plain_text", Text: truncate.UTF16(msg.Title, MaxHeaderLen)}}}
		if msg.Text != "" {
			req.Blocks = append(req.Blocks, block{Type: "section", Text: textObject{Type: "mrkdwn", Text: truncate.UTF16(msg.Text, MaxSectionLen)}})
		}
	}
	if c.isBot {
		return c.sendBot(ctx, req)
	}
	return c.sendWebhook(ctx, req)
}

func (c *Channel) sendWebhook(ctx context.Context, req postRequest) error {
	secrets := []string{c.webhookURL}
	if u, err := url.Parse(c.webhookURL); err == nil {
		secrets = append(secrets, u.Path)
	}
	res, err := httpx.PostJSON(ctx, c.client, c.webhookURL, nil, req, secrets...)
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	if res.StatusCode/100 == 2 {
		return nil
	}
	// Webhooks answer with a plain-text error code such as "channel_not_found".
	desc := strings.TrimSpace(string(res.Body))
	if desc == "" || len(desc) > 200 {
		desc = http.StatusText(res.StatusCode)
	}
	return httpx.Classify(res.StatusCode, res.Header, fmt.Errorf("slack: %d %s", res.StatusCode, desc))
}

// retryable are chat.postMessage errors that can succeed later; any other error code won't.
var retryable = map[string]bool{
	"ratelimited":            true,
	"rate_limited":           true,
	"message_limit_exceeded": true,
	"request_timeout":        true,
	"internal_error":         true,
	"fatal_error":            true,
	"service_unavailable":    true,
}

func (c *Channel) sendBot(ctx context.Context, req postRequest) error {
	header := http.Header{"Authorization": {"Bearer " + c.token}}
	res, err := httpx.PostJSON(ctx, c.client, c.baseURL+"/chat.postMessage", header, req, c.token)
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	var body apiResponse
	jsonErr := json.Unmarshal(res.Body, &body)
	if res.StatusCode != http.StatusOK || jsonErr != nil {
		return httpx.Classify(res.StatusCode, res.Header, fmt.Errorf("slack: %d %s", res.StatusCode, http.StatusText(res.StatusCode)))
	}
	if body.OK {
		return nil
	}
	err = fmt.Errorf("slack: %s", body.Error)
	if retryable[body.Error] {
		if d, ok := httpx.ParseRetryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
			return pigeon.RetryAfter(err, d)
		}
		return err
	}
	return pigeon.Permanent(err)
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Escape escapes s for mrkdwn so it can't form links, mentions such as
// <!channel>, or other control sequences. Slack has no way to escape
// formatting characters such as * and _.
func Escape(s string) string { return escaper.Replace(s) }
