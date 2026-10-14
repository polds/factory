package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the Postgres system of record and run queue.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to url and checks the connection. Call Migrate before use.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool. It waits until acquired connections are returned.
func (s *Store) Close() {
	s.pool.Close()
}
