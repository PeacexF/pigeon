package pigeon

import (
	"context"
	"errors"
	"time"

	"github.com/PeacexF/pigeon/internal/dedupe"
	"github.com/PeacexF/pigeon/internal/memqueue"
)

const maxDedupeKeys = 100_000

// memQueue is the default Queue: bounded, in memory, lost on exit.
type memQueue struct {
	q      *memqueue.Queue[*Job]
	dedupe *dedupe.Window
}

func newMemQueue(size int) *memQueue {
	return &memQueue{q: memqueue.New[*Job](size), dedupe: dedupe.New(maxDedupeKeys)}
}

func (m *memQueue) Enqueue(ctx context.Context, b Batch) (string, error) {
	if b.IdempotencyKey != "" {
		if holder, ok := m.dedupe.Reserve(b.IdempotencyKey, b.EventID, time.Now(), b.IdempotencyTTL); !ok {
			return holder, ErrDuplicate
		}
	}
	if err := m.q.Put(ctx, b.Jobs...); err != nil {
		if b.IdempotencyKey != "" {
			m.dedupe.Release(b.IdempotencyKey, b.EventID)
		}
		if errors.Is(err, memqueue.ErrClosed) {
			return "", ErrClosed
		}
		return "", err
	}
	return "", nil
}

func (m *memQueue) Dequeue(ctx context.Context) (*Job, error) { return m.q.Get(ctx) }

func (m *memQueue) Complete(context.Context, *Job) error {
	m.q.Done()
	return nil
}

func (m *memQueue) Retry(_ context.Context, j *Job, at time.Time, _ error) error {
	m.q.Later(j, time.Until(at))
	return nil
}

func (m *memQueue) Bury(context.Context, *Job, error) error {
	m.q.Done()
	return nil
}

func (m *memQueue) Close(context.Context) error {
	m.q.Stop()
	return nil
}

// drain stops new jobs and waits for the queued ones to finish, since they
// are lost once the process exits. It returns how many are left if ctx ends first.
func (m *memQueue) drain(ctx context.Context) (int, error) {
	m.q.Close()
	return m.q.Wait(ctx)
}
