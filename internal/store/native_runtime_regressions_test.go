package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/store"
)

func TestNativeSettleSecondAttempt(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "attempt2", 1, 2, 2500)
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	reservation, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "hold", Cents: 100, ScopeIDs: scopes, CeilingCents: 2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: "call", AttemptID: "1", Cents: 100,
	}); err != nil {
		t.Fatal(err)
	}
	second, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: "call", AttemptID: "2", Cents: 40, Failed: true,
	})
	if err != nil || second.Duplicate || second.CountedCents != 40 {
		t.Fatalf("second %+v %v", second, err)
	}
	spent := countWhere(t, h, `SELECT spent_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID)
	if spent != 140 {
		t.Fatalf("spent %d", spent)
	}
	charges := countWhere(t, h, `SELECT count(*) FROM budget_charges WHERE call_id = $1`, "call")
	if charges != 2 {
		t.Fatalf("charges %d", charges)
	}

	tightID, tightInv, _, _ := openProof(t, h, "attempt2-ceiling", 1, 2, 100)
	tightScopes := store.AncestorScopes(tightID, tightInv, "coordinator", "lead")
	tight, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: tightInv, IdempotencyKey: "hold", Cents: 100, ScopeIDs: tightScopes, CeilingCents: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: tight.ID, CallID: "tight", AttemptID: "1", Cents: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: tight.ID, CallID: "tight", AttemptID: "2", Cents: 40, Failed: true,
	}); !errors.Is(err, store.ErrBudgetExceeded) {
		t.Fatalf("over ceiling %v", err)
	}
	if countWhere(t, h, `SELECT spent_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+tightInv) != 100 {
		t.Fatal("rejected attempt changed spent")
	}
	if countWhere(t, h, `SELECT count(*) FROM budget_charges WHERE call_id = $1`, "tight") != 1 {
		t.Fatal("rejected attempt left a charge")
	}
}

func TestNativeSettleConcurrentAttempts(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "attempt-race", 1, 2, 2500)
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	reservation, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "hold", Cents: 100, ScopeIDs: scopes, CeilingCents: 2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	cents := []int64{100, 40}
	var mu sync.Mutex
	got := map[string]store.Settlement{}
	errs := raceN(2, func(i int) error {
		out, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
			ReservationID: reservation.ID, CallID: "race", AttemptID: fmt.Sprintf("%d", i), Cents: cents[i],
		})
		if err != nil {
			return err
		}
		mu.Lock()
		got[out.AttemptID] = out
		mu.Unlock()
		return nil
	})
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 {
		t.Fatalf("settlements %+v", got)
	}
	for id, out := range got {
		if out.Duplicate {
			t.Fatalf("attempt %s counted as a duplicate", id)
		}
		want := cents[0]
		if id == "1" {
			want = cents[1]
		}
		if out.CountedCents != want {
			t.Fatalf("attempt %s counted %d", id, out.CountedCents)
		}
	}
	spent := countWhere(t, h, `SELECT spent_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID)
	if spent != 140 {
		t.Fatalf("spent %d", spent)
	}
}

func TestNativeJoinMixedPolicies(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	failed := mixedJoin(t, h, ctx, "mix-fail", store.JoinBlockParent, store.JoinFailParent, true, true)
	if failed.Status != store.NativeFailed || failed.Policy != store.JoinFailParent || failed.SpawnedReplacement {
		t.Fatalf("block then fail %+v", failed)
	}
	blocked := mixedJoin(t, h, ctx, "mix-block", store.JoinFailParent, store.JoinBlockParent, false, true)
	if blocked.Status != store.NativeBlocked || blocked.Policy != store.JoinBlockParent || blocked.SpawnedReplacement {
		t.Fatalf("fail succeeded then block %+v", blocked)
	}
}

func TestNativeCancelDelegateOverlap(t *testing.T) {
	h := newHarness(t, true)
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	watch, err := pgx.Connect(ctx, os.Getenv("FACTORY_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close(context.Background()) })

	factoryID, invocationID, _, rootID := openProof(t, h, "overlap", 4, 20, 50000)
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", ReserveCents: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := h.raw.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(ctx) }()
	var holdPID int
	if err := hold.QueryRow(ctx, `
		SELECT pg_backend_pid() FROM native_tasks WHERE id = $1 FOR UPDATE`, lead.Child.ID).Scan(&holdPID); err != nil {
		t.Fatal(err)
	}
	cancelDone := make(chan error, 1)
	go func() {
		_, err := coordination.CancelSubtree(ctx, h.st, rootID)
		cancelDone <- err
	}()
	waitBlocked(t, ctx, watch, holdPID, 1)

	const overlaps = 12
	errs := make([]error, overlaps)
	var wg sync.WaitGroup
	for i := range overlaps {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = coordination.Delegate(ctx, h.st, store.DelegateInput{
				FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: lead.Child.ID,
				Assignee: "specialist-a", IdempotencyKey: fmt.Sprintf("overlap-%d", i), ReserveCents: 1,
			})
		}(i)
	}
	waitUntil(t, ctx, func() bool {
		var n int
		if err := watch.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%FROM native_policies%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n >= overlaps
	})
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	for i, err := range errs {
		if !errors.Is(err, store.ErrSpawnDenied) {
			t.Fatalf("delegate %d %v", i, err)
		}
	}
	pending := countWhere(t, h, `
		SELECT count(*) FROM native_tasks
		WHERE parent_task_id = $1 AND task_status = 'pending'`, lead.Child.ID)
	if pending != 0 {
		t.Fatalf("pending %d", pending)
	}
}

func TestNativeFactoryScopePerInvocation(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationA, agentID, _ := openProof(t, h, "fac-a", 1, 2, 2500)
	invocationB := insertNativeInvocation(t, h, factoryID, "fac-b")
	if _, _, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationB, AgentID: agentID,
		Tree: proofTree(), CeilingCents: 9000, Slots: 1, MaxLive: 2,
	}); err != nil {
		t.Fatal(err)
	}
	settleInvocation(t, h, factoryID, invocationA, 100, 2500)
	settleInvocation(t, h, factoryID, invocationB, 70, 9000)
	assertRollup(t, h, invocationA, 100)
	assertRollup(t, h, invocationB, 70)
	if countWhere(t, h, `SELECT ceiling_cents FROM budget_scopes WHERE scope_id = $1`, "factory:"+factoryID+":"+invocationA) != 2500 {
		t.Fatal("invocation A factory ceiling")
	}
	if countWhere(t, h, `SELECT ceiling_cents FROM budget_scopes WHERE scope_id = $1`, "factory:"+factoryID+":"+invocationB) != 9000 {
		t.Fatal("invocation B factory ceiling")
	}
	if countWhere(t, h, `SELECT count(*) FROM budget_scopes WHERE scope_id = $1`, "factory:"+factoryID) != 0 {
		t.Fatal("factory spend is shared across invocations")
	}
}

func mixedJoin(t *testing.T, h *harness, ctx context.Context, key, lowerPolicy, higherPolicy string, lowerFailed, higherFailed bool) store.JoinResult {
	t.Helper()
	factoryID, invocationID, agentID, rootID := openProof(t, h, key, 2, 8, 2500)
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, assignee := range []string{"specialist-a", "specialist-b"} {
		if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
			FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: lead.Child.ID,
			Assignee: assignee, IdempotencyKey: assignee,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ids := joinChildIDs(t, h, lead.Child.ID)
	if len(ids) != 2 {
		t.Fatalf("children %v", ids)
	}
	setJoinPolicy(t, h, ids[0], lowerPolicy)
	setJoinPolicy(t, h, ids[1], higherPolicy)
	finishChild(t, h, ctx, ids[0], agentID, lowerFailed)
	finishChild(t, h, ctx, ids[1], agentID, higherFailed)
	before := countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	joined, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: lead.Child.ID})
	if err != nil {
		t.Fatal(err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != before {
		t.Fatal("join synthesized a task")
	}
	return joined
}

func joinChildIDs(t *testing.T, h *harness, parentID string) []string {
	t.Helper()
	rows, err := h.raw.Query(context.Background(), `
		SELECT child_task_id FROM native_joins
		WHERE parent_task_id = $1
		ORDER BY child_task_id`, parentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func setJoinPolicy(t *testing.T, h *harness, childID, policy string) {
	t.Helper()
	if _, err := h.raw.Exec(context.Background(), `
		UPDATE native_joins SET policy = $2 WHERE child_task_id = $1`, childID, policy); err != nil {
		t.Fatal(err)
	}
}

func finishChild(t *testing.T, h *harness, ctx context.Context, taskID, agentID string, failed bool) {
	t.Helper()
	if _, err := coordination.ClaimActivation(ctx, h.st, taskID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Complete(ctx, h.st, taskID, failed); err != nil {
		t.Fatal(err)
	}
}

func settleInvocation(t *testing.T, h *harness, factoryID, invocationID string, cents, ceiling int64) {
	t.Helper()
	ctx := context.Background()
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	reservation, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "hold", Cents: cents, ScopeIDs: scopes, CeilingCents: ceiling,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: invocationID, AttemptID: "1", Cents: cents,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertRollup(t *testing.T, h *harness, invocationID string, want int64) {
	t.Helper()
	roll, err := h.st.RollupSpent(context.Background(), invocationID)
	if err != nil {
		t.Fatal(err)
	}
	if roll["factory"] != want || roll["invocation"] != want {
		t.Fatalf("rollup %s %+v", invocationID, roll)
	}
}

func waitUntil(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for cancel and delegate to overlap")
}

func waitBlocked(t *testing.T, ctx context.Context, watch *pgx.Conn, blockerPID, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		var n int
		if err := watch.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE pg_blocking_pids(pid) @> ARRAY[$1]::int[]`, blockerPID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d sessions blocked by %d", want, blockerPID)
}
