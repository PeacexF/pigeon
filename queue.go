package pigeon

import (
	"context"
	"time"
)

// Queue holds deliveries between Send and the workers. The default queue is
// in memory and bounded (see WithQueueSize); WithQueue replaces it, for
// example with the Postgres queue in contrib/pgqueue, so deliveries survive
// restarts.
//
// A Job is leased to one worker from Dequeue until that worker calls
// Complete, Retry or Bury. A durable queue should make a job available again
// if its lease is never finished, for example because the process crashed;
// that is what makes delivery at-least-once across restarts.
//
// Implementations must be safe for concurrent use. pigeontest.TestQueue
// checks an implementation against this contract.
type Queue interface {
	// Enqueue stores every job in b or none of them. If b.IdempotencyKey is
	// set and an earlier batch holds it for less than b.IdempotencyTTL, it
	// stores nothing and returns the holder's event ID with ErrDuplicate.
	// It may block while the queue is full, until ctx is done.
	Enqueue(ctx context.Context, b Batch) (holder string, err error)

	// Dequeue blocks until a job is due and leases it to the caller.
	// It returns ctx.Err() once ctx is done.
	Dequeue(ctx context.Context) (*Job, error)

	// Complete removes a delivered job.
	Complete(ctx context.Context, j *Job) error

	// Retry ends the lease and makes the job due again at the given time.
	// j.Attempts already counts the attempt that failed with cause; cause is
	// nil when the job was put back without an attempt, e.g. by the rate limiter.
	Retry(ctx context.Context, j *Job, at time.Time, cause error) error

	// Bury removes a job that failed for good. Durable queues should keep it
	// as a dead letter that can be inspected and replayed.
	Bury(ctx context.Context, j *Job, cause error) error

	// Close releases the queue's resources. Gateway.Close calls it after the
	// workers have stopped.
	Close(ctx context.Context) error
}

// Batch is the set of jobs one event produces.
type Batch struct {
	EventID        string
	IdempotencyKey string
	IdempotencyTTL time.Duration
	Jobs           []*Job
}

// Job is one message waiting to be delivered to its channel.
type Job struct {
	// Message is the rendered message; Message.Channel names the channel.
	// Message.Attempt is set by the worker for each attempt.
	Message Message

	// Attempts counts the delivery attempts made so far.
	Attempts int

	// Lease identifies the current lease. The queue sets it in Dequeue;
	// the gateway leaves it alone.
	Lease string
}
