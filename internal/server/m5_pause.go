package server

import (
	"context"

	"github.com/polds/factory/internal/automation"
)

// pausingAutomations strips the run from a paused automation's intake slot.
// The slot is still recorded with an empty run_id, so match_count moves and
// runs_started does not. The pause flag lives in automation_overrides and
// survives ApplyDefinition.
type pausingAutomations struct {
	automation.Store
	paused func(ctx context.Context, name string) bool
}

func (p pausingAutomations) EnsureIntake(ctx context.Context, workItemID, deliveryID string, slots []automation.IntakeSlot) (automation.IntakeResult, error) {
	next := make([]automation.IntakeSlot, len(slots))
	copy(next, slots)
	if p.paused != nil {
		for i := range next {
			if next[i].Run != nil && p.paused(ctx, next[i].Automation) {
				next[i].Run = nil
			}
		}
	}
	return p.Store.EnsureIntake(ctx, workItemID, deliveryID, next)
}

func (s *Server) automationPaused(ctx context.Context, name string) bool {
	fac := s.snapshot().factory
	if fac.ID == "" || name == "" || s.store == nil {
		return false
	}
	stats, err := s.store.AutomationStats(ctx, fac.ID)
	if err != nil {
		s.log.Error("automation pause", "automation", name, "err", err)
		return false
	}
	for _, st := range stats {
		if st.Name == name {
			return st.RuntimePaused
		}
	}
	return false
}

func (s *Server) notePaused(ctx context.Context, res automation.Result) {
	for _, outcome := range res.Outcomes {
		if outcome.Automation == "" || outcome.RunID != "" {
			continue
		}
		if s.automationPaused(ctx, outcome.Automation) {
			s.log.Info("automation dispatch", "reason", "paused", "automation", outcome.Automation, "work_item_id", outcome.WorkItemID)
		}
	}
}
