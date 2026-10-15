package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/polds/factory/internal/gateway"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

type beat struct {
	workerID string
	at       time.Time
}

type execRec struct {
	runID    string
	execID   string
	workerID string
}

func (s *Server) WorkerConnected(ctx context.Context, w gateway.WorkerInfo) error {
	if err := s.store.UpsertWorker(ctx, store.Worker{
		ID:              w.WorkerID,
		Pool:            s.cfg.WorkerPool,
		Platform:        s.cfg.WorkerPlatform,
		LabelsJSON:      json.RawMessage(`{}`),
		LastHeartbeatAt: s.now(),
		MaxConcurrent:   s.cfg.WorkerMaxConcurrent,
		Status:          "connected",
	}); err != nil {
		return err
	}
	s.mu.Lock()
	if _, ok := s.idleSince[w.WorkerID]; !ok {
		s.idleSince[w.WorkerID] = s.now()
	}
	s.mu.Unlock()
	s.nudge()
	return nil
}

// WorkerSeen returns without waiting on the database. The gateway queues the
// pong only after this returns, so a slow TouchWorker must not run here.
func (s *Server) WorkerSeen(_ context.Context, workerID string, at time.Time) {
	select {
	case s.pings <- beat{workerID: workerID, at: at}:
	default:
	}
}

func (s *Server) Claimed(_ context.Context, m protocol.TaskClaimed) error {
	s.log.Info("claimed", "task_id", m.TaskID, "worker_id", m.WorkerID)
	return nil
}

func (s *Server) Terminal(ctx context.Context, workerID string, m any) error {
	switch msg := m.(type) {
	case protocol.TaskCompleted:
		s.log.Info("task_completed", "task_id", msg.TaskID, "worker_id", workerID, "execution_id", msg.ExecutionID)
		s.finish(ctx, msg.TaskID, msg.ExecutionID, store.ExecCompleted, "", msg.Message, nil, msg.TaskState)
	case protocol.TaskFailed:
		s.log.Info("task_failed", "task_id", msg.TaskID, "worker_id", workerID, "execution_id", msg.ExecutionID, "failure_reason", msg.FailureReason)
		code := msg.ExitCode
		s.finish(ctx, msg.TaskID, msg.ExecutionID, store.ExecFailed, msg.FailureReason, msg.Message, &code, msg.TaskState)
	default:
		s.log.Info("terminal ignored", "worker_id", workerID)
	}
	return nil
}

func (s *Server) Rejected(ctx context.Context, m protocol.TaskRejected) error {
	s.countersMu.Lock()
	s.counters.RecordReject()
	s.countersMu.Unlock()
	run, err := s.store.GetRun(ctx, m.TaskID)
	if err != nil {
		s.log.Error("reject run", "task_id", m.TaskID, "err", err)
		return err
	}
	if run.CancelRequested || isTerminalStatus(run.Status) {
		s.settleCancel(ctx, m.TaskID)
		return nil
	}
	if s.runSuspended(ctx, m.TaskID) {
		if err := s.parkSuspendedRun(ctx, m.TaskID); err != nil {
			s.log.Error("reject suspended run", "task_id", m.TaskID, "err", err)
			return err
		}
		s.releaseWorkerSlot(m.TaskID)
		s.untrackRun(m.TaskID)
		s.log.Info("reject leaves suspended run", "task_id", m.TaskID)
		return nil
	}
	if err := s.prepareRequeue(ctx, m.TaskID); err != nil {
		s.log.Error("prepare requeue", "task_id", m.TaskID, "err", err)
		return err
	}
	reason := strings.TrimSpace(m.Reason)
	if reason == "" {
		reason = "task_rejected"
	}
	if err := s.store.Requeue(ctx, m.TaskID, reason); err != nil {
		s.log.Error("requeue", "task_id", m.TaskID, "err", err)
		return err
	}
	s.untrackRun(m.TaskID)
	s.log.Info("requeued", "task_id", m.TaskID)
	s.nudge()
	return nil
}

func (s *Server) WorkerGone(ctx context.Context, connID string) {
	alive := map[string]bool{}
	for _, info := range s.gw.Connected() {
		alive[info.WorkerID] = true
	}
	s.mu.Lock()
	var runs []string
	seen := map[string]struct{}{}
	for _, rec := range s.live {
		if alive[rec.workerID] {
			continue
		}
		if _, ok := seen[rec.runID]; ok {
			continue
		}
		seen[rec.runID] = struct{}{}
		runs = append(runs, rec.runID)
	}
	s.mu.Unlock()
	requeued := false
	for _, id := range runs {
		run, err := s.store.GetRun(ctx, id)
		if err != nil {
			s.log.Error("disconnect run", "run_id", id, "conn_id", connID, "err", err)
			continue
		}
		if run.CancelRequested || isTerminalStatus(run.Status) {
			s.settleCancel(ctx, id)
			continue
		}
		if s.runSuspended(ctx, id) {
			if err := s.parkSuspendedRun(ctx, id); err != nil {
				s.log.Error("disconnect suspended run", "run_id", id, "conn_id", connID, "err", err)
				continue
			}
			s.releaseWorkerSlot(id)
			s.untrackRun(id)
			s.log.Info("disconnect leaves suspended run", "run_id", id, "conn_id", connID)
			continue
		}
		if err := s.forceExpire(ctx, id); err != nil {
			s.log.Error("expire on disconnect", "run_id", id, "conn_id", connID, "err", err)
			continue
		}
		requeued = true
	}
	if requeued {
		s.nudge()
	}
}

func (s *Server) track(rec execRec) {
	s.mu.Lock()
	s.live[rec.execID] = rec
	s.byRun[rec.runID] = rec.execID
	s.mu.Unlock()
}

func (s *Server) tracked(execID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.live[execID]
	return ok
}

func (s *Server) untrack(execID string) {
	s.mu.Lock()
	if rec, ok := s.live[execID]; ok {
		if s.byRun[rec.runID] == execID {
			delete(s.byRun, rec.runID)
		}
		delete(s.live, execID)
	}
	s.mu.Unlock()
}

func (s *Server) untrackRun(runID string) {
	s.mu.Lock()
	if execID, ok := s.byRun[runID]; ok {
		delete(s.live, execID)
		delete(s.byRun, runID)
	}
	s.mu.Unlock()
}

func (s *Server) execForRun(runID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byRun[runID]
}

func (s *Server) workerForRun(runID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	execID := s.byRun[runID]
	return s.live[execID].workerID
}

func (s *Server) runsForWorker(workerID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, rec := range s.live {
		if rec.workerID == workerID {
			ids = append(ids, rec.runID)
		}
	}
	return ids
}
