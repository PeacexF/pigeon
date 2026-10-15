// Package webhook delivers pigeon messages as JSON POST requests to any URL.
//
// The body is a Payload. Every request carries these headers:
//
//	Content-Type:      application/json
//	Idempotency-Key:   the message ID, the same on every retry
//	Pigeon-Event-Type: the event type
//	Pigeon-Signature:  t=<unix seconds>,v1=<hex HMAC-SHA256> (only with WithSecret)
//
// The signature is computed over "<t>.<body>" with the shared secret, so
// receivers can check that a request came from pigeon and is recent; Verify
// does that in Go.
//
// Any 2xx response is a success. Redirects are not followed and fail the
// delivery permanently, as do 4xx responses other than 408 and 429.
//
// # Internal addresses
//
// By default the channel refuses to connect to loopback, private,
// link-local, carrier-grade NAT and other non-public addresses, including
// cloud metadata endpoints. The check runs when connecting, so DNS tricks
// can't get around it. WithAllowPrivate turns it off for receivers on your
// own network. A client set with WithHTTPClient is used as is, without the
// check. The default client ignores proxy environment variables.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/internal/httpx"
	"github.com/PeacexF/pigeon/internal/ssrf"
)

// Payload is the JSON body of each request.
type Payload struct {
	// ID identifies this message (event and channel) and is the same on every retry.
	ID        string         `json:"id"`
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	Channel   string         `json:"channel"`
	Title     string         `json:"title,omitempty"`
	Text      string         `json:"text"`
	Data      map[string]any `json:"data,omitempty"`
	Attempt   int            `json:"attempt"`
}

// Header names.
const (
	HeaderIdempotencyKey = "Idempotency-Key"
	HeaderEventType      = "Pigeon-Event-Type"
	HeaderSignature      = "Pigeon-Signature"
)

// Channel posts to one URL. Create it with New.
type Channel struct {
	url          string
	client       *http.Client
	ownClient    bool
	allowPrivate bool
	secret       []byte
	header       http.Header
}

// Option configures a Channel.
type Option func(*Channel)

// New returns a Channel posting to rawURL.
func New(rawURL string, opts ...Option) *Channel {
	c := &Channel{url: rawURL, header: http.Header{}}
	for _, opt := range opts {
		opt(c)
	}
	if c.client == nil && !c.ownClient {
		c.client = newClient(c.allowPrivate)
	}
	return c
}

// WithSecret signs every request with HMAC-SHA256 using secret. See Verify.
func WithSecret(secret string) Option { return func(c *Channel) { c.secret = []byte(secret) } }

// WithHeader adds a header to every request, e.g. an Authorization header.
func WithHeader(key, value string) Option { return func(c *Channel) { c.header.Add(key, value) } }

// WithAllowPrivate allows connections to non-public addresses, for receivers
// on your own network. Only use it when the URL is not user-controlled.
func WithAllowPrivate() Option { return func(c *Channel) { c.allowPrivate = true } }

// WithHTTPClient replaces the default client. The internal-address check and
// the no-redirects policy are then up to client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Channel) { c.client, c.ownClient = client, true }
}

func newClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		dialer.Control = ssrf.Control
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // a proxy would make the dialer check the proxy's address, not the receiver's
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Validate reports configuration that can never work.
func (c *Channel) Validate() error {
	var problems []string
	u, err := url.Parse(c.url)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		problems = append(problems, "URL must be an http or https URL")
	case !c.allowPrivate && !c.ownClient:
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !ssrf.Allowed(ip) {
			problems = append(problems, fmt.Sprintf("%s is not a public address (see WithAllowPrivate)", ip))
		}
	}
	if c.client == nil {
		problems = append(problems, "nil HTTP client")
	}
	for k := range c.header {
		if strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, HeaderSignature) {
			problems = append(problems, fmt.Sprintf("header %s is set by the channel", k))
		}
	}
	if len(problems) > 0 {
		return errors.New("webhook: " + strings.Join(problems, "; "))
	}
	return nil
}

// Close releases idle connections of the default client.
func (c *Channel) Close() error {
	if !c.ownClient && c.client != nil {
		c.client.CloseIdleConnections()
	}
	return nil
}

// Send posts msg as a Payload.
func (c *Channel) Send(ctx context.Context, msg *pigeon.Message) error {
	body, err := json.Marshal(Payload{
		ID:        msg.ID,
		EventID:   msg.EventID,
		EventType: msg.EventType,
		Channel:   msg.Channel,
		Title:     msg.Title,
		Text:      msg.Text,
		Data:      msg.Data,
		Attempt:   msg.Attempt,
	})
	if err != nil {
		return pigeon.Permanent(fmt.Errorf("webhook: encode payload: %w", err))
	}

	header := c.header.Clone()
	header.Set("Content-Type", "application/json")
	header.Set(HeaderIdempotencyKey, msg.ID)
	header.Set(HeaderEventType, msg.EventType)
	if len(c.secret) > 0 {
		header.Set(HeaderSignature, sign(c.secret, time.Now(), body))
	}

	// The URL may carry a token, so errors name only the host.
	host := "invalid URL"
	if u, err := url.Parse(c.url); err == nil {
		host = u.Host
	}
	res, err := httpx.Post(ctx, c.client, c.url, header, body, c.url)
	if err != nil {
		err = fmt.Errorf("webhook %s: %w", host, err)
		if errors.Is(err, ssrf.ErrBlocked) {
			return pigeon.Permanent(err)
		}
		return err
	}

	code := res.StatusCode
	switch code / 100 {
	case 2:
		return nil
	case 3:
		return pigeon.Permanent(fmt.Errorf("webhook %s: %d redirect not followed", host, code))
	default:
		return httpx.Classify(code, res.Header, fmt.Errorf("webhook %s: %d %s", host, code, http.StatusText(code)))
	}
}

func sign(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

func mac(secret []byte, ts string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// DefaultTolerance is how old a signature Verify accepts when given a tolerance of 0.
const DefaultTolerance = 5 * time.Minute

// ErrInvalidSignature is returned by Verify for a missing, malformed, wrong or expired signature.
var ErrInvalidSignature = errors.New("webhook: invalid signature")

// Verify checks a Pigeon-Signature header value against the raw request body
// and the shared secret, and rejects signatures more than tolerance away from
// now (DefaultTolerance if tolerance is 0). A header may hold several v1
// signatures, e.g. while rotating secrets; one matching is enough.
//
// Read the body before decoding it, and verify the exact bytes received.
func Verify(secret, signature string, body []byte, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	var ts string
	var sigs []string
	for part := range strings.SplitSeq(signature, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return fmt.Errorf("%w: malformed header", ErrInvalidSignature)
	}
	if age := time.Since(time.Unix(unix, 0)); age > tolerance || age < -tolerance {
		return fmt.Errorf("%w: timestamp outside tolerance", ErrInvalidSignature)
	}
	want := mac([]byte(secret), ts, body)
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return fmt.Errorf("%w: no matching signature", ErrInvalidSignature)
}
