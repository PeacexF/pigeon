package pgqueue

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrateLock is an arbitrary pg_advisory_xact_lock key, so concurrent
// Migrate calls from several instances apply each migration once.
const migrateLock = 0x70696765 // "pige"

// Migrate creates or upgrades pigeon's tables. It is safe to call on every
// start and from several processes at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS pigeon_schema_migrations (
			version    integer PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			return err
		}
		var current int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM pigeon_schema_migrations`).Scan(&current); err != nil {
			return err
		}
		for _, f := range files {
			name := strings.TrimPrefix(f, "migrations/")
			version, err := strconv.Atoi(name[:strings.IndexByte(name, '_')])
			if err != nil {
				return fmt.Errorf("pgqueue: migration %s: bad version", name)
			}
			if version <= current {
				continue
			}
			sql, err := migrations.ReadFile(f)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("pgqueue: migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO pigeon_schema_migrations (version) VALUES ($1)`, version); err != nil {
				return err
			}
		}
		return nil
	})
}
