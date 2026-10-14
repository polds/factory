package store

import (
	"context"
	"fmt"
	"time"
)

// UpsertWorker inserts or replaces the worker's mutable fields.
func (s *Store) UpsertWorker(ctx context.Context, w Worker) error {
	if w.ID == "" || w.Pool == "" {
		return fmt.Errorf("store: worker id and pool are required")
	}
	var heartbeat any
	if !w.LastHeartbeatAt.IsZero() {
		heartbeat = w.LastHeartbeatAt
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO workers (id, pool, labels_json, platform, last_heartbeat_at, max_concurrent, status)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
		    pool = EXCLUDED.pool,
		    labels_json = EXCLUDED.labels_json,
		    platform = EXCLUDED.platform,
		    last_heartbeat_at = EXCLUDED.last_heartbeat_at,
		    max_concurrent = EXCLUDED.max_concurrent,
		    status = EXCLUDED.status`,
		w.ID, w.Pool, []byte(jsonOr(w.LabelsJSON, `{}`)), w.Platform, heartbeat, w.MaxConcurrent, w.Status,
	); err != nil {
		return fmt.Errorf("store: upsert worker: %w", err)
	}
	return nil
}

// TouchWorker sets last_heartbeat_at. A missing worker returns ErrNotFound.
func (s *Store) TouchWorker(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE workers SET last_heartbeat_at = $2 WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("store: touch worker: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
