package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

func TestFailureClassBackfillCASEMatchesMigration(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("migrations/0006_m5.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), store.FailureBackfillCASE) {
		t.Fatal("0006_m5.sql does not contain store.FailureBackfillCASE verbatim")
	}
}

func TestFailureClassBackfill(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	applyThrough0005(t, h)

	factoryID, agentID := h.seed(t)
	exitCode := func(n int) *int { return &n }
	exit10 := insertPreM5Run(t, h, factoryID, agentID, preM5Run{
		status: "failed", reason: "container_exit", exit: exitCode(10),
		execState: "failed", event: "message.delta",
		payload: `{"type":"message.delta","text":"before gid"}`,
	})
	cases := []classCase{{exit10, "agent", "agent_reported"}}
	cases = append(cases,
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(20), execState: "failed",
		}), "configuration", "config_invalid"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "model_unavailable", execState: "failed",
		}), "infrastructure", "model_unavailable"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "environment_setup_failed", execState: "failed",
		}), "infrastructure", "environment_setup_failed"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(137), execState: "failed",
			details: `{"kubernetes":{"container":{"termination_reason":"OOMKilled"}}}`,
		}), "infrastructure", "oom_killed"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(143), execState: "failed",
			details: `{"kubernetes":{"pod":{"reason":"Evicted"}}}`,
		}), "infrastructure", "evicted"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(137), execState: "failed",
			details: `{"kubernetes":{"events":[{"reason":"OOMKilled"}]}}`,
		}), "infrastructure", "oom_killed"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(137), execState: "failed",
		}), "infrastructure", "container_killed"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(9), execState: "failed",
		}), "infrastructure", "container_exit"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "worker_gone", execState: "failed",
		}), "infrastructure", "worker_lost"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "lost", execState: "orphaned",
		}), "infrastructure", "worker_lost"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "disk_full", execState: "failed",
		}), "infrastructure", "unknown"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "container_exit", exit: exitCode(1), execState: "failed",
			event: "terminal", payload: `{"type":"terminal","status":"failed_by_agent"}`,
		}), "agent", "agent_reported"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", execState: "failed",
		}), "", ""},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "succeeded", execState: "completed",
		}), "", ""},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "failed", reason: "superseded_duplicate_building_run",
		}), "cancelled", "superseded"},
		classCase{insertPreM5Run(t, h, factoryID, agentID, preM5Run{
			status: "cancelled",
		}), "cancelled", "cancelled"},
	)

	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertClasses(t, h, cases)

	var events, withGID int
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*), count(gid)
		FROM run_events
		WHERE run_id = $1`, exit10).Scan(&events, &withGID); err != nil {
		t.Fatal(err)
	}
	if events == 0 || events != withGID {
		t.Fatalf("events %d with gid %d", events, withGID)
	}

	if err := h.st.SetRunClass(ctx, exit10, "agent", "manual"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/0006_m5.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	cases[0].code = "manual"
	assertClasses(t, h, cases)

	if err := h.st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var files int
	if err := h.raw.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 6 {
		t.Fatalf("schema_migrations %d", files)
	}
}

func TestSetRunClass(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	if err := h.st.SetRunClass(ctx, run.ID, "infrastructure", "unknown"); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureClass != "infrastructure" || got.FailureCode != "unknown" {
		t.Fatalf("class %s/%s", got.FailureClass, got.FailureCode)
	}
	if err := h.st.SetRunClass(ctx, run.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	cleared, err := h.st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.FailureClass != "" || cleared.FailureCode != "" {
		t.Fatalf("cleared %s/%s", cleared.FailureClass, cleared.FailureCode)
	}
	if err := h.st.SetRunClass(ctx, "run_missing", "agent", "agent_reported"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
}

func TestListWorkers(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	if _, err := h.st.ListWorkers(ctx, time.Time{}); err == nil {
		t.Fatal("zero now accepted")
	}
	now := time.Now().UTC().Add(time.Second)
	hb := now.Add(-1500 * time.Millisecond)
	if err := h.st.UpsertWorker(ctx, store.Worker{
		ID: "w-b", Pool: "b", Platform: "linux", LabelsJSON: json.RawMessage(`{"slot":"b"}`),
		LastHeartbeatAt: hb, MaxConcurrent: 2, Status: "connected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpsertWorker(ctx, store.Worker{
		ID: "w-a", Pool: "a", Platform: "darwin", MaxConcurrent: 4, Status: "connected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkWorkerConnected(ctx, "w-a", "1.2.3", "docker"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkWorkerConnected(ctx, "w-a", "1.2.4", "podman"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetWorkerInFlight(ctx, "w-a", 3); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetWorkerInFlight(ctx, "w-a", -1); err == nil {
		t.Fatal("negative in_flight accepted")
	}
	if err := h.st.MarkWorkerConnected(ctx, "missing", "0", "docker"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing worker %v", err)
	}

	run := h.createRun(t, factoryID, agentID, "a", store.PriorityBatch)
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO worker_rejections (worker_id, run_id, at)
		VALUES ('w-a', $1, $2)`, run.ID, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO worker_rejections (worker_id, run_id, at)
		VALUES ('w-a', $1, $2)`, run.ID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordRejection(ctx, "w-a", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordRejection(ctx, "w-a", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordRejection(ctx, "missing", run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing worker rejection %v", err)
	}
	var old int
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*) FROM worker_rejections
		WHERE at < clock_timestamp() - interval '7 days'`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old != 0 {
		t.Fatalf("rejections older than 7 days %d", old)
	}

	listed, err := h.st.ListWorkers(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != "w-a" || listed[1].ID != "w-b" {
		t.Fatalf("order %+v", listed)
	}
	a := listed[0]
	if a.Backend != "podman" || a.Version != "1.2.4" || a.Reconnects != 1 || a.InFlight != 3 {
		t.Fatalf("session %+v", a)
	}
	if a.HeartbeatAgeMS != -1 || a.ConnectedAt == nil || a.Rejected1h != 2 {
		t.Fatalf("age %d connected %v rejected %d", a.HeartbeatAgeMS, a.ConnectedAt, a.Rejected1h)
	}
	b := listed[1]
	if b.HeartbeatAgeMS != 1500 || b.Reconnects != 0 || b.Rejected1h != 0 || !jsonEqual(b.LabelsJSON, []byte(`{"slot":"b"}`)) {
		t.Fatalf("b %+v labels %s", b, b.LabelsJSON)
	}
}

func TestPoolStatsTimeToAssign(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	now := time.Now().UTC().Truncate(time.Second).Add(time.Minute)
	if err := h.st.UpsertWorker(ctx, store.Worker{
		ID: "w-pool", Pool: "alpha", Platform: "linux", MaxConcurrent: 1, Status: "connected",
	}); err != nil {
		t.Fatal(err)
	}
	fast := startAndFinish(t, h, factoryID, agentID, "alpha", store.Terminal{State: store.ExecCompleted})
	slow := startAndFinish(t, h, factoryID, agentID, "alpha", store.Terminal{State: store.ExecCompleted})
	queued := h.createRun(t, factoryID, agentID, "alpha", store.PriorityBatch)
	only := h.createRun(t, factoryID, agentID, "onlyq", store.PriorityBatch)
	if _, err := h.raw.Exec(ctx, `
		UPDATE runs SET created_at = $2, assigned_at = $3 WHERE id = $1`,
		fast.ID, now.Add(-30*time.Minute), now.Add(-30*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		UPDATE runs SET created_at = $2, assigned_at = $3 WHERE id = $1`,
		slow.ID, now.Add(-40*time.Minute), now.Add(-40*time.Minute+3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO worker_rejections (worker_id, run_id, at) VALUES ('w-pool', $1, $2)`,
		queued.ID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordRejection(ctx, "w-pool", queued.ID); err != nil {
		t.Fatal(err)
	}
	_ = only

	stats, err := h.st.PoolStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	byPool := map[string]store.PoolStat{}
	for _, stat := range stats {
		byPool[stat.Pool] = stat
	}
	alpha, ok := byPool["alpha"]
	if !ok || alpha.QueueDepth != 1 || alpha.Rejected1h != 1 || alpha.P50TimeToAssignMS == nil || *alpha.P50TimeToAssignMS != 2000 {
		t.Fatalf("alpha %+v", alpha)
	}
	quiet, ok := byPool["onlyq"]
	if !ok || quiet.QueueDepth != 1 || quiet.Rejected1h != 0 || quiet.P50TimeToAssignMS != nil {
		t.Fatalf("onlyq %+v", quiet)
	}
}

func TestSteerExactlyOnce(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	factoryID, agentID := h.seed(t)
	cursorRun := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	ids := make([]string, 3)
	for i, kind := range []string{store.SteerInterrupt, store.SteerFollowup, store.SteerFollowup} {
		text := "keep going"
		if i == 2 {
			text = ""
		}
		st, err := h.st.EnqueueSteer(ctx, cursorRun.ID, kind, text, "ada")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(st.ID, store.PrefixSteer) || st.Kind != kind || st.Text != text || st.Actor != "ada" {
			t.Fatalf("steer %+v", st)
		}
		ids[i] = st.ID
	}
	if _, err := h.st.EnqueueSteer(ctx, cursorRun.ID, "sideways", "x", ""); err == nil {
		t.Fatal("bad kind accepted")
	}
	if _, err := h.st.EnqueueSteer(ctx, "run_missing", store.SteerInterrupt, "x", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing run %v", err)
	}
	sort.Strings(ids)
	page, err := h.st.NextSteers(ctx, cursorRun.ID, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != ids[1] || page[1].ID != ids[2] {
		t.Fatalf("after cursor %+v want %v", page, ids[1:])
	}
	for _, st := range page {
		if err := h.st.MarkSteerDelivered(ctx, st.ID); err != nil {
			t.Fatal(err)
		}
		if err := h.st.MarkSteerDelivered(ctx, st.ID); err != nil {
			t.Fatal(err)
		}
	}
	left, err := h.st.NextSteers(ctx, cursorRun.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].ID != ids[0] {
		t.Fatalf("remaining %+v", left)
	}
	if err := h.st.MarkSteerDelivered(ctx, left[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkSteerDelivered(ctx, "str_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing steer %v", err)
	}

	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	const n = 16
	want := map[string]struct{}{}
	for range n {
		st, err := h.st.EnqueueSteer(ctx, run.ID, store.SteerInterrupt, "stop", "ada")
		if err != nil {
			t.Fatal(err)
		}
		want[st.ID] = struct{}{}
	}
	var mu sync.Mutex
	got := map[string]int{}
	errCh := make(chan error, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				batch, err := h.st.NextSteers(ctx, run.ID, "")
				if err != nil {
					errCh <- err
					return
				}
				if len(batch) == 0 {
					return
				}
				for _, st := range batch {
					mu.Lock()
					got[st.ID]++
					mu.Unlock()
					if err := h.st.MarkSteerDelivered(ctx, st.ID); err != nil {
						errCh <- err
						return
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if len(got) != n {
		t.Fatalf("distinct %d", len(got))
	}
	for id := range want {
		if got[id] != 1 {
			t.Fatalf("steer %s delivered %d times", id, got[id])
		}
	}
	var delivered int
	if err := h.raw.QueryRow(ctx, `
		SELECT count(*) FROM steer_requests
		WHERE run_id = $1 AND delivered_at IS NOT NULL`, run.ID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered != n {
		t.Fatalf("delivered rows %d", delivered)
	}
}

func TestSteerClaimRollsBackWhenStoreCloses(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	st, err := h.st.EnqueueSteer(ctx, run.ID, store.SteerFollowup, "later", "ada")
	if err != nil {
		t.Fatal(err)
	}
	other := openSchemaStore(t, h)
	claimed, err := other.NextSteers(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != st.ID {
		t.Fatalf("claimed %+v", claimed)
	}
	other.Close()
	again, err := h.st.NextSteers(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].ID != st.ID {
		t.Fatalf("after close %+v", again)
	}
	if err := h.st.MarkSteerDelivered(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	empty, err := h.st.NextSteers(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("still pending %+v", empty)
	}
}

func TestRunTimelinePhases(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	bare := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	open, err := h.st.RunTimeline(ctx, bare.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Phase != store.PhaseQueued || open[0].DurationMS != 0 || open[0].ExecutionID != "" {
		t.Fatalf("open %+v", open)
	}
	if _, err := h.st.RunTimeline(ctx, "run_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing %v", err)
	}

	run, exec := startRunning(t, h, factoryID, agentID, "timeline")
	if _, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	t0 := base
	t1 := base.Add(2 * time.Second)
	t2 := base.Add(5 * time.Second)
	t4 := base.Add(9 * time.Second)
	t5 := base.Add(12 * time.Second)
	t6 := base.Add(14 * time.Second)
	if _, err := h.raw.Exec(ctx, `
		UPDATE runs
		SET created_at = $2, assigned_at = $3, started_at = $4, ended_at = $5
		WHERE id = $1`, run.ID, t0, t1, t2, t6); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		UPDATE executions SET started_at = $2, ended_at = $3 WHERE id = $1`, exec.ID, t2, t4); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO executions (
		    id, run_id, attempt, worker_id, state, run_token_sha256, started_at, ended_at)
		VALUES ('exec_second', $1, 2, 'worker-a', 'failed', $2, $3, $4)`,
		run.ID, tokenHash("second"), t5, t6); err != nil {
		t.Fatal(err)
	}
	phases, err := h.st.RunTimeline(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Phase{
		{Phase: store.PhaseQueued, At: t0, DurationMS: 2000, ExecutionID: exec.ID},
		{Phase: store.PhaseAssigned, At: t1, DurationMS: 3000, ExecutionID: exec.ID},
		{Phase: store.PhaseRunning, At: t2, DurationMS: 4000, ExecutionID: exec.ID},
		{Phase: store.PhaseTerminal, At: t4, DurationMS: 0, ExecutionID: exec.ID},
		{Phase: store.PhaseQueued, At: t4, DurationMS: 3000, ExecutionID: "exec_second"},
		{Phase: store.PhaseRunning, At: t5, DurationMS: 2000, ExecutionID: "exec_second"},
		{Phase: store.PhaseTerminal, At: t6, DurationMS: 0, ExecutionID: "exec_second"},
	}
	if len(phases) != len(want) {
		t.Fatalf("phases %+v", phases)
	}
	for i := range want {
		got := phases[i]
		if got.Phase != want[i].Phase || !got.At.Equal(want[i].At) || got.DurationMS != want[i].DurationMS || got.ExecutionID != want[i].ExecutionID {
			t.Fatalf("phase %d %+v want %+v", i, got, want[i])
		}
	}
}

func TestRunUsageAggregates(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run, exec := startRunning(t, h, factoryID, agentID, "usage")
	if _, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 0, []json.RawMessage{
		json.RawMessage(`{"type":"message.delta","text":"hi"}`),
		json.RawMessage(`{"type":"usage","turn":1,"model_id":"zeta","input_tokens":10,"output_tokens":4,"cost_cents":2}`),
		json.RawMessage(`{"type":"usage","turn":2,"model_id":"alpha","input_tokens":3,"output_tokens":1,"cost_cents":5}`),
		json.RawMessage(`{"type":"usage","turn":3,"model_id":"zeta","input_tokens":7,"output_tokens":8}`),
	}); err != nil {
		t.Fatal(err)
	}
	usage, err := h.st.RunUsage(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Total.InputTokens != 20 || usage.Total.OutputTokens != 13 || usage.Total.CostCents != 7 {
		t.Fatalf("total %+v", usage.Total)
	}
	if len(usage.Calls) != 3 || usage.Calls[0].Seq != 2 || usage.Calls[2].CostCents != 0 || usage.Calls[1].ModelID != "alpha" {
		t.Fatalf("calls %+v", usage.Calls)
	}
	if len(usage.ByModel) != 2 || usage.ByModel[0].ModelID != "alpha" || usage.ByModel[0].Calls != 1 {
		t.Fatalf("by model %+v", usage.ByModel)
	}
	if usage.ByModel[1].ModelID != "zeta" || usage.ByModel[1].InputTokens != 17 || usage.ByModel[1].Calls != 2 {
		t.Fatalf("zeta %+v", usage.ByModel[1])
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
		VALUES ($1, 99, $2, 99, clock_timestamp(), 'usage', '[]'::jsonb)`, run.ID, exec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.RunUsage(ctx, run.ID); err == nil {
		t.Fatal("invalid usage payload accepted")
	}
	if _, err := h.st.RunUsage(ctx, "run_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
}

func TestFactoryEventsResumeNoGap(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryID, agentID := h.seed(t)
	if _, err := h.st.ApplyDefinition(ctx, "beta", validDefinition()); err != nil {
		t.Fatal(err)
	}
	beta, err := h.st.GetFactory(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	betaAgents, err := h.st.ListAgents(ctx, beta.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, exec := startRunning(t, h, factoryID, agentID, "events-a")
	_, otherExec := startRunning(t, h, beta.ID, betaAgents[0].ID, "events-b")
	if _, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 0, eventBatch(0, 5)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendEvents(ctx, otherExec.RunID, otherExec.ID, 0, eventBatch(0, 1)); err != nil {
		t.Fatal(err)
	}

	var cursor int64
	var got []int64
	for {
		page, err := h.st.FactoryEventsFrom(ctx, factoryID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 2 {
			t.Fatalf("page %d", len(page))
		}
		for _, ev := range page {
			if ev.GID <= cursor || ev.RunID != run.ID {
				t.Fatalf("event %+v cursor %d", ev, cursor)
			}
			got = append(got, ev.GID)
			cursor = ev.GID
		}
	}
	want := factoryGIDs(t, h, factoryID)
	if !int64sEqual(got, want) {
		t.Fatalf("gids %v want %v", got, want)
	}

	ch, err := h.st.Listen(ctx, "run_events")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendEvents(ctx, run.ID, exec.ID, 5, eventBatch(5, 1)); err != nil {
		t.Fatal(err)
	}
	waitNotify(t, ch, run.ID)
	rest, err := h.st.FactoryEventsFrom(ctx, factoryID, cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 1 || rest[0].GID <= cursor || rest[0].RunID != run.ID {
		t.Fatalf("resume %+v", rest)
	}
	got = append(got, rest[0].GID)
	if !int64sEqual(got, factoryGIDs(t, h, factoryID)) {
		t.Fatalf("after resume %v", got)
	}
	if _, err := h.st.FactoryEventsFrom(ctx, factoryID, -1, 1); err == nil {
		t.Fatal("negative cursor accepted")
	}
	if _, err := h.st.FactoryEventsFrom(ctx, factoryID, 0, 0); err == nil {
		t.Fatal("zero limit accepted")
	}
	if _, err := h.st.FactoryEventsFrom(ctx, "fac_missing", 0, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing factory %v", err)
	}
}

func TestInboxOldestFirst(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	if _, err := h.st.ApplyDefinition(ctx, "beta", validDefinition()); err != nil {
		t.Fatal(err)
	}
	beta, err := h.st.GetFactory(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)

	approvalItem := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "Needs approval"})
	approval, err := h.st.RequestApproval(ctx, approvalItem.ID, "ship")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE approvals SET requested_at = $2 WHERE id = $1`, approval.ID, base); err != nil {
		t.Fatal(err)
	}

	questionItem := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "Needs answer"})
	questionRun := h.createRun(t, factoryID, agentID, "hold", store.PriorityBatch)
	question, err := h.st.CreateInputRequest(ctx, questionItem.ID, questionRun.ID, "", "which region?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE input_requests SET created_at = $2 WHERE id = $1`, question.ID, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	failedItem := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "Broken deploy"})
	failed := createLinkedRun(t, h, factoryID, agentID, failedItem.ID, "Broken deploy")
	failedExec := leaseAndStart(t, h, failed)
	if _, err := h.st.FinishExecution(ctx, failedExec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRunClass(ctx, failed.ID, "infrastructure", "disk_full"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET ended_at = $2 WHERE id = $1`, failed.ID, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	staleItem := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "Old failure"})
	stale := createLinkedRun(t, h, factoryID, agentID, staleItem.ID, "Old failure")
	staleExec := leaseAndStart(t, h, stale)
	if _, err := h.st.FinishExecution(ctx, staleExec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE runs SET ended_at = clock_timestamp() - interval '8 days' WHERE id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}

	hiddenItem := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "Superseded failure"})
	hidden := createLinkedRun(t, h, factoryID, agentID, hiddenItem.ID, "Superseded failure")
	hiddenExec := leaseAndStart(t, h, hidden)
	if _, err := h.st.FinishExecution(ctx, hiddenExec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRunClass(ctx, hidden.ID, "cancelled", "superseded"); err != nil {
		t.Fatal(err)
	}
	later := createLinkedRun(t, h, factoryID, agentID, hiddenItem.ID, "retry")
	laterExec := leaseAndStart(t, h, later)
	if _, err := h.st.FinishExecution(ctx, laterExec.ID, store.Terminal{State: store.ExecCompleted}); err != nil {
		t.Fatal(err)
	}

	orphan := h.createRun(t, factoryID, agentID, "inbox", store.PriorityBatch)
	orphanExec := leaseAndStart(t, h, orphan)
	if _, err := h.st.FinishExecution(ctx, orphanExec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"}); err != nil {
		t.Fatal(err)
	}

	betaItem := createWorkItem(t, h, beta.ID, store.NewWorkItem{Source: store.SourceIssue, Title: "Other factory"})
	if _, err := h.st.RequestApproval(ctx, betaItem.ID, "ship"); err != nil {
		t.Fatal(err)
	}

	items, err := h.st.Inbox(ctx, factoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("inbox %+v", items)
	}
	if items[0].Kind != store.InboxApproval || items[0].ID != approval.ID || items[0].Action != store.InboxActionApprove || items[0].Detail != "ship" || items[0].RunID != "" || items[0].Title != "Needs approval" {
		t.Fatalf("approval %+v", items[0])
	}
	if items[1].Kind != store.InboxQuestion || items[1].ID != question.ID || items[1].Action != store.InboxActionAnswer || items[1].Detail != "which region?" || items[1].RunID != questionRun.ID {
		t.Fatalf("question %+v", items[1])
	}
	if items[2].Kind != store.InboxFailed || items[2].ID != failed.ID || items[2].Action != store.InboxActionOpen || items[2].Detail != "disk_full" || items[2].Title != "Broken deploy" || items[2].RunID != failed.ID {
		t.Fatalf("failed %+v", items[2])
	}
	if items[3].ID != orphan.ID || items[3].WorkItemID != "" || items[3].Detail != "disk_full" {
		t.Fatalf("orphan %+v", items[3])
	}
	if _, err := h.st.Inbox(ctx, "fac_missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing factory %v", err)
	}
}

func TestAutomationPauseSurvivesApply(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	fac, err := h.st.GetFactory(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetAutomationPaused(ctx, factoryID, "", true); err == nil {
		t.Fatal("empty name accepted")
	}
	if err := h.st.SetAutomationPaused(ctx, "fac_missing", "nightly", true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing factory %v", err)
	}
	if err := h.st.SetAutomationPaused(ctx, factoryID, "early", true); err != nil {
		t.Fatal(err)
	}
	insertAutomation(t, h, "auto_early", fac.CurrentDefinitionID, factoryID, "early", true)
	insertAutomation(t, h, "auto_nightly", fac.CurrentDefinitionID, factoryID, "nightly", true)
	insertAutomation(t, h, "auto_off", fac.CurrentDefinitionID, factoryID, "off", false)
	if err := h.st.SetAutomationPaused(ctx, factoryID, "nightly", true); err != nil {
		t.Fatal(err)
	}

	fired := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	schedule, err := h.st.UpsertSchedule(ctx, store.Schedule{
		FactoryID: factoryID, Automation: "nightly", Name: "main", Cron: "0 * * * *",
		NextFireAt: fired.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE schedules SET last_fired_at = $2 WHERE id = $1`, schedule.ID, fired); err != nil {
		t.Fatal(err)
	}
	delivery, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, DeliveryID: "d1",
		ReceivedAt: fired.Add(-time.Hour), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetMatchedAutomations(ctx, delivery.ID, []string{"nightly"}); err != nil {
		t.Fatal(err)
	}
	other, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, DeliveryID: "d2", Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetMatchedAutomations(ctx, other.ID, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceSchedule, Title: "slot"})
	emptySlot := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceSchedule, Title: "empty slot"})
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO intake_slots (work_item_id, automation, delivery_id, run_id)
		VALUES ($1, 'nightly', 'slot-1', $2), ($3, 'nightly', 'slot-2', '')`,
		item.ID, run.ID, emptySlot.ID); err != nil {
		t.Fatal(err)
	}

	stats, err := h.st.AutomationStats(ctx, factoryID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]store.AutomationStat{}
	for _, stat := range stats {
		byName[stat.Name] = stat
	}
	if len(byName) != 3 {
		t.Fatalf("stats %+v", stats)
	}
	early := byName["early"]
	if !early.RuntimePaused || !early.Enabled || early.MatchCount != 0 || early.RunsStarted != 0 || early.LastFiredAt != nil {
		t.Fatalf("early %+v", early)
	}
	nightly := byName["nightly"]
	if !nightly.RuntimePaused || !nightly.Enabled || nightly.MatchCount != 1 || nightly.RunsStarted != 1 || nightly.LastFiredAt == nil || !nightly.LastFiredAt.Equal(fired) {
		t.Fatalf("nightly %+v", nightly)
	}
	if string(nightly.Triggers) != `["cron"]` {
		t.Fatalf("triggers %s", nightly.Triggers)
	}
	if byName["off"].Enabled || byName["off"].RuntimePaused {
		t.Fatalf("off %+v", byName["off"])
	}

	if _, err := h.st.ApplyDefinition(ctx, "acme", validDefinition()); err != nil {
		t.Fatal(err)
	}
	again, err := h.st.GetFactory(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if again.CurrentDefinitionID == fac.CurrentDefinitionID {
		t.Fatal("definition did not change")
	}
	insertAutomation(t, h, "auto_nightly_2", again.CurrentDefinitionID, factoryID, "nightly", true)
	stats, err = h.st.AutomationStats(ctx, factoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Name != "nightly" || !stats[0].RuntimePaused {
		t.Fatalf("after apply %+v", stats)
	}
}

func TestFactorySummaryCounts(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	if _, err := h.st.FactorySummary(ctx, factoryID, to, from); err == nil {
		t.Fatal("empty range accepted")
	}

	failed := startAndFinish(t, h, factoryID, agentID, "summary", store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"})
	other := startAndFinish(t, h, factoryID, agentID, "summary", store.Terminal{State: store.ExecFailed, FailureReason: "disk_full"})
	succeeded := startAndFinish(t, h, factoryID, agentID, "summary", store.Terminal{State: store.ExecCompleted})
	cancelled := h.createRun(t, factoryID, agentID, "summary", store.PriorityBatch)
	if _, err := h.st.RequestCancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	active := h.createRun(t, factoryID, agentID, "summary", store.PriorityBatch)
	edge := h.createRun(t, factoryID, agentID, "summary", store.PriorityBatch)
	if _, err := h.st.RequestCancel(ctx, edge.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRunClass(ctx, failed.ID, "infrastructure", "unknown"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRunClass(ctx, other.ID, "agent", "agent_reported"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetRunClass(ctx, cancelled.ID, "cancelled", "cancelled"); err != nil {
		t.Fatal(err)
	}
	setRunWindow(t, h, failed.ID, from.Add(time.Hour), from.Add(time.Hour+time.Second))
	setRunWindow(t, h, other.ID, from.Add(2*time.Hour), from.Add(2*time.Hour+3*time.Second))
	setRunWindow(t, h, succeeded.ID, from.Add(3*time.Hour), time.Time{})
	setRunWindow(t, h, cancelled.ID, from.Add(4*time.Hour), time.Time{})
	setRunWindow(t, h, edge.ID, to, time.Time{})
	_ = active

	item := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "gate"})
	if _, err := h.st.RequestApproval(ctx, item.ID, "ship"); err != nil {
		t.Fatal(err)
	}
	questionItem := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "ask"})
	if _, err := h.st.CreateInputRequest(ctx, questionItem.ID, active.ID, "", "why?"); err != nil {
		t.Fatal(err)
	}
	pr := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "pr"})
	if _, _, err := h.st.ClaimOpenPullRequest(ctx, pr.ID, "https://example.test/pull/1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE work_items SET created_at = $2 WHERE id = $1`, pr.ID, from.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	outside := createWorkItem(t, h, factoryID, store.NewWorkItem{Source: store.SourceIssue, Title: "old pr"})
	if _, _, err := h.st.ClaimOpenPullRequest(ctx, outside.ID, "https://example.test/pull/2"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE work_items SET created_at = $2 WHERE id = $1`, outside.ID, from.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	unmatched, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, DeliveryID: "sum-1",
		ReceivedAt: from.Add(time.Hour), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkDelivery(ctx, unmatched.ID, store.DeliveryUnmatched); err != nil {
		t.Fatal(err)
	}
	old, _, err := h.st.InsertDelivery(ctx, store.Delivery{
		FactoryID: factoryID, Provider: store.ProviderWebhook, DeliveryID: "sum-2",
		ReceivedAt: from.Add(-time.Hour), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkDelivery(ctx, old.ID, store.DeliveryUnmatched); err != nil {
		t.Fatal(err)
	}

	summary, err := h.st.FactorySummary(ctx, factoryID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunsByStatus["failed"] != 2 || summary.RunsByStatus["succeeded"] != 1 || summary.RunsByStatus["cancelled"] != 1 || summary.RunsByStatus["queued"] != 0 {
		t.Fatalf("status %+v", summary.RunsByStatus)
	}
	if summary.RunsByFailureClass["infrastructure"] != 1 || summary.RunsByFailureClass["agent"] != 1 || summary.RunsByFailureClass["cancelled"] != 1 {
		t.Fatalf("class %+v", summary.RunsByFailureClass)
	}
	if summary.QueueWaitP50MS == nil || *summary.QueueWaitP50MS != 2000 {
		t.Fatalf("p50 %v", summary.QueueWaitP50MS)
	}
	if summary.ActiveRuns != 1 || summary.WaitingOnHuman != 2 || summary.PRsOpened != 1 || summary.DeliveriesUnmatched != 1 {
		t.Fatalf("summary %+v", summary)
	}
	if _, err := h.st.FactorySummary(ctx, "fac_missing", from, to); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing factory %v", err)
	}
}

type classCase struct {
	id, class, code string
}

func applyThrough0005(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	names := []string{
		"0001_init.sql", "0002_m3.sql", "0003_m4.sql", "0004_automation.sql", "0005_building_guard.sql",
	}
	for _, name := range names {
		body, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.ApplySQLForTest(ctx, string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := h.raw.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := h.raw.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			t.Fatal(err)
		}
	}
}

func assertClasses(t *testing.T, h *harness, cases []classCase) {
	t.Helper()
	for _, c := range cases {
		got, err := h.st.GetRun(context.Background(), c.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.FailureClass != c.class || got.FailureCode != c.code {
			t.Fatalf("run %s class %s/%s want %s/%s", c.id, got.FailureClass, got.FailureCode, c.class, c.code)
		}
	}
}

type preM5Run struct {
	status    string
	reason    string
	execState string
	details   string
	event     string
	payload   string
	exit      *int
}

// insertPreM5Run writes a run in the 0005 shape. CreateRun selects the
// columns 0006 adds, so the fixture cannot go through that method.
func insertPreM5Run(t *testing.T, h *harness, factoryID, agentID string, in preM5Run) string {
	t.Helper()
	ctx := context.Background()
	id, err := store.NewID(store.PrefixRun)
	if err != nil {
		t.Fatal(err)
	}
	var exit any
	if in.exit != nil {
		exit = *in.exit
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO runs (
		    id, factory_id, agent_id, status, priority, pool, prompt, title,
		    failure_reason, exit_code, ended_at)
		VALUES (
		    $1, $2, $3, $4, 1, 'default', 'say hello', $4,
		    NULLIF($5, ''), $6, clock_timestamp())`,
		id, factoryID, agentID, in.status, in.reason, exit); err != nil {
		t.Fatal(err)
	}
	if in.execState == "" && in.event == "" {
		return id
	}
	execID, err := store.NewID(store.PrefixExec)
	if err != nil {
		t.Fatal(err)
	}
	var details any
	if in.details != "" {
		details = []byte(in.details)
	}
	state := in.execState
	if state == "" {
		state = "failed"
	}
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO executions (
		    id, run_id, attempt, worker_id, state, failure_reason, exit_code,
		    failure_details_json, run_token_sha256)
		VALUES ($1, $2, 1, 'worker-a', $3, NULLIF($4, ''), $5, $6::jsonb, $7)`,
		execID, id, state, in.reason, exit, details, tokenHash(id)); err != nil {
		t.Fatal(err)
	}
	if in.event != "" {
		if _, err := h.raw.Exec(ctx, `
			INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
			VALUES ($1, 1, $2, 0, clock_timestamp(), $3, $4::jsonb)`,
			id, execID, in.event, in.payload); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func startAndFinish(t *testing.T, h *harness, factoryID, agentID, pool string, term store.Terminal) store.Run {
	t.Helper()
	run, exec := startRunning(t, h, factoryID, agentID, pool)
	applied, err := h.st.FinishExecution(context.Background(), exec.ID, term)
	if err != nil || !applied {
		t.Fatalf("finish applied %v err %v", applied, err)
	}
	return run
}

func startRunning(t *testing.T, h *harness, factoryID, agentID, pool string) (store.Run, store.Execution) {
	t.Helper()
	run := h.createRun(t, factoryID, agentID, pool, store.PriorityBatch)
	exec := leaseAndStart(t, h, run)
	return run, exec
}

func leaseAndStart(t *testing.T, h *harness, run store.Run) store.Execution {
	t.Helper()
	ctx := context.Background()
	leased, err := h.st.Lease(ctx, run.Pool, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].RunID != run.ID {
		t.Fatalf("leased %+v want %s", leased, run.ID)
	}
	exec, err := h.st.StartExecution(ctx, run.ID, "worker-a", tokenHash(run.ID))
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func createLinkedRun(t *testing.T, h *harness, factoryID, agentID, workItemID, title string) store.Run {
	t.Helper()
	run, err := h.st.CreateRun(context.Background(), store.NewRun{
		FactoryID:  factoryID,
		AgentID:    agentID,
		Pool:       "inbox",
		Priority:   store.PriorityBatch,
		Prompt:     "say hello",
		Title:      title,
		WorkItemID: workItemID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func insertAutomation(t *testing.T, h *harness, id, definitionID, factoryID, name string, enabled bool) {
	t.Helper()
	if _, err := h.raw.Exec(context.Background(), `
		INSERT INTO automations (
		    id, definition_id, factory_id, name, enabled, triggers_json, prompt, overrides_json)
		VALUES ($1, $2, $3, $4, $5, '["cron"]'::jsonb, '', '{}'::jsonb)`,
		id, definitionID, factoryID, name, enabled); err != nil {
		t.Fatal(err)
	}
}

func setRunWindow(t *testing.T, h *harness, id string, created, assigned time.Time) {
	t.Helper()
	var assignedAt any
	if !assigned.IsZero() {
		assignedAt = assigned
	}
	if _, err := h.raw.Exec(context.Background(), `
		UPDATE runs SET created_at = $2, assigned_at = $3 WHERE id = $1`, id, created, assignedAt); err != nil {
		t.Fatal(err)
	}
}

func factoryGIDs(t *testing.T, h *harness, factoryID string) []int64 {
	t.Helper()
	rows, err := h.raw.Query(context.Background(), `
		SELECT run_events.gid
		FROM run_events
		JOIN runs ON runs.id = run_events.run_id
		WHERE runs.factory_id = $1
		ORDER BY run_events.gid`, factoryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			t.Fatal(err)
		}
		out = append(out, gid)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonEqual(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	leftRaw, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightRaw, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftRaw) == string(rightRaw)
}

func waitNotify(t *testing.T, ch <-chan string, want string) {
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

func openSchemaStore(t *testing.T, h *harness) *store.Store {
	t.Helper()
	ctx := context.Background()
	var schema string
	if err := h.raw.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn, err := withSearchPath(os.Getenv("FACTORY_TEST_DATABASE_URL"), schema, true)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
	})
	return st
}
