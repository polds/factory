package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

func requireNow(now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("store: now is required")
	}
	return nil
}

// ListWorkers returns every worker, pool then id.
// now is the caller's clock for heartbeat age and rejections in the last hour.
// HeartbeatAgeMS is -1 when last_heartbeat_at is null.
func (s *Store) ListWorkers(ctx context.Context, now time.Time) ([]WorkerInfo, error) {
	if err := requireNow(now); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT
		    workers.id,
		    workers.pool,
		    workers.platform,
		    COALESCE(workers.backend, ''),
		    COALESCE(workers.version, ''),
		    workers.labels_json,
		    workers.last_heartbeat_at,
		    workers.connected_at,
		    workers.max_concurrent,
		    workers.in_flight,
		    workers.reconnects,
		    workers.status,
		    CASE
		        WHEN workers.last_heartbeat_at IS NULL THEN -1
		        ELSE floor(
		            extract(epoch FROM ($1::timestamptz - workers.last_heartbeat_at)) * 1000
		        )::bigint
		    END,
		    (
		        SELECT count(*)::int
		        FROM worker_rejections
		        WHERE worker_rejections.worker_id = workers.id
		          AND worker_rejections.at >= $1::timestamptz - interval '1 hour'
		          AND worker_rejections.at <= $1::timestamptz
		    )
		FROM workers
		ORDER BY workers.pool, workers.id`, now)
	if err != nil {
		return nil, fmt.Errorf("store: list workers: %w", err)
	}
	defer rows.Close()
	out := []WorkerInfo{}
	for rows.Next() {
		var info WorkerInfo
		var labels []byte
		if err := rows.Scan(
			&info.ID, &info.Pool, &info.Platform, &info.Backend, &info.Version, &labels,
			&info.LastHeartbeatAt, &info.ConnectedAt, &info.MaxConcurrent, &info.InFlight,
			&info.Reconnects, &info.Status, &info.HeartbeatAgeMS, &info.Rejected1h,
		); err != nil {
			return nil, fmt.Errorf("store: list workers: %w", err)
		}
		info.LabelsJSON = labels
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list workers: %w", err)
	}
	return out, nil
}

// PoolStats returns one row per pool that has a worker, a queued run, or an
// assignment in the last hour. Queue depth is the current run_queue count.
// p50 time-to-assign is assigned_at - created_at for runs assigned in
// [now-1h, now].
func (s *Store) PoolStats(ctx context.Context, now time.Time) ([]PoolStat, error) {
	if err := requireNow(now); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		WITH pools AS (
		    SELECT pool FROM workers
		    UNION
		    SELECT pool FROM run_queue
		    UNION
		    SELECT pool FROM runs
		    WHERE assigned_at IS NOT NULL
		      AND assigned_at >= $1::timestamptz - interval '1 hour'
		      AND assigned_at <= $1::timestamptz
		),
		depth AS (
		    SELECT pool, count(*)::int AS queue_depth
		    FROM run_queue
		    GROUP BY pool
		),
		assign AS (
		    SELECT
		        pool,
		        percentile_cont(0.5) WITHIN GROUP (
		            ORDER BY extract(epoch FROM (assigned_at - created_at)) * 1000
		        ) AS p50_ms
		    FROM runs
		    WHERE assigned_at IS NOT NULL
		      AND assigned_at >= $1::timestamptz - interval '1 hour'
		      AND assigned_at <= $1::timestamptz
		    GROUP BY pool
		),
		rej AS (
		    SELECT workers.pool, count(*)::int AS rejected_1h
		    FROM worker_rejections
		    JOIN workers ON workers.id = worker_rejections.worker_id
		    WHERE worker_rejections.at >= $1::timestamptz - interval '1 hour'
		      AND worker_rejections.at <= $1::timestamptz
		    GROUP BY workers.pool
		)
		SELECT
		    pools.pool,
		    COALESCE(depth.queue_depth, 0),
		    assign.p50_ms,
		    COALESCE(rej.rejected_1h, 0)
		FROM pools
		LEFT JOIN depth ON depth.pool = pools.pool
		LEFT JOIN assign ON assign.pool = pools.pool
		LEFT JOIN rej ON rej.pool = pools.pool
		ORDER BY pools.pool`, now)
	if err != nil {
		return nil, fmt.Errorf("store: pool stats: %w", err)
	}
	defer rows.Close()
	out := []PoolStat{}
	for rows.Next() {
		var stat PoolStat
		var p50 *float64
		if err := rows.Scan(&stat.Pool, &stat.QueueDepth, &p50, &stat.Rejected1h); err != nil {
			return nil, fmt.Errorf("store: pool stats: %w", err)
		}
		if p50 != nil {
			ms := int64(math.Round(*p50))
			stat.P50TimeToAssignMS = &ms
		}
		out = append(out, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pool stats: %w", err)
	}
	return out, nil
}

// RecordRejection stores one task_rejected. Rows older than seven days are
// deleted in the same transaction. A quiet pool keeps older rows until the
// next rejection, because nothing else runs this delete.
func (s *Store) RecordRejection(ctx context.Context, workerID, runID string) error {
	if workerID == "" || runID == "" {
		return fmt.Errorf("store: worker and run are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: record rejection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := requireRow(ctx, tx, `SELECT 1 FROM workers WHERE id = $1`, workerID); err != nil {
		return err
	}
	if err := requireRow(ctx, tx, `SELECT 1 FROM runs WHERE id = $1`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM worker_rejections
		WHERE at < clock_timestamp() - interval '7 days'`); err != nil {
		return fmt.Errorf("store: record rejection: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO worker_rejections (worker_id, run_id)
		VALUES ($1, $2)`, workerID, runID); err != nil {
		return fmt.Errorf("store: record rejection: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: record rejection: %w", err)
	}
	return nil
}

func requireRow(ctx context.Context, q rowQuery, query, id string) error {
	var one int
	err := q.QueryRow(ctx, query, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: record rejection: %w", err)
	}
	return nil
}

// MarkWorkerConnected records a session. The first call leaves reconnects
// unchanged. A later call, when connected_at is already set, increments it.
func (s *Store) MarkWorkerConnected(ctx context.Context, id, version, backend string) error {
	if id == "" {
		return fmt.Errorf("store: worker id is required")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE workers
		SET backend = $2,
		    version = $3,
		    connected_at = clock_timestamp(),
		    reconnects = CASE
		        WHEN connected_at IS NULL THEN reconnects
		        ELSE reconnects + 1
		    END
		WHERE id = $1`, id, backend, version)
	if err != nil {
		return fmt.Errorf("store: mark worker connected: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWorkerInFlight stores the worker's current task count.
// The console reads it from ListWorkers. n must be >= 0.
func (s *Store) SetWorkerInFlight(ctx context.Context, id string, n int) error {
	if id == "" {
		return fmt.Errorf("store: worker id is required")
	}
	if n < 0 {
		return fmt.Errorf("store: in_flight must be >= 0")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE workers SET in_flight = $2 WHERE id = $1`, id, n)
	if err != nil {
		return fmt.Errorf("store: set in flight: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
