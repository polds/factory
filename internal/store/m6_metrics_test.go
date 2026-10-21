package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

func TestMetricsGolden(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)

	const dayMS = int64(86400000)
	day := 24 * time.Hour
	mar1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mar10 := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	mar15 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	mar18 := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)
	mar20 := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	mar22 := time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC)
	apr1 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	apr2 := apr1.Add(day)
	apr3 := apr2.Add(day)
	apr4 := apr3.Add(day)
	apr5 := apr4.Add(day)
	apr6 := apr5.Add(day)
	apr8 := apr6.Add(2 * day)
	repo := "acme/app"

	itemA, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID, Source: store.SourceIssue, SourceRef: "acme/app#1", Title: "A",
	})
	if err != nil {
		t.Fatal(err)
	}
	itemB, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID, Source: store.SourceAPI, SourceRef: "api-b", Title: "B",
	})
	if err != nil {
		t.Fatal(err)
	}
	stampTime(t, h, `UPDATE work_items SET created_at = $2 WHERE id = $1`, itemA.ID, apr1)
	stampTime(t, h, `UPDATE work_items SET created_at = $2 WHERE id = $1`, itemB.ID, mar18)

	old := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-old", ModelID: "m-old", Purpose: api.PurposeWork,
	}, mar1)
	stampRunStatus(t, h, old.ID, store.StatusSucceeded)
	parent := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-parent", WorkItemID: itemA.ID, ModelID: "m-parent", Purpose: api.PurposeWork,
	}, apr2)
	addUsage(t, h, parent, 100)
	stampRunStatus(t, h, parent.ID, store.StatusSucceeded)
	child := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-child", ParentRunID: parent.ID, ModelID: "m-child", Purpose: api.PurposeWork,
	}, apr3)
	addUsage(t, h, child, 50)
	stampRunStatus(t, h, child.ID, store.StatusSucceeded)
	runB := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-b", WorkItemID: itemB.ID, ModelID: "m-b", Purpose: api.PurposeWork,
	}, apr4)
	stampRunStatus(t, h, runB.ID, store.StatusFailed)
	scorerRun := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-scorer", ModelID: "m-scorer", Purpose: api.PurposeScorer,
	}, apr4)
	stampRunStatus(t, h, scorerRun.ID, store.StatusSucceeded)
	benchRun := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-bench", ModelID: "m-bench", Purpose: api.PurposeBenchmark,
	}, apr5)
	stampRunStatus(t, h, benchRun.ID, store.StatusSucceeded)
	improveRun := metricRun(t, h, factoryID, agentID, store.NewRun{
		Pool: "metrics-improve", ModelID: "m-improve", Purpose: api.PurposeImprovement,
	}, apr6)
	stampRunStatus(t, h, improveRun.ID, store.StatusSucceeded)

	pass := mustScore(t, h, factoryID, parent.ID, "quality", "good", true, api.ScorerStatusOK)
	fail := mustScore(t, h, factoryID, child.ID, "quality", "bad", false, api.ScorerStatusOK)
	pending := mustScore(t, h, factoryID, runB.ID, "quality", "", false, api.ScorerStatusPending)
	invalid := mustScore(t, h, factoryID, scorerRun.ID, "quality", "", false, api.ScorerStatusInvalid)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, pass.ID, apr4)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, fail.ID, apr4)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, pending.ID, apr4)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, invalid.ID, apr4)

	marchFail := mustScore(t, h, factoryID, old.ID, "regress", "old", false, api.ScorerStatusOK)
	marchFail2 := mustScore(t, h, factoryID, old.ID, "drift", "old", false, api.ScorerStatusOK)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, marchFail.ID, mar10)
	stampTime(t, h, `UPDATE scorer_results SET created_at = $2 WHERE id = $1`, marchFail2.ID, mar10)

	mustPR(t, h, factoryID, repo, 1, itemA.ID, apr2, apr5, apr3, false, 50)
	mustPR(t, h, factoryID, repo, 2, itemA.ID, apr3, apr6, apr4, true, 200)
	mustPR(t, h, factoryID, repo, 3, itemB.ID, mar20, apr4, mar22, false, 600)

	aprilBatch := mustBatchPR(t, h, factoryID, "implementer", marchFail.ID, "https://github.com/acme/app/pull/91", 91)
	marchBatch := mustBatchPR(t, h, factoryID, "editor", marchFail2.ID, "https://github.com/acme/app/pull/90", 90)
	stampTime(t, h, `UPDATE improvement_batches SET created_at = $2 WHERE id = $1`, aprilBatch, apr6)
	stampTime(t, h, `UPDATE improvement_batches SET created_at = $2 WHERE id = $1`, marchBatch, mar15)

	got, err := h.st.Metrics(ctx, factoryID, apr1, apr8)
	if err != nil {
		t.Fatal(err)
	}
	if named(t, got.TotalRuns.ByAgent, "implementer") != 6 {
		t.Fatalf("agents %+v", got.TotalRuns.ByAgent)
	}
	if named(t, got.TotalRuns.ByStatus, store.StatusSucceeded) != 5 || named(t, got.TotalRuns.ByStatus, store.StatusFailed) != 1 {
		t.Fatalf("status %+v", got.TotalRuns.ByStatus)
	}
	if named(t, got.TotalRuns.BySource, "") != 4 || named(t, got.TotalRuns.BySource, store.SourceAPI) != 1 || named(t, got.TotalRuns.BySource, store.SourceIssue) != 1 {
		t.Fatalf("source %+v", got.TotalRuns.BySource)
	}
	if len(got.TotalRuns.ByModel) != 6 || got.TotalRuns.Measurement != 3 {
		t.Fatalf("models %+v measurement %d", got.TotalRuns.ByModel, got.TotalRuns.Measurement)
	}
	if got.PRsOpened != 2 || got.PRsMerged != 3 {
		t.Fatalf("prs opened %d merged %d", got.PRsOpened, got.PRsMerged)
	}
	if got.Autonomy.N != 3 || got.Autonomy.Share != 2.0/3.0 || got.Autonomy.ForgeConnectedAt != nil {
		t.Fatalf("autonomy %+v", got.Autonomy)
	}
	if got.CycleTime.P50MS != 5*dayMS {
		t.Fatalf("p50 %d", got.CycleTime.P50MS)
	}
	wantStages := map[string]int64{
		"kickoff_to_pr":         2 * dayMS,
		"pr_to_first_review":    dayMS,
		"first_review_to_merge": 2 * dayMS,
	}
	if len(got.CycleTime.Stages) != len(wantStages) {
		t.Fatalf("stages %+v", got.CycleTime.Stages)
	}
	for _, stage := range got.CycleTime.Stages {
		if stage.P50MS != wantStages[stage.Name] {
			t.Fatalf("stage %+v", stage)
		}
	}
	if got.CostPerPR.MedianCents != 75 || got.CostPerPR.BySize.S != 75 || got.CostPerPR.BySize.M != 75 || got.CostPerPR.BySize.L != 0 || got.CostPerPR.BySize.XL != 0 {
		t.Fatalf("cost %+v", got.CostPerPR)
	}
	if len(got.MostExpensivePRs) != 2 {
		t.Fatalf("expensive %+v", got.MostExpensivePRs)
	}
	for i, pr := range got.MostExpensivePRs {
		number := i + 1
		lines := 50
		if number == 2 {
			lines = 200
		}
		if pr.Repo != repo || pr.Number != number || pr.CostCents != 75 || pr.ChangedLines != lines || pr.URL != fmt.Sprintf("https://github.com/%s/pull/%d", repo, number) {
			t.Fatalf("expensive %d %+v", i, pr)
		}
	}
	if len(got.ScorerCards) != 1 || got.ScorerCards[0].Name != "quality" || len(got.ScorerCards[0].PassRateByDay) != 1 {
		t.Fatalf("cards %+v", got.ScorerCards)
	}
	card := got.ScorerCards[0].PassRateByDay[0]
	if card.Day != "2026-04-04" || card.N != 2 || card.PassRate != 0.5 {
		t.Fatalf("card %+v", card)
	}
	if len(got.ImprovementPRs) != 2 {
		t.Fatalf("improvement %+v", got.ImprovementPRs)
	}
	if got.ImprovementPRs[0].BatchID != aprilBatch || !got.ImprovementPRs[0].CreatedAt.Equal(apr6) || got.ImprovementPRs[0].URL != "https://github.com/acme/app/pull/91" {
		t.Fatalf("newest %+v", got.ImprovementPRs[0])
	}
	if got.ImprovementPRs[1].BatchID != marchBatch || !got.ImprovementPRs[1].CreatedAt.Equal(mar15) || got.ImprovementPRs[1].Agent != "editor" {
		t.Fatalf("outside %+v", got.ImprovementPRs[1])
	}
}

func metricRun(t *testing.T, h *harness, factoryID, agentID string, in store.NewRun, at time.Time) store.Run {
	t.Helper()
	in.FactoryID = factoryID
	in.AgentID = agentID
	if in.Priority == 0 {
		in.Priority = store.PriorityBatch
	}
	if in.Prompt == "" {
		in.Prompt = "work"
	}
	if in.Title == "" {
		in.Title = "work"
	}
	run, err := h.st.CreateRun(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	stampTime(t, h, `UPDATE runs SET created_at = $2 WHERE id = $1`, run.ID, at)
	return run
}

func stampRunStatus(t *testing.T, h *harness, id, status string) {
	t.Helper()
	stampTime(t, h, `UPDATE runs SET status = $2 WHERE id = $1`, id, status)
}

func addUsage(t *testing.T, h *harness, run store.Run, cents int) {
	t.Helper()
	exec := leaseAndStart(t, h, run)
	raw := json.RawMessage(fmt.Sprintf(`{"type":"usage","cost_cents":%d}`, cents))
	if _, err := h.st.AppendEvents(context.Background(), run.ID, exec.ID, 0, []json.RawMessage{raw}); err != nil {
		t.Fatal(err)
	}
}

func mustPR(t *testing.T, h *harness, factory, repo string, number int, workItem string, opened, merged, review time.Time, human bool, lines int) {
	t.Helper()
	_, err := h.st.UpsertPRFact(context.Background(), store.PRFact{
		FactoryID:     factory,
		Repo:          repo,
		Number:        number,
		WorkItemID:    workItem,
		OpenedAt:      &opened,
		MergedAt:      &merged,
		FirstReviewAt: &review,
		HumanPushSeen: &human,
		ChangedLines:  &lines,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustBatchPR(t *testing.T, h *harness, factory, agent, failureID, url string, number int) string {
	t.Helper()
	batch, err := h.st.CreateBatch(context.Background(), store.NewImprovementBatch{
		FactoryID:  factory,
		Agent:      agent,
		FailureIDs: []string{failureID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetBatchPR(context.Background(), batch.ID, url, number, ""); err != nil {
		t.Fatal(err)
	}
	return batch.ID
}

func named(t *testing.T, rows []api.NamedCount, name string) int {
	t.Helper()
	for _, row := range rows {
		if row.Name == name {
			return row.Count
		}
	}
	t.Fatalf("missing %q in %+v", name, rows)
	return 0
}
