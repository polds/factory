package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/store"
)

// claim leases the queue row and marks the run assigned.
// The returned prompt is the runs.prompt value committed in this same
// transaction, under the queue-row lock and the runs-row lock taken by
// the status update. A resume that already committed a new prompt is
// visible here. A resume that still needs the queue row waits until this
// transaction ends, in this process or another.
func (s *Server) claim(ctx context.Context, runID string, ttl time.Duration) (string, bool, error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `
		WITH picked AS (
		    SELECT run_id
		    FROM run_queue
		    WHERE run_id = $1
		      AND (lease_until IS NULL OR lease_until <= clock_timestamp())
		    FOR UPDATE SKIP LOCKED
		)
		UPDATE run_queue q
		SET lease_until = clock_timestamp() + ($2::bigint * interval '1 millisecond')
		FROM picked
		WHERE q.run_id = picked.run_id
		RETURNING q.run_id`, runID, ttl.Milliseconds()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("server: claim: %w", err)
	}
	var prompt string
	err = tx.QueryRow(ctx, `
		UPDATE runs SET status = 'assigned'
		WHERE id = $1 AND status = 'queued'
		RETURNING prompt`, runID).Scan(&prompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("server: claim: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("server: claim: %w", err)
	}
	return prompt, true, nil
}

func (s *Server) releaseClaim(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if _, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'queued', worker_id = NULL
		WHERE id = $1 AND status = 'assigned'`, runID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE run_queue SET lease_until = NULL WHERE run_id = $1`, runID)
	return err
}

func (s *Server) rewind(ctx context.Context, runID, execID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if _, err := s.db.Exec(ctx, `
		UPDATE executions
		SET state = 'orphaned', ended_at = clock_timestamp()
		WHERE id = $1 AND state IN ('assigned', 'running')`, execID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'queued', worker_id = NULL
		WHERE id = $1 AND status IN ('assigned', 'running')`, runID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE run_queue SET lease_until = NULL WHERE run_id = $1`, runID)
	return err
}

func (s *Server) prepareRequeue(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if _, err := s.db.Exec(ctx, `
		UPDATE executions
		SET state = 'orphaned', ended_at = clock_timestamp()
		WHERE run_id = $1 AND state IN ('assigned', 'running')`, runID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'assigned', worker_id = NULL
		WHERE id = $1 AND status = 'running'`, runID)
	return err
}

func (s *Server) renewLease(ctx context.Context, runID string, ttl time.Duration) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	_, err := s.db.Exec(ctx, `
		UPDATE run_queue
		SET lease_until = clock_timestamp() + ($2::bigint * interval '1 millisecond')
		WHERE run_id = $1 AND lease_until IS NOT NULL`, runID, ttl.Milliseconds())
	return err
}

func (s *Server) liveExecution(ctx context.Context, runID string) (string, error) {
	if id := s.execForRun(runID); id != "" {
		var state string
		err := s.db.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, id).Scan(&state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if state == store.ExecAssigned || state == store.ExecRunning {
			return id, nil
		}
	}
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM executions
		WHERE run_id = $1 AND state IN ('assigned', 'running')
		ORDER BY attempt DESC
		LIMIT 1`, runID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) executionForEvent(ctx context.Context, runID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM executions WHERE run_id = $1 ORDER BY attempt DESC LIMIT 1`, runID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id, err = store.NewID(store.PrefixExec)
	if err != nil {
		return "", err
	}
	// Not a run token. The column is NOT NULL and this execution never runs.
	var sum [32]byte
	tag, err := s.db.Exec(ctx, `
		INSERT INTO executions (id, run_id, attempt, worker_id, state, run_token_sha256, ended_at)
		VALUES ($1, $2, 1, '', 'cancelled', $3, clock_timestamp())
		ON CONFLICT (run_id, attempt) DO NOTHING`, id, runID, sum[:])
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return id, nil
	}
	err = s.db.QueryRow(ctx, `SELECT id FROM executions WHERE run_id = $1 AND attempt = 1`, runID).Scan(&id)
	return id, err
}

// forceExpire drops the lease so ExpireLeases requeues the run. The
// execution becomes orphaned. That state is not written onto the run as
// worker_lost: the run is queued again and has no failure class.
func (s *Server) forceExpire(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	if _, err := s.db.Exec(ctx, `
		UPDATE run_queue SET lease_until = '1970-01-02T00:00:00Z'
		WHERE run_id = $1`, runID); err != nil {
		s.leaseMu.Unlock()
		return err
	}
	s.leaseMu.Unlock()
	orphans, err := s.store.ExpireLeases(ctx, s.now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, orphan := range orphans {
		if id, ok := s.byRun[orphan.RunID]; ok && id == orphan.ExecutionID {
			delete(s.byRun, orphan.RunID)
		}
		delete(s.live, orphan.ExecutionID)
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) matchToken(ctx context.Context, runID string, sum []byte) (string, bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, run_token_sha256
		FROM executions
		WHERE run_id = $1
		ORDER BY attempt DESC`, runID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var matched string
	found := 0
	for rows.Next() {
		var id string
		var hash []byte
		if err := rows.Scan(&id, &hash); err != nil {
			return "", false, err
		}
		found++
		if subtle.ConstantTimeCompare(hash, sum) == 1 && matched == "" {
			matched = id
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if found == 0 {
		var dummy [32]byte
		subtle.ConstantTimeCompare(dummy[:], sum)
		return "", false, nil
	}
	return matched, matched != "", nil
}

func (s *Server) isLeader(ctx context.Context) bool {
	if s.leader == nil {
		return false
	}
	var ok bool
	if err := s.leader.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4, $2::int4)`, leaderLockClass, leaderLockObj).Scan(&ok); err != nil {
		s.log.Error("leader lock", "err", err)
		return false
	}
	return ok
}
