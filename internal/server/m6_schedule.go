package server

import (
	"context"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

// scheduleBound resolves the agent the scheduler should account, then falls
// back for measurement runs whose agent is not in the production snapshot.
// Judge runs have an empty agent id. Candidate benchmark trials may name an
// agent that exists only at another definition SHA. Option (a) keeps those
// runs assignable. The agent id on the run stays empty for judges, so metrics
// do not count them under the scored agent.
func (s *Server) scheduleBound(ctx context.Context, snap loaded, run store.Run) (boundAgent, bool) {
	if bound, ok := s.resolveBound(ctx, snap, run.AgentID); ok {
		return bound, true
	}
	if !measurementPurpose(run.Purpose) {
		return boundAgent{}, false
	}
	return s.measurementFallback(snap), true
}

func measurementPurpose(purpose string) bool {
	switch purpose {
	case api.PurposeScorer, api.PurposeBenchmark, api.PurposeImprovement:
		return true
	default:
		return false
	}
}

// measurementFallback is the runner and pool used when a measurement run
// cannot resolve an agent. Image and OS come from scorerDefaults.runner.
// The pool is the process worker pool.
func (s *Server) measurementFallback(snap loaded) boundAgent {
	var runner definition.Runner
	if snap.tree != nil && snap.tree.Factory.ScorerDefaults != nil {
		if row, ok := snap.tree.Runners[snap.tree.Factory.ScorerDefaults.Runner]; ok {
			runner = row
		}
	}
	return boundAgent{
		runner: runner,
		agent:  definition.Agent{Pool: s.cfg.WorkerPool},
	}
}
