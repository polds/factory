package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const runColumns = `
    id, factory_id, COALESCE(agent_id, ''), status, pool, priority, prompt, title, repo,
    COALESCE(worker_id, ''), COALESCE(failure_reason, ''), exit_code, cancel_requested,
    created_at, started_at, ended_at`

func scanRun(row pgx.Row) (Run, error) {
	var run Run
	var priority int
	err := row.Scan(
		&run.ID, &run.FactoryID, &run.AgentID, &run.Status, &run.Pool, &priority,
		&run.Prompt, &run.Title, &run.Repo, &run.WorkerID, &run.FailureReason,
		&run.ExitCode, &run.CancelRequested, &run.CreatedAt, &run.StartedAt, &run.EndedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	run.Priority = Priority(priority)
	return run, nil
}

// CreateRun inserts the run and its queue row, then notifies channel run_queue
// with the run id. The notification is delivered at commit.
func (s *Store) CreateRun(ctx context.Context, r NewRun) (Run, error) {
	if r.FactoryID == "" || r.Pool == "" {
		return Run{}, fmt.Errorf("store: factory and pool are required")
	}
	switch r.Priority {
	case PriorityBatch, PriorityPipeline, PriorityInteractive:
	default:
		return Run{}, fmt.Errorf("store: priority %d is not valid", r.Priority)
	}
	id, err := NewID(PrefixRun)
	if err != nil {
		return Run{}, err
	}
	var agentID any
	if r.AgentID != "" {
		agentID = r.AgentID
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("store: create run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (id, factory_id, agent_id, status, priority, pool, prompt, title, repo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, r.FactoryID, agentID, StatusQueued, int(r.Priority), r.Pool, r.Prompt, r.Title, r.Repo,
	); err != nil {
		return Run{}, fmt.Errorf("store: create run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())`,
		id, r.FactoryID, r.Pool, int(r.Priority),
	); err != nil {
		return Run{}, fmt.Errorf("store: enqueue run: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('run_queue', $1)`, id); err != nil {
		return Run{}, fmt.Errorf("store: notify run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, fmt.Errorf("store: create run: %w", err)
	}
	return s.GetRun(ctx, id)
}

// GetRun returns the run or ErrNotFound.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	run, err := scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Run{}, err
		}
		return Run{}, fmt.Errorf("store: get run: %w", err)
	}
	return run, nil
}

// ListRuns returns runs matching f, newest first.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM runs
		WHERE ($1 = '' OR factory_id = $1)
		  AND ($2 = '' OR agent_id = $2)
		  AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4`, f.FactoryID, f.AgentID, f.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list runs: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	return out, nil
}

// Lease claims up to n queued runs in pool whose lease is free.
// Order is priority descending, then enqueued_at, then run id.
// Claimed rows are skipped by concurrent leases (FOR UPDATE SKIP LOCKED)
// and stay unavailable until lease_until.
func (s *Store) Lease(ctx context.Context, pool string, ttl time.Duration, n int) ([]QueuedRun, error) {
	if pool == "" {
		return nil, fmt.Errorf("store: pool is required")
	}
	if n < 0 {
		return nil, fmt.Errorf("store: lease count must be >= 0")
	}
	if n == 0 || ttl <= 0 {
		if ttl <= 0 && n > 0 {
			return nil, fmt.Errorf("store: lease ttl must be positive")
		}
		return []QueuedRun{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH picked AS (
		    SELECT run_id
		    FROM run_queue
		    WHERE pool = $1
		      AND (lease_until IS NULL OR lease_until <= clock_timestamp())
		    ORDER BY priority DESC, enqueued_at ASC, run_id ASC
		    LIMIT $2
		    FOR UPDATE SKIP LOCKED
		),
		upd AS (
		    UPDATE run_queue q
		    SET lease_until = clock_timestamp() + ($3::bigint * interval '1 millisecond')
		    FROM picked
		    WHERE q.run_id = picked.run_id
		    RETURNING q.run_id, q.factory_id, q.pool, q.priority, q.enqueued_at, q.lease_until, q.attempts
		),
		mark AS (
		    -- Data-modifying CTE: Postgres runs it even though the outer SELECT
		    -- does not read mark. Leased runs move to status assigned.
		    UPDATE runs r
		    SET status = 'assigned'
		    WHERE r.id IN (SELECT run_id FROM upd)
		      AND r.status = 'queued'
		    RETURNING r.id
		)
		SELECT upd.run_id, upd.factory_id, upd.pool, upd.priority, upd.enqueued_at, upd.lease_until, upd.attempts
		FROM upd
		ORDER BY upd.priority DESC, upd.enqueued_at ASC, upd.run_id ASC`,
		pool, n, ttl.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("store: lease: %w", err)
	}
	defer rows.Close()
	out := []QueuedRun{}
	for rows.Next() {
		var row QueuedRun
		var priority int
		if err := rows.Scan(&row.RunID, &row.FactoryID, &row.Pool, &priority, &row.EnqueuedAt, &row.LeaseUntil, &row.Attempts); err != nil {
			return nil, fmt.Errorf("store: lease: %w", err)
		}
		row.Priority = Priority(priority)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: lease: %w", err)
	}
	return out, nil
}

// Requeue returns a leased run to the queue after task_rejected.
// attempts increases by one. No execution failure row is written.
// reason is required and is not persisted.
func (s *Store) Requeue(ctx context.Context, runID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("store: requeue reason is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: requeue: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE run_queue
		SET attempts = attempts + 1, lease_until = NULL
		WHERE run_id = $1`, runID)
	if err != nil {
		return fmt.Errorf("store: requeue: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	tag, err = tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, worker_id = NULL
		WHERE id = $1 AND status IN ($3, $4)`,
		runID, StatusQueued, StatusQueued, StatusAssigned)
	if err != nil {
		return fmt.Errorf("store: requeue: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: requeue %s: run is not queued or assigned", runID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: requeue: %w", err)
	}
	return nil
}

// StartExecution opens a new execution for a leased run and stores the token
// hash only. The id differs from any earlier execution of the same run.
func (s *Store) StartExecution(ctx context.Context, runID, workerID string, tokenSHA256 []byte) (Execution, error) {
	if workerID == "" {
		return Execution{}, fmt.Errorf("store: worker id is required")
	}
	if len(tokenSHA256) != 32 {
		return Execution{}, fmt.Errorf("store: run token hash must be 32 bytes")
	}
	id, err := NewID(PrefixExec)
	if err != nil {
		return Execution{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, ErrNotFound
	}
	if err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	if status != StatusAssigned && status != StatusRunning {
		return Execution{}, fmt.Errorf("store: run %s is %s", runID, status)
	}
	var live int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM executions
		WHERE run_id = $1 AND state IN ($2, $3)`,
		runID, ExecAssigned, ExecRunning,
	).Scan(&live); err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	if live > 0 {
		return Execution{}, fmt.Errorf("store: run %s already has a non-terminal execution", runID)
	}

	var attempt int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt), 0) + 1 FROM executions WHERE run_id = $1`, runID).Scan(&attempt); err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO executions (id, run_id, attempt, worker_id, state, run_token_sha256)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, runID, attempt, workerID, ExecRunning, tokenSHA256,
	); err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, worker_id = $3, started_at = COALESCE(started_at, clock_timestamp())
		WHERE id = $1`,
		runID, StatusRunning, workerID,
	); err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Execution{}, fmt.Errorf("store: start execution: %w", err)
	}
	hash := make([]byte, len(tokenSHA256))
	copy(hash, tokenSHA256)
	return Execution{
		ID:             id,
		RunID:          runID,
		Attempt:        attempt,
		WorkerID:       workerID,
		State:          ExecRunning,
		RunTokenSHA256: hash,
	}, nil
}

// FinishExecution applies t once. A second call returns applied=false and
// leaves the execution and run status unchanged. A terminal for an orphaned
// execution does not change the run.
func (s *Store) FinishExecution(ctx context.Context, execID string, t Terminal) (bool, error) {
	runStatus, err := runStatusForTerminal(t.State)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("store: finish execution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var details any
	if len(t.FailureDetails) > 0 {
		details = []byte(t.FailureDetails)
	}
	var runID string
	err = tx.QueryRow(ctx, `
		UPDATE executions
		SET state = $2,
		    failure_reason = NULLIF($3, ''),
		    exit_code = $4,
		    failure_details_json = $5::jsonb,
		    ended_at = clock_timestamp()
		WHERE id = $1 AND state IN ($6, $7)
		RETURNING run_id`,
		execID, t.State, t.FailureReason, t.ExitCode, details, ExecAssigned, ExecRunning,
	).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		var state string
		err = tx.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, execID).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, fmt.Errorf("store: finish execution: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("store: finish execution: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: finish execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2,
		    failure_reason = NULLIF($3, ''),
		    exit_code = $4,
		    ended_at = clock_timestamp()
		WHERE id = $1 AND status IN ($5, $6)`,
		runID, runStatus, t.FailureReason, t.ExitCode, StatusAssigned, StatusRunning,
	); err != nil {
		return false, fmt.Errorf("store: finish execution: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return false, fmt.Errorf("store: finish execution: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("store: finish execution: %w", err)
	}
	return true, nil
}

func runStatusForTerminal(state string) (string, error) {
	switch state {
	case ExecCompleted:
		return StatusSucceeded, nil
	case ExecFailed:
		return StatusFailed, nil
	case ExecCancelled:
		return StatusCancelled, nil
	default:
		return "", fmt.Errorf("store: unsupported terminal state %q", state)
	}
}

// ExpireLeases marks non-terminal executions orphaned when lease_until <= now,
// clears the lease, and makes the run leasable again. now is the caller's
// clock so tests can advance past lease_until without sleeping.
func (s *Store) ExpireLeases(ctx context.Context, now time.Time) ([]OrphanedExecution, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT run_id
		FROM run_queue
		WHERE lease_until IS NOT NULL AND lease_until <= $1
		FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: expire leases: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	rows.Close()
	if len(ids) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("store: expire leases: %w", err)
		}
		return []OrphanedExecution{}, nil
	}

	orphanedRows, err := tx.Query(ctx, `
		UPDATE executions
		SET state = $2, ended_at = $3
		WHERE run_id = ANY($1) AND state IN ($4, $5)
		RETURNING id, run_id, worker_id`,
		ids, ExecOrphaned, now, ExecAssigned, ExecRunning)
	if err != nil {
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	orphans := []OrphanedExecution{}
	for orphanedRows.Next() {
		var row OrphanedExecution
		if err := orphanedRows.Scan(&row.ExecutionID, &row.RunID, &row.WorkerID); err != nil {
			orphanedRows.Close()
			return nil, fmt.Errorf("store: expire leases: %w", err)
		}
		orphans = append(orphans, row)
	}
	if err := orphanedRows.Err(); err != nil {
		orphanedRows.Close()
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	orphanedRows.Close()

	if _, err := tx.Exec(ctx, `
		UPDATE run_queue
		SET lease_until = NULL, attempts = attempts + 1
		WHERE run_id = ANY($1)`, ids); err != nil {
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, worker_id = NULL
		WHERE id = ANY($1) AND status IN ($3, $4)`,
		ids, StatusQueued, StatusAssigned, StatusRunning); err != nil {
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: expire leases: %w", err)
	}
	return orphans, nil
}

// RequestCancel cancels a queued or assigned run immediately. A running run
// is flagged and stays running until FinishExecution. A terminal run returns
// ErrAlreadyTerminal.
func (s *Store) RequestCancel(ctx context.Context, runID string) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("store: cancel: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: cancel: %w", err)
	}
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return Run{}, ErrAlreadyTerminal
	case StatusQueued, StatusAssigned:
		if _, err := tx.Exec(ctx, `
			UPDATE runs
			SET status = $2, cancel_requested = true, ended_at = clock_timestamp(), worker_id = NULL
			WHERE id = $1`, runID, StatusCancelled); err != nil {
			return Run{}, fmt.Errorf("store: cancel: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE executions
			SET state = $2, ended_at = clock_timestamp()
			WHERE run_id = $1 AND state IN ($3, $4)`,
			runID, ExecCancelled, ExecAssigned, ExecRunning); err != nil {
			return Run{}, fmt.Errorf("store: cancel: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
			return Run{}, fmt.Errorf("store: cancel: %w", err)
		}
	case StatusRunning:
		if _, err := tx.Exec(ctx, `UPDATE runs SET cancel_requested = true WHERE id = $1`, runID); err != nil {
			return Run{}, fmt.Errorf("store: cancel: %w", err)
		}
	default:
		return Run{}, fmt.Errorf("store: cancel status %q", status)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, fmt.Errorf("store: cancel: %w", err)
	}
	return s.GetRun(ctx, runID)
}
