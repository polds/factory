package server

import (
	"context"
	"time"

	"github.com/polds/factory/internal/scheduler"
	"github.com/polds/factory/internal/store"
)

func (s *Server) tick(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	if !s.isLeader(ctx) {
		return
	}
	if _, err := s.store.ExpireLeases(ctx, s.now()); err != nil {
		s.log.Error("expire leases", "err", err)
	}
	s.schedule(ctx)
}

func (s *Server) schedule(ctx context.Context) {
	now := s.now()
	queued, err := s.store.ListRuns(ctx, store.RunFilter{Status: store.StatusQueued, Limit: 200})
	if err != nil {
		s.log.Error("list queued", "err", err)
		return
	}
	running, err := s.store.ListRuns(ctx, store.RunFilter{Status: store.StatusRunning, Limit: 200})
	if err != nil {
		s.log.Error("list running", "err", err)
		return
	}
	assigned, err := s.store.ListRuns(ctx, store.RunFilter{Status: store.StatusAssigned, Limit: 200})
	if err != nil {
		s.log.Error("list assigned", "err", err)
		return
	}
	snap := s.snapshot()
	held := s.suspendedSnapshot()
	pending, byID := s.pending(queued, snap)
	var inflight []scheduler.Pending
	// A suspended run keeps its execution and stays out of the worker slot
	// and the factory and agent caps.
	inflight = append(inflight, s.asPending(withoutSuspended(running, held), snap)...)
	inflight = append(inflight, s.asPending(withoutSuspended(assigned, held), snap)...)
	workers := s.candidates(now)
	for _, asg := range scheduler.Plan(now, pending, workers, inflight, defaultCaps()) {
		run, ok := byID[asg.RunID]
		if !ok {
			continue
		}
		bound, ok := snap.byID[run.AgentID]
		if !ok {
			continue
		}
		s.assignOne(ctx, run, asg.WorkerID, bound)
	}
}

func (s *Server) pending(runs []store.Run, snap loaded) ([]scheduler.Pending, map[string]store.Run) {
	out := make([]scheduler.Pending, 0, len(runs))
	byID := make(map[string]store.Run, len(runs))
	for _, run := range runs {
		if run.CancelRequested {
			continue
		}
		item, ok := s.toPending(run, snap)
		if !ok {
			continue
		}
		out = append(out, item)
		byID[run.ID] = run
	}
	return out, byID
}

func (s *Server) asPending(runs []store.Run, snap loaded) []scheduler.Pending {
	out := make([]scheduler.Pending, 0, len(runs))
	for _, run := range runs {
		item, ok := s.toPending(run, snap)
		if ok {
			out = append(out, item)
		}
	}
	return out
}

func (s *Server) toPending(run store.Run, snap loaded) (scheduler.Pending, bool) {
	bound, ok := snap.byID[run.AgentID]
	if !ok {
		return scheduler.Pending{}, false
	}
	pool := bound.agent.Pool
	if pool == "" {
		pool = s.cfg.WorkerPool
	}
	platform := bound.runner.OS
	if platform == "" {
		platform = s.cfg.WorkerPlatform
	}
	return scheduler.Pending{
		RunID:      run.ID,
		FactoryID:  run.FactoryID,
		AgentID:    run.AgentID,
		Pool:       pool,
		Platform:   platform,
		Priority:   scheduler.Class(run.Priority),
		EnqueuedAt: run.CreatedAt,
	}, true
}

func (s *Server) candidates(now time.Time) []scheduler.Candidate {
	type agg struct {
		inflight  int
		last      time.Time
		freshZero bool
	}
	grouped := map[string]*agg{}
	var ids []string
	for _, info := range s.gw.Connected() {
		row := grouped[info.WorkerID]
		if row == nil {
			row = &agg{}
			grouped[info.WorkerID] = row
			ids = append(ids, info.WorkerID)
		}
		row.inflight += info.InFlight
		if info.LastPing.IsZero() {
			row.freshZero = true
			continue
		}
		if row.last.IsZero() || info.LastPing.After(row.last) {
			row.last = info.LastPing
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scheduler.Candidate, 0, len(ids))
	for _, id := range ids {
		row := grouped[id]
		hb := row.last
		// A connection that has not pinged yet is fresh. The gateway leaves
		// LastPing zero until the first ping, which would otherwise look stale.
		if row.freshZero || hb.IsZero() {
			hb = now
		}
		idle := s.idleSince[id]
		if idle.IsZero() {
			idle = now
			s.idleSince[id] = idle
		}
		inflight := row.inflight
		for _, rec := range s.live {
			if rec.workerID != id {
				continue
			}
			if _, ok := s.suspended[rec.runID]; ok {
				inflight--
			}
		}
		if inflight < 0 {
			inflight = 0
		}
		out = append(out, scheduler.Candidate{
			WorkerID:      id,
			Pool:          s.cfg.WorkerPool,
			Platform:      s.cfg.WorkerPlatform,
			LastHeartbeat: hb,
			IdleSince:     idle,
			InFlight:      inflight,
			MaxConcurrent: s.cfg.WorkerMaxConcurrent,
		})
	}
	return out
}

func (s *Server) assignOne(ctx context.Context, run store.Run, workerID string, bound boundAgent) {
	ok, err := s.claim(ctx, run.ID, s.cfg.LeaseTTL)
	if err != nil {
		s.log.Error("claim", "run_id", run.ID, "err", err)
		return
	}
	if !ok {
		return
	}
	bound = s.boundForRun(run, bound)
	env, err := s.taskEnv(ctx, run, bound)
	if err != nil {
		s.failConfig(ctx, run.ID, workerID, err)
		return
	}
	exec, err := s.store.StartExecution(ctx, run.ID, workerID, tokenHash(env["FACTORY_RUN_TOKEN"]))
	if err != nil {
		s.log.Error("start execution", "run_id", run.ID, "err", err)
		if relErr := s.releaseClaim(ctx, run.ID); relErr != nil {
			s.log.Error("release claim", "run_id", run.ID, "err", relErr)
		}
		return
	}
	asg := s.assignment(run, exec, bound, env)
	s.track(execRec{runID: run.ID, execID: exec.ID, workerID: workerID})
	if err := s.gw.Assign(ctx, workerID, asg); err != nil {
		if s.tracked(exec.ID) {
			if rewindErr := s.rewind(ctx, run.ID, exec.ID); rewindErr != nil {
				s.log.Error("rewind", "run_id", run.ID, "err", rewindErr)
			}
			s.untrack(exec.ID)
		}
		s.log.Error("assign", "run_id", run.ID, "worker_id", workerID, "err", err)
		return
	}
	wait := s.now().Sub(run.CreatedAt)
	s.countersMu.Lock()
	s.counters.RecordAssignment(wait)
	s.countersMu.Unlock()
	s.mu.Lock()
	s.idleSince[workerID] = s.now()
	s.mu.Unlock()
}

func (s *Server) pingLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case beat := <-s.pings:
			if err := s.store.TouchWorker(s.ctx, beat.workerID, beat.at); err != nil && s.ctx.Err() == nil {
				s.log.Debug("touch worker", "worker_id", beat.workerID, "err", err)
			}
			for _, runID := range s.runsForWorker(beat.workerID) {
				if err := s.renewLease(s.ctx, runID, s.cfg.LeaseTTL); err != nil && s.ctx.Err() == nil {
					s.log.Error("renew lease", "run_id", runID, "err", err)
				}
			}
		}
	}
}
