package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PeacexF/pigeon/internal/backoff"
	"github.com/PeacexF/pigeon/internal/dedupe"
	"github.com/PeacexF/pigeon/internal/id"
	"github.com/PeacexF/pigeon/internal/memqueue"
	"github.com/PeacexF/pigeon/internal/ratelimit"
	"github.com/PeacexF/pigeon/internal/render"
	"github.com/PeacexF/pigeon/internal/route"
)

const maxDedupeKeys = 100_000

// Gateway routes events to channels and delivers them in the background,
// with retries, rate limiting and deduplication. It is safe for concurrent use.
//
// Delivery is at-least-once while the process runs. Deliveries still pending
// when the process exits without Close are lost.
type Gateway struct {
	channels       []*channelState
	routes         route.Table[*channelState]
	templates      render.Registry
	dedupe         *dedupe.Window
	hooks          Hooks
	logger         *slog.Logger
	attemptTimeout time.Duration

	queue   *memqueue.Queue[*job]
	runCtx  context.Context // cancels workers and in-flight attempts on Close
	stop    context.CancelFunc
	workers sync.WaitGroup
	closed  atomic.Bool
}

type channelState struct {
	name    string
	ch      Channel
	limiter *ratelimit.Limiter
	retry   RetryPolicy
}

type job struct {
	msg      Message
	ch       *channelState
	attempts int
}

// New builds a Gateway and starts its workers. It fails if the configuration
// has any error, such as a route naming an unknown channel or a template
// that doesn't parse. Call Close to stop it.
func New(opts ...Option) (*Gateway, error) {
	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	errs := cfg.errs

	if len(cfg.channels) == 0 {
		errs = append(errs, errors.New("pigeon: no channels configured"))
	}
	if len(cfg.routes) == 0 {
		errs = append(errs, errors.New("pigeon: no routes configured"))
	}
	if cfg.queueSize < len(cfg.channels) {
		errs = append(errs, fmt.Errorf("pigeon: queue size %d is smaller than the %d channels an event may fan out to", cfg.queueSize, len(cfg.channels)))
	}

	g := &Gateway{
		dedupe:         dedupe.New(cfg.dedupeWindow, maxDedupeKeys),
		hooks:          cfg.hooks,
		logger:         cfg.logger,
		attemptTimeout: cfg.attemptTimeout,
	}

	byName := make(map[string]*channelState, len(cfg.channels))
	broken := make(map[string]bool) // already reported; don't also report every route that names them
	for _, spec := range cfg.channels {
		cs, err := buildChannel(spec, cfg.retry)
		if err != nil {
			errs = append(errs, err)
			broken[spec.name] = true
			continue
		}
		if _, dup := byName[spec.name]; dup {
			errs = append(errs, fmt.Errorf("pigeon: channel %q registered twice", spec.name))
			continue
		}
		byName[spec.name] = cs
		g.channels = append(g.channels, cs)
	}

	for _, spec := range cfg.routes {
		if spec.pattern == "" {
			errs = append(errs, errors.New("pigeon: route with empty pattern"))
			continue
		}
		if len(spec.channels) == 0 {
			errs = append(errs, fmt.Errorf("pigeon: route %q has no channels", spec.pattern))
			continue
		}
		targets := make([]*channelState, 0, len(spec.channels))
		for _, name := range spec.channels {
			cs, ok := byName[name]
			if !ok {
				if !broken[name] {
					errs = append(errs, fmt.Errorf("pigeon: route %q: unknown channel %q", spec.pattern, name))
				}
				continue
			}
			targets = append(targets, cs)
		}
		g.routes.Add(spec.pattern, targets...)
	}

	for _, spec := range cfg.templates {
		if spec.Pattern == "" {
			errs = append(errs, errors.New("pigeon: template with empty pattern"))
			continue
		}
		for _, name := range spec.Channels {
			if _, ok := byName[name]; !ok && !broken[name] {
				errs = append(errs, fmt.Errorf("pigeon: template %q: unknown channel %q", spec.Pattern, name))
			}
		}
		if err := g.templates.Add(spec, cfg.funcs); err != nil {
			errs = append(errs, fmt.Errorf("pigeon: template %q: %w", spec.Pattern, err))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	g.queue = memqueue.New[*job](cfg.queueSize)
	g.runCtx, g.stop = context.WithCancel(context.Background())
	for range cfg.workers {
		g.workers.Go(g.work)
	}
	return g, nil
}

func buildChannel(spec channelSpec, defaultRetry RetryPolicy) (*channelState, error) {
	wrap := func(err error) error { return fmt.Errorf("pigeon: channel %q: %w", spec.name, err) }

	if !validName(spec.name) {
		return nil, wrap(errors.New("name must be non-empty and contain only letters, digits, '-' and '_'"))
	}
	if spec.ch == nil {
		return nil, wrap(errors.New("nil Channel"))
	}
	if err := errors.Join(spec.errs...); err != nil {
		return nil, wrap(err)
	}
	if v, ok := spec.ch.(validator); ok {
		if err := v.Validate(); err != nil {
			return nil, wrap(err)
		}
	}

	cs := &channelState{name: spec.name, ch: spec.ch, retry: defaultRetry}
	if spec.retry != nil {
		cs.retry = *spec.retry
	}
	switch d, hasDefault := spec.ch.(rateDefaulter); {
	case spec.rate != nil:
		cs.limiter = ratelimit.New(spec.rate.N, spec.rate.Per)
	case hasDefault && !spec.noRate:
		r := d.DefaultRate()
		cs.limiter = ratelimit.New(r.N, r.Per)
	default:
		cs.limiter = ratelimit.New(0, 0)
	}
	return cs, nil
}

func validName(s string) bool {
	if s == "" {
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

// Send renders e for every channel its type routes to and queues the
// deliveries. It returns the event ID once queued; delivery happens in the
// background and is reported through Hooks.
//
// Send returns ErrInvalidEvent if e has no Type, ErrNoRoute if no route
// matches, a template error if rendering fails, ErrDuplicate together with
// the original event ID if e.IdempotencyKey was already used, and ErrClosed
// after Close. When the queue is full, Send blocks until there is room or
// ctx is done.
func (g *Gateway) Send(ctx context.Context, e Event) (string, error) {
	if g.closed.Load() {
		return "", ErrClosed
	}
	if e.Type == "" {
		return "", fmt.Errorf("%w: type is required", ErrInvalidEvent)
	}
	if e.ID == "" {
		e.ID = id.New()
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}

	targets := g.routes.Lookup(e.Type)
	if len(targets) == 0 {
		return "", fmt.Errorf("%w %q", ErrNoRoute, e.Type)
	}

	jobs := make([]*job, 0, len(targets))
	names := make([]string, 0, len(targets))
	for _, cs := range targets {
		msg, err := g.render(e, cs.name)
		if err != nil {
			return "", err
		}
		jobs = append(jobs, &job{msg: msg, ch: cs})
		names = append(names, cs.name)
	}

	if e.IdempotencyKey != "" {
		if holder, ok := g.dedupe.Reserve(e.IdempotencyKey, e.ID, time.Now()); !ok {
			if g.hooks.OnDuplicate != nil {
				g.hooks.OnDuplicate(ctx, e, holder)
			}
			return holder, fmt.Errorf("%w %q: already used by event %s", ErrDuplicate, e.IdempotencyKey, holder)
		}
	}

	if err := g.queue.Put(ctx, jobs...); err != nil {
		if e.IdempotencyKey != "" {
			g.dedupe.Release(e.IdempotencyKey, e.ID)
		}
		if errors.Is(err, memqueue.ErrClosed) {
			return "", ErrClosed
		}
		return "", err
	}

	if g.hooks.OnEnqueue != nil {
		g.hooks.OnEnqueue(ctx, e, names)
	}
	return e.ID, nil
}

func (g *Gateway) render(e Event, channel string) (Message, error) {
	title, text, err := g.templates.Render(render.Input{ID: e.ID, Type: e.Type, Channel: channel, Time: e.Time, Data: e.Data})
	if err != nil {
		return Message{}, fmt.Errorf("pigeon: render %q for channel %q: %w", e.Type, channel, err)
	}
	return Message{
		ID:        e.ID + ":" + channel,
		EventID:   e.ID,
		EventType: e.Type,
		Channel:   channel,
		Title:     title,
		Text:      text,
		Data:      e.Data,
	}, nil
}

// Close stops accepting events and waits for queued deliveries, including
// pending retries, until they finish or ctx is done. It then stops the
// workers and closes every channel that implements io.Closer. If ctx ends
// first, the remaining deliveries are dropped and the error wraps ctx.Err().
// Calling Close again returns ErrClosed.
func (g *Gateway) Close(ctx context.Context) error {
	if !g.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}
	g.queue.Close()
	pending, waitErr := g.queue.Wait(ctx)
	g.stop()
	g.queue.Stop()
	g.workers.Wait()

	var errs []error
	if waitErr != nil {
		errs = append(errs, fmt.Errorf("pigeon: close: %d deliveries dropped: %w", pending, waitErr))
	}
	for _, cs := range g.channels {
		if c, ok := cs.ch.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, fmt.Errorf("pigeon: close channel %q: %w", cs.name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (g *Gateway) work() {
	for {
		j, err := g.queue.Get(g.runCtx)
		if err != nil {
			return
		}
		g.deliver(j)
	}
}

func (g *Gateway) deliver(j *job) {
	if wait := j.ch.limiter.Take(time.Now()); wait > 0 {
		g.queue.Later(j, wait)
		return
	}

	j.attempts++
	msg := j.msg
	msg.Attempt = j.attempts

	ctx, cancel := context.WithTimeout(g.runCtx, g.attemptTimeout)
	defer cancel()
	start := time.Now()
	err := j.ch.ch.Send(ctx, &msg)
	took := time.Since(start)

	if err != nil && g.runCtx.Err() != nil {
		return // cut short by Close; reported as dropped
	}
	if g.hooks.OnAttempt != nil {
		g.hooks.OnAttempt(ctx, &msg, err, took)
	}

	log := g.logger.With("event_id", msg.EventID, "event_type", msg.EventType, "channel", msg.Channel, "attempt", msg.Attempt)
	switch {
	case err == nil:
		log.Debug("pigeon: delivered", "took", took)
		if g.hooks.OnDelivered != nil {
			g.hooks.OnDelivered(ctx, &msg)
		}
		g.queue.Done()

	case IsPermanent(err) || j.attempts >= j.ch.retry.MaxAttempts:
		log.Warn("pigeon: dead letter", "err", err, "permanent", IsPermanent(err))
		if g.hooks.OnDeadLetter != nil {
			g.hooks.OnDeadLetter(ctx, &msg, err)
		}
		g.queue.Done()

	default:
		delay := backoff.FullJitter(j.attempts, j.ch.retry.BaseDelay, j.ch.retry.MaxDelay)
		if ra, ok := RetryAfterDelay(err); ok {
			j.ch.limiter.Pause(time.Now().Add(ra))
			delay = max(delay, ra)
		}
		log.Debug("pigeon: retrying", "err", err, "in", delay)
		g.queue.Later(j, delay)
	}
}
