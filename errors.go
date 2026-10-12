package pigeon

import (
	"errors"
	"time"
)

var (
	// ErrNoRoute is returned by Send when no route matches the event type.
	ErrNoRoute = errors.New("pigeon: no route for event type")

	// ErrDuplicate is returned by Send, together with the original event ID,
	// when the idempotency key was already used within the dedupe window.
	ErrDuplicate = errors.New("pigeon: duplicate idempotency key")

	// ErrInvalidEvent is returned by Send for events that can never be delivered.
	ErrInvalidEvent = errors.New("pigeon: invalid event")

	// ErrClosed is returned by Send and Close once the Gateway is closed.
	ErrClosed = errors.New("pigeon: gateway closed")
)

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying, such as an unknown recipient or
// revoked credentials. The delivery goes straight to the dead letter hook.
// Permanent(nil) returns nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

// IsPermanent reports whether err, or any error it wraps, was marked with Permanent.
func IsPermanent(err error) bool {
	_, ok := errors.AsType[*permanentError](err)
	return ok
}

type retryAfterError struct {
	err   error
	delay time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

// RetryAfter marks err as retryable no sooner than d, typically from a 429
// response. It also pauses every delivery to the same channel for d.
// RetryAfter(nil, d) returns nil.
func RetryAfter(err error, d time.Duration) error {
	if err == nil {
		return nil
	}
	return &retryAfterError{err: err, delay: d}
}

// RetryAfterDelay returns the delay attached by RetryAfter, if any.
func RetryAfterDelay(err error) (time.Duration, bool) {
	if r, ok := errors.AsType[*retryAfterError](err); ok {
		return r.delay, true
	}
	return 0, false
}
