package pigeon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Channel delivers messages to one destination.
//
// Send must honor ctx and classify failures with Permanent or RetryAfter;
// any other error is retried with backoff. Send may be called concurrently.
//
// A Channel may also implement:
//   - Validate() error, checked by New so misconfiguration fails at startup
//   - DefaultRate() Rate, the provider's limit, used unless overridden with RateLimit or NoRateLimit
//   - io.Closer, called by Gateway.Close
type Channel interface {
	Send(ctx context.Context, msg *Message) error
}

// Rate is N deliveries per Per.
type Rate struct {
	N   int
	Per time.Duration
}

type validator interface{ Validate() error }

type rateDefaulter interface{ DefaultRate() Rate }

// ChannelFunc adapts a function to the Channel interface.
type ChannelFunc func(ctx context.Context, msg *Message) error

// Send calls f(ctx, msg).
func (f ChannelFunc) Send(ctx context.Context, msg *Message) error { return f(ctx, msg) }

// Validate rejects a nil function.
func (f ChannelFunc) Validate() error {
	if f == nil {
		return errors.New("nil ChannelFunc")
	}
	return nil
}

// Writer returns a Channel that writes each message to w as
// "[event.type] title\ntext" (or "[event.type] text" without a title).
// Writes are serialized.
func Writer(w io.Writer) Channel {
	return &writerChannel{w: w}
}

type writerChannel struct {
	mu sync.Mutex
	w  io.Writer
}

func (c *writerChannel) Send(_ context.Context, m *Message) error {
	body := m.Text
	if m.Title != "" {
		body = m.Title + "\n" + m.Text
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := fmt.Fprintf(c.w, "[%s] %s\n", m.EventType, body)
	return err
}

func (c *writerChannel) Validate() error {
	if c.w == nil {
		return errors.New("nil io.Writer")
	}
	return nil
}
