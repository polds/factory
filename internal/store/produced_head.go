package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	ProducedRecording = "recording"
	ProducedReady     = "ready"
)

// ProducedHead is the stored tip of one task. It has no bundle bytes.
type ProducedHead struct {
	TaskID       string
	RunID        string
	ExecutionID  string
	HeadSHA      string
	BundleSHA256 string
	State        string
}

// PutProducedHead inserts the row. A second insert for the same task replaces
// the SHA only when the new state is ready.
func (s *Store) PutProducedHead(ctx context.Context, row ProducedHead) error {
	if row.TaskID == "" || row.RunID == "" || row.ExecutionID == "" || len(row.HeadSHA) != 40 || row.BundleSHA256 == "" {
		return fmt.Errorf("%w: produced head", ErrInputInvalid)
	}
	if row.State != ProducedRecording && row.State != ProducedReady {
		return fmt.Errorf("%w: produced head state", ErrInputInvalid)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO native_produced_heads (
		    task_id, run_id, execution_id, head_sha, bundle_sha256, state)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (task_id) DO UPDATE
		SET run_id = EXCLUDED.run_id,
		    execution_id = EXCLUDED.execution_id,
		    head_sha = EXCLUDED.head_sha,
		    bundle_sha256 = EXCLUDED.bundle_sha256,
		    state = EXCLUDED.state`,
		row.TaskID, row.RunID, row.ExecutionID, row.HeadSHA, row.BundleSHA256, row.State)
	if err != nil {
		return fmt.Errorf("store: produced head: %w", err)
	}
	return nil
}

// GetProducedHead returns the row for the task.
func (s *Store) GetProducedHead(ctx context.Context, taskID string) (ProducedHead, error) {
	var row ProducedHead
	err := s.pool.QueryRow(ctx, `
		SELECT task_id, run_id, execution_id, head_sha, bundle_sha256, state
		FROM native_produced_heads WHERE task_id = $1`, taskID).Scan(
		&row.TaskID, &row.RunID, &row.ExecutionID, &row.HeadSHA, &row.BundleSHA256, &row.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProducedHead{}, ErrNotFound
	}
	if err != nil {
		return ProducedHead{}, fmt.Errorf("store: produced head: %w", err)
	}
	return row, nil
}

// RunSeat is the native task bound to a run. A run with no activation is not found.
func (s *Store) RunSeat(ctx context.Context, runID string) (assignee, taskID, invocationID string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT t.assignee, t.id, t.invocation_id
		FROM native_activations a
		JOIN native_tasks t ON t.id = a.task_id
		WHERE a.run_id = $1
		ORDER BY a.fence DESC
		LIMIT 1`, runID).Scan(&assignee, &taskID, &invocationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return "", "", "", fmt.Errorf("store: run seat: %w", err)
	}
	return assignee, taskID, invocationID, nil
}

// TaskByAssignee returns the native task id for that seat on the invocation.
func (s *Store) TaskByAssignee(ctx context.Context, invocationID, assignee string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM native_tasks
		WHERE invocation_id = $1 AND assignee = $2
		ORDER BY created_at
		LIMIT 1`, invocationID, assignee).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: task by assignee: %w", err)
	}
	return id, nil
}

// NativeChild is one required child of a parent task.
type NativeChild struct {
	ID       string
	Assignee string
	Status   string
}

// RequiredChildren lists the join edges under a parent. It does not join.
func (s *Store) RequiredChildren(ctx context.Context, parentID string) ([]NativeChild, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.assignee, c.task_status
		FROM native_joins j
		JOIN native_tasks c ON c.id = j.child_task_id
		WHERE j.parent_task_id = $1 AND j.required
		ORDER BY c.id`, parentID)
	if err != nil {
		return nil, fmt.Errorf("store: children: %w", err)
	}
	defer rows.Close()
	var out []NativeChild
	for rows.Next() {
		var row NativeChild
		if err := rows.Scan(&row.ID, &row.Assignee, &row.Status); err != nil {
			return nil, fmt.Errorf("store: children: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: children: %w", err)
	}
	return out, nil
}
