package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CoalesceWake records one durable wake for the task. Repeated calls keep
// a single native_wake_intents row and route it to the assignee given here,
// which the caller sets to the task's current assignee.
func (s *Store) CoalesceWake(ctx context.Context, in WakeIntent) (WakeIntent, error) {
	if in.TaskID == "" || in.InvocationID == "" || in.Assignee == "" || in.Reason == "" {
		return WakeIntent{}, fmt.Errorf("%w: wake task, invocation, assignee, and reason are required", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WakeIntent{}, fmt.Errorf("store: coalesce wake: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := coalesceWakeTx(ctx, tx, in)
	if err != nil {
		return WakeIntent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WakeIntent{}, fmt.Errorf("store: coalesce wake: %w", err)
	}
	return out, nil
}

func coalesceWakeTx(ctx context.Context, tx pgx.Tx, in WakeIntent) (WakeIntent, error) {
	var factoryID, invocationID string
	err := tx.QueryRow(ctx, `
		SELECT factory_id, invocation_id FROM native_tasks WHERE id = $1 FOR UPDATE`, in.TaskID).
		Scan(&factoryID, &invocationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WakeIntent{}, ErrNotFound
		}
		return WakeIntent{}, fmt.Errorf("store: coalesce wake: %w", err)
	}
	if invocationID != in.InvocationID {
		return WakeIntent{}, ErrNotFound
	}
	row, err := scanWake(tx.QueryRow(ctx, `
		INSERT INTO native_wake_intents (
		    task_id, invocation_id, assignee, reason, pending, coalesce_count)
		VALUES ($1, $2, $3, $4, true, 1)
		ON CONFLICT (task_id) DO UPDATE
		SET assignee = EXCLUDED.assignee,
		    reason = EXCLUDED.reason,
		    pending = true,
		    coalesce_count = native_wake_intents.coalesce_count + 1,
		    updated_at = now()
		RETURNING task_id, invocation_id, assignee, reason, pending, coalesce_count, updated_at`,
		in.TaskID, in.InvocationID, in.Assignee, in.Reason))
	if err != nil {
		return WakeIntent{}, fmt.Errorf("store: coalesce wake: %w", err)
	}
	return row, nil
}

// GetWake returns the coalesced wake intent for a task.
func (s *Store) GetWake(ctx context.Context, taskID string) (WakeIntent, error) {
	row, err := scanWake(s.pool.QueryRow(ctx, `
		SELECT task_id, invocation_id, assignee, reason, pending, coalesce_count, updated_at
		FROM native_wake_intents
		WHERE task_id = $1`, taskID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WakeIntent{}, ErrNotFound
		}
		return WakeIntent{}, fmt.Errorf("store: get wake: %w", err)
	}
	return row, nil
}

func scanWake(row pgx.Row) (WakeIntent, error) {
	var out WakeIntent
	err := row.Scan(&out.TaskID, &out.InvocationID, &out.Assignee, &out.Reason, &out.Pending, &out.CoalesceCount, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WakeIntent{}, ErrNotFound
		}
		return WakeIntent{}, err
	}
	return out, nil
}

// CountWakeIntents returns how many wake rows exist for one task.
func (s *Store) CountWakeIntents(ctx context.Context, taskID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM native_wake_intents WHERE task_id = $1`, taskID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count wakes: %w", err)
	}
	return n, nil
}
