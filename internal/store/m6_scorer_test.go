package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

func TestScorerReplaceOnDemand(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "scorer-replace", store.PriorityBatch)

	first, err := h.st.UpsertScorerResult(ctx, store.ScorerResult{
		FactoryID:  factoryID,
		RunID:      run.ID,
		ScorerName: "quality",
		Label:      "old",
		Score:      0.2,
		Passed:     false,
		Reasoning:  "first",
		Status:     api.ScorerStatusOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.st.UpsertScorerResult(ctx, store.ScorerResult{
		FactoryID:  factoryID,
		RunID:      run.ID,
		ScorerName: "quality",
		Label:      "new",
		Score:      0.9,
		Passed:     true,
		Reasoning:  "replaced",
		Status:     api.ScorerStatusOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Label != "new" || !second.Passed || second.ReviewedAt != nil {
		t.Fatalf("replaced %+v", second)
	}
	if n := countRows(t, h, `SELECT count(*) FROM scorer_results WHERE run_id = $1 AND scorer_name = 'quality'`, run.ID); n != 1 {
		t.Fatalf("rows %d", n)
	}
	scores, err := h.st.RunScores(ctx, run.ID)
	if err != nil || len(scores) != 1 || scores[0].Label != "new" {
		t.Fatalf("scores %+v %v", scores, err)
	}
}

func TestScorerResultsAndPassRate(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	apr4 := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	apr5 := apr4.Add(24 * time.Hour)
	apr6 := apr5.Add(24 * time.Hour)

	passRun := h.createRun(t, factoryID, agentID, "scorer-pass", store.PriorityBatch)
	failRun := h.createRun(t, factoryID, agentID, "scorer-fail", store.PriorityBatch)
	pendingRun := h.createRun(t, factoryID, agentID, "scorer-pending", store.PriorityBatch)
	invalidRun := h.createRun(t, factoryID, agentID, "scorer-invalid", store.PriorityBatch)

	pass := mustScore(t, h, factoryID, passRun.ID, "quality", "good", true, api.ScorerStatusOK)
	fail := mustScore(t, h, factoryID, failRun.ID, "quality", "bad", false, api.ScorerStatusOK)
	pending := mustScore(t, h, factoryID, pendingRun.ID, "quality", "", false, api.ScorerStatusPending)
	invalid := mustScore(t, h, factoryID, invalidRun.ID, "quality", "", false, api.ScorerStatusInvalid)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, pass.ID, apr4)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, fail.ID, apr4)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, pending.ID, apr4)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, invalid.ID, apr5)

	passed := true
	onlyPass, err := h.st.ScorerResults(ctx, factoryID, "quality", apr4, apr6, &passed)
	if err != nil || len(onlyPass) != 1 || onlyPass[0].ID != pass.ID {
		t.Fatalf("passed %+v %v", onlyPass, err)
	}
	window, err := h.st.ScorerResults(ctx, factoryID, "quality", apr4, apr5, nil)
	if err != nil || len(window) != 3 {
		t.Fatalf("window %+v %v", window, err)
	}

	days, err := h.st.ScorerPassRateByDay(ctx, factoryID, "quality", apr4, apr6)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Day != "2026-04-04" || days[0].N != 2 || days[0].PassRate != 0.5 {
		t.Fatalf("days %+v", days)
	}

	listed, err := h.st.UnreviewedFailures(ctx, factoryID, "implementer")
	if err != nil || len(listed) != 1 || listed[0].ResultID != fail.ID || listed[0].Agent != "implementer" {
		t.Fatalf("unreviewed %+v %v", listed, err)
	}
	other, err := h.st.UnreviewedFailures(ctx, factoryID, "missing")
	if err != nil || len(other) != 0 {
		t.Fatalf("other agent %+v %v", other, err)
	}
}

func mustScore(t *testing.T, h *harness, factory, runID, scorer, label string, passed bool, status string) store.ScorerResult {
	t.Helper()
	row, err := h.st.UpsertScorerResult(context.Background(), store.ScorerResult{
		FactoryID:  factory,
		RunID:      runID,
		ScorerName: scorer,
		Label:      label,
		Passed:     passed,
		Status:     status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}
