package store_test

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/store"
)

//go:embed migrations/*.sql
var testMigrationFS embed.FS

const lifecycleSecret = "rt_SECRET_PLANT"

func TestLifecycleChain(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run, err := h.st.CreateRun(ctx, store.NewRun{
		FactoryID: factoryID,
		AgentID:   agentID,
		Pool:      "default",
		Priority:  store.PriorityPipeline,
		Prompt:    lifecycleSecret,
		Title:     "chain",
		Stage:     store.StageBuilding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Lease(ctx, run.Pool, time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, run.ID, "worker-a", tokenHash(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	seqs, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 0, []json.RawMessage{
		json.RawMessage(`{"type":"message.delta","text":"kept"}`),
	})
	if err != nil || len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("append %v %v", seqs, err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = status WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'idle' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'succeeded' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}

	page := factoryPage(t, h, factoryID)
	var life []store.FactoryEvent
	var message *store.FactoryEvent
	for i := range page {
		ev := page[i]
		if ev.RunID != run.ID {
			t.Fatalf("other run %+v", ev)
		}
		if ev.FactorySeq != int64(i+1) {
			t.Fatalf("factory_seq %d at %d", ev.FactorySeq, i)
		}
		switch ev.Type {
		case "run.created", "run.status":
			if ev.Seq != 0 || ev.ExecutionID != "" || ev.Idx != 0 {
				t.Fatalf("lifecycle row looks like a harness event %+v", ev)
			}
			life = append(life, ev)
		case "message.delta":
			message = &page[i]
		default:
			t.Fatalf("type %s", ev.Type)
		}
	}
	want := []string{"queued", "assigned", "running", "idle", "succeeded"}
	if len(life) != len(want) || life[0].Type != "run.created" {
		t.Fatalf("lifecycle %+v", life)
	}
	var prev int64
	for i, ev := range life {
		if i > 0 && ev.Type != "run.status" {
			t.Fatalf("event %d type %s", i, ev.Type)
		}
		if ev.FactorySeq <= prev {
			t.Fatalf("factory_seq %d then %d", prev, ev.FactorySeq)
		}
		prev = ev.FactorySeq
		assertLifecyclePayload(t, ev.Payload, run.ID, want[i], store.StageBuilding)
	}
	if message == nil || message.Seq != 1 || message.ExecutionID != exec.ID {
		t.Fatalf("harness event %+v", message)
	}
	if message.FactorySeq <= life[2].FactorySeq || message.FactorySeq >= life[3].FactorySeq {
		t.Fatalf("message factory_seq %d between running %d and idle %d", message.FactorySeq, life[2].FactorySeq, life[3].FactorySeq)
	}

	perRun, err := h.st.EventsFrom(ctx, run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(perRun) != 1 || perRun[0].Seq != 1 || perRun[0].Type != "message.delta" {
		t.Fatalf("per-run %+v", perRun)
	}

	failed := h.createRun(t, factoryID, agentID, "failed-pool", store.PriorityBatch)
	leaseAndStart(t, h, failed)
	loaded, err := h.st.GetRun(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	var execID string
	if err := h.raw.QueryRow(ctx, `SELECT id FROM executions WHERE run_id = $1`, failed.ID).Scan(&execID); err != nil {
		t.Fatal(err)
	}
	applied, err := h.st.FinishExecution(ctx, execID, store.Terminal{
		State:         store.ExecFailed,
		FailureReason: lifecycleSecret,
	})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	if loaded.Stage != "" {
		t.Fatalf("stage %q", loaded.Stage)
	}
	failedEvents := lifecycleEvents(t, h, factoryID, failed.ID)
	if len(failedEvents) != 4 || failedEvents[3].Type != "run.status" {
		t.Fatalf("failed chain %+v", failedEvents)
	}
	assertLifecyclePayload(t, failedEvents[0].Payload, failed.ID, store.StatusQueued, "")
	assertLifecyclePayload(t, failedEvents[3].Payload, failed.ID, store.StatusFailed, "")
}

func TestLifecycleAutomation(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	a := &store.Automation{Store: h.st}
	item, created, err := a.OpenWorkItem(ctx, store.ProviderGitHub, "acme/repo#life", newAutoItem(factoryID, "acme/repo#life", "life"))
	if err != nil || !created {
		t.Fatalf("open %+v %v", item, err)
	}
	intake, err := a.EnsureIntake(ctx, item.ID, "dlv_life", []automation.IntakeSlot{
		{Automation: "labeled", Run: autoRun(factoryID, "implementer")},
	})
	if err != nil || len(intake.Slots) != 1 || !intake.Slots[0].Inserted || intake.Slots[0].Run.ID == "" {
		t.Fatalf("intake %+v %v", intake, err)
	}
	events := lifecycleEvents(t, h, factoryID, intake.Slots[0].Run.ID)
	if len(events) != 1 || events[0].Type != "run.created" {
		t.Fatalf("automation events %+v", events)
	}
	assertLifecyclePayload(t, events[0].Payload, intake.Slots[0].Run.ID, store.StatusQueued, store.StageTriage)
	if strings.Contains(string(events[0].Payload), "do the work") {
		t.Fatalf("payload kept the prompt %s", events[0].Payload)
	}
}

func TestLifecycleCancel(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	cancelled, err := h.st.RequestCancel(ctx, run.ID)
	if err != nil || cancelled.Status != store.StatusCancelled {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	events := lifecycleEvents(t, h, factoryID, run.ID)
	if len(events) != 2 || events[0].Type != "run.created" || events[1].Type != "run.status" {
		t.Fatalf("cancel events %+v", events)
	}
	if events[1].FactorySeq <= events[0].FactorySeq {
		t.Fatalf("seq %d then %d", events[0].FactorySeq, events[1].FactorySeq)
	}
	assertLifecyclePayload(t, events[0].Payload, run.ID, store.StatusQueued, "")
	assertLifecyclePayload(t, events[1].Payload, run.ID, store.StatusCancelled, "")
}

func TestLifecycleRollback(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	tx, err := h.raw.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool, prompt)
		VALUES ('run_rollback', $1, 'queued', 1, 'default', $2)`, factoryID, lifecycleSecret); err != nil {
		t.Fatal(err)
	}
	var inside int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM run_lifecycle_events WHERE run_id = 'run_rollback'`).Scan(&inside); err != nil {
		t.Fatal(err)
	}
	if inside != 1 {
		t.Fatalf("uncommitted lifecycle rows %d", inside)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var outside int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM run_lifecycle_events WHERE run_id = 'run_rollback'`).Scan(&outside); err != nil {
		t.Fatal(err)
	}
	if outside != 0 {
		t.Fatalf("rolled back rows %d", outside)
	}
	var runs int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM runs WHERE id = 'run_rollback'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("rolled back run still stored")
	}
}

func TestLifecyclePerRunSeq(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run, exec := startRunning(t, h, factoryID, agentID, "seq-pool")
	seqs, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 0, eventBatch(0, 2))
	if err != nil || len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("seqs %v %v", seqs, err)
	}
	before := perRunSeqs(t, h, run.ID)
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'idle' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'failed' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	after := perRunSeqs(t, h, run.ID)
	if strings.Join(before, ",") != "1:0,2:1" || strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("before %v after %v", before, after)
	}
	rows, err := h.st.EventsFrom(ctx, run.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Type == "run.created" || row.Type == "run.status" {
			t.Fatalf("lifecycle row on the per-run stream %+v", row)
		}
	}
	var maxSeq int64
	if err := h.raw.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM run_events WHERE run_id = $1`, run.ID).Scan(&maxSeq); err != nil {
		t.Fatal(err)
	}
	if maxSeq != 2 {
		t.Fatalf("max seq %d", maxSeq)
	}
}

func TestLifecycleMigrationOnM5(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	for _, name := range embeddedMigrations(t) {
		if name >= "0009" {
			break
		}
		body, err := testMigrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO factories (id, name, schema_version, definition_source)
		VALUES ('fac_m5', 'm5rows', 'v1alpha1', 'git')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool, prompt, stage)
		VALUES ('run_m5', 'fac_m5', 'running', 1, 'default', $1, 'building')`, lifecycleSecret); err != nil {
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

	body, err := testMigrationFS.ReadFile("migrations/0009_run_lifecycle_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}

	var life int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM run_lifecycle_events`).Scan(&life); err != nil {
		t.Fatal(err)
	}
	if life != 0 {
		t.Fatalf("backfilled %d lifecycle rows", life)
	}
	var seq int64
	var execID, eventType string
	if err := h.raw.QueryRow(ctx, `
		SELECT seq, execution_id, type FROM run_events WHERE run_id = 'run_m5'`).Scan(&seq, &execID, &eventType); err != nil {
		t.Fatal(err)
	}
	if seq != 1 || execID != "exec_m5" || eventType != "message.delta" {
		t.Fatalf("preserved event %d %s %s", seq, execID, eventType)
	}
	var nullable string
	if err := h.raw.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'run_events'
		  AND column_name = 'execution_id'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" {
		t.Fatalf("execution_id nullable %s", nullable)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO run_events (run_id, seq, idx, ts, type, payload_json)
		VALUES ('run_m5', 2, 1, clock_timestamp(), 'message.delta', '{"type":"message.delta","text":"no-exec"}')`); err == nil {
		t.Fatal("run_events accepted a null execution_id")
	}

	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'idle' WHERE id = 'run_m5'`); err != nil {
		t.Fatal(err)
	}
	events := lifecycleEvents(t, h, "fac_m5", "run_m5")
	if len(events) != 1 {
		t.Fatalf("events after idle %+v", events)
	}
	assertLifecyclePayload(t, events[0].Payload, "run_m5", "idle", store.StageBuilding)
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO runs (id, factory_id, status, priority, pool, prompt)
		VALUES ('run_new', 'fac_m5', 'queued', 1, 'default', $1)`, lifecycleSecret); err != nil {
		t.Fatal(err)
	}
	created := lifecycleEvents(t, h, "fac_m5", "run_new")
	if len(created) != 1 || created[0].Type != "run.created" {
		t.Fatalf("new run %+v", created)
	}
	assertLifecyclePayload(t, created[0].Payload, "run_new", store.StatusQueued, "")
	kept, err := h.st.EventsFrom(ctx, "run_m5", 0, 10)
	if err != nil || len(kept) != 1 || kept[0].Seq != 1 {
		t.Fatalf("events from %+v %v", kept, err)
	}
}

func TestLifecycleNotify(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryID, agentID := h.seed(t)
	notes, err := h.st.Listen(ctx, "run_events")
	if err != nil {
		t.Fatal(err)
	}
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	waitNotify(t, notes, run.ID)
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'queued' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	// run_events is database-wide. Other tests NOTIFY their own run ids on
	// this channel. A same-status update notifies nothing, so only this
	// run's payload is a failure. The create notification was already taken.
	quiet := time.NewTimer(300 * time.Millisecond)
	defer quiet.Stop()
quiet:
	for {
		select {
		case got, ok := <-notes:
			if !ok {
				t.Fatalf("Listen closed while checking %s", run.ID)
			}
			if got == run.ID {
				t.Fatalf("same status notified %s", got)
			}
		case <-quiet.C:
			break quiet
		}
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET status = 'idle' WHERE id = $1`, run.ID); err != nil {
		t.Fatal(err)
	}
	waitNotify(t, notes, run.ID)
}

func factoryPage(t *testing.T, h *harness, factoryID string) []store.FactoryEvent {
	t.Helper()
	page, err := h.st.FactoryEventsFrom(context.Background(), factoryID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func lifecycleEvents(t *testing.T, h *harness, factoryID, runID string) []store.FactoryEvent {
	t.Helper()
	var out []store.FactoryEvent
	for _, ev := range factoryPage(t, h, factoryID) {
		if ev.RunID == runID && (ev.Type == "run.created" || ev.Type == "run.status") {
			out = append(out, ev)
		}
	}
	return out
}

func assertLifecyclePayload(t *testing.T, raw json.RawMessage, runID, status, stage string) {
	t.Helper()
	if strings.Contains(string(raw), lifecycleSecret) {
		t.Fatalf("payload contains a secret: %s", raw)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"run_id": runID, "status": status}
	if stage != "" {
		want["stage"] = stage
	}
	if len(payload) != len(want) {
		t.Fatalf("payload %s", raw)
	}
	for key, value := range want {
		if payload[key] != value {
			t.Fatalf("payload %s key %s = %v", raw, key, payload[key])
		}
	}
}

func perRunSeqs(t *testing.T, h *harness, runID string) []string {
	t.Helper()
	rows, err := h.raw.Query(context.Background(), `
		SELECT seq::text || ':' || idx::text FROM run_events WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			t.Fatal(err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func embeddedMigrations(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(testMigrationFS, "migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("embedded migrations: empty")
	}
	return names
}

var embeddedCreateTable = regexp.MustCompile(`(?i)CREATE TABLE(?: IF NOT EXISTS)?\s+([a-z_][a-z0-9_]*)`)

func embeddedTables(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{"schema_migrations": true}
	for _, name := range embeddedMigrations(t) {
		body, err := testMigrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		stripped := stripSQLLineComments(string(body))
		for _, match := range embeddedCreateTable.FindAllStringSubmatch(stripped, -1) {
			seen[match[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func stripSQLLineComments(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
