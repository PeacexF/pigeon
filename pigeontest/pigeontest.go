// Package pigeontest helps test code that uses pigeon, without network access.
package pigeontest

import (
	"context"
	"slices"
	"sync"

	"github.com/PeacexF/pigeon"
)

// Recorder is a pigeon.Channel that records delivered messages and can be told
// to fail. The zero value is ready to use. It is safe for concurrent use.
type Recorder struct {
	mu       sync.Mutex
	messages []pigeon.Message
	attempts int
	failN    int
	failErr  error
}

// Send records msg, or fails if FailNext is in effect.
func (r *Recorder) Send(_ context.Context, msg *pigeon.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.failN > 0 {
		r.failN--
		return r.failErr
	}
	r.messages = append(r.messages, *msg)
	return nil
}

// FailNext makes the next n Send calls return err. Wrap err with
// pigeon.Permanent or pigeon.RetryAfter to exercise those paths.
func (r *Recorder) FailNext(n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failN, r.failErr = n, err
}

// Messages returns the successfully delivered messages in delivery order.
func (r *Recorder) Messages() []pigeon.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.messages)
}

// Attempts returns how many times Send was called, failures included.
func (r *Recorder) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// Reset forgets all messages, attempts and pending failures.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages, r.attempts, r.failN, r.failErr = nil, 0, 0, nil
}
