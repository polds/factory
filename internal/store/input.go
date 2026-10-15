package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const inputColumns = `
    id, work_item_id, run_id, execution_id, question, answer, answered_by,
    created_at, answered_at`

func scanInput(row pgx.Row) (InputRequest, error) {
	var req InputRequest
	err := row.Scan(
		&req.ID, &req.WorkItemID, &req.RunID, &req.ExecutionID, &req.Question,
		&req.Answer, &req.AnsweredBy, &req.CreatedAt, &req.AnsweredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InputRequest{}, ErrNotFound
	}
	if err != nil {
		return InputRequest{}, err
	}
	return req, nil
}

// CreateInputRequest records a question and marks the work item awaiting
// input in one transaction. A repeat of the same pending question returns
// that row. A different pending question returns ErrConflict.
func (s *Store) CreateInputRequest(ctx context.Context, workItemID, runID, executionID, question string) (InputRequest, error) {
	if workItemID == "" || runID == "" || question == "" {
		return InputRequest{}, fmt.Errorf("store: work item, run, and question are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: create input: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the work item first so two questions cannot both pass the
	// pending check. The partial unique index is the backstop.
	var status string
	err = tx.QueryRow(ctx, `
		SELECT status FROM work_items WHERE id = $1 FOR UPDATE`, workItemID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return InputRequest{}, ErrNotFound
	}
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: create input: %w", err)
	}
	if status != WorkItemOpen && status != WorkItemAwaitingInput {
		return InputRequest{}, fmt.Errorf("store: work item is not open")
	}

	pending, err := scanInput(tx.QueryRow(ctx, `
		SELECT `+inputColumns+`
		FROM input_requests
		WHERE work_item_id = $1 AND answered_at IS NULL
		FOR UPDATE`, workItemID))
	if err == nil {
		if pending.Question == question && pending.RunID == runID {
			if err := tx.Commit(ctx); err != nil {
				return InputRequest{}, fmt.Errorf("store: create input: %w", err)
			}
			return pending, nil
		}
		return InputRequest{}, ErrConflict
	}
	if !errors.Is(err, ErrNotFound) {
		return InputRequest{}, fmt.Errorf("store: create input: %w", err)
	}

	id, err := NewID(PrefixInputRequest)
	if err != nil {
		return InputRequest{}, err
	}
	created, err := scanInput(tx.QueryRow(ctx, `
		INSERT INTO input_requests (id, work_item_id, run_id, execution_id, question)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+inputColumns, id, workItemID, runID, executionID, question))
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: create input: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET status = $2,
		    suspended_run_id = $3,
		    suspended_execution_id = $4,
		    suspended_at = clock_timestamp()
		WHERE id = $1`, workItemID, WorkItemAwaitingInput, runID, executionID)
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: create input: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return InputRequest{}, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return InputRequest{}, fmt.Errorf("store: create input: %w", err)
	}
	return created, nil
}

// AnswerInputRequest records the answer for the work item's latest request.
// The same answer returns the stored row. A different answer returns ErrConflict.
// The first answer clears suspension and returns an awaiting_input work item to open.
func (s *Store) AnswerInputRequest(ctx context.Context, workItemID, answer, answeredBy string) (InputRequest, error) {
	if workItemID == "" {
		return InputRequest{}, fmt.Errorf("store: work item is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: answer input: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanInput(tx.QueryRow(ctx, `
		SELECT `+inputColumns+`
		FROM input_requests
		WHERE work_item_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE`, workItemID))
	if errors.Is(err, ErrNotFound) {
		return InputRequest{}, ErrNotFound
	}
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: answer input: %w", err)
	}
	if current.AnsweredAt != nil {
		if err := tx.Commit(ctx); err != nil {
			return InputRequest{}, fmt.Errorf("store: answer input: %w", err)
		}
		if current.Answer == answer {
			return current, nil
		}
		return InputRequest{}, ErrConflict
	}
	updated, err := scanInput(tx.QueryRow(ctx, `
		UPDATE input_requests
		SET answer = $2,
		    answered_by = $3,
		    answered_at = clock_timestamp()
		WHERE id = $1
		RETURNING `+inputColumns, current.ID, answer, answeredBy))
	if err != nil {
		return InputRequest{}, fmt.Errorf("store: answer input: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_items
		SET suspended_run_id = '',
		    suspended_execution_id = '',
		    suspended_at = NULL,
		    status = CASE WHEN status = $2 THEN $3 ELSE status END
		WHERE id = $1`, workItemID, WorkItemAwaitingInput, WorkItemOpen); err != nil {
		return InputRequest{}, fmt.Errorf("store: answer input: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InputRequest{}, fmt.Errorf("store: answer input: %w", err)
	}
	return updated, nil
}

// PendingInputRequest returns the unanswered request for a work item, or ErrNotFound.
func (s *Store) PendingInputRequest(ctx context.Context, workItemID string) (InputRequest, error) {
	row, err := scanInput(s.pool.QueryRow(ctx, `
		SELECT `+inputColumns+`
		FROM input_requests
		WHERE work_item_id = $1 AND answered_at IS NULL`, workItemID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return InputRequest{}, err
		}
		return InputRequest{}, fmt.Errorf("store: pending input: %w", err)
	}
	return row, nil
}

// ListSuspendedRunIDs returns run ids parked on an unanswered question.
func (s *Store) ListSuspendedRunIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT suspended_run_id
		FROM work_items
		WHERE suspended_run_id <> ''
		ORDER BY suspended_run_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list suspended runs: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: list suspended runs: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list suspended runs: %w", err)
	}
	return out, nil
}
