package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/store"
)

func TestPurposeMigrationOnM5(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	applyBefore0010(t, h)

	if _, err := h.raw.Exec(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_m5', 'm5rows', 'v1alpha1', 'git')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool, prompt, stage)
		VALUES ('run_m5', 'fac_m5', 'running', 1, 'default', 'kept', 'building')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO executions (id, run_id, attempt, worker_id, state, run_token_sha256)
		VALUES ('exec_m5', 'run_m5', 1, 'worker-a', 'running', $1)`, tokenHash("m5")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
		VALUES ('run_m5', 1, 'exec_m5', 0, clock_timestamp(), 'message.delta', '{"type":"message.delta","text":"kept"}')`); err != nil {
		t.Fatal(err)
	}
	before := countRows(t, h, `SELECT count(*) FROM run_lifecycle_events`)
	if before < 1 {
		t.Fatalf("lifecycle rows before 0010: %d", before)
	}

	body, err := store.MigrationFS.ReadFile("migrations/0010_m6.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(body)
	if strings.Contains(strings.ToLower(sqlText), "update runs") {
		t.Fatal("0010 updates runs")
	}
	for _, want := range []string{
		api.PurposeWork, api.PurposeScorer, api.PurposeBenchmark, api.PurposeImprovement,
		api.ScorerStatusOK, api.ScorerStatusInvalid, api.ScorerStatusPending,
		api.BatchQueued, api.BatchRunning, api.BatchPROpen, api.BatchMerged, api.BatchClosed, api.BatchFailed,
		api.TrialPass, api.TrialFail, api.TrialErrored, api.TrialPending,
	} {
		if !strings.Contains(sqlText, "'"+want+"'") {
			t.Fatalf("0010 missing %s", want)
		}
	}
	if err := h.st.ApplySQLForTest(ctx, sqlText); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, sqlText); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	var purpose, status string
	if err := h.raw.QueryRow(ctx, `SELECT purpose, status FROM runs WHERE id = 'run_m5'`).Scan(&purpose, &status); err != nil {
		t.Fatal(err)
	}
	if purpose != api.PurposeWork || status != "running" {
		t.Fatalf("old run purpose %s status %s", purpose, status)
	}
	after := countRows(t, h, `SELECT count(*) FROM run_lifecycle_events`)
	if after != before {
		t.Fatalf("lifecycle rows %d before %d", after, before)
	}
	if n := countRows(t, h, `SELECT count(*) FROM run_events WHERE run_id = 'run_m5'`); n != 1 {
		t.Fatalf("events %d", n)
	}

	if _, err := h.raw.Exec(ctx, `UPDATE runs SET purpose = 'work' WHERE id = 'run_m5'`); err != nil {
		t.Fatalf("same-value purpose update: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE runs SET purpose = 'scorer' WHERE id = 'run_m5'`,
		`UPDATE runs SET purpose = '' WHERE id = 'run_m5'`,
		`UPDATE runs SET purpose = NULL WHERE id = 'run_m5'`,
	} {
		if _, err := h.raw.Exec(ctx, stmt); err == nil || !strings.Contains(err.Error(), "runs.purpose is immutable") {
			t.Fatalf("%s err %v", stmt, err)
		}
	}
	if err := h.raw.QueryRow(ctx, `SELECT purpose FROM runs WHERE id = 'run_m5'`).Scan(&purpose); err != nil {
		t.Fatal(err)
	}
	if purpose != api.PurposeWork {
		t.Fatalf("purpose after rejected updates %s", purpose)
	}
}

func TestPurposeOnCreate(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)

	empty := h.createRun(t, factoryID, agentID, "purpose-empty", store.PriorityBatch)
	if empty.Purpose != api.PurposeWork {
		t.Fatalf("empty purpose %s", empty.Purpose)
	}
	scorer, err := h.st.CreateRun(ctx, store.NewRun{
		FactoryID: factoryID,
		AgentID:   agentID,
		Pool:      "purpose-scorer",
		Priority:  store.PriorityBatch,
		Prompt:    "score",
		Title:     "score",
		Purpose:   api.PurposeScorer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if scorer.Purpose != api.PurposeScorer {
		t.Fatalf("scorer purpose %s", scorer.Purpose)
	}
	loaded, err := h.st.GetRun(ctx, scorer.ID)
	if err != nil || loaded.Purpose != api.PurposeScorer {
		t.Fatalf("get %+v %v", loaded.Purpose, err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET purpose = 'work' WHERE id = $1`, scorer.ID); err == nil || !strings.Contains(err.Error(), "runs.purpose is immutable") {
		t.Fatalf("raw purpose change %v", err)
	}

	item, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.SourceAPI,
		Title:     "build",
	})
	if err != nil {
		t.Fatal(err)
	}
	built, created, err := h.st.BeginBuildingRun(ctx, store.NewRun{
		FactoryID:  factoryID,
		AgentID:    agentID,
		Pool:       "purpose-bench",
		Priority:   store.PriorityPipeline,
		Prompt:     "bench",
		Title:      "bench",
		WorkItemID: item.ID,
		Stage:      store.StageBuilding,
		Purpose:    api.PurposeBenchmark,
	})
	if err != nil || !created || built.Purpose != api.PurposeBenchmark {
		t.Fatalf("building %+v created %v err %v", built.Purpose, created, err)
	}

	a := &store.Automation{Store: h.st}
	opened, _, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#purpose", newAutoItem(factoryID, "acme/repo#purpose", "intake"))
	if err != nil {
		t.Fatal(err)
	}
	intake, err := a.EnsureIntake(ctx, opened.ID, "dlv_purpose", []automation.IntakeSlot{
		{Automation: "beta", Run: autoRun(factoryID, "implementer")},
	})
	if err != nil || len(intake.Slots) != 1 || intake.Slots[0].Run.ID == "" {
		t.Fatalf("intake %+v %v", intake, err)
	}
	auto, err := h.st.GetRun(ctx, intake.Slots[0].Run.ID)
	if err != nil || auto.Purpose != api.PurposeWork {
		t.Fatalf("automation purpose %s %v", auto.Purpose, err)
	}

	if _, err := h.st.CreateRun(ctx, store.NewRun{
		FactoryID: factoryID,
		Pool:      "purpose-bad",
		Priority:  store.PriorityBatch,
		Purpose:   "audit",
	}); err == nil {
		t.Fatal("invalid purpose accepted")
	}
}

func TestPurposeIDPrefixes(t *testing.T) {
	for _, prefix := range []string{
		store.PrefixScorerResult,
		store.PrefixImproveBatch,
		store.PrefixBenchmarkRun,
		store.PrefixBenchmarkTrial,
	} {
		id, err := store.NewID(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+26 {
			t.Fatalf("id %s", id)
		}
	}
}

func applyBefore0010(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	for _, name := range embeddedMigrations(t) {
		if name >= "0010" {
			break
		}
		body, err := store.MigrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func stampTime(t *testing.T, h *harness, query string, args ...any) {
	t.Helper()
	if _, err := h.raw.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
