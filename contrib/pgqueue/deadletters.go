package pgqueue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/PeacexF/pigeon"
)

// DeadLetter is a job that failed for good.
type DeadLetter struct {
	Message   pigeon.Message
	Attempts  int
	Error     string
	CreatedAt time.Time // when the event was sent
	BuriedAt  time.Time
}

// Filter selects dead letters. Zero fields match everything.
type Filter struct {
	Channel   string
	EventType string
	Since     time.Time // buried at or after
	Limit     int       // DeadLetters only; 0 means 100
}

func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.Channel != "" {
		add("channel = $%d", f.Channel)
	}
	if f.EventType != "" {
		add("event_type = $%d", f.EventType)
	}
	if !f.Since.IsZero() {
		add("buried_at >= $%d", f.Since)
	}
	if len(conds) == 0 {
		return "TRUE", nil
	}
	return strings.Join(conds, " AND "), args
}

// DeadLetters lists dead letters matching f, most recently buried first.
func (q *Queue) DeadLetters(ctx context.Context, f Filter) ([]DeadLetter, error) {
	where, args := f.where()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := q.pool.Query(ctx, `
		SELECT message, attempts, error, created_at, buried_at FROM pigeon_dead_letters
		WHERE `+where+fmt.Sprintf(` ORDER BY buried_at DESC, id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("pgqueue: dead letters: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DeadLetter, error) {
		var d DeadLetter
		var raw []byte
		if err := row.Scan(&raw, &d.Attempts, &d.Error, &d.CreatedAt, &d.BuriedAt); err != nil {
			return d, err
		}
		msg, err := decode(raw)
		d.Message = msg
		return d, err
	})
}

const replaySQL = `
	WITH d AS (
		DELETE FROM pigeon_dead_letters WHERE %s
		RETURNING id, event_id, event_type, channel, message, created_at
	), ins AS (
		INSERT INTO pigeon_jobs (id, event_id, event_type, channel, message, created_at)
		SELECT id, event_id, event_type, channel, message, created_at FROM d
		ON CONFLICT (id) DO NOTHING
	)
	SELECT count(*) FROM d`

// Replay moves the dead letter with the given message ID back into the
// queue, with its attempts reset. It returns ErrNotFound if there is none.
func (q *Queue) Replay(ctx context.Context, messageID string) error {
	n, err := q.replay(ctx, "id = $1", messageID)
	if err == nil && n == 0 {
		return fmt.Errorf("pgqueue: replay %q: %w", messageID, ErrNotFound)
	}
	return err
}

// ReplayAll moves every dead letter matching f back into the queue and
// returns how many it moved. f.Limit is ignored.
func (q *Queue) ReplayAll(ctx context.Context, f Filter) (int, error) {
	where, args := f.where()
	return q.replay(ctx, where, args...)
}

func (q *Queue) replay(ctx context.Context, where string, args ...any) (int, error) {
	var n int
	if err := q.pool.QueryRow(ctx, fmt.Sprintf(replaySQL, where), args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("pgqueue: replay: %w", err)
	}
	if n > 0 {
		q.notify(ctx)
	}
	return n, nil
}

// Cleaned reports what Cleanup removed.
type Cleaned struct {
	Attempts        int64
	DeadLetters     int64
	IdempotencyKeys int64
}

// Cleanup deletes attempt log rows and dead letters older than before, and
// idempotency keys that have expired. Run it periodically, e.g. daily with
// before set to a retention period ago.
func (q *Queue) Cleanup(ctx context.Context, before time.Time) (Cleaned, error) {
	var c Cleaned
	err := pgx.BeginFunc(ctx, q.pool, func(tx pgx.Tx) error {
		for _, step := range []struct {
			n    *int64
			sql  string
			args []any
		}{
			{&c.Attempts, `DELETE FROM pigeon_attempts WHERE at < $1`, []any{before}},
			{&c.DeadLetters, `DELETE FROM pigeon_dead_letters WHERE buried_at < $1`, []any{before}},
			{&c.IdempotencyKeys, `DELETE FROM pigeon_idempotency_keys WHERE expires_at <= now()`, nil},
		} {
			tag, err := tx.Exec(ctx, step.sql, step.args...)
			if err != nil {
				return err
			}
			*step.n = tag.RowsAffected()
		}
		return nil
	})
	if err != nil {
		return Cleaned{}, fmt.Errorf("pgqueue: cleanup: %w", err)
	}
	return c, nil
}
