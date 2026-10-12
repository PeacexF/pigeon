package pigeon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"text/template"
	"time"

	"github.com/PeacexF/pigeon/internal/render"
)

// Option configures a Gateway.
type Option func(*config)

// ChannelOption configures one channel registration.
type ChannelOption func(*channelSpec)

// TemplateOption configures one template.
type TemplateOption func(*render.Spec)

type config struct {
	channels       []channelSpec
	routes         []routeSpec
	templates      []render.Spec
	funcs          template.FuncMap
	retry          RetryPolicy
	workers        int
	queueSize      int
	dedupeWindow   time.Duration
	attemptTimeout time.Duration
	logger         *slog.Logger
	hooks          Hooks
	errs           []error
}

type channelSpec struct {
	name   string
	ch     Channel
	rate   *Rate
	noRate bool
	retry  *RetryPolicy
	errs   []error
}

type routeSpec struct {
	pattern  string
	channels []string
}

func newConfig() *config {
	return &config{
		funcs:          template.FuncMap{},
		retry:          DefaultRetryPolicy(),
		workers:        4,
		queueSize:      1024,
		dedupeWindow:   24 * time.Hour,
		attemptTimeout: 30 * time.Second,
		logger:         slog.New(slog.DiscardHandler),
	}
}

func (c *config) fail(err error) { c.errs = append(c.errs, fmt.Errorf("pigeon: %w", err)) }

// RetryPolicy controls how failed deliveries are retried.
type RetryPolicy struct {
	// MaxAttempts includes the first attempt; 1 disables retries.
	MaxAttempts int

	// The delay before retry n is random in [0, min(MaxDelay, BaseDelay·2ⁿ⁻¹)].
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// DefaultRetryPolicy is 5 attempts with delays starting at 1s and capped at 5m.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 5 * time.Minute}
}

func (p RetryPolicy) validate() error {
	switch {
	case p.MaxAttempts < 1:
		return errors.New("retry policy: MaxAttempts must be at least 1")
	case p.BaseDelay <= 0:
		return errors.New("retry policy: BaseDelay must be positive")
	case p.MaxDelay < p.BaseDelay:
		return errors.New("retry policy: MaxDelay must be at least BaseDelay")
	}
	return nil
}

// Hooks observe the delivery pipeline. Nil fields are skipped. Hooks run
// synchronously on Send's or a worker's goroutine, so they must be fast and
// must not block.
type Hooks struct {
	OnEnqueue    func(ctx context.Context, e Event, channels []string)
	OnDuplicate  func(ctx context.Context, e Event, originalID string)
	OnAttempt    func(ctx context.Context, msg *Message, err error, took time.Duration)
	OnDelivered  func(ctx context.Context, msg *Message)
	OnDeadLetter func(ctx context.Context, msg *Message, err error)
}

// WithChannel registers ch under name. Names may contain only letters,
// digits, '-' and '_'. The same Channel value may be registered under
// several names; each registration has its own rate limit.
func WithChannel(name string, ch Channel, opts ...ChannelOption) Option {
	return func(c *config) {
		spec := channelSpec{name: name, ch: ch}
		for _, opt := range opts {
			opt(&spec)
		}
		c.channels = append(c.channels, spec)
	}
}

// WithRoute delivers events whose type matches pattern to the named channels.
// In patterns, * matches any run of characters, dots included: "alert.*",
// "*.critical", "*". An event matching several routes goes to the union of
// their channels, once each.
func WithRoute(pattern string, channels ...string) Option {
	return func(c *config) {
		c.routes = append(c.routes, routeSpec{pattern: pattern, channels: channels})
	}
}

// WithTemplate renders events whose type matches pattern with a text/template.
// The template sees .ID, .Type, .Channel, .Time and .Data; a missing key in
// .Data is an error at Send. When several templates match, a channel-scoped
// one (see For) beats a global one, then an exact pattern beats a wildcard,
// then the pattern with the most literal characters wins. Events with no
// matching template render as their type followed by sorted "key: value" lines.
func WithTemplate(pattern, text string, opts ...TemplateOption) Option {
	return func(c *config) {
		spec := render.Spec{Pattern: pattern, Text: text}
		for _, opt := range opts {
			opt(&spec)
		}
		c.templates = append(c.templates, spec)
	}
}

// WithTemplateFuncs makes funcs available to every template, regardless of option order.
func WithTemplateFuncs(funcs template.FuncMap) Option {
	return func(c *config) { maps.Copy(c.funcs, funcs) }
}

// WithRetry sets the default retry policy for all channels. The default is DefaultRetryPolicy.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) {
		if err := p.validate(); err != nil {
			c.fail(err)
			return
		}
		c.retry = p
	}
}

// WithWorkers sets how many deliveries run concurrently. The default is 4.
func WithWorkers(n int) Option {
	return func(c *config) {
		if n < 1 {
			c.fail(errors.New("workers must be at least 1"))
			return
		}
		c.workers = n
	}
}

// WithQueueSize bounds how many deliveries may be unfinished at once, counting
// queued, retrying and in-flight ones. When full, Send blocks. The default is 1024.
func WithQueueSize(n int) Option {
	return func(c *config) {
		if n < 1 {
			c.fail(errors.New("queue size must be at least 1"))
			return
		}
		c.queueSize = n
	}
}

// WithDedupeWindow sets how long idempotency keys are remembered. The default is 24h.
func WithDedupeWindow(d time.Duration) Option {
	return func(c *config) {
		if d <= 0 {
			c.fail(errors.New("dedupe window must be positive"))
			return
		}
		c.dedupeWindow = d
	}
}

// WithAttemptTimeout bounds each call to Channel.Send. The default is 30s.
func WithAttemptTimeout(d time.Duration) Option {
	return func(c *config) {
		if d <= 0 {
			c.fail(errors.New("attempt timeout must be positive"))
			return
		}
		c.attemptTimeout = d
	}
}

// WithLogger sets the logger for delivery outcomes. By default nothing is logged.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l == nil {
			c.fail(errors.New("nil logger"))
			return
		}
		c.logger = l
	}
}

// WithHooks sets the pipeline hooks.
func WithHooks(h Hooks) Option {
	return func(c *config) { c.hooks = h }
}

// RateLimit allows n deliveries per period on this channel, in bursts of up to n.
// It overrides the channel's DefaultRate.
func RateLimit(n int, per time.Duration) ChannelOption {
	return func(s *channelSpec) {
		if n < 1 || per <= 0 {
			s.errs = append(s.errs, errors.New("rate limit needs n >= 1 and a positive period"))
			return
		}
		s.rate = &Rate{N: n, Per: per}
		s.noRate = false
	}
}

// NoRateLimit removes the channel's DefaultRate. Pauses requested with RetryAfter still apply.
func NoRateLimit() ChannelOption {
	return func(s *channelSpec) {
		s.rate = nil
		s.noRate = true
	}
}

// Retry overrides the retry policy for this channel.
func Retry(p RetryPolicy) ChannelOption {
	return func(s *channelSpec) {
		if err := p.validate(); err != nil {
			s.errs = append(s.errs, err)
			return
		}
		s.retry = &p
	}
}

// Title adds a title template, used as e.g. an email subject or a bold first line.
func Title(text string) TemplateOption {
	return func(s *render.Spec) { s.Title = &text }
}

// For limits the template to the named channels.
func For(channels ...string) TemplateOption {
	return func(s *render.Spec) { s.Channels = append(s.Channels, channels...) }
}
