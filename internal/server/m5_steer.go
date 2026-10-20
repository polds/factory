package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

// steerTick is how often a poller looks again when no in-process wake
// arrives. Another factoryd instance has no shared wake channel.
const steerTick = 200 * time.Millisecond

// A positive idle timeout is copied onto AgentConfigSnapshot so the worker
// can pass --idle-on-complete. factoryd does not build that argv. A finished
// run still reports "idle window ended" once the run is terminal. idle_until
// below is computed while the run is running and a harness terminal event
// has been stored.

func (s *Server) Interrupt(ctx context.Context, id, actor string) (string, error) {
	return s.enqueueSteer(ctx, id, store.SteerInterrupt, "", actor)
}

func (s *Server) Followup(ctx context.Context, id, actor, text string) (string, error) {
	return s.enqueueSteer(ctx, id, store.SteerFollowup, text, actor)
}

func (s *Server) enqueueSteer(ctx context.Context, id, kind, text, actor string) (string, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", api.ErrNotFound
		}
		return "", err
	}
	if reason := s.steerUnavailable(ctx, run); reason != "" {
		return "", api.ErrSteerUnavailable{Reason: reason}
	}
	st, err := s.store.EnqueueSteer(ctx, id, kind, text, actor)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", api.ErrNotFound
		}
		return "", err
	}
	s.wakeSteers()
	return st.ID, nil
}

func (s *Server) wakeSteers() {
	if s.steerWake == nil {
		return
	}
	select {
	case s.steerWake <- struct{}{}:
	default:
	}
}

// steerUnavailable returns the conflict reason, or "" when a steer can be queued.
// The strings are the API contract.
func (s *Server) steerUnavailable(ctx context.Context, run store.Run) string {
	switch run.Status {
	case store.StatusQueued, store.StatusAssigned:
		return "run has not started"
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
		ended := s.now()
		if run.EndedAt != nil {
			ended = *run.EndedAt
		}
		return "run finished; idle window ended at " + ended.UTC().Format(time.RFC3339)
	case store.StatusRunning:
		if !s.steerCapable(ctx, run.ID) {
			return "worker/harness does not support steering"
		}
		return ""
	default:
		return "run has not started"
	}
}

func (s *Server) steerCapable(ctx context.Context, runID string) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM run_events
			WHERE run_id = $1
			  AND type = 'run.capabilities'
			  AND COALESCE((payload_json->>'steer')::boolean, false)
		)`, runID).Scan(&ok)
	if err != nil {
		return false
	}
	return ok
}

// idleUntil is the harness terminal timestamp plus the agent's
// idleTimeoutMinutes. It is set only while the run is still running.
func (s *Server) idleUntil(ctx context.Context, run store.Run) (time.Time, bool) {
	if run.Status != store.StatusRunning {
		return time.Time{}, false
	}
	var ts time.Time
	err := s.db.QueryRow(ctx, `
		SELECT ts FROM run_events
		WHERE run_id = $1 AND type = 'terminal'
		ORDER BY seq DESC
		LIMIT 1`, run.ID).Scan(&ts)
	if err != nil {
		return time.Time{}, false
	}
	minutes := 0
	snap := s.snapshot()
	if bound, ok := snap.byID[run.AgentID]; ok {
		minutes = s.effectiveIdleMinutes(ctx, run, bound)
	}
	return ts.Add(time.Duration(minutes) * time.Minute), true
}

// effectiveIdleMinutes is the idle timeout assignment() copies onto the
// worker snapshot: the intake automation's timeout when it is positive,
// otherwise the bound agent's.
func (s *Server) effectiveIdleMinutes(ctx context.Context, run store.Run, bound boundAgent) int {
	minutes := bound.agent.IdleTimeoutMinutes
	if auto, ok := s.intakeAutomation(ctx, run.ID); ok && auto.IdleTimeoutMinutes > 0 {
		minutes = auto.IdleTimeoutMinutes
	}
	return minutes
}

// ListSteers is the API-key history. Unknown runs are 404.
// The read does not claim or acknowledge.
func (s *Server) ListSteers(ctx context.Context, runID string) ([]api.SteerRecord, error) {
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, api.ErrNotFound
		}
		return nil, err
	}
	rows, err := s.store.ListSteers(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]api.SteerRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.SteerRecord{
			ID:          row.ID,
			Kind:        row.Kind,
			Text:        row.Text,
			Actor:       row.Actor,
			CreatedAt:   row.CreatedAt.UTC(),
			DeliveredAt: utcPtr(row.DeliveredAt),
		})
	}
	return out, nil
}

func (s *Server) steerState(ctx context.Context, run store.Run) api.SteerState {
	reason := s.steerUnavailable(ctx, run)
	state := api.SteerState{Enabled: reason == "", Reason: reason}
	if until, ok := s.idleUntil(ctx, run); ok {
		state.IdleUntil = &until
	}
	return state
}

// NextSteers long-polls. after acknowledges every undelivered steer of this
// run with id <= after. A terminal run returns immediately and does not claim.
// wait 0 returns the current batch. A claim that the client drops is
// redelivered after store.SteerLease because the claim is a committed lease.
// SteerReleaseRequested acks after, clears unacked leases, and does not claim.
func (s *Server) NextSteers(ctx context.Context, runToken, runID, after string, wait time.Duration) ([]api.Steer, error) {
	if _, ok, err := s.matchToken(ctx, runID, tokenHash(runToken)); err != nil {
		return nil, err
	} else if !ok {
		if _, getErr := s.store.GetRun(ctx, runID); errors.Is(getErr, store.ErrNotFound) {
			return nil, api.ErrNotFound
		}
		return nil, api.ErrUnauthorized
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, api.ErrNotFound
		}
		return nil, err
	}
	if api.SteerReleaseRequested(ctx) {
		if after != "" {
			if err := s.ackSteers(ctx, runID, after); err != nil {
				return nil, err
			}
		}
		if err := s.store.ReleaseSteerClaims(ctx, runID); err != nil {
			return nil, err
		}
		return []api.Steer{}, nil
	}
	if isTerminalStatus(run.Status) {
		return []api.Steer{}, nil
	}
	if after != "" {
		if err := s.ackSteers(ctx, runID, after); err != nil {
			return nil, err
		}
	}
	if contextDone(ctx) {
		return nil, nil
	}
	batch, err := s.claimSteers(ctx, runID, after)
	if err != nil || len(batch) > 0 || wait <= 0 {
		return batch, err
	}
	timer := time.NewTimer(steerTick)
	defer timer.Stop()
	wake := s.steerWake
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-wake:
		case <-timer.C:
		}
		if contextDone(ctx) {
			return nil, nil
		}
		run, err = s.store.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if isTerminalStatus(run.Status) {
			return []api.Steer{}, nil
		}
		batch, err = s.claimSteers(ctx, runID, after)
		if err != nil || len(batch) > 0 {
			return batch, err
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(steerTick)
	}
}

func contextDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (s *Server) claimSteers(ctx context.Context, runID, after string) ([]api.Steer, error) {
	if contextDone(ctx) {
		return nil, nil
	}
	rows, err := s.store.NextSteers(ctx, runID, after, s.now())
	if err != nil {
		return nil, err
	}
	if contextDone(ctx) && len(rows) == 0 {
		return nil, nil
	}
	out := make([]api.Steer, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.Steer{ID: row.ID, Kind: row.Kind, Text: row.Text})
	}
	return out, nil
}

func (s *Server) ackSteers(ctx context.Context, runID, after string) error {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM steer_requests
		WHERE run_id = $1 AND id <= $2 AND delivered_at IS NULL`, runID, after)
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
		if err := s.store.MarkSteerDelivered(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("server: ack steer: %w", err)
		}
	}
	return nil
}
