// Package store owns PostgreSQL access. Full repositories land in A3/B2.
package store

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	Pool *pgxpool.Pool
}

func Connect(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("empty DATABASE_URL")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

func (s *Store) Close() { s.Pool.Close() }

// Migrate serializes concurrent API/worker boot, records checksums, and applies
// pending migrations atomically. Changed historical migrations fail startup.
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('dispatch_schema_migrations'))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS _migrations (id TEXT PRIMARY KEY, checksum TEXT)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE _migrations ADD COLUMN IF NOT EXISTS checksum TEXT`); err != nil {
		return err
	}
	for _, n := range names {
		b, err := migrationsFS.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(b))
		var stored string
		err = tx.QueryRow(ctx, `SELECT checksum FROM _migrations WHERE id=$1`, n).Scan(&stored)
		if err == nil {
			if stored != checksum {
				return fmt.Errorf("migration %s checksum changed after application", n)
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("migrate %s: %w", n, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO _migrations(id,checksum) VALUES($1,$2)`, n, checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
