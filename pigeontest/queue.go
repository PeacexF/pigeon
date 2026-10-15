package pigeontest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/PeacexF/pigeon"
)

// TestQueue checks that a pigeon.Queue implementation honors the contract
// the Gateway relies on. newQueue must return an empty queue each time it is
// called; the suite closes it. The suite takes a few seconds, since it uses
// real time to test retries and expiring idempotency keys.
func TestQueue(t *testing.T, newQueue func(t *testing.T) pigeon.Queue) {
	run := func(name string, f func(t *testing.T, q pigeon.Queue)) {
		t.Run(name, func(t *testing.T) {
			q := newQueue(t)
			t.Cleanup(func() { _ = q.Close(context.Background()) })
			f(t, q)
		})
	}
	run("RoundTrip", testRoundTrip)
	run("Complete", testComplete)
	run("Bury", testBury)
	run("RetryLater", testRetryLater)
	run("RetryNow", testRetryNow)
	run("Idempotency", testIdempotency)
	run("IdempotencyExpires", testIdempotencyExpires)
	run("DequeueHonorsContext", testDequeueHonorsContext)
	run("ConcurrentDequeue", testConcurrentDequeue)
}

func batch(event string, n int) pigeon.Batch {
	b := pigeon.Batch{EventID: event}
	for i := range n {
		ch := fmt.Sprintf("ch%d", i)
		b.Jobs = append(b.Jobs, &pigeon.Job{Message: pigeon.Message{
			ID:        event + ":" + ch,
			EventID:   event,
			EventType: "test.event",
			Channel:   ch,
			Title:     "title " + ch,
			Text:      "text " + ch,
			// Only JSON-stable values, so durable queues round-trip them exactly.
			Data: map[string]any{"s": "x", "n": 1.5, "b": true, "l": []any{"a"}, "m": map[string]any{"k": "v"}},
		}})
	}
	return b
}

func enqueue(t *testing.T, q pigeon.Queue, b pigeon.Batch) {
	t.Helper()
	if _, err := q.Enqueue(t.Context(), b); err != nil {
		t.Fatalf("Enqueue(%s): %v", b.EventID, err)
	}
}

func dequeue(t *testing.T, q pigeon.Queue) *pigeon.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	j, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	return j
}

func expectEmpty(t *testing.T, q pigeon.Queue, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	defer cancel()
	if j, err := q.Dequeue(ctx); err == nil {
		t.Fatalf("Dequeue returned %s, want nothing due", j.Message.ID)
	}
}

func testRoundTrip(t *testing.T, q pigeon.Queue) {
	b := batch("ev1", 3)
	enqueue(t, q, b)
	want := map[string]pigeon.Message{}
	for _, j := range b.Jobs {
		want[j.Message.ID] = j.Message
	}
	for range 3 {
		j := dequeue(t, q)
		w, ok := want[j.Message.ID]
		if !ok {
			t.Fatalf("unexpected or repeated job %q", j.Message.ID)
		}
		delete(want, j.Message.ID)
		if !reflect.DeepEqual(j.Message, w) {
			t.Errorf("message changed in the queue:\n got %+v\nwant %+v", j.Message, w)
		}
		if j.Attempts != 0 {
			t.Errorf("new job has %d attempts", j.Attempts)
		}
	}
}

func testComplete(t *testing.T, q pigeon.Queue) {
	enqueue(t, q, batch("ev1", 2))
	for range 2 {
		if err := q.Complete(t.Context(), dequeue(t, q)); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}
	expectEmpty(t, q, 300*time.Millisecond)
}

func testBury(t *testing.T, q pigeon.Queue) {
	enqueue(t, q, batch("ev1", 1))
	j := dequeue(t, q)
	j.Attempts = 3
	if err := q.Bury(t.Context(), j, errors.New("boom")); err != nil {
		t.Fatalf("Bury: %v", err)
	}
	expectEmpty(t, q, 300*time.Millisecond)
}

func testRetryLater(t *testing.T, q pigeon.Queue) {
	enqueue(t, q, batch("ev1", 1))
	j := dequeue(t, q)
	j.Attempts = 1
	at := time.Now().Add(time.Second)
	if err := q.Retry(t.Context(), j, at, errors.New("boom")); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	expectEmpty(t, q, 500*time.Millisecond)
	j = dequeue(t, q)
	if time.Now().Before(at.Add(-50 * time.Millisecond)) {
		t.Errorf("retried job came back %v early", time.Until(at))
	}
	if j.Attempts != 1 {
		t.Errorf("Attempts = %d after a retry, want 1", j.Attempts)
	}
}

func testRetryNow(t *testing.T, q pigeon.Queue) {
	enqueue(t, q, batch("ev1", 1))
	j := dequeue(t, q)
	if err := q.Retry(t.Context(), j, time.Now(), nil); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if j = dequeue(t, q); j.Attempts != 0 {
		t.Errorf("Attempts = %d after putting back without an attempt", j.Attempts)
	}
}

func testIdempotency(t *testing.T, q pigeon.Queue) {
	first := batch("ev1", 1)
	first.IdempotencyKey, first.IdempotencyTTL = "key", time.Hour
	enqueue(t, q, first)

	dup := batch("ev2", 2)
	dup.IdempotencyKey, dup.IdempotencyTTL = "key", time.Hour
	holder, err := q.Enqueue(t.Context(), dup)
	if !errors.Is(err, pigeon.ErrDuplicate) || holder != "ev1" {
		t.Fatalf("duplicate Enqueue = %q, %v; want ev1, ErrDuplicate", holder, err)
	}

	other := batch("ev3", 1)
	other.IdempotencyKey, other.IdempotencyTTL = "other-key", time.Hour
	enqueue(t, q, other)

	got := map[string]bool{}
	for range 2 {
		got[dequeue(t, q).Message.EventID] = true
	}
	if !got["ev1"] || !got["ev3"] {
		t.Errorf("dequeued events %v, want ev1 and ev3", got)
	}
	expectEmpty(t, q, 300*time.Millisecond)
}

func testIdempotencyExpires(t *testing.T, q pigeon.Queue) {
	first := batch("ev1", 1)
	first.IdempotencyKey, first.IdempotencyTTL = "key", 300*time.Millisecond
	enqueue(t, q, first)
	time.Sleep(time.Second)

	again := batch("ev2", 1)
	again.IdempotencyKey, again.IdempotencyTTL = "key", time.Hour
	if _, err := q.Enqueue(t.Context(), again); err != nil {
		t.Fatalf("Enqueue after the key expired: %v", err)
	}
}

func testDequeueHonorsContext(t *testing.T, q pigeon.Queue) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := q.Dequeue(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Dequeue on an empty queue = %v, want context.DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Dequeue returned %v after its context ended", took)
	}
}

func testConcurrentDequeue(t *testing.T, q pigeon.Queue) {
	const events, perEvent, workers = 25, 4, 8
	for i := range events {
		enqueue(t, q, batch(fmt.Sprintf("ev%02d", i), perEvent))
	}

	var mu sync.Mutex
	seen := map[string]int{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				j, err := q.Dequeue(ctx)
				if err != nil {
					return
				}
				mu.Lock()
				seen[j.Message.ID]++
				done := len(seen) == events*perEvent
				mu.Unlock()
				if err := q.Complete(ctx, j); err != nil && ctx.Err() == nil {
					t.Errorf("Complete: %v", err)
				}
				if done {
					cancel()
				}
			}
		})
	}
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Error("timed out")
		cancel()
	}
	wg.Wait()

	if len(seen) != events*perEvent {
		t.Errorf("dequeued %d distinct jobs, want %d", len(seen), events*perEvent)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %s dequeued %d times", id, n)
		}
	}
}
