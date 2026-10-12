// Package memqueue is the default in-memory job queue.
//
// Capacity counts every unfinished item: queued, waiting to retry, or in
// flight. Retries therefore never need new space, and workers never block
// when putting an item back.
package memqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrClosed is returned by Put after Close.
var ErrClosed = errors.New("memqueue: closed")

// Queue is a bounded queue of items of type T. Use New to create one.
type Queue[T any] struct {
	slots   chan struct{}
	ready   chan T
	putLock chan struct{} // serializes Put so concurrent batches can't each hold part of the capacity and deadlock
	closing chan struct{}

	mu          sync.Mutex
	closed      bool
	stopped     bool
	outstanding int
	idle        chan struct{}
	timers      map[*time.Timer]struct{}
}

// New returns a queue holding at most size unfinished items.
func New[T any](size int) *Queue[T] {
	return &Queue[T]{
		slots:   make(chan struct{}, size),
		ready:   make(chan T, size),
		putLock: make(chan struct{}, 1),
		closing: make(chan struct{}),
		timers:  make(map[*time.Timer]struct{}),
	}
}

// Put adds all items or none. It blocks until there is room, ctx is done, or the queue is closed.
func (q *Queue[T]) Put(ctx context.Context, items ...T) error {
	if len(items) > cap(q.slots) {
		return fmt.Errorf("memqueue: %d items exceed capacity %d", len(items), cap(q.slots))
	}

	select {
	case q.putLock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-q.closing:
		return ErrClosed
	}
	defer func() { <-q.putLock }()

	acquired := 0
	release := func() {
		for range acquired {
			<-q.slots
		}
	}
	for range items {
		select {
		case q.slots <- struct{}{}:
			acquired++
		case <-ctx.Done():
			release()
			return ctx.Err()
		case <-q.closing:
			release()
			return ErrClosed
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		release()
		return ErrClosed
	}
	q.outstanding += len(items)
	for _, it := range items {
		q.ready <- it
	}
	return nil
}

// Get blocks until an item is ready or ctx is done.
func (q *Queue[T]) Get(ctx context.Context) (T, error) {
	select {
	case it := <-q.ready:
		return it, nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// Later makes an item obtained from Get ready again after d.
func (q *Queue[T]) Later(item T, d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		q.mu.Lock()
		_, ok := q.timers[t]
		delete(q.timers, t)
		q.mu.Unlock()
		if ok {
			q.ready <- item
		}
	})
	q.timers[t] = struct{}{}
}

// Done marks an item obtained from Get as finished, freeing its capacity.
func (q *Queue[T]) Done() {
	<-q.slots
	q.mu.Lock()
	defer q.mu.Unlock()
	q.outstanding--
	if q.outstanding == 0 && q.idle != nil {
		close(q.idle)
		q.idle = nil
	}
}

// Close stops accepting new items. Items already queued remain.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.closing)
	}
}

// Wait blocks until every item is Done or ctx is done. It returns the number
// of items still unfinished and ctx's error if it gave up.
func (q *Queue[T]) Wait(ctx context.Context) (int, error) {
	q.mu.Lock()
	if q.outstanding == 0 {
		q.mu.Unlock()
		return 0, nil
	}
	if q.idle == nil {
		q.idle = make(chan struct{})
	}
	idle := q.idle
	q.mu.Unlock()

	select {
	case <-idle:
		return 0, nil
	case <-ctx.Done():
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.outstanding == 0 {
			return 0, nil
		}
		return q.outstanding, ctx.Err()
	}
}

// Stop cancels pending Later timers and ignores future Later calls.
func (q *Queue[T]) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	for t := range q.timers {
		t.Stop()
		delete(q.timers, t)
	}
}
