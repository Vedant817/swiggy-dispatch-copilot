// Package store owns PostgreSQL access. Full repositories land in A3/B2.
package store

import (
	"context"
	"embed"
	"fmt"
	"sort"

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

// Migrate applies embedded SQL files in lexical order inside a single transaction each.
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
	for _, n := range names {
		b, err := migrationsFS.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		if _, err := s.Pool.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("migrate %s: %w", n, err)
		}
	}
	return nil
}
