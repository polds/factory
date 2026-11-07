package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/coordination/budget"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestNativeTreePinAndRefusals(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "pin", 2, 4, 2500)
	policy, err := h.st.GetNativePolicy(ctx, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := proofTree()
	reloaded.Personas = append(reloaded.Personas, definition.NativePersona{Name: "extra", ReportsTo: "lead", SpawnableBy: []string{"lead"}})
	again, _, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID,
		Tree: reloaded, CeilingCents: 1, Slots: 2, MaxLive: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Digest != policy.Digest || again.StageOwner != "coordinator" {
		t.Fatalf("reload %+v", again)
	}
	bad := proofTree()
	bad.Personas[0].ReportsTo = "lead"
	if _, _, err := h.st.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: insertNativeInvocation(t, h, factoryID, "cycle"),
		AgentID: agentID, Tree: bad, CeilingCents: 2500, Slots: 1, MaxLive: 4,
	}); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "specialist-a", IdempotencyKey: "nope", ReserveCents: 10,
	}); !errors.Is(err, definition.ErrDelegationDenied) {
		t.Fatalf("delegate %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != 1 {
		t.Fatal("unauthorized spawn wrote a task")
	}
	if countWhere(t, h, `SELECT count(*) FROM budget_reservations WHERE invocation_id = $1`, invocationID) != 0 {
		t.Fatal("unauthorized spawn wrote a reservation")
	}
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", ReserveCents: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE native_tasks SET task_depth = 2 WHERE id = $1`, lead.Child.ID); err != nil {
		t.Fatal(err)
	}
	before := countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: lead.Child.ID,
		Assignee: "specialist-a", IdempotencyKey: "deep", ReserveCents: 10,
	}); !errors.Is(err, definition.ErrDepthExceeded) {
		t.Fatalf("depth %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != before {
		t.Fatal("depth refusal wrote a task")
	}
}

func TestNativeLiveCountAndBudgetRollback(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, rootID := openProof(t, h, "live", 2, 2, 100)
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "one", ReserveCents: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "two", ReserveCents: 0,
	}); !errors.Is(err, store.ErrSpawnDenied) {
		t.Fatalf("live %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != 2 {
		t.Fatal("live cap wrote a task")
	}
	tightID, tightInv, _, tightRoot := openProof(t, h, "budget-deny", 2, 4, 100)
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: tightID, InvocationID: tightInv, ParentTaskID: tightRoot,
		Assignee: "lead", IdempotencyKey: "over", ReserveCents: 500,
	}); !errors.Is(err, store.ErrBudgetExceeded) {
		t.Fatalf("budget %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, tightInv) != 1 {
		t.Fatal("budget refusal wrote a task")
	}
	if countWhere(t, h, `SELECT count(*) FROM budget_reservations WHERE invocation_id = $1`, tightInv) != 0 {
		t.Fatal("budget refusal wrote a reservation")
	}
}

func TestNativeDelegateIdempotent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, rootID := openProof(t, h, "once", 2, 4, 2500)
	first, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "same", ReserveCents: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "same", ReserveCents: 25,
	})
	if err != nil || !second.Replay || second.Child.ID != first.Child.ID || second.ReservationID != first.ReservationID {
		t.Fatalf("replay %+v %v", second, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != 2 {
		t.Fatal("idempotency key wrote two children")
	}
	if countWhere(t, h, `SELECT count(*) FROM budget_reservations WHERE invocation_id = $1`, invocationID) != 1 {
		t.Fatal("idempotency key wrote two reservations")
	}
}

func TestNativeDelegateIdempotentRace(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, rootID := openProof(t, h, "race-key", 2, 8, 2500)
	errs := raceN(8, func(int) error {
		_, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
			FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
			Assignee: "lead", IdempotencyKey: "same", ReserveCents: 10,
		})
		return err
	})
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE parent_task_id = $1`, rootID) != 1 {
		t.Fatal("race wrote more than one child")
	}
}

func TestNativeConcurrentReserve(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "race-budget", 2, 4, 2500)
	other := insertNativeInvocation(t, h, factoryID, "foreign-budget")
	if _, err := h.raw.Exec(ctx, `
		INSERT INTO budget_scopes (scope_id, invocation_id, kind, ceiling_cents)
		VALUES ('factory:foreign-race', $1, 'factory', 90000)`, other); err != nil {
		t.Fatal(err)
	}
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	errs := raceN(2, func(i int) error {
		_, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
			InvocationID: invocationID, IdempotencyKey: fmt.Sprintf("race-%d", i),
			Cents: 2000, ScopeIDs: scopes, CeilingCents: 2500,
		})
		return err
	})
	exceeded := 0
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, store.ErrBudgetExceeded) {
			t.Fatal(err)
		}
		exceeded++
	}
	if exceeded != 1 {
		t.Fatalf("exceeded %d errs %v", exceeded, errs)
	}
	reserved := countWhere(t, h, `SELECT reserved_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID)
	if reserved != 2000 {
		t.Fatalf("reserved %d", reserved)
	}
}

func TestNativeClaimFenceAndCapacity(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "fence", 1, 4, 2500)
	first, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{InvocationID: "inv_wrong", AgentID: agentID})
	if err != nil || !first.StageOwner || first.Fence != 1 {
		t.Fatalf("claim %+v %v", first, err)
	}
	if _, err := h.st.ClaimNativeActivation(ctx, store.ClaimInput{TaskID: rootID, InvocationID: "inv_wrong"}); !errors.Is(err, store.ErrInputInvalid) && !errors.Is(err, store.ErrClaimFenced) {
		t.Fatalf("direct mismatch %v", err)
	}
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{}); !errors.Is(err, store.ErrClaimFenced) {
		t.Fatalf("second %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_activations WHERE task_id = $1`, rootID) != 1 {
		t.Fatal("drop response created two activations")
	}
	may, known, err := h.st.NativeRunMayAdvance(ctx, first.RunID)
	if err != nil || !may || !known {
		t.Fatalf("owner may %v known %v err %v", may, known, err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	held, slots, err := h.st.CapacityHeld(ctx, invocationID)
	if err != nil || held != 0 || slots != 1 {
		t.Fatalf("held %d slots %d err %v", held, slots, err)
	}
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	leadAct, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	childMay, childKnown, err := h.st.NativeRunMayAdvance(ctx, leadAct.RunID)
	if err != nil || childMay || !childKnown {
		t.Fatalf("child may %v known %v", childMay, childKnown)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: lead.Child.ID}); err != nil {
		t.Fatal(err)
	}
	spec, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: lead.Child.ID,
		Assignee: "specialist-a", IdempotencyKey: "spec",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.ClaimActivation(ctx, h.st, spec.Child.ID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	_ = factoryID
}

func TestNativeClaimFenceRace(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	_, _, _, rootID := openProof(t, h, "claim-race", 2, 4, 2500)
	errs := raceN(8, func(int) error {
		_, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{})
		return err
	})
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		if !errors.Is(err, store.ErrClaimFenced) {
			t.Fatal(err)
		}
	}
	if wins != 1 || countWhere(t, h, `SELECT count(*) FROM native_activations WHERE task_id = $1`, rootID) != 1 {
		t.Fatalf("wins %d", wins)
	}
	var schema string
	if err := h.raw.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn, err := withSearchPath(os.Getenv("FACTORY_TEST_DATABASE_URL"), schema, true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	if _, err := coordination.ClaimActivation(ctx, other, rootID, store.ClaimInput{}); !errors.Is(err, store.ErrClaimFenced) {
		t.Fatalf("second instance %v", err)
	}
}

func TestNativeSteerRetryParkAndCancel(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "steer", 2, 4, 2500)
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CoalesceWake(ctx, store.WakeIntent{TaskID: rootID, InvocationID: invocationID, Assignee: "coordinator", Reason: "assign"}); err != nil {
		t.Fatal(err)
	}
	pending, err := coordination.Wake(ctx, h.st, store.WakeInput{TaskID: rootID, NegotiatedSteer: false})
	if err != nil || pending.Delivery != "pending" {
		t.Fatalf("pending %+v %v", pending, err)
	}
	var still bool
	if err := h.raw.QueryRow(ctx, `SELECT pending FROM native_wake_intents WHERE task_id = $1`, rootID).Scan(&still); err != nil || !still {
		t.Fatalf("wake pending %v %v", still, err)
	}
	steered, err := coordination.Wake(ctx, h.st, store.WakeInput{TaskID: rootID, NegotiatedSteer: true})
	if err != nil || steered.Delivery != "steer" {
		t.Fatalf("steer %+v %v", steered, err)
	}
	retried, err := coordination.RetryExecution(ctx, h.st, rootID)
	if err != nil || retried.RunID == "" || retried.ExecutionID == steered.Activation.ExecutionID {
		t.Fatalf("retry %+v %v", retried, err)
	}
	if retried.RunID != pending.Activation.RunID {
		t.Fatalf("retry run %s %s", retried.RunID, pending.Activation.RunID)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID, BlockerIDs: []string{rootID}}); err != nil {
		t.Fatal(err)
	}
	resumed, err := coordination.ResumePark(ctx, h.st, store.ClaimInput{TaskID: rootID, AgentID: agentID})
	if err != nil || resumed.RunID != retried.RunID || resumed.ExecutionID == retried.ExecutionID {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	if _, err := coordination.CancelSubtree(ctx, h.st, rootID); err != nil {
		t.Fatal(err)
	}
	runs := countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1`, factoryID)
	if _, err := coordination.Wake(ctx, h.st, store.WakeInput{TaskID: rootID}); !errors.Is(err, store.ErrNoResurrect) {
		t.Fatalf("resurrect %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1`, factoryID) != runs {
		t.Fatal("cancelled wake created a run")
	}
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "after-cancel",
	}); !errors.Is(err, store.ErrSpawnDenied) {
		t.Fatalf("later spawn %v", err)
	}
	if _, err := coordination.BoardResume(ctx, h.st, store.BoardResumeInput{TaskID: rootID, Principal: ""}); !errors.Is(err, store.ErrPrincipalRejected) {
		t.Fatalf("principal %v", err)
	}
	var priorStatus string
	var ended *time.Time
	if err := h.raw.QueryRow(ctx, `
		SELECT r.status, r.ended_at
		FROM native_terminal_evidence e
		JOIN runs r ON r.id = e.run_id
		WHERE e.task_id = $1`, rootID).Scan(&priorStatus, &ended); err != nil {
		t.Fatal(err)
	}
	resumedBoard, err := coordination.BoardResume(ctx, h.st, store.BoardResumeInput{TaskID: rootID, Principal: store.CeilingPrincipal, AgentID: agentID})
	if err != nil || resumedBoard.PriorStatus != priorStatus || resumedBoard.NewRunID == resumedBoard.PriorRunID {
		t.Fatalf("board %+v %v", resumedBoard, err)
	}
	var after string
	var endedAfter *time.Time
	if err := h.raw.QueryRow(ctx, `SELECT status, ended_at FROM runs WHERE id = $1`, resumedBoard.PriorRunID).Scan(&after, &endedAfter); err != nil {
		t.Fatal(err)
	}
	if after != priorStatus || !ended.Equal(*endedAfter) {
		t.Fatalf("cancelled run changed %s %s", priorStatus, after)
	}
}

func TestNativeWakeCompletedPreservesTerminal(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	_, _, agentID, rootID := openProof(t, h, "linked", 2, 4, 2500)
	act, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	startNativeRun(t, h, act.RunID, "worker-wake")
	if _, err := coordination.Complete(ctx, h.st, rootID, false); err != nil {
		t.Fatal(err)
	}
	var status, evidence string
	var ended *time.Time
	if err := h.raw.QueryRow(ctx, `SELECT status, ended_at FROM runs WHERE id = $1`, act.RunID).Scan(&status, &ended); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, act.RunID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	woke, err := coordination.WakeCompleted(ctx, h.st, rootID, store.WakeInput{})
	if err != nil || woke.LinkedRunID == act.RunID || woke.PriorStatus != status {
		t.Fatalf("wake %+v %v", woke, err)
	}
	var after, evidenceAfter string
	var endedAfter *time.Time
	if err := h.raw.QueryRow(ctx, `SELECT status, ended_at FROM runs WHERE id = $1`, act.RunID).Scan(&after, &endedAfter); err != nil {
		t.Fatal(err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, act.RunID).Scan(&evidenceAfter); err != nil {
		t.Fatal(err)
	}
	if after != status || evidenceAfter != evidence || ended == nil || endedAfter == nil || !ended.Equal(*endedAfter) {
		t.Fatalf("terminal changed %s %s %s %s", status, after, evidence, evidenceAfter)
	}
}

func TestNativeJoinPolicies(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "join", 2, 4, 2500)
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	joined, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || joined.Synthesized || joined.SpawnedReplacement || len(joined.Unresolved) != 1 {
		t.Fatalf("unresolved %+v %v", joined, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != 2 {
		t.Fatal("join synthesized a task")
	}
	if _, err := coordination.Complete(ctx, h.st, lead.Child.ID, true); err != nil {
		t.Fatal(err)
	}
	failed, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || failed.Status != store.NativeFailed || failed.SpawnedReplacement {
		t.Fatalf("fail %+v %v", failed, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != 2 {
		t.Fatal("fail_parent spawned a replacement")
	}
	_, invocationID, agentID, rootID = openProof(t, h, "block", 2, 4, 2500)
	factoryID = countFactory(t, h, invocationID)
	blockedLead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinBlockParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.ClaimActivation(ctx, h.st, blockedLead.Child.ID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Complete(ctx, h.st, blockedLead.Child.ID, true); err != nil {
		t.Fatal(err)
	}
	blocked, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || blocked.Status != store.NativeBlocked || blocked.SpawnedReplacement {
		t.Fatalf("block %+v %v", blocked, err)
	}
}

func TestNativeAlertEpisode(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "alert", 1, 2, 1000)
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	if _, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "zero", Cents: 0, ScopeIDs: scopes, CeilingCents: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	scopeID := "factory:" + factoryID + ":" + invocationID
	charges := []int64{900, -200, 200}
	for i, delta := range charges {
		if _, err := h.st.ApplyBudgetDelta(ctx, scopeID, "call", string(rune('a'+i)), delta); err != nil {
			t.Fatal(err)
		}
	}
	alerts := countWhere(t, h, `SELECT count(*) FROM budget_alerts WHERE scope_id = $1`, scopeID)
	if alerts != 2 || alerts != coordination.NoteThreshold(1000, charges) {
		t.Fatalf("alerts %d math %d", alerts, budget.NoteThreshold(1000, charges))
	}
}

func TestNativeBudgetDedupRollupAndCeiling(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "dedup", 1, 2, 2500)
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	reservation, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "hold", Cents: 250, ScopeIDs: scopes, CeilingCents: 2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: "call", AttemptID: "1", Cents: 250,
	})
	if err != nil || first.CountedCents != 250 {
		t.Fatalf("settle %+v %v", first, err)
	}
	dup, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: "call", AttemptID: "1", Cents: 250,
	})
	if err != nil || !dup.Duplicate || dup.CountedCents != 250 {
		t.Fatalf("dup %+v %v", dup, err)
	}
	failedHold, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "failed", Cents: 40, ScopeIDs: scopes, CeilingCents: 2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: failedHold.ID, CallID: "call", AttemptID: "2", Cents: 40, Failed: true,
	})
	if err != nil || failed.CountedCents != 40 {
		t.Fatalf("failed %+v %v", failed, err)
	}
	spent := countWhere(t, h, `SELECT spent_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID)
	if spent != 290 {
		t.Fatalf("spent %d", spent)
	}
	roll := map[string]int64{}
	rows, err := h.st.RollupSpent(ctx, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	for kind, amount := range rows {
		roll[kind] = amount
		if amount != 290 {
			t.Fatalf("kind %s spent %d", kind, amount)
		}
	}
	if roll["invocation"] != 290 || roll["factory"] != 290 {
		t.Fatalf("rollup %+v", roll)
	}
	over, err := h.st.OvershootBeyondCeiling(ctx, "invocation:"+invocationID)
	if err != nil || over != 0 {
		t.Fatalf("overshoot %d %v", over, err)
	}
	if _, err := h.raw.Exec(ctx, `UPDATE budget_scopes SET spent_cents = ceiling_cents + 1 WHERE scope_id = $1`, "invocation:"+invocationID); err == nil {
		t.Fatal("ceiling constraint accepted an overshoot")
	}
	audit, err := coordination.AuthorizeCeiling(ctx, h.st, store.CeilingInput{
		ScopeID: "invocation:" + invocationID, Principal: store.CeilingPrincipal, NewCeiling: 4000,
	})
	if err != nil || audit.SpentCents != 0 || audit.NewCeiling != 4000 {
		t.Fatalf("audit %+v %v", audit, err)
	}
	var ceiling int64
	if err := h.raw.QueryRow(ctx, `SELECT ceiling_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID).Scan(&ceiling); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.AuthorizeCeiling(ctx, h.st, store.CeilingInput{
		ScopeID: "invocation:" + invocationID, Principal: "other", NewCeiling: 9000,
	}); !errors.Is(err, store.ErrPrincipalRejected) {
		t.Fatalf("bad principal %v", err)
	}
	var ceilingAfter int64
	if err := h.raw.QueryRow(ctx, `SELECT ceiling_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID).Scan(&ceilingAfter); err != nil {
		t.Fatal(err)
	}
	if ceilingAfter != ceiling {
		t.Fatalf("ceiling changed %d %d", ceiling, ceilingAfter)
	}
	sharedID, sharedInv, _, _ := openProofShared(t, h, "shared", 1, 2, 2500)
	sharedScopes := store.AncestorScopes(sharedID, sharedInv, "coordinator", "lead")
	if _, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: sharedInv, IdempotencyKey: "s", Cents: 1, ScopeIDs: sharedScopes, CeilingCents: 2500, SharedKey: true,
	}); err != nil {
		t.Fatal(err)
	}
	admitted, err := coordination.Admit(ctx, h.st, store.AdmitInput{InvocationID: sharedInv, ScopeIDs: sharedScopes, SharedKey: true})
	if err != nil || admitted.GuaranteesCeiling {
		t.Fatalf("shared %+v %v", admitted, err)
	}
}

func TestNativeLateCharge(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "late", 1, 2, 2500)
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	reservation, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "late", Cents: 300, ScopeIDs: scopes, CeilingCents: 2500,
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: "late", AttemptID: "1", Cents: 500,
	})
	if err != nil || settled.QuarantinedCents != 200 || settled.CountedCents != 300 || !settled.Paused || settled.Code != "usage_late_over_reservation" {
		t.Fatalf("late %+v %v", settled, err)
	}
	spent := countWhere(t, h, `SELECT spent_cents FROM budget_scopes WHERE scope_id = $1`, "invocation:"+invocationID)
	if spent != 300 {
		t.Fatalf("spent %d", spent)
	}
	if _, err := coordination.Admit(ctx, h.st, store.AdmitInput{InvocationID: invocationID, ScopeIDs: scopes}); !errors.Is(err, store.ErrBudgetExceeded) {
		t.Fatalf("admit %v", err)
	}
}

func TestNativeMissingUsage(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, _, _ := openProof(t, h, "missing", 1, 2, 2500)
	scopes := store.AncestorScopes(factoryID, invocationID, "coordinator", "lead")
	deadline := time.Now().Add(-time.Minute)
	reservation, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "wait", Cents: 300, ScopeIDs: scopes,
		CeilingCents: 2500, UsageDeadline: &deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordination.Admit(ctx, h.st, store.AdmitInput{InvocationID: invocationID, ScopeIDs: scopes, Now: time.Now()})
	var denied *store.BudgetDenied
	if !errors.As(err, &denied) || denied.Bound != 300 {
		t.Fatalf("admit %v", err)
	}
	if _, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "new", Cents: 1, ScopeIDs: scopes, CeilingCents: 2500,
	}); !errors.Is(err, store.ErrBudgetExceeded) {
		t.Fatalf("new reserve %v", err)
	}
	if _, err := coordination.SettleUsage(ctx, h.st, store.SettleInput{
		ReservationID: reservation.ID, CallID: "ok", AttemptID: "1", Cents: 300,
	}); err != nil {
		t.Fatal(err)
	}
	admitted, err := coordination.Admit(ctx, h.st, store.AdmitInput{InvocationID: invocationID, ScopeIDs: scopes, Now: time.Now()})
	if err != nil || !admitted.Admitted {
		t.Fatalf("admit after settlement %+v %v", admitted, err)
	}
	if _, err := coordination.ReserveAncestors(ctx, h.st, store.ReserveInput{
		InvocationID: invocationID, IdempotencyKey: "after", Cents: 1, ScopeIDs: scopes, CeilingCents: 2500,
	}); err != nil {
		t.Fatalf("reserve after settlement %v", err)
	}
}

func openProof(t *testing.T, h *harness, key string, slots, maxLive int, ceiling int64) (factoryID, invocationID, agentID, rootID string) {
	t.Helper()
	return openTree(t, h, key, slots, maxLive, ceiling, false)
}

func openProofShared(t *testing.T, h *harness, key string, slots, maxLive int, ceiling int64) (factoryID, invocationID, agentID, rootID string) {
	t.Helper()
	return openTree(t, h, key, slots, maxLive, ceiling, true)
}

func openTree(t *testing.T, h *harness, key string, slots, maxLive int, ceiling int64, shared bool) (factoryID, invocationID, agentID, rootID string) {
	t.Helper()
	factoryID, invocationID, agentID = seedNativeInvocation(t, h, key)
	_, task, err := h.st.OpenNativeTree(context.Background(), store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID,
		Tree: proofTree(), CeilingCents: ceiling, Slots: slots, MaxLive: maxLive, SharedKey: shared,
	})
	if err != nil {
		t.Fatal(err)
	}
	return factoryID, invocationID, agentID, task.ID
}

func proofTree() definition.NativeTree {
	return definition.NativeTree{
		NativeHierarchy: true, Synthetic: true,
		MaxDepth: definition.NativeProofMaxDepth, AbsoluteMaxDepth: definition.NativeAbsoluteMaxDepth,
		Delegation: "direct_manager",
		Personas: []definition.NativePersona{
			{Name: "coordinator", Role: "manager"},
			{Name: "lead", ReportsTo: "coordinator", SpawnableBy: []string{"coordinator"}, Role: "manager"},
			{Name: "specialist-a", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
			{Name: "specialist-b", ReportsTo: "lead", SpawnableBy: []string{"lead"}, Role: "specialist"},
		},
	}
}

func countWhere(t *testing.T, h *harness, query, arg string) int {
	t.Helper()
	var n int
	if err := h.raw.QueryRow(context.Background(), query, arg).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countFactory(t *testing.T, h *harness, invocationID string) string {
	t.Helper()
	var id string
	if err := h.raw.QueryRow(context.Background(), `SELECT factory_id FROM invocations WHERE id = $1`, invocationID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func raceN(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = fn(i)
		}(i)
	}
	wg.Wait()
	return errs
}
