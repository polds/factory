package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AcquireLease takes or renews a named lease until now+ttl.
// A missing name is inserted. ON CONFLICT updates only when expires_at is
// strictly before now or holder is already this holder. An update of a
// missing row would grant nothing. acquired is false when someone else
// holds an unexpired lease; that is not an error.
func (s *Store) AcquireLease(ctx context.Context, name, holder string, ttl time.Duration, now time.Time) (bool, error) {
	if name == "" || holder == "" {
		return false, fmt.Errorf("store: lease name and holder are required")
	}
	if ttl <= 0 {
		return false, fmt.Errorf("store: lease ttl must be positive")
	}
	if now.IsZero() {
		return false, fmt.Errorf("store: now is required")
	}
	var got string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO leader_leases (name, holder, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE
		SET holder = EXCLUDED.holder,
		    expires_at = EXCLUDED.expires_at
		WHERE leader_leases.expires_at < $4
		   OR leader_leases.holder = $2
		RETURNING holder`,
		name, holder, now.Add(ttl), now,
	).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: acquire lease: %w", err)
	}
	return got == holder, nil
}
