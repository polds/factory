package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/cron"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

func TestM8InvokeAPIReplayAndConflict(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})

	body := map[string]any{
		"sourceOccurrenceKey": "human:review:api-1",
		"intent":              "new",
		"priority":            "interactive",
	}
	first := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusCreated)
	second := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusOK)
	if first["invocationId"] != second["invocationId"] {
		t.Fatalf("replay ids %v vs %v", first["invocationId"], second["invocationId"])
	}
	body["intent"] = "continue"
	env.postJSONExpect(t, "/api/v1/factory/pipe/invocations", body, http.StatusConflict)
}

func TestM8InsertInvocationReplayIdempotent(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	key := "replay-idempotent"
	body := map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}
	first := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusCreated)
	second := env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusOK)
	if first["invocationId"] != second["invocationId"] {
		t.Fatalf("replay mismatch %v vs %v", first["invocationId"], second["invocationId"])
	}
}

func TestM8GrantRejectedWithoutSideEffects(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	body := map[string]any{
		"sourceOccurrenceKey": "grant:deny:1",
		"intent":              "new",
		"priority":            "interactive",
		"inputs":              map[string]any{"executeTarget": true},
	}
	env.postJSONExpect(t, "/api/v1/factory/pipe/invocations", body, http.StatusForbidden)
	var count int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM invocations WHERE source_occurrence_key = $1`, "grant:deny:1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invocations written on grant reject: %d", count)
	}
}

func TestM8ScheduleOccurrenceKeyFormat(t *testing.T) {
	key := invocation.ScheduleOccurrenceKey("sch_test", "2026-11-09T00:00:00Z")
	want := "schedule:sch_test:slot:2026-11-09T00:00:00Z"
	if key != want {
		t.Fatalf("got %q want %q", key, want)
	}
}

func (e *env) postJSON(t *testing.T, path string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	return e.postJSONExpect(t, path, body, wantStatus)
}

func (e *env) postJSONExpect(t *testing.T, path string, body map[string]any, wantStatus int) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s status %d", path, resp.StatusCode)
	}
	if wantStatus == http.StatusForbidden || wantStatus == http.StatusConflict {
		return nil
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestM8ResultUnauthorizedIsEmpty403(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	req, err := http.NewRequest(http.MethodGet, env.base+"/api/v1/factory/pipe/invocations/inv_missing/result", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || len(body) != 0 {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestM8NeutralStageDoesNotAdvancePipeline(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	body := map[string]any{
		"sourceOccurrenceKey": "neutral:stage:1",
		"intent":              "new",
		"priority":            "interactive",
	}
	env.postJSON(t, "/api/v1/factory/pipe/invocations", body, http.StatusCreated)
	var runID, itemID, stage string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT id, work_item_id, stage FROM runs ORDER BY created_at DESC LIMIT 1`).Scan(&runID, &itemID, &stage); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(context.Background(), `UPDATE runs SET status = $2 WHERE id = $1`, runID, store.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	env.srv.advanceAfterTerminal(context.Background(), runID)
	task := env.getTask(t, itemID)
	if task.Status != store.WorkItemComplete || task.Stage != stage {
		t.Fatalf("task %+v stage %s", task, stage)
	}
	var n int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE work_item_id = $1`, itemID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("runs %d", n)
	}
}

func TestM8ReplayFinishesMissingWorkItem(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	key := "probe:crash-window"
	got := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusCreated)
	itemID, _ := got["workItemId"].(string)
	if itemID == "" {
		t.Fatal("missing work item")
	}
	if _, err := env.raw.Exec(context.Background(), `UPDATE invocations SET work_item_id = NULL WHERE source_occurrence_key = $1`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(context.Background(), `UPDATE work_items SET invocation_id = NULL WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	var runsBefore int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE work_item_id = $1`, itemID).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	again := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusOK)
	if again["workItemId"] != itemID {
		t.Fatalf("replay minted a second work item: %+v", again)
	}
	var runsAfter int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE work_item_id = $1`, itemID).Scan(&runsAfter); err != nil {
		t.Fatal(err)
	}
	if runsBefore == 0 || runsAfter != runsBefore {
		t.Fatalf("stage runs %d -> %d", runsBefore, runsAfter)
	}
}

func TestOrphanInvocationRepairStartsOnce(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	key := "probe:crash-before-run"
	got := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusCreated)
	itemID, _ := got["workItemId"].(string)
	if itemID == "" {
		t.Fatal("missing work item")
	}
	ctx := context.Background()
	if _, err := env.raw.Exec(ctx, `DELETE FROM run_lifecycle_events WHERE run_id IN (SELECT id FROM runs WHERE work_item_id = $1)`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `DELETE FROM run_queue WHERE run_id IN (SELECT id FROM runs WHERE work_item_id = $1)`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE work_item_id = $1)`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `DELETE FROM executions WHERE run_id IN (SELECT id FROM runs WHERE work_item_id = $1)`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `DELETE FROM runs WHERE work_item_id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `UPDATE invocations SET work_item_id = NULL WHERE source_occurrence_key = $1`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `UPDATE work_items SET invocation_id = NULL WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	again := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusOK)
	if again["workItemId"] != itemID {
		t.Fatalf("repair created a second item: %+v", again)
	}
	var runs int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM runs WHERE work_item_id = $1`, itemID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("stage runs %d", runs)
	}
	third := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}, http.StatusOK)
	if third["workItemId"] != itemID {
		t.Fatalf("third replay %+v", third)
	}
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM runs WHERE work_item_id = $1`, itemID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("stage runs after replay %d", runs)
	}
}

func TestConcurrentAdmitStartsOneTriageRun(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	admitConcurrent(t, env, "verifier:concurrent-admit", store.StageTriage)
}

func TestConcurrentAdmitStartsOneBuildingRun(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBuildingOnlyPipeline(t)
	}})
	admitConcurrent(t, env, "verifier:concurrent-admit-building", store.StageBuilding)
}

func admitConcurrent(t *testing.T, env *env, key, stage string) {
	t.Helper()
	const n = 8
	body := map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	statuses := make([]int, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, err := http.NewRequest(http.MethodPost, env.base+"/api/v1/factory/pipe/invocations", bytes.NewReader(raw))
			if err != nil {
				errs[i] = err
				return
			}
			req.Header.Set("Authorization", "Bearer "+env.apiKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.Copy(io.Discard, resp.Body)
			statuses[i] = resp.StatusCode
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	created := 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
		default:
			t.Fatalf("statuses %v", statuses)
		}
	}
	if created != 1 {
		t.Fatalf("created %d statuses %v", created, statuses)
	}
	ctx := context.Background()
	var items, runs int
	var gotStage string
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE source_ref = $1`, key).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := env.raw.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(r.stage), '')
		FROM runs r
		JOIN work_items w ON w.id = r.work_item_id
		WHERE w.source_ref = $1`, key).Scan(&runs, &gotStage); err != nil {
		t.Fatal(err)
	}
	if items != 1 || runs != 1 || gotStage != stage {
		t.Fatalf("items %d runs %d stage %s", items, runs, gotStage)
	}
}

func writeBuildingOnlyPipeline(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "factory.yaml"), `
schemaVersion: v1alpha1
name: pipe
agentDefaults:
  model: auto
  runner: local
  pool: standard
pipeline:
  stages: [building]
  agents:
    building: implementer
`)
	writeAgent(t, root, "implementer", "IMPLEMENT", "model: building-model")
	return root
}

func TestEmitFreshScheduleIsCronCaller(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	var factoryID string
	if err := env.raw.QueryRow(context.Background(), `SELECT id FROM factories WHERE name = 'pipe'`).Scan(&factoryID); err != nil {
		t.Fatal(err)
	}
	tree := env.srv.snapshot().tree
	if tree.Automations == nil {
		tree.Automations = map[string]definition.Automation{}
	}
	tree.Automations["nightly"] = definition.Automation{
		Name:           "nightly",
		Enabled:        true,
		OccurrenceMode: "fresh",
		Agent:          "triage",
		Triggers: []definition.Trigger{{
			Schedule: &definition.Schedule{Name: "nightly", OccurrenceMode: "fresh"},
		}},
	}
	sch, err := env.srv.store.UpsertSchedule(context.Background(), store.Schedule{
		FactoryID:  factoryID,
		Automation: "nightly",
		Name:       "nightly",
		Cron:       "0 * * * *",
		NextFireAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	err = (cronDB{env.srv}).Emit(context.Background(), cron.Event{
		Event:       cron.EventCronFired,
		FactoryID:   factoryID,
		Automation:  "nightly",
		Name:        "nightly",
		ExternalRef: "schedule:" + sch.ID,
		DueAt:       due,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	wantKey := invocation.ScheduleOccurrenceKey(sch.ID, due.Format(time.RFC3339Nano))
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM invocations WHERE source_occurrence_key = $1`, wantKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("fresh cron invocations %d", n)
	}
}

func TestFreshReplyDoesNotPreferLegacyContinue(t *testing.T) {
	auto, ok := freshFromList([]definition.Automation{{
		Name:           "review",
		OccurrenceMode: occurrenceFresh,
	}})
	if !ok || auto.Name != "review" {
		t.Fatal("fresh match must admit before legacy continue")
	}
	if _, ok := freshFromList([]definition.Automation{{Name: "legacy"}}); ok {
		t.Fatal("legacy mode must stay on engine.Handle")
	}
}
