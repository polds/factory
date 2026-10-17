package store_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/store"
)

// Tests share a Postgres advisory-lock key and LISTEN/NOTIFY, so they stay sequential.
// Each test creates a schema t_<rand> and connects with search_path set to it.

func TestExportedStructsOmitSecrets(t *testing.T) {
	t.Parallel()
	types := []reflect.Type{
		reflect.TypeOf(store.Run{}),
		reflect.TypeOf(store.NewRun{}),
		reflect.TypeOf(store.Factory{}),
		reflect.TypeOf(store.Agent{}),
		reflect.TypeOf(store.Event{}),
		reflect.TypeOf(store.Worker{}),
		reflect.TypeOf(store.QueuedRun{}),
		reflect.TypeOf(store.OrphanedExecution{}),
		reflect.TypeOf(store.AppliedDefinition{}),
		reflect.TypeOf(store.DefinedAgent{}),
		reflect.TypeOf(store.DefinedRunner{}),
		reflect.TypeOf(store.DefinedAutomation{}),
		reflect.TypeOf(store.Terminal{}),
		reflect.TypeOf(store.RunFilter{}),
		reflect.TypeOf(store.WorkItem{}),
		reflect.TypeOf(store.NewWorkItem{}),
		reflect.TypeOf(store.WorkItemFilter{}),
		reflect.TypeOf(store.Approval{}),
		reflect.TypeOf(store.Delivery{}),
		reflect.TypeOf(store.Schedule{}),
		reflect.TypeOf(store.ExternalRef{}),
		reflect.TypeOf(store.OutboundThread{}),
		reflect.TypeOf(store.InputRequest{}),
		reflect.TypeOf(store.WorkerInfo{}),
		reflect.TypeOf(store.PoolStat{}),
		reflect.TypeOf(store.Steer{}),
		reflect.TypeOf(store.Phase{}),
		reflect.TypeOf(store.RunUsage{}),
		reflect.TypeOf(store.FactoryEvent{}),
		reflect.TypeOf(store.InboxItem{}),
		reflect.TypeOf(store.AutomationStat{}),
		reflect.TypeOf(store.FactorySummary{}),
	}
	for _, typ := range types {
		assertNoSecretFields(t, typ)
	}
	webhookType := reflect.TypeOf(store.WebhookRegistration{})
	for i := 0; i < webhookType.NumField(); i++ {
		field := webhookType.Field(i)
		switch field.Name {
		case "SecretRef", "URLTokenHash":
			continue
		}
		if secretish(field.Name) {
			t.Fatalf("WebhookRegistration.%s looks like a secret", field.Name)
		}
	}
	execType := reflect.TypeOf(store.Execution{})
	for i := 0; i < execType.NumField(); i++ {
		field := execType.Field(i)
		if field.Name == "RunTokenSHA256" {
			continue
		}
		if secretish(field.Name) {
			t.Fatalf("Execution.%s looks like a secret", field.Name)
		}
	}
	// InputTokens and OutputTokens are usage counts, not credentials.
	for _, typ := range []reflect.Type{
		reflect.TypeOf(store.UsageCall{}),
		reflect.TypeOf(store.ModelUsage{}),
		reflect.TypeOf(store.UsageTotal{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			switch field.Name {
			case "InputTokens", "OutputTokens":
				continue
			}
			if secretish(field.Name) {
				t.Fatalf("%s.%s looks like a secret", typ.Name(), field.Name)
			}
		}
	}
}

func TestLeaseSkipLocked(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	factoryID, agentID := h.seed(t)

	want := make([]string, 10)
	for i := range want {
		run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
		want[i] = run.ID
	}
	other := h.createRun(t, factoryID, agentID, "other", store.PriorityInteractive)

	var mu sync.Mutex
	got := map[string]int{}
	errCh := make(chan error, 5)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				leased, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
				if err != nil {
					errCh <- err
					return
				}
				if len(leased) != 1 {
					errCh <- fmt.Errorf("lease returned %d runs", len(leased))
					return
				}
				mu.Lock()
				got[leased[0].RunID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if len(got) != 10 {
		t.Fatalf("distinct leased ids %d", len(got))
	}
	for _, id := range want {
		if got[id] != 1 {
			t.Fatalf("run %s leased %d times", id, got[id])
		}
	}
	leasedOther, err := h.st.Lease(ctx, "other", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(leasedOther) != 1 || leasedOther[0].RunID != other.ID {
		t.Fatalf("other pool lease %+v", leasedOther)
	}
}

func TestLeasePriorityOrder(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)

	batch := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	interactive := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	pipeline := h.createRun(t, factoryID, agentID, "default", store.PriorityPipeline)

	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 3 {
		t.Fatalf("leased %d", len(leased))
	}
	got := []string{leased[0].RunID, leased[1].RunID, leased[2].RunID}
	want := []string{interactive.ID, pipeline.ID, batch.ID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v want %v", got, want)
	}
	if leased[0].Priority != store.PriorityInteractive || leased[1].Priority != store.PriorityPipeline || leased[2].Priority != store.PriorityBatch {
		t.Fatalf("priorities %+v", leased)
	}

	listed, err := h.st.ListRuns(ctx, store.RunFilter{FactoryID: factoryID})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("ListRuns %d", len(listed))
	}
}

func TestLeaseFIFOWithinPriority(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	var ids []string
	for range 3 {
		run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
		ids = append(ids, run.ID)
		time.Sleep(5 * time.Millisecond)
	}
	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 3 {
		t.Fatalf("leased %d", len(leased))
	}
	for i, id := range ids {
		if leased[i].RunID != id {
			t.Fatalf("fifo position %d got %s want %s", i, leased[i].RunID, id)
		}
	}
}

func TestRequeueOnReject(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityPipeline)
	if run.Prompt != "say hello" || run.Status != store.StatusQueued {
		t.Fatalf("created %+v", run)
	}

	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].RunID != run.ID || leased[0].Attempts != 0 {
		t.Fatalf("lease %+v", leased)
	}
	assigned, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.Status != store.StatusAssigned {
		t.Fatalf("leased status %s", assigned.Status)
	}
	if err := h.st.Requeue(ctx, run.ID, "task_rejected"); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusQueued {
		t.Fatalf("status %s", got.Status)
	}
	again, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].RunID != run.ID || again[0].Attempts != 1 {
		t.Fatalf("requeued lease %+v", again)
	}
	var failed, total int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state = 'failed'), count(*) FROM executions WHERE run_id = $1`, run.ID).Scan(&failed, &total); err != nil {
		t.Fatal(err)
	}
	if failed != 0 || total != 0 {
		t.Fatalf("failed executions %d total %d", failed, total)
	}
}

func TestStartExecutionRejectsSecondLive(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	if _, err := h.st.Lease(ctx, "default", 30*time.Second, 1); err != nil {
		t.Fatal(err)
	}
	first, err := h.st.StartExecution(ctx, run.ID, "worker-a", tokenHash("live-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.StartExecution(ctx, run.ID, "worker-b", tokenHash("live-2")); err == nil {
		t.Fatal("second live execution was accepted")
	}
	var n int
	var state, id string
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*), min(state), min(id) FROM executions WHERE run_id = $1`, run.ID).Scan(&n, &state, &id); err != nil {
		t.Fatal(err)
	}
	if n != 1 || state != store.ExecRunning || id != first.ID {
		t.Fatalf("executions %d state %s id %s", n, state, id)
	}
	got, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusRunning || got.WorkerID != "worker-a" {
		t.Fatalf("run after rejected start %+v", got)
	}
}

func TestExpireLeasesOrphans(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	hash := tokenHash("attempt-1")
	exec, err := h.st.StartExecution(ctx, run.ID, "worker-a", hash)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Attempt != 1 || exec.State != store.ExecRunning || exec.RunID != run.ID {
		t.Fatalf("execution %+v", exec)
	}
	if !bytesEqual(exec.RunTokenSHA256, hash) {
		t.Fatal("token hash not stored")
	}
	started, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != store.StatusRunning || started.WorkerID != "worker-a" {
		t.Fatalf("running run %+v", started)
	}

	orphans, err := h.st.ExpireLeases(ctx, leased[0].LeaseUntil.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ExecutionID != exec.ID || orphans[0].RunID != run.ID || orphans[0].WorkerID != "worker-a" {
		t.Fatalf("orphans %+v", orphans)
	}
	var state string
	if err := h.raw.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, exec.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != store.ExecOrphaned {
		t.Fatalf("state %s", state)
	}
	after, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != store.StatusQueued {
		t.Fatalf("status after expire %s", after.Status)
	}

	again, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].RunID != run.ID {
		t.Fatalf("re-lease %+v", again)
	}
	exec2, err := h.st.StartExecution(ctx, run.ID, "worker-b", tokenHash("attempt-2"))
	if err != nil {
		t.Fatal(err)
	}
	if exec2.ID == exec.ID || exec2.Attempt != 2 {
		t.Fatalf("new execution %+v old %s", exec2, exec.ID)
	}
}

func TestLateTerminalAfterOrphanDoesNotChangeRun(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, run.ID, "worker-a", tokenHash("late"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ExpireLeases(ctx, leased[0].LeaseUntil.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	applied, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "lost", ExitCode: intPtr(1)})
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("late terminal applied")
	}
	got, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusQueued {
		t.Fatalf("status %s", got.Status)
	}
	var state string
	if err := h.raw.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, exec.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != store.ExecOrphaned {
		t.Fatalf("state %s", state)
	}
}

func TestFinishExecutionIdempotent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	if _, err := h.st.Lease(ctx, "default", 30*time.Second, 1); err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, run.ID, "worker-a", tokenHash("finish"))
	if err != nil {
		t.Fatal(err)
	}
	terminal := store.Terminal{State: store.ExecCompleted, ExitCode: intPtr(0)}
	applied, err := h.st.FinishExecution(ctx, exec.ID, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("first finish did not apply")
	}
	got, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusSucceeded {
		t.Fatalf("status %s", got.Status)
	}
	applied, err = h.st.FinishExecution(ctx, exec.ID, terminal)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("second finish applied")
	}
	again, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != got.Status {
		t.Fatalf("status changed %s -> %s", got.Status, again.Status)
	}
	var state string
	var exitCode *int
	if err := h.raw.QueryRow(ctx, `SELECT state, exit_code FROM executions WHERE id = $1`, exec.ID).Scan(&state, &exitCode); err != nil {
		t.Fatal(err)
	}
	if state != store.ExecCompleted || exitCode == nil || *exitCode != 0 {
		t.Fatalf("execution state %s exit %v", state, exitCode)
	}
	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 0 {
		t.Fatalf("finished run leased %+v", leased)
	}
}

func TestAppendEventsDedup(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	if _, err := h.st.Lease(ctx, "default", 30*time.Second, 1); err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, run.ID, "worker-a", tokenHash("events"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 0, eventBatch(0, 5))
	if err != nil {
		t.Fatal(err)
	}
	if !int64sEqual(first, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("first seqs %v", first)
	}
	second, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 3, eventBatch(3, 4))
	if err != nil {
		t.Fatal(err)
	}
	if !int64sEqual(second, []int64{4, 5, 6, 7}) {
		t.Fatalf("second seqs %v", second)
	}
	var rows, distinctSeq, distinctIdx int
	if err := h.raw.QueryRow(ctx, `SELECT count(*), count(DISTINCT seq), count(DISTINCT idx) FROM run_events WHERE run_id = $1`, run.ID).Scan(&rows, &distinctSeq, &distinctIdx); err != nil {
		t.Fatal(err)
	}
	if rows != 7 || distinctSeq != 7 || distinctIdx != 7 {
		t.Fatalf("rows %d seqs %d idx %d", rows, distinctSeq, distinctIdx)
	}
	events, err := h.st.EventsFrom(ctx, run.ID, 5, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 6 || events[1].Seq != 7 {
		t.Fatalf("from seq 5: %+v", events)
	}
	if events[0].Idx != 5 || events[1].Idx != 6 || events[0].Type != "message.delta" {
		t.Fatalf("event meta %+v %+v", events[0], events[1])
	}
	if events[0].ExecutionID != exec.ID || events[0].RunID != run.ID {
		t.Fatalf("event ids %+v", events[0])
	}
}

func TestNotifyOnEnqueue(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryID, agentID := h.seed(t)

	ch, err := h.st.Listen(ctx, "run_queue")
	if err != nil {
		t.Fatal(err)
	}
	// run_queue is database-wide. Other tests NOTIFY their own run ids on
	// this channel. Wait for this test's payload and ignore the rest.
	const other = "run_other"
	if _, err := h.raw.Exec(ctx, `SELECT pg_notify('run_queue', $1)`, other); err != nil {
		t.Fatal(err)
	}
	waitNotify := func(want string) {
		t.Helper()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case got, ok := <-ch:
				if !ok {
					t.Fatalf("Listen closed while waiting for %s", want)
				}
				if got == want {
					return
				}
			case <-timer.C:
				t.Fatalf("Listen did not receive %s within 2s", want)
			}
		}
	}
	waitNotify(other)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	waitNotify(run.ID)
}

func TestApplyDefinitionAtomic(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	defID, err := h.st.ApplyDefinition(ctx, "acme", validDefinition())
	if err != nil {
		t.Fatal(err)
	}
	fac, err := h.st.GetFactory(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	byID, err := h.st.GetFactory(ctx, fac.ID)
	if err != nil {
		t.Fatal(err)
	}
	if byID.ID != fac.ID || fac.Name != "acme" || fac.CurrentDefinitionID != defID || fac.Alias != "acme" {
		t.Fatalf("factory %+v", fac)
	}
	if fac.ProductionRef != "" {
		t.Fatalf("production_ref %q", fac.ProductionRef)
	}
	agents, err := h.st.ListAgents(ctx, fac.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Name != "implementer" || agents[0].Runner != "local" || agents[0].ID == "" {
		t.Fatalf("agents %+v", agents)
	}
	factories, err := h.st.ListFactories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(factories) != 1 || factories[0].ID != fac.ID {
		t.Fatalf("factories %+v", factories)
	}

	bad := validDefinition()
	bad.Agents = []store.DefinedAgent{{
		Name:      "implementer",
		AgentType: "IMPLEMENT",
		Runner:    "missing",
	}}
	if _, err := h.st.ApplyDefinition(ctx, "acme", bad); err == nil {
		t.Fatal("expected foreign key error")
	}
	after, err := h.st.GetFactory(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentDefinitionID != defID {
		t.Fatalf("definition changed %s -> %s", defID, after.CurrentDefinitionID)
	}
	agentsAfter, err := h.st.ListAgents(ctx, fac.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentsAfter) != 1 || agentsAfter[0].ID != agents[0].ID || agentsAfter[0].Runner != "local" {
		t.Fatalf("agents changed %+v", agentsAfter)
	}
	var defCount, strayAgents int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM definitions`).Scan(&defCount); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM agents WHERE definition_id <> $1`, defID).Scan(&strayAgents); err != nil {
		t.Fatal(err)
	}
	if defCount != 1 || strayAgents != 0 {
		t.Fatalf("definitions %d stray agents %d", defCount, strayAgents)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- h.st.Migrate(ctx)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var files, distinct int
	if err := h.raw.QueryRow(ctx, `SELECT count(*), count(DISTINCT filename) FROM schema_migrations`).Scan(&files, &distinct); err != nil {
		t.Fatal(err)
	}
	if files != 6 || distinct != 6 {
		t.Fatalf("schema_migrations rows %d distinct %d", files, distinct)
	}
	nameRows, err := h.raw.Query(ctx, `SELECT filename FROM schema_migrations ORDER BY filename`)
	if err != nil {
		t.Fatal(err)
	}
	defer nameRows.Close()
	var filenames []string
	for nameRows.Next() {
		var filename string
		if err := nameRows.Scan(&filename); err != nil {
			t.Fatal(err)
		}
		filenames = append(filenames, filename)
	}
	if err := nameRows.Err(); err != nil {
		t.Fatal(err)
	}
	wantFiles := []string{
		"0001_init.sql", "0002_m3.sql", "0003_m4.sql", "0004_automation.sql",
		"0005_building_guard.sql", "0006_m5.sql",
	}
	if strings.Join(filenames, ",") != strings.Join(wantFiles, ",") {
		t.Fatalf("filenames %v", filenames)
	}
	var tables int
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_name IN (
		    'factories', 'definitions', 'agents', 'runners', 'runs', 'executions',
		    'run_events', 'workers', 'run_queue', 'schema_migrations',
		    'work_items', 'approvals', 'routers', 'skills', 'automations',
		    'scorers', 'webhooks', 'deliveries', 'integration_installations',
		    'webhook_registrations', 'schedules', 'leader_leases',
		    'external_refs', 'outbound_threads', 'input_requests',
		    'intake_slots', 'delivery_effects',
		    'worker_rejections', 'steer_requests', 'automation_overrides'
		  )`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 30 {
		t.Fatalf("tables %d", tables)
	}
	assertColumns(t, h, "runs", []string{
		"work_item_id", "parent_run_id", "conversation_id", "stage", "model_id",
		"failure_class", "failure_code", "assigned_at",
	})
	assertColumns(t, h, "workers", []string{
		"backend", "version", "reconnects", "in_flight", "connected_at",
	})
	assertColumns(t, h, "run_events", []string{"gid"})
	assertColumns(t, h, "worker_rejections", []string{"id", "worker_id", "run_id", "at"})
	assertColumns(t, h, "steer_requests", []string{
		"id", "run_id", "kind", "text", "created_at", "delivered_at", "actor",
		"claimed_by", "claim_expires_at",
	})
	var failureClassIdx bool
	if err := h.raw.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_indexes
		    WHERE schemaname = current_schema()
		      AND indexname = 'runs_failure_class_idx'
		)`).Scan(&failureClassIdx); err != nil {
		t.Fatal(err)
	}
	if !failureClassIdx {
		t.Fatal("runs_failure_class_idx missing")
	}
	assertColumns(t, h, "automation_overrides", []string{
		"factory_id", "name", "paused", "updated_at",
	})
	assertColumns(t, h, "work_items", []string{
		"id", "factory_id", "source", "source_ref", "title", "body", "stage", "status",
		"created_by", "conversation_id", "revision_count", "created_at",
		"suspended_run_id", "suspended_execution_id", "suspended_at",
		"open_pr_url",
	})
	assertColumns(t, h, "intake_slots", []string{
		"work_item_id", "automation", "delivery_id", "run_id",
	})
	assertColumns(t, h, "delivery_effects", []string{
		"delivery_id", "work_item_id", "kind", "run_id", "note", "seq",
	})
	assertColumns(t, h, "approvals", []string{
		"id", "work_item_id", "gate", "requested_at", "decided_at", "decided_by",
		"decision", "note",
	})
	assertColumns(t, h, "routers", []string{
		"id", "definition_id", "factory_id", "name", "type", "default_model", "routing_json",
	})
	assertColumns(t, h, "skills", []string{
		"id", "definition_id", "factory_id", "agent_id", "name", "path", "content_digest",
	})
	assertColumns(t, h, "automations", []string{
		"id", "definition_id", "factory_id", "name", "enabled", "triggers_json", "prompt",
		"overrides_json",
	})
	assertColumns(t, h, "scorers", []string{
		"id", "definition_id", "factory_id", "name", "labels_json", "passing_score",
		"sampling_rate", "model", "self_improve",
	})
	assertColumns(t, h, "webhooks", []string{
		"id", "definition_id", "factory_id", "name", "auth_mode", "signature_scheme",
		"delivery_id_header", "spec_json",
	})
}

func TestNoSecretColumns(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	rows, err := h.raw.Query(ctx, `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tokenCols int
	for rows.Next() {
		var table, column, dataType string
		if err := rows.Scan(&table, &column, &dataType); err != nil {
			t.Fatal(err)
		}
		if column == "run_token_sha256" {
			tokenCols++
			if table != "executions" || dataType != "bytea" {
				t.Fatalf("run_token_sha256 on %s type %s", table, dataType)
			}
			continue
		}
		// Names and digests, not secret values. Same class as run_token_sha256.
		if table == "webhook_registrations" && column == "secret_ref" && dataType == "text" {
			continue
		}
		if table == "webhook_registrations" && column == "url_token_hash" && dataType == "bytea" {
			continue
		}
		if secretish(column) || strings.Contains(strings.ToLower(column), "env_var") {
			t.Fatalf("secret column %s.%s", table, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if tokenCols != 1 {
		t.Fatalf("run_token_sha256 columns %d", tokenCols)
	}
}

func TestGetRunNotFound(t *testing.T) {
	h := newHarness(t, true)
	_, err := h.st.GetRun(context.Background(), "run_missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestRequestCancel(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)

	queued := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	cancelled, err := h.st.RequestCancel(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != store.StatusCancelled || cancelled.ID != queued.ID {
		t.Fatalf("cancel %+v", cancelled)
	}
	leased, err := h.st.Lease(ctx, "default", 30*time.Second, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 0 {
		t.Fatalf("cancelled run leased %+v", leased)
	}
	stillQueued, err := h.st.ListRuns(ctx, store.RunFilter{FactoryID: factoryID, Status: store.StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if len(stillQueued) != 0 {
		t.Fatalf("queued after cancel %+v", stillQueued)
	}

	running := h.createRun(t, factoryID, agentID, "default", store.PriorityInteractive)
	if _, err := h.st.Lease(ctx, "default", 30*time.Second, 1); err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, running.ID, "worker-a", tokenHash("cancel"))
	if err != nil {
		t.Fatal(err)
	}
	marked, err := h.st.RequestCancel(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Status != store.StatusRunning || !marked.CancelRequested {
		t.Fatalf("running cancel %+v", marked)
	}
	applied, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCancelled})
	if err != nil || !applied {
		t.Fatalf("finish applied %v err %v", applied, err)
	}
	final, err := h.st.GetRun(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != store.StatusCancelled {
		t.Fatalf("final %s", final.Status)
	}
	if _, err := h.st.RequestCancel(ctx, running.ID); !errors.Is(err, store.ErrAlreadyTerminal) {
		t.Fatalf("second cancel err %v", err)
	}
}

func TestUpsertAndTouchWorker(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	worker := store.Worker{
		ID:              "w1",
		Pool:            "default",
		Platform:        "linux",
		LabelsJSON:      json.RawMessage(`{"slot":"a"}`),
		LastHeartbeatAt: at,
		MaxConcurrent:   2,
		Status:          "connected",
	}
	if err := h.st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Minute)
	if err := h.st.TouchWorker(ctx, "w1", later); err != nil {
		t.Fatal(err)
	}
	var pool, platform string
	var hb time.Time
	var maxc int
	if err := h.raw.QueryRow(ctx, `SELECT pool, platform, last_heartbeat_at, max_concurrent FROM workers WHERE id = $1`, "w1").Scan(&pool, &platform, &hb, &maxc); err != nil {
		t.Fatal(err)
	}
	if pool != "default" || platform != "linux" || !hb.Equal(later) || maxc != 2 {
		t.Fatalf("worker pool %s platform %s hb %s max %d", pool, platform, hb, maxc)
	}
	worker.Pool = "gpu"
	worker.MaxConcurrent = 4
	if err := h.st.UpsertWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT pool, max_concurrent FROM workers WHERE id = $1`, "w1").Scan(&pool, &maxc); err != nil {
		t.Fatal(err)
	}
	if pool != "gpu" || maxc != 4 {
		t.Fatalf("upsert pool %s max %d", pool, maxc)
	}
	if err := h.st.TouchWorker(ctx, "missing", later); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("touch missing err %v", err)
	}
}

type harness struct {
	st  *store.Store
	raw *pgx.Conn
}

func newHarness(t *testing.T, migrate bool) *harness {
	t.Helper()
	base := os.Getenv("FACTORY_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	ctx := context.Background()
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(entropy[:])
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	rawDSN, err := withSearchPath(base, schema, false)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	poolDSN, err := withSearchPath(base, schema, true)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	st, err := store.Open(ctx, poolDSN)
	if err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	raw, err := pgx.Connect(ctx, rawDSN)
	if err != nil {
		st.Close()
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		if err := raw.Close(context.Background()); err != nil {
			t.Errorf("close db: %v", err)
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		if err := admin.Close(context.Background()); err != nil {
			t.Errorf("close admin: %v", err)
		}
	})
	if migrate {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return &harness{st: st, raw: raw}
}

func (h *harness) seed(t *testing.T) (factoryID, agentID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.st.ApplyDefinition(ctx, "acme", validDefinition()); err != nil {
		t.Fatal(err)
	}
	fac, err := h.st.GetFactory(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := h.st.ListAgents(ctx, fac.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents %+v", agents)
	}
	return fac.ID, agents[0].ID
}

func (h *harness) createRun(t *testing.T, factoryID, agentID, pool string, priority store.Priority) store.Run {
	t.Helper()
	run, err := h.st.CreateRun(context.Background(), store.NewRun{
		FactoryID: factoryID,
		AgentID:   agentID,
		Pool:      pool,
		Priority:  priority,
		Prompt:    "say hello",
		Title:     "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func validDefinition() store.AppliedDefinition {
	return store.AppliedDefinition{
		SchemaVersion: "v1alpha1",
		Alias:         "acme",
		GitSHA:        "abc",
		SpecJSON:      json.RawMessage(`{"name":"acme"}`),
		Runners: []store.DefinedRunner{{
			Name:              "local",
			PlatformJSON:      json.RawMessage(`{"os":"linux"}`),
			InstanceShapeJSON: json.RawMessage(`{"cpu":1}`),
			SetupCommands:     json.RawMessage(`["true"]`),
		}},
		Agents: []store.DefinedAgent{{
			Name:      "implementer",
			AgentType: "IMPLEMENT",
			Runner:    "local",
			SpecJSON:  json.RawMessage(`{"model":"auto"}`),
		}},
	}
}

func withSearchPath(raw, schema string, pool bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	if pool {
		q.Set("pool_max_conns", "16")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func eventBatch(start, n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		out[i] = json.RawMessage(fmt.Sprintf(`{"type":"message.delta","text":"%d"}`, start+i))
	}
	return out
}

func tokenHash(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func intPtr(n int) *int { return &n }

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func int64sEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertColumns(t *testing.T, h *harness, table string, want []string) {
	t.Helper()
	rows, err := h.raw.Query(context.Background(), `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("%s missing column %s", table, name)
		}
	}
}

func assertNoSecretFields(t *testing.T, typ reflect.Type) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if secretish(field.Name) {
			t.Fatalf("%s.%s looks like a secret", typ.Name(), field.Name)
		}
	}
}

func secretish(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "secret") || strings.Contains(n, "token") || strings.Contains(n, "password") || strings.Contains(n, "envvar") || strings.Contains(n, "api_key") || strings.Contains(n, "apikey")
}
