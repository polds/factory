package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/events"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

// settleCancel makes a cancel-requested run terminal and records one
// run.terminal. Disconnect and task_rejected must not requeue it.
func (s *Server) settleCancel(ctx context.Context, runID string) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("cancel run", "run_id", runID, "err", err)
		}
		return
	}
	if !run.CancelRequested && !isTerminalStatus(run.Status) {
		return
	}
	if !isTerminalStatus(run.Status) {
		execID, err := s.liveExecution(ctx, runID)
		if err != nil {
			s.log.Error("cancel execution", "run_id", runID, "err", err)
			return
		}
		if execID != "" {
			s.finish(ctx, runID, execID, store.ExecCancelled, "", "", nil, nil)
		} else if err := s.commitBareCancel(ctx, runID); err != nil {
			s.log.Error("mark cancelled", "run_id", runID, "err", err)
			return
		}
		run, err = s.store.GetRun(ctx, runID)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("cancel run", "run_id", runID, "err", err)
			}
			return
		}
	}
	s.stopGrace(runID)
	s.untrackRun(runID)
	s.ensureTerminalEvent(ctx, run)
}

func (s *Server) ensureTerminalEvent(ctx context.Context, run store.Run) {
	if !isTerminalStatus(run.Status) {
		return
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE run_id = $1 AND type = 'run.terminal'`, run.ID).Scan(&n); err != nil {
		s.log.Error("terminal event", "run_id", run.ID, "err", err)
		return
	}
	if n > 0 {
		s.hub.Publish(run.ID)
		return
	}
	execID, err := s.executionForEvent(ctx, run.ID)
	if err != nil {
		s.log.Error("terminal execution", "run_id", run.ID, "err", err)
		return
	}
	if err := s.appendServerEvent(ctx, run.ID, execID, serverEventBase, terminalPayload(s.now(), run.Status, run.FailureReason, "", run.ExitCode)); err != nil {
		s.log.Error("terminal event", "run_id", run.ID, "err", err)
		return
	}
	s.archive(ctx, run.ID, execID)
}

func (s *Server) finish(ctx context.Context, runID, execID, state, reason, summary string, exit *int, taskState *protocol.TaskState) {
	if execID == "" {
		execID = s.execForRun(runID)
	}
	if execID == "" {
		s.log.Error("terminal without execution", "run_id", runID)
		return
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		s.log.Error("terminal run", "run_id", runID, "err", err)
		return
	}
	if run.CancelRequested || (taskState != nil && *taskState == protocol.TaskStateCancelled) {
		state = store.ExecCancelled
	}
	// Status and run.terminal commit together. A reader that sees the terminal
	// status already sees the event; FinishExecution and AppendEvents were
	// separate commits, and a poll could land between them.
	applied, err := s.commitFinish(ctx, runID, execID, state, reason, summary, exit)
	if err != nil {
		s.log.Error("finish execution", "run_id", runID, "execution_id", execID, "err", err)
		return
	}
	s.stopGrace(runID)
	s.untrack(execID)
	if !applied {
		return
	}
	s.clearSuspended(runID)
	s.archive(ctx, runID, execID)
	s.hub.Publish(runID)
	s.advanceAfterTerminal(ctx, runID)
	s.nudge()
}

// commitFinish applies one terminal transition and inserts run.terminal
// before it commits. A second call returns false and does not write another
// event. An execution that is already terminal does not change the run.
func (s *Server) commitFinish(ctx context.Context, runID, execID, state, reason, summary string, exit *int) (bool, error) {
	status, err := runStatus(state)
	if err != nil {
		return false, err
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var gotRun string
	err = tx.QueryRow(ctx, `
		UPDATE executions
		SET state = $2,
		    failure_reason = NULLIF($3, ''),
		    exit_code = $4,
		    failure_details_json = NULL,
		    ended_at = clock_timestamp()
		WHERE id = $1 AND state IN ($5, $6)
		RETURNING run_id`,
		execID, state, reason, exit, store.ExecAssigned, store.ExecRunning,
	).Scan(&gotRun)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM executions WHERE id = $1)`, execID).Scan(&exists)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, store.ErrNotFound
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if gotRun != runID {
		return false, fmt.Errorf("server: execution %s belongs to run %s", execID, gotRun)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2,
		    failure_reason = NULLIF($3, ''),
		    exit_code = $4,
		    ended_at = clock_timestamp()
		WHERE id = $1 AND status IN ($5, $6)`,
		runID, status, reason, exit, store.StatusAssigned, store.StatusRunning,
	); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return false, err
	}
	payload := terminalPayload(s.now(), status, reason, summary, exit)
	if err := s.insertTerminalEvent(ctx, tx, runID, execID, payload); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// cancelRun flags a running run, or cancels a queued or assigned run and
// writes run.terminal in that same transaction.
func (s *Server) cancelRun(ctx context.Context, runID string) (store.Run, error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return store.Run{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Run{}, store.ErrNotFound
	}
	if err != nil {
		return store.Run{}, err
	}
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
		return store.Run{}, store.ErrAlreadyTerminal
	case store.StatusQueued, store.StatusAssigned:
		if _, err := tx.Exec(ctx, `
			UPDATE runs
			SET status = $2, cancel_requested = true, ended_at = clock_timestamp(), worker_id = NULL
			WHERE id = $1`, runID, store.StatusCancelled); err != nil {
			return store.Run{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE executions
			SET state = $2, ended_at = clock_timestamp()
			WHERE run_id = $1 AND state IN ($3, $4)`,
			runID, store.ExecCancelled, store.ExecAssigned, store.ExecRunning); err != nil {
			return store.Run{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
			return store.Run{}, err
		}
		execID, err := executionInTx(ctx, tx, runID)
		if err != nil {
			return store.Run{}, err
		}
		payload := terminalPayload(s.now(), store.StatusCancelled, "", "", nil)
		if err := s.insertTerminalEvent(ctx, tx, runID, execID, payload); err != nil {
			return store.Run{}, err
		}
	case store.StatusRunning:
		if _, err := tx.Exec(ctx, `UPDATE runs SET cancel_requested = true WHERE id = $1`, runID); err != nil {
			return store.Run{}, err
		}
	default:
		return store.Run{}, fmt.Errorf("server: cancel status %q", status)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Run{}, err
	}
	return s.store.GetRun(ctx, runID)
}

// commitBareCancel cancels a run that has no live execution, and writes
// run.terminal before that status is visible.
func (s *Server) commitBareCancel(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = 'cancelled',
		    cancel_requested = true,
		    ended_at = COALESCE(ended_at, clock_timestamp()),
		    worker_id = NULL
		WHERE id = $1 AND status NOT IN ('succeeded', 'failed', 'cancelled')`, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return err
	}
	execID, err := executionInTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	payload := terminalPayload(s.now(), store.StatusCancelled, "", "", nil)
	if err := s.insertTerminalEvent(ctx, tx, runID, execID, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func executionInTx(ctx context.Context, tx pgx.Tx, runID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
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
	var sum [32]byte
	tag, err := tx.Exec(ctx, `
		INSERT INTO executions (id, run_id, attempt, worker_id, state, run_token_sha256, ended_at)
		VALUES ($1, $2, 1, '', 'cancelled', $3, clock_timestamp())
		ON CONFLICT (run_id, attempt) DO NOTHING`, id, runID, sum[:])
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return id, nil
	}
	err = tx.QueryRow(ctx, `SELECT id FROM executions WHERE run_id = $1 AND attempt = 1`, runID).Scan(&id)
	return id, err
}

// insertTerminalEvent writes run.terminal on tx. The caller has already
// updated the run status on tx and has not committed. The test hook runs
// in that window: other connections still see the previous status.
func (s *Server) insertTerminalEvent(ctx context.Context, tx pgx.Tx, runID, execID string, payload []byte) error {
	s.mu.Lock()
	pause := s.beforeTerminalEvent
	s.mu.Unlock()
	if pause != nil {
		pause()
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, "events:"+runID); err != nil {
		return fmt.Errorf("server: terminal event: %w", err)
	}
	var owner string
	err := tx.QueryRow(ctx, `SELECT run_id FROM executions WHERE id = $1`, execID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("server: terminal event: %w", store.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("server: terminal event: %w", err)
	}
	if owner != runID {
		return fmt.Errorf("server: execution %s belongs to run %s", execID, owner)
	}
	typ, ts, err := terminalMeta(payload)
	if err != nil {
		return err
	}
	var seq int64
	err = tx.QueryRow(ctx, `
		SELECT seq FROM run_events WHERE execution_id = $1 AND idx = $2`, execID, serverEventBase).Scan(&seq)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("server: terminal event: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
			VALUES (
			    $1,
			    (SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = $1),
			    $2, $3, $4, $5, $6::jsonb
			)
			RETURNING seq`,
			runID, execID, serverEventBase, ts, typ, payload).Scan(&seq)
		if err != nil {
			return fmt.Errorf("server: terminal event: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('run_events', $1)`, runID); err != nil {
		return fmt.Errorf("server: terminal event: %w", err)
	}
	return nil
}

func terminalMeta(payload []byte) (string, time.Time, error) {
	var meta struct {
		Type string `json:"type"`
		TS   string `json:"ts"`
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return "", time.Time{}, fmt.Errorf("server: terminal event: %w", err)
	}
	if meta.Type == "" {
		return "", time.Time{}, errors.New("server: terminal event type is required")
	}
	ts, err := time.Parse(time.RFC3339Nano, meta.TS)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("server: terminal event ts: %w", err)
	}
	return meta.Type, ts, nil
}

func terminalPayload(now time.Time, status, reason, summary string, exit *int) []byte {
	payload := map[string]any{
		"type":   "run.terminal",
		"ts":     now.UTC().Format(time.RFC3339Nano),
		"status": status,
	}
	if summary != "" {
		payload["summary"] = summary
	}
	if reason != "" {
		payload["failure_reason"] = reason
	}
	if exit != nil {
		payload["exit_code"] = *exit
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"type":"run.terminal","status":"` + status + `"}`)
	}
	return raw
}

func runStatus(state string) (string, error) {
	switch state {
	case store.ExecCompleted:
		return store.StatusSucceeded, nil
	case store.ExecFailed:
		return store.StatusFailed, nil
	case store.ExecCancelled:
		return store.StatusCancelled, nil
	default:
		return "", fmt.Errorf("server: terminal state %q", state)
	}
}

func (s *Server) appendServerEvent(ctx context.Context, runID, execID string, idx int, payload []byte) error {
	_, err := s.store.AppendEvents(ctx, runID, execID, idx, []json.RawMessage{payload})
	if err != nil {
		return err
	}
	s.hub.Publish(runID)
	return nil
}

func (s *Server) archive(ctx context.Context, runID, execID string) {
	if strings.TrimSpace(s.cfg.S3.Endpoint) == "" {
		return
	}
	evs, err := s.allEvents(ctx, runID)
	if err != nil {
		s.markUnavailable(ctx, runID, execID)
		return
	}
	var buf bytes.Buffer
	for _, ev := range evs {
		if ev.ExecutionID != execID {
			continue
		}
		buf.Write(ev.Payload)
		if !bytes.HasSuffix(ev.Payload, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	key := "runs/" + runID + "/" + execID + "/events.ndjson"
	// One Put. The client retries HTTP 5xx itself. A transport error returns
	// immediately and is a hard failure: the run stays terminal.
	if err := s.cfg.S3.Put(ctx, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), "application/x-ndjson"); err != nil {
		s.log.Error("archive events", "run_id", runID, "execution_id", execID, "err", err)
		s.markUnavailable(ctx, runID, execID)
	}
}

func (s *Server) markUnavailable(ctx context.Context, runID, execID string) {
	payload, err := json.Marshal(map[string]string{
		"type": "transcript_unavailable",
		"ts":   s.now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return
	}
	if err := s.appendServerEvent(ctx, runID, execID, serverEventBase+1, payload); err != nil {
		s.log.Error("transcript_unavailable", "run_id", runID, "err", err)
	}
}

func (s *Server) allEvents(ctx context.Context, runID string) ([]store.Event, error) {
	var all []store.Event
	var from int64
	for {
		page, err := s.store.EventsFrom(ctx, runID, from, 500)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		from = page[len(page)-1].Seq
		if len(page) < 500 {
			return all, nil
		}
	}
}

// logSource is the events.Source adapter. It is a separate type because
// gateway.Sessions and events.Source both require a method named Terminal
// with different signatures, and a Go type can declare that name once.
type logSource struct {
	s *Server
}

func (src logSource) EventsFrom(ctx context.Context, runID string, fromSeq int64, limit int) ([]events.Event, error) {
	return src.s.eventsFrom(ctx, runID, fromSeq, limit)
}

func (src logSource) Terminal(ctx context.Context, runID string) (bool, error) {
	return src.s.runTerminal(ctx, runID)
}

func (s *Server) eventsFrom(ctx context.Context, runID string, fromSeq int64, limit int) ([]events.Event, error) {
	if limit <= 0 {
		limit = 256
	}
	rows, err := s.store.EventsFrom(ctx, runID, fromSeq, limit)
	if err != nil {
		return nil, err
	}
	out := make([]events.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, events.Event{
			Seq:  row.Seq,
			Type: sseType(row.Type),
			Data: row.Payload,
		})
	}
	return out, nil
}

func (s *Server) runTerminal(ctx context.Context, runID string) (bool, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	return isTerminalStatus(run.Status), nil
}

func isTerminalStatus(status string) bool {
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
		return true
	default:
		return false
	}
}

func sseType(typ string) string {
	typ = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, typ)
	if typ == "" {
		return "message"
	}
	return typ
}

func (s *Server) failConfig(ctx context.Context, runID, workerID string, cause error) {
	s.log.Error("assignment config", "run_id", runID, "err", cause)
	sum := tokenHash("config-invalid")
	exec, err := s.store.StartExecution(ctx, runID, workerID, sum)
	if err != nil {
		s.log.Error("config execution", "run_id", runID, "err", err)
		if relErr := s.releaseClaim(ctx, runID); relErr != nil {
			s.log.Error("release claim", "run_id", runID, "err", relErr)
		}
		return
	}
	s.finish(ctx, runID, exec.ID, store.ExecFailed, "config_invalid", cause.Error(), nil, nil)
}
