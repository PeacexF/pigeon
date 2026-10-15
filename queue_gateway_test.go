package pigeon_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/PeacexF/pigeon"
	"github.com/PeacexF/pigeon/pigeontest"
)

// durableQueue looks like a custom, durable Queue to the Gateway: embedding
// only the interface hides the in-memory queue's drain, so Close doesn't wait.
type durableQueue struct {
	pigeon.Queue

	mu           sync.Mutex
	batches      int
	retries      []retry
	buried       []string
	closed       bool
	failDequeues int
}

type retry struct {
	id       string
	attempts int
	in       time.Duration
	cause    error
}

func newDurableQueue() *durableQueue { return &durableQueue{Queue: pigeon.NewMemQueue(64)} }

func (q *durableQueue) Enqueue(ctx context.Context, b pigeon.Batch) (string, error) {
	q.mu.Lock()
	q.batches++
	q.mu.Unlock()
	return q.Queue.Enqueue(ctx, b)
}

func (q *durableQueue) Dequeue(ctx context.Context) (*pigeon.Job, error) {
	q.mu.Lock()
	if q.failDequeues > 0 {
		q.failDequeues--
		q.mu.Unlock()
		return nil, errors.New("database unavailable")
	}
	q.mu.Unlock()
	return q.Queue.Dequeue(ctx)
}

func (q *durableQueue) Retry(ctx context.Context, j *pigeon.Job, at time.Time, cause error) error {
	q.mu.Lock()
	q.retries = append(q.retries, retry{j.Message.ID, j.Attempts, time.Until(at), cause})
	q.mu.Unlock()
	return q.Queue.Retry(ctx, j, at, cause)
}

func (q *durableQueue) Bury(ctx context.Context, j *pigeon.Job, cause error) error {
	q.mu.Lock()
	q.buried = append(q.buried, j.Message.ID)
	q.mu.Unlock()
	return q.Queue.Bury(ctx, j, cause)
}

func (q *durableQueue) Close(ctx context.Context) error {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	return q.Queue.Close(ctx)
}

func TestWithQueueDelivers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDurableQueue()
		rec := &pigeontest.Recorder{}
		g := newGateway(t, pigeon.WithQueue(q), pigeon.WithChannel("ops", rec), pigeon.WithRoute("*", "ops"))

		id := mustSend(t, g, pigeon.Event{Type: "a", IdempotencyKey: "k"})
		holder, err := g.Send(t.Context(), pigeon.Event{Type: "a", IdempotencyKey: "k"})
		if !errors.Is(err, pigeon.ErrDuplicate) || holder != id {
			t.Errorf("duplicate Send = %q, %v", holder, err)
		}
		synctest.Wait()
		mustClose(t, g)

		if len(rec.Messages()) != 1 || q.batches != 2 || !q.closed {
			t.Errorf("messages %d, batches %d, queue closed %v", len(rec.Messages()), q.batches, q.closed)
		}
	})
}

func TestCloseLeavesDurableQueueAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDurableQueue()
		rec := &pigeontest.Recorder{}
		rec.FailNext(1, pigeon.RetryAfter(errors.New("slow down"), time.Hour))
		g := newGateway(t, pigeon.WithQueue(q), pigeon.WithChannel("ops", rec), pigeon.WithRoute("*", "ops"))

		mustSend(t, g, pigeon.Event{Type: "a"})
		synctest.Wait()
		start := time.Now()
		mustClose(t, g)
		if waited := time.Since(start); waited > 0 {
			t.Errorf("Close waited %v for a job that stays in the durable queue", waited)
		}
		if len(q.retries) != 1 || q.retries[0].in < time.Hour || q.retries[0].attempts != 1 || q.retries[0].cause == nil {
			t.Errorf("retries = %+v, want one in an hour after attempt 1", q.retries)
		}
	})
}

func TestCloseLetsInFlightAttemptFinish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDurableQueue()
		var delivered bool
		slow := pigeon.ChannelFunc(func(ctx context.Context, _ *pigeon.Message) error {
			select {
			case <-time.After(2 * time.Second):
				delivered = true
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		g := newGateway(t, pigeon.WithQueue(q), pigeon.WithChannel("ops", slow), pigeon.WithRoute("*", "ops"))
		mustSend(t, g, pigeon.Event{Type: "a"})
		synctest.Wait()
		mustClose(t, g)
		if !delivered {
			t.Error("Close cut the in-flight attempt short")
		}
	})
}

func TestClosePutsBackCutAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDurableQueue()
		stuck := pigeon.ChannelFunc(func(ctx context.Context, _ *pigeon.Message) error {
			<-ctx.Done()
			return ctx.Err()
		})
		var attempts int
		g := newGateway(t, pigeon.WithQueue(q), pigeon.WithChannel("ops", stuck), pigeon.WithRoute("*", "ops"),
			pigeon.WithAttemptTimeout(time.Hour),
			pigeon.WithHooks(pigeon.Hooks{OnAttempt: func(context.Context, *pigeon.Message, error, time.Duration) { attempts++ }}))
		mustSend(t, g, pigeon.Event{Type: "a"})
		synctest.Wait()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := g.Close(ctx); err != nil {
			t.Errorf("Close = %v; a durable queue keeps the job, so nothing is dropped", err)
		}
		if attempts != 0 {
			t.Errorf("%d attempts reported for one cut short by Close", attempts)
		}
		if len(q.retries) != 1 || q.retries[0].attempts != 0 || q.retries[0].cause != nil || q.retries[0].in > 0 {
			t.Errorf("retries = %+v, want the job put back now without an attempt", q.retries)
		}
	})
}

func TestUnknownChannelInQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDurableQueue()
		// A job left by an earlier configuration.
		_, _ = q.Queue.Enqueue(t.Context(), pigeon.Batch{EventID: "old", Jobs: []*pigeon.Job{{Message: pigeon.Message{ID: "old:gone", Channel: "gone"}}}})

		var dead error
		g := newGateway(t, pigeon.WithQueue(q), pigeon.WithChannel("ops", &pigeontest.Recorder{}), pigeon.WithRoute("*", "ops"),
			pigeon.WithHooks(pigeon.Hooks{OnDeadLetter: func(_ context.Context, _ *pigeon.Message, err error) { dead = err }}))
		synctest.Wait()
		mustClose(t, g)

		if !pigeon.IsPermanent(dead) || !strings.Contains(dead.Error(), `channel "gone" is not configured`) {
			t.Errorf("dead letter = %v", dead)
		}
		if len(q.buried) != 1 || q.buried[0] != "old:gone" {
			t.Errorf("buried = %v", q.buried)
		}
	})
}

func TestDequeueErrorsAreRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := newDurableQueue()
		q.failDequeues = 3
		rec := &pigeontest.Recorder{}
		g := newGateway(t, pigeon.WithQueue(q), pigeon.WithWorkers(1), pigeon.WithChannel("ops", rec), pigeon.WithRoute("*", "ops"))
		mustSend(t, g, pigeon.Event{Type: "a"})
		time.Sleep(time.Minute)
		mustClose(t, g)
		if len(rec.Messages()) != 1 {
			t.Errorf("delivered %d messages after dequeue errors", len(rec.Messages()))
		}
	})
}

func TestQueueOptions(t *testing.T) {
	ch := pigeon.WithChannel("ops", &pigeontest.Recorder{})
	route := pigeon.WithRoute("*", "ops")
	if _, err := pigeon.New(ch, route, pigeon.WithQueue(nil)); err == nil || !strings.Contains(err.Error(), "nil Queue") {
		t.Errorf("WithQueue(nil): %v", err)
	}
	_, err := pigeon.New(ch, route, pigeon.WithQueue(newDurableQueue()), pigeon.WithQueueSize(5))
	if err == nil || !strings.Contains(err.Error(), "only to the in-memory queue") {
		t.Errorf("WithQueue with WithQueueSize: %v", err)
	}
}

func TestEnqueueErrorIsReturned(t *testing.T) {
	q := &failingQueue{Queue: pigeon.NewMemQueue(8)}
	g := newGateway(t, pigeon.WithQueue(q), pigeon.WithChannel("ops", &pigeontest.Recorder{}), pigeon.WithRoute("*", "ops"))
	defer mustClose(t, g)
	if _, err := g.Send(t.Context(), pigeon.Event{Type: "a"}); err == nil || !strings.Contains(err.Error(), "pigeon: enqueue: disk full") {
		t.Errorf("Send = %v", err)
	}
}

type failingQueue struct{ pigeon.Queue }

func (failingQueue) Enqueue(context.Context, pigeon.Batch) (string, error) {
	return "", errors.New("disk full")
}
