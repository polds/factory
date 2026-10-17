package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/store"
)

// worker-lost tokens are execution states and failure reasons, not run
// statuses. ExpireLeases marks the execution orphaned and requeues the run.
// Classifying that requeued row (status queued, no reason) is an empty class.
// Do not persist worker_lost onto it. Pass the execution state as RunStatus
// only when the run itself is already terminal.
func workerLostState(state string) bool {
	switch state {
	case store.ExecOrphaned, failure.ReasonLeaseExpired, failure.ReasonWorkerGone:
		return true
	default:
		return false
	}
}

// classifyRun is the only classification call in the server. execState is the
// latest execution's state. On a terminal run it replaces RunStatus when it
// is a worker-lost token, so the result matches store.FailureBackfillCASE.
func classifyRun(status, reason, harness, execState string, exit *int, details json.RawMessage) failure.Result {
	in := failure.Input{
		RunStatus:     status,
		FailureReason: reason,
		ExitCode:      exit,
		HarnessStatus: harness,
		Details:       details,
	}
	if isTerminalStatus(status) && workerLostState(execState) {
		in.RunStatus = execState
	}
	return failure.Classify(in)
}

type classRow struct {
	status    string
	reason    string
	exit      *int
	harness   string
	execState string
	details   json.RawMessage
}

func (s *Server) loadClassRow(ctx context.Context, runID string) (classRow, error) {
	var row classRow
	var details []byte
	err := s.db.QueryRow(ctx, `
		SELECT
		    r.status,
		    COALESCE(r.failure_reason, ''),
		    r.exit_code,
		    COALESCE((
		        SELECT e.payload_json->>'status'
		        FROM run_events e
		        WHERE e.run_id = r.id AND e.type IN ('terminal', 'run.terminal')
		        ORDER BY e.seq DESC
		        LIMIT 1
		    ), ''),
		    COALESCE((
		        SELECT x.state
		        FROM executions x
		        WHERE x.run_id = r.id
		        ORDER BY x.attempt DESC
		        LIMIT 1
		    ), ''),
		    (
		        SELECT x.failure_details_json
		        FROM executions x
		        WHERE x.run_id = r.id
		        ORDER BY x.attempt DESC
		        LIMIT 1
		    )
		FROM runs r
		WHERE r.id = $1`, runID).Scan(&row.status, &row.reason, &row.exit, &row.harness, &row.execState, &details)
	if errors.Is(err, pgx.ErrNoRows) {
		return classRow{}, store.ErrNotFound
	}
	if err != nil {
		return classRow{}, err
	}
	if len(details) > 0 {
		row.details = json.RawMessage(details)
	}
	return row, nil
}

func (s *Server) classInput(ctx context.Context, runID string) (failure.Input, error) {
	row, err := s.loadClassRow(ctx, runID)
	if err != nil {
		return failure.Input{}, err
	}
	in := failure.Input{
		RunStatus:     row.status,
		FailureReason: row.reason,
		ExitCode:      row.exit,
		HarnessStatus: row.harness,
		Details:       row.details,
	}
	if isTerminalStatus(row.status) && workerLostState(row.execState) {
		in.RunStatus = row.execState
	}
	return in, nil
}

func (s *Server) failureInput(ctx context.Context, runID string) (failure.Result, error) {
	in, err := s.classInput(ctx, runID)
	if err != nil {
		return failure.Result{}, err
	}
	return failure.Classify(in), nil
}

// sweepFailureClasses fills failure_class on failed and cancelled runs that
// have none. markRunFailed, the building-guard migration, and a crash between
// status and class all leave that NULL. The sweep is idempotent and uses the
// same inputs as commitFinish. Open and the leader tick both call it.
func (s *Server) sweepFailureClasses(ctx context.Context) error {
	if s.db == nil || s.store == nil {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT id
		FROM runs
		WHERE status IN ('failed', 'cancelled') AND failure_class IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err := s.persistClass(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (s *Server) persistClass(ctx context.Context, runID string) error {
	result, err := s.failureInput(ctx, runID)
	if err != nil {
		return err
	}
	if result.Class == "" {
		return nil
	}
	if err := s.store.SetRunClass(ctx, runID, string(result.Class), result.Code); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		UPDATE run_events
		SET payload_json = jsonb_set(
		    jsonb_set(payload_json, '{failure_class}', to_jsonb($2::text), true),
		    '{failure_code}', to_jsonb($3::text), true)
		WHERE run_id = $1 AND type = 'run.terminal'`,
		runID, string(result.Class), result.Code)
	return err
}

// reclassifyAfterHarness runs when a harness terminal event arrives after the
// run was already classified. The new inputs replace class and code when they
// differ. A repeat of the same event does not write.
func (s *Server) reclassifyAfterHarness(ctx context.Context, runID string, lines [][]byte) {
	if !hasHarnessTerminal(lines) {
		return
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || !isTerminalStatus(run.Status) {
		return
	}
	result, err := s.failureInput(ctx, runID)
	if err != nil {
		s.log.Error("reclassify", "run_id", runID, "err", err)
		return
	}
	if string(result.Class) == run.FailureClass && result.Code == run.FailureCode {
		return
	}
	if err := s.persistClass(ctx, runID); err != nil {
		s.log.Error("reclassify", "run_id", runID, "err", err)
	}
}

func hasHarnessTerminal(lines [][]byte) bool {
	for _, line := range lines {
		var meta struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &meta) != nil {
			continue
		}
		if meta.Type == "terminal" {
			return true
		}
	}
	return false
}

// recordLateExit stores an exit code and details that arrived after the run
// was already cancelled. Status stays cancelled. Classify then sees the exit,
// so 137 is container_killed and 10 is agent_reported.
func (s *Server) recordLateExit(ctx context.Context, runID, execID, reason string, exit *int, details json.RawMessage) {
	if exit == nil && len(details) == 0 && reason == "" {
		return
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	if run.Status != store.StatusCancelled && !run.CancelRequested {
		return
	}
	var detailsArg any
	if len(details) > 0 {
		detailsArg = []byte(details)
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE executions
		SET exit_code = COALESCE($2, exit_code),
		    failure_reason = COALESCE(NULLIF($3, ''), failure_reason),
		    failure_details_json = COALESCE($4::jsonb, failure_details_json)
		WHERE id = $1`, execID, exit, reason, detailsArg); err != nil {
		s.log.Error("late exit", "run_id", runID, "err", err)
		return
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE runs
		SET exit_code = COALESCE($2, exit_code),
		    failure_reason = COALESCE(NULLIF($3, ''), failure_reason)
		WHERE id = $1 AND status = 'cancelled'`, runID, exit, reason); err != nil {
		s.log.Error("late exit", "run_id", runID, "err", err)
		return
	}
	if err := s.persistClass(ctx, runID); err != nil {
		s.log.Error("late exit class", "run_id", runID, "err", err)
	}
}

func classNames(result failure.Result) (string, string) {
	if result.Class == "" {
		return "", ""
	}
	return string(result.Class), result.Code
}

func detailsArg(details json.RawMessage) any {
	if len(details) == 0 {
		return nil
	}
	return []byte(details)
}

// harnessStatusTx reads the newest terminal event visible on tx.
func harnessStatusTx(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(payload_json->>'status', '')
		FROM run_events
		WHERE run_id = $1 AND type IN ('terminal', 'run.terminal')
		ORDER BY seq DESC
		LIMIT 1`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("server: harness status: %w", err)
	}
	return status, nil
}
