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
	"github.com/PeacexF/pigeon/internal/id"
	"github.com/PeacexF/pigeon/internal/ratelimit"
	"github.com/PeacexF/pigeon/internal/render"
	"github.com/PeacexF/pigeon/internal/route"
)

// Gateway routes events to channels and delivers them in the background,
// with retries, rate limiting and deduplication. It is safe for concurrent use.
//
// With the default in-memory queue, delivery is at-least-once while the
// process runs, and deliveries still pending when the process exits without
// Close are lost. A durable Queue (see WithQueue) keeps them across restarts.
type Gateway struct {
	channels       []*channelState
	byName         map[string]*channelState
	routes         route.Table[*channelState]
	templates      render.Registry
	dedupeWindow   time.Duration
	hooks          Hooks
	logger         *slog.Logger
	attemptTimeout time.Duration

	queue       Queue
	dequeueCtx  context.Context // ends when Close stops taking new jobs
	stopDequeue context.CancelFunc
	runCtx      context.Context // ends when Close gives up on in-flight attempts
	stop        context.CancelFunc
	workers     sync.WaitGroup
	closed      atomic.Bool
}

// drainer is implemented by queues that lose jobs on exit, so Close waits for them.
type drainer interface {
	drain(ctx context.Context) (pending int, err error)
}

type channelState struct {
	name    string
	ch      Channel
	limiter *ratelimit.Limiter
	retry   RetryPolicy
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
	switch {
	case cfg.queue != nil && cfg.queueSizeSet:
		errs = append(errs, errors.New("pigeon: WithQueueSize applies only to the in-memory queue, not with WithQueue"))
	case cfg.queue == nil && cfg.queueSize < len(cfg.channels):
		errs = append(errs, fmt.Errorf("pigeon: queue size %d is smaller than the %d channels an event may fan out to", cfg.queueSize, len(cfg.channels)))
	}

	g := &Gateway{
		dedupeWindow:   cfg.dedupeWindow,
		hooks:          cfg.hooks,
		logger:         cfg.logger,
		attemptTimeout: cfg.attemptTimeout,
	}

	byName := make(map[string]*channelState, len(cfg.channels))
	g.byName = byName
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

	g.queue = cfg.queue
	if g.queue == nil {
		g.queue = newMemQueue(cfg.queueSize)
	}
	g.runCtx, g.stop = context.WithCancel(context.Background())
	g.dequeueCtx, g.stopDequeue = context.WithCancel(g.runCtx)
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

	jobs := make([]*Job, 0, len(targets))
	names := make([]string, 0, len(targets))
	for _, cs := range targets {
		msg, err := g.render(e, cs.name)
		if err != nil {
			return "", err
		}
		jobs = append(jobs, &Job{Message: msg})
		names = append(names, cs.name)
	}

	holder, err := g.queue.Enqueue(ctx, Batch{
		EventID:        e.ID,
		IdempotencyKey: e.IdempotencyKey,
		IdempotencyTTL: g.dedupeWindow,
		Jobs:           jobs,
	})
	switch {
	case errors.Is(err, ErrDuplicate):
		if g.hooks.OnDuplicate != nil {
			g.hooks.OnDuplicate(ctx, e, holder)
		}
		return holder, fmt.Errorf("%w %q: already used by event %s", ErrDuplicate, e.IdempotencyKey, holder)
	case errors.Is(err, ErrClosed):
		return "", ErrClosed
	case err != nil:
		return "", fmt.Errorf("pigeon: enqueue: %w", err)
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

// Close stops accepting events and shuts the Gateway down. With the
// default in-memory queue, it first waits for queued deliveries, including
// pending retries, until they finish or ctx is done; with a durable queue
// they stay queued for the next run. Close then lets in-flight attempts
// finish until ctx is done, stops the workers, closes the queue, and closes
// every channel that implements io.Closer. If ctx ends first, the remaining
// in-memory deliveries are dropped and the error wraps ctx.Err().
// Calling Close again returns ErrClosed.
func (g *Gateway) Close(ctx context.Context) error {
	if !g.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}
	var errs []error
	if d, ok := g.queue.(drainer); ok {
		if pending, err := d.drain(ctx); err != nil {
			errs = append(errs, fmt.Errorf("pigeon: close: %d deliveries dropped: %w", pending, err))
		}
	}

	g.stopDequeue()
	stopped := make(chan struct{})
	go func() {
		g.workers.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		g.stop()
		<-stopped
	}
	g.stop()

	if err := g.queue.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("pigeon: close queue: %w", err))
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
	failures := 0
	for g.dequeueCtx.Err() == nil {
		j, err := g.queue.Dequeue(g.dequeueCtx)
		if g.dequeueCtx.Err() != nil {
			if j != nil {
				g.putBack(j)
			}
			return
		}
		if err != nil {
			// A durable queue can fail, e.g. while its database restarts.
			failures++
			g.logger.Error("pigeon: dequeue", "err", err)
			t := time.NewTimer(backoff.FullJitter(failures, 100*time.Millisecond, 10*time.Second))
			select {
			case <-t.C:
			case <-g.dequeueCtx.Done():
				t.Stop()
				return
			}
			continue
		}
		failures = 0
		g.deliver(j)
	}
}

func (g *Gateway) deliver(j *Job) {
	cs := g.byName[j.Message.Channel]
	if cs == nil {
		// Only a durable queue can hold jobs for a channel that is no longer configured.
		err := Permanent(fmt.Errorf("pigeon: channel %q is not configured", j.Message.Channel))
		g.deadLetter(g.runCtx, j, &j.Message, err)
		return
	}
	if wait := cs.limiter.Take(time.Now()); wait > 0 {
		g.queueOp("retry", g.queue.Retry(g.runCtx, j, time.Now().Add(wait), nil))
		return
	}

	j.Attempts++
	msg := j.Message
	msg.Attempt = j.Attempts

	ctx, cancel := context.WithTimeout(g.runCtx, g.attemptTimeout)
	defer cancel()
	start := time.Now()
	err := cs.ch.Send(ctx, &msg)
	took := time.Since(start)

	if err != nil && g.runCtx.Err() != nil {
		j.Attempts-- // cut short by Close, so it doesn't count
		g.putBack(j)
		return
	}
	if g.hooks.OnAttempt != nil {
		g.hooks.OnAttempt(ctx, &msg, err, took)
	}

	log := g.logger.With("event_id", msg.EventID, "event_type", msg.EventType, "channel", msg.Channel, "attempt", msg.Attempt)
	switch {
	case err == nil:
		log.Debug("pigeon: delivered", "took", took)
		g.queueOp("complete", g.queue.Complete(g.runCtx, j))
		if g.hooks.OnDelivered != nil {
			g.hooks.OnDelivered(ctx, &msg)
		}

	case IsPermanent(err) || j.Attempts >= cs.retry.MaxAttempts:
		g.deadLetter(ctx, j, &msg, err)

	default:
		delay := backoff.FullJitter(j.Attempts, cs.retry.BaseDelay, cs.retry.MaxDelay)
		if ra, ok := RetryAfterDelay(err); ok {
			cs.limiter.Pause(time.Now().Add(ra))
			delay = max(delay, ra)
		}
		log.Debug("pigeon: retrying", "err", err, "in", delay)
		g.queueOp("retry", g.queue.Retry(g.runCtx, j, time.Now().Add(delay), err))
	}
}

func (g *Gateway) deadLetter(ctx context.Context, j *Job, msg *Message, err error) {
	g.logger.Warn("pigeon: dead letter", "event_id", msg.EventID, "event_type", msg.EventType,
		"channel", msg.Channel, "attempt", msg.Attempt, "err", err, "permanent", IsPermanent(err))
	g.queueOp("bury", g.queue.Bury(g.runCtx, j, err))
	if g.hooks.OnDeadLetter != nil {
		g.hooks.OnDeadLetter(ctx, msg, err)
	}
}

// putBack returns a job taken during shutdown to the queue, so a durable
// queue hands it out again without waiting for its lease to expire.
func (g *Gateway) putBack(j *Job) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(g.runCtx), 5*time.Second)
	defer cancel()
	g.queueOp("retry", g.queue.Retry(ctx, j, time.Now(), nil))
}

// queueOp logs a failed queue operation. A durable queue hands the job out
// again once its lease expires, so the delivery isn't lost.
func (g *Gateway) queueOp(op string, err error) {
	if err != nil {
		g.logger.Error("pigeon: queue "+op, "err", err)
	}
}
