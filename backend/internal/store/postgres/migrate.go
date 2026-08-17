package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is an arbitrary but fixed key for pg_advisory_lock.
//
// Every replica runs migrations on boot, and in Kubernetes they boot together.
// Without a lock, two of them race on CREATE TABLE and one gets a duplicate
// object error — survivable, but it also means two could apply *different*
// migrations concurrently, which is not. The lock is session-scoped and
// released when the migration connection closes, including if the process dies
// mid-migration.
const migrationLockID int64 = 0x6753_0001 // "GS" 0001

// Migrate applies every unapplied migration, in filename order, in a
// transaction each.
//
// Each file runs as one statement batch over the simple protocol, because the
// schema contains a plpgsql function body with embedded semicolons that naive
// splitting would mangle.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}

	files, err := migrationNames()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	// A dedicated connection: the advisory lock must be held for the whole run
	// on one session, and simple protocol is needed for multi-statement files.
	cfg := pool.Config().ConnConfig.Copy()
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("migration connection: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer func() {
		// Best-effort: the lock also releases when the session ends.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name        TEXT        PRIMARY KEY,
			checksum    TEXT        NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied := map[string]string{}
	rows, err := conn.Query(ctx, `SELECT name, checksum FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("reading schema_migrations: %w", err)
	}
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			rows.Close()
			return err
		}
		applied[name] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range files {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		sum := checksum(body)

		if prev, ok := applied[name]; ok {
			if prev != sum {
				// An edited migration means the database and the repository
				// disagree about what the schema is. Continuing would apply
				// later migrations onto a shape nobody has: refuse loudly.
				return fmt.Errorf(
					"migration %s was modified after it was applied (recorded %s, found %s); "+
						"add a new migration instead of editing an applied one",
					name, prev[:12], sum[:12])
			}
			continue
		}

		start := time.Now()
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("applying %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`, name, sum); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("recording %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing %s: %w", name, err)
		}
		log.Info("applied migration", "name", name, "took", time.Since(start).Round(time.Millisecond))
	}

	return nil
}

func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	// Filenames are zero-padded, so lexical order is application order.
	sort.Strings(names)
	return names, nil
}

func checksum(b []byte) string {
	// Normalise line endings so a checkout on Windows does not read as a
	// modified migration.
	normalised := strings.ReplaceAll(string(b), "\r\n", "\n")
	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:])
}
