// Package ntfy delivers pigeon messages to an ntfy topic, on https://ntfy.sh
// or a self-hosted server.
//
// Messages are published as JSON. A title becomes the notification title.
// Text beyond ntfy's 4096-byte message limit is truncated with "…".
// Priority, tags, click URL and icon are set per channel; register the same
// topic under several names to use different settings for different events.
package ntfy

import (
	"context"
	"encoding/base64"
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

const (
	// DefaultBaseURL is the public ntfy server.
	DefaultBaseURL = "https://ntfy.sh"

	// MaxMessageBytes is ntfy's message size limit.
	MaxMessageBytes = 4096
)

// Priority is a notification priority from PriorityMin to PriorityMax.
type Priority int

// Priorities.
const (
	PriorityMin     Priority = 1
	PriorityLow     Priority = 2
	PriorityDefault Priority = 3
	PriorityHigh    Priority = 4
	PriorityMax     Priority = 5
)

// Channel publishes to one ntfy topic. Create it with New.
type Channel struct {
	topic    string
	baseURL  string
	client   *http.Client
	token    string
	user     string
	password string
	priority Priority
	tags     []string
	click    string
	icon     string
	markdown bool
}

// Option configures a Channel.
type Option func(*Channel)

// New returns a Channel publishing to topic on ntfy.sh. Anyone who knows a
// topic name on ntfy.sh can read it, so pick one that is hard to guess or use
// a server with access control.
func New(topic string, opts ...Option) *Channel {
	c := &Channel{topic: topic, baseURL: DefaultBaseURL, client: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithBaseURL publishes to a self-hosted server, e.g. "https://ntfy.example.com".
func WithBaseURL(u string) Option {
	return func(c *Channel) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient replaces http.DefaultClient.
func WithHTTPClient(client *http.Client) Option { return func(c *Channel) { c.client = client } }

// WithToken authenticates with an access token ("tk_...").
func WithToken(token string) Option { return func(c *Channel) { c.token = token } }

// WithBasicAuth authenticates with a username and password.
func WithBasicAuth(user, password string) Option {
	return func(c *Channel) { c.user, c.password = user, password }
}

// WithPriority sets the priority of every notification.
func WithPriority(p Priority) Option { return func(c *Channel) { c.priority = p } }

// WithTags adds tags; tags that match an emoji short code, such as "warning", show as that emoji.
func WithTags(tags ...string) Option { return func(c *Channel) { c.tags = append(c.tags, tags...) } }

// WithClick opens u when the notification is tapped.
func WithClick(u string) Option { return func(c *Channel) { c.click = u } }

// WithIcon shows the image at u with the notification.
func WithIcon(u string) Option { return func(c *Channel) { c.icon = u } }

// WithMarkdown renders the text as Markdown in clients that support it.
func WithMarkdown() Option { return func(c *Channel) { c.markdown = true } }

// Validate reports configuration that can never work.
func (c *Channel) Validate() error {
	var problems []string
	if !validTopic(c.topic) {
		problems = append(problems, fmt.Sprintf("topic %q must be 1-64 letters, digits, '-' or '_'", c.topic))
	}
	if u, err := url.Parse(c.baseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		problems = append(problems, fmt.Sprintf("base URL %q must be an http or https URL", c.baseURL))
	}
	if c.client == nil {
		problems = append(problems, "nil HTTP client")
	}
	if c.priority != 0 && (c.priority < PriorityMin || c.priority > PriorityMax) {
		problems = append(problems, fmt.Sprintf("priority %d out of range 1-5", c.priority))
	}
	if c.token != "" && c.user != "" {
		problems = append(problems, "both token and basic auth set")
	}
	if len(problems) > 0 {
		return errors.New("ntfy: " + strings.Join(problems, "; "))
	}
	return nil
}

func validTopic(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// DefaultRate matches ntfy.sh's per-client limit: a burst of 60, then one
// request every 5 seconds. Self-hosted servers set their own limits, so
// there is no default for them.
func (c *Channel) DefaultRate() pigeon.Rate {
	if c.baseURL == DefaultBaseURL {
		return pigeon.Rate{N: 60, Per: 5 * time.Minute}
	}
	return pigeon.Rate{}
}

type publishRequest struct {
	Topic    string   `json:"topic"`
	Message  string   `json:"message"`
	Title    string   `json:"title,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Priority Priority `json:"priority,omitempty"`
	Click    string   `json:"click,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	Markdown bool     `json:"markdown,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// Send publishes msg.
func (c *Channel) Send(ctx context.Context, msg *pigeon.Message) error {
	req := publishRequest{
		Topic:    c.topic,
		Message:  truncate.Bytes(msg.Text, MaxMessageBytes),
		Title:    msg.Title,
		Tags:     c.tags,
		Priority: c.priority,
		Click:    c.click,
		Icon:     c.icon,
		Markdown: c.markdown,
	}
	header := http.Header{}
	switch {
	case c.token != "":
		header.Set("Authorization", "Bearer "+c.token)
	case c.user != "":
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.password)))
	}

	res, err := httpx.PostJSON(ctx, c.client, c.baseURL+"/", header, req, c.token, c.password)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	if res.StatusCode/100 == 2 {
		return nil
	}

	var body errorResponse
	_ = json.Unmarshal(res.Body, &body)
	desc := body.Error
	if desc == "" {
		desc = http.StatusText(res.StatusCode)
	}
	return httpx.Classify(res.StatusCode, res.Header, fmt.Errorf("ntfy: %d %s", res.StatusCode, desc))
}
