// Package pgqueue is a durable pigeon.Queue backed by PostgreSQL, so
// deliveries survive restarts and crashes, and failed ones can be inspected
// and replayed.
//
//	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
//	...
//	if err := pgqueue.Migrate(ctx, pool); err != nil { ... }
//	q := pgqueue.New(pool)
//	p, err := pigeon.New(pigeon.WithQueue(q), ...)
//
// Several processes can share one database: each job is leased to one
// worker at a time with SELECT ... FOR UPDATE SKIP LOCKED. A lease that is
// never finished, because the process crashed, expires after the lease
// timeout and the job is delivered again, so delivery is at-least-once
// across restarts. Keep the lease timeout above pigeon's attempt timeout.
//
// Workers wait for new jobs with LISTEN/NOTIFY and poll as a fallback,
// which also picks up scheduled retries.
//
// Idempotency keys are stored in the same transaction as the jobs. Failed
// jobs move to a dead-letter table (see DeadLetters, Replay), and every
// attempt is logged. Cleanup removes old log rows, old dead letters and
// expired keys; run it periodically.
//
// Message.Data is stored as JSON, so after a round trip numbers are float64
// and other types become their JSON equivalents.
package pgqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PeacexF/pigeon"
)

const notifyChannel = "pigeon_jobs"

// maxErrorLen bounds stored error messages.
const maxErrorLen = 2000

// ErrLeaseLost is returned when finishing a job whose lease expired and
// passed to another worker. The other worker now owns the delivery.
var ErrLeaseLost = errors.New("pgqueue: lease lost")

// ErrNotFound is returned by Replay for an unknown dead letter.
var ErrNotFound = errors.New("pgqueue: not found")

// Queue is a pigeon.Queue stored in PostgreSQL. Create it with New after
// running Migrate.
type Queue struct {
	pool         *pgxpool.Pool
	leaseTimeout time.Duration
	pollInterval time.Duration

	ctx    context.Context // ends on Close
	cancel context.CancelFunc

	listenOnce sync.Once
	listening  sync.WaitGroup

	mu   sync.Mutex
	wake chan struct{} // closed and replaced on every notification
}

// Option configures a Queue.
type Option func(*Queue)

// WithLeaseTimeout sets how long a worker owns a job before it is handed
// out again. It must exceed pigeon's attempt timeout. The default is 5m.
func WithLeaseTimeout(d time.Duration) Option { return func(q *Queue) { q.leaseTimeout = d } }

// WithPollInterval sets how often idle workers look for due jobs without a
// notification, which bounds how late a scheduled retry can start. The
// default is 1s.
func WithPollInterval(d time.Duration) Option { return func(q *Queue) { q.pollInterval = d } }

// New returns a Queue using pool. The pool stays owned by the caller.
func New(pool *pgxpool.Pool, opts ...Option) *Queue {
	q := &Queue{pool: pool, leaseTimeout: 5 * time.Minute, pollInterval: time.Second, wake: make(chan struct{})}
	for _, opt := range opts {
		opt(q)
	}
	q.leaseTimeout = max(q.leaseTimeout, time.Second)
	q.pollInterval = max(q.pollInterval, 10*time.Millisecond)
	q.ctx, q.cancel = context.WithCancel(context.Background())
	return q
}

// storedMessage pins the JSON layout of pigeon.Message in the database.
type storedMessage struct {
	ID        string         `json:"id"`
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	Channel   string         `json:"channel"`
	Title     string         `json:"title,omitempty"`
	Text      string         `json:"text"`
	Data      map[string]any `json:"data,omitempty"`
}

func encode(m pigeon.Message) ([]byte, error) {
	return json.Marshal(storedMessage{m.ID, m.EventID, m.EventType, m.Channel, m.Title, m.Text, m.Data})
}

func decode(raw []byte) (pigeon.Message, error) {
	var s storedMessage
	if err := json.Unmarshal(raw, &s); err != nil {
		return pigeon.Message{}, err
	}
	return pigeon.Message{ID: s.ID, EventID: s.EventID, EventType: s.EventType, Channel: s.Channel, Title: s.Title, Text: s.Text, Data: s.Data}, nil
}

func seconds(d time.Duration) float64 { return max(d, 0).Seconds() }

func errText(err error) *string {
	if err == nil {
		return nil
	}
	s := err.Error()
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen]
	}
	return &s
}

// Enqueue implements pigeon.Queue.
func (q *Queue) Enqueue(ctx context.Context, b pigeon.Batch) (string, error) {
	if q.ctx.Err() != nil {
		return "", pigeon.ErrClosed
	}
	payloads := make([][]byte, len(b.Jobs))
	for i, j := range b.Jobs {
		raw, err := encode(j.Message)
		if err != nil {
			return "", fmt.Errorf("pgqueue: encode message: %w", err)
		}
		payloads[i] = raw
	}

	var holder string
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		if b.IdempotencyKey != "" {
			err := tx.QueryRow(ctx, `
				INSERT INTO pigeon_idempotency_keys (key, event_id, expires_at)
				VALUES ($1, $2, now() + make_interval(secs => $3))
				ON CONFLICT (key) DO UPDATE SET event_id = excluded.event_id, expires_at = excluded.expires_at
					WHERE pigeon_idempotency_keys.expires_at <= now()
				RETURNING event_id`,
				b.IdempotencyKey, b.EventID, seconds(b.IdempotencyTTL)).Scan(new(string))
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.QueryRow(ctx, `SELECT event_id FROM pigeon_idempotency_keys WHERE key = $1`, b.IdempotencyKey).Scan(&holder); err != nil {
					return err
				}
				return pigeon.ErrDuplicate
			}
			if err != nil {
				return err
			}
		}
		for i, j := range b.Jobs {
			m := j.Message
			if _, err := tx.Exec(ctx, `
				INSERT INTO pigeon_jobs (id, event_id, event_type, channel, message, attempts)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				m.ID, m.EventID, m.EventType, m.Channel, payloads[i], j.Attempts); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `SELECT pg_notify($1, '')`, notifyChannel)
		return err
	})
	if errors.Is(err, pigeon.ErrDuplicate) {
		return holder, pigeon.ErrDuplicate
	}
	if err != nil {
		return "", fmt.Errorf("pgqueue: enqueue: %w", err)
	}
	return "", nil
}

// Dequeue implements pigeon.Queue.
func (q *Queue) Dequeue(ctx context.Context) (*pigeon.Job, error) {
	q.listenOnce.Do(func() { q.listening.Go(q.listen) })
	for {
		if q.ctx.Err() != nil {
			return nil, pigeon.ErrClosed
		}
		q.mu.Lock()
		wake := q.wake // before claiming, so a notification during the claim isn't missed
		q.mu.Unlock()

		j, err := q.claim(ctx)
		if err != nil || j != nil {
			return j, err
		}
		t := time.NewTimer(q.pollInterval)
		select {
		case <-wake:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-q.ctx.Done():
			t.Stop()
			return nil, pigeon.ErrClosed
		}
		t.Stop()
	}
}

func (q *Queue) claim(ctx context.Context) (*pigeon.Job, error) {
	lease := newLease()
	var raw []byte
	var attempts int
	err := q.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM pigeon_jobs
			WHERE run_at <= now()
			ORDER BY run_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE pigeon_jobs j SET lease = $1, run_at = now() + make_interval(secs => $2)
		FROM next WHERE j.id = next.id
		RETURNING j.message, j.attempts`,
		lease, seconds(q.leaseTimeout)).Scan(&raw, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("pgqueue: dequeue: %w", err)
	}
	msg, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("pgqueue: decode message: %w", err)
	}
	return &pigeon.Job{Message: msg, Attempts: attempts, Lease: lease}, nil
}

func newLease() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// finish runs a statement that returns how many leased rows it changed.
func (q *Queue) finish(ctx context.Context, op, sql string, args ...any) error {
	var n int
	if err := q.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return fmt.Errorf("pgqueue: %s: %w", op, err)
	}
	if n == 0 {
		return fmt.Errorf("pgqueue: %s: %w", op, ErrLeaseLost)
	}
	return nil
}

// Complete implements pigeon.Queue.
func (q *Queue) Complete(ctx context.Context, j *pigeon.Job) error {
	return q.finish(ctx, "complete", `
		WITH done AS (
			DELETE FROM pigeon_jobs WHERE id = $1 AND lease = $2
			RETURNING id, event_id, channel
		), log AS (
			INSERT INTO pigeon_attempts (job_id, event_id, channel, attempt, outcome)
			SELECT id, event_id, channel, $3, 'delivered' FROM done
		)
		SELECT count(*) FROM done`,
		j.Message.ID, j.Lease, j.Attempts)
}

// Retry implements pigeon.Queue.
func (q *Queue) Retry(ctx context.Context, j *pigeon.Job, at time.Time, cause error) error {
	err := q.finish(ctx, "retry", `
		WITH r AS (
			UPDATE pigeon_jobs
			SET attempts = $3, run_at = now() + make_interval(secs => $4), lease = NULL,
				last_error = coalesce($5, last_error)
			WHERE id = $1 AND lease = $2
			RETURNING id, event_id, channel
		), log AS (
			INSERT INTO pigeon_attempts (job_id, event_id, channel, attempt, outcome, error)
			SELECT id, event_id, channel, $3, 'retry', $5 FROM r WHERE $5::text IS NOT NULL
		)
		SELECT count(*) FROM r`,
		j.Message.ID, j.Lease, j.Attempts, seconds(time.Until(at)), errText(cause))
	if err == nil && !at.After(time.Now()) {
		q.notify(ctx) // due now: wake idle workers instead of waiting for the next poll
	}
	return err
}

// Bury implements pigeon.Queue: the job becomes a dead letter.
func (q *Queue) Bury(ctx context.Context, j *pigeon.Job, cause error) error {
	if cause == nil {
		cause = errors.New("buried")
	}
	return q.finish(ctx, "bury", `
		WITH b AS (
			DELETE FROM pigeon_jobs WHERE id = $1 AND lease = $2
			RETURNING id, event_id, event_type, channel, message, created_at
		), dl AS (
			INSERT INTO pigeon_dead_letters (id, event_id, event_type, channel, message, attempts, error, created_at)
			SELECT id, event_id, event_type, channel, message, $3, $4, created_at FROM b
			ON CONFLICT (id) DO UPDATE SET attempts = excluded.attempts, error = excluded.error, buried_at = now()
		), log AS (
			INSERT INTO pigeon_attempts (job_id, event_id, channel, attempt, outcome, error)
			SELECT id, event_id, channel, $3, 'dead', $4 FROM b
		)
		SELECT count(*) FROM b`,
		j.Message.ID, j.Lease, j.Attempts, *errText(cause))
}

func (q *Queue) notify(ctx context.Context) {
	_, _ = q.pool.Exec(ctx, `SELECT pg_notify($1, '')`, notifyChannel)
}

// Close stops the queue's background listener. It doesn't close the pool.
func (q *Queue) Close(context.Context) error {
	q.cancel()
	q.listening.Wait()
	return nil
}

// Depth returns how many jobs are waiting or in flight.
func (q *Queue) Depth(ctx context.Context) (int, error) {
	var n int
	err := q.pool.QueryRow(ctx, `SELECT count(*) FROM pigeon_jobs`).Scan(&n)
	return n, err
}

func (q *Queue) listen() {
	failures := 0
	for q.ctx.Err() == nil {
		if err := q.listenConn(); err == nil || q.ctx.Err() != nil {
			return
		}
		// Polling keeps the queue working until the listener reconnects.
		failures++
		t := time.NewTimer(min(time.Duration(failures)*time.Second, 30*time.Second))
		select {
		case <-t.C:
		case <-q.ctx.Done():
			t.Stop()
		}
	}
}

func (q *Queue) listenConn() error {
	conn, err := q.pool.Acquire(q.ctx)
	if err != nil {
		return err
	}
	// A connection that has LISTENed must not go back to the pool.
	defer func() {
		_ = conn.Conn().Close(context.Background())
		conn.Release()
	}()
	if _, err := conn.Exec(q.ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	for {
		if _, err := conn.Conn().WaitForNotification(q.ctx); err != nil {
			return err
		}
		q.mu.Lock()
		close(q.wake)
		q.wake = make(chan struct{})
		q.mu.Unlock()
	}
}
