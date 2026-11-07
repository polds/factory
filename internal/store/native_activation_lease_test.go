package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestNativeChildActivationReachesLease(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "lease-child", 1, 4, 2500)
	if _, err := h.raw.Exec(ctx, `UPDATE invocations SET target_repository = $2 WHERE id = $1`, invocationID, "example/fixture"); err != nil {
		t.Fatal(err)
	}
	tasksBefore := countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	queueBefore := countWhere(t, h, `SELECT count(*) FROM run_queue q JOIN runs r ON r.id = q.run_id WHERE r.factory_id = $1`, factoryID)
	if _, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "specialist-a", IdempotencyKey: "lateral",
	}); !errors.Is(err, definition.ErrDelegationDenied) {
		t.Fatalf("delegate %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != tasksBefore {
		t.Fatal("refusal inserted a child")
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue q JOIN runs r ON r.id = q.run_id WHERE r.factory_id = $1`, factoryID) != queueBefore {
		t.Fatal("refusal inserted a queue row")
	}

	rootAct, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	assertQueuedNativeRun(t, h, rootAct, agentID, invocationID, "example/fixture", true)
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, rootAct.RunID) != 0 {
		t.Fatal("parked root stayed leasable")
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1`, rootAct.RunID) != 0 {
		t.Fatal("park inserted an execution")
	}

	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	assertQueuedNativeRun(t, h, child, agentID, invocationID, "example/fixture", false)
	if _, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID}); !errors.Is(err, store.ErrClaimFenced) {
		t.Fatalf("second claim %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, child.RunID) != 1 {
		t.Fatal("second claim duplicated the queue row")
	}
	if countWhere(t, h, `SELECT count(*) FROM runs WHERE id = $1`, child.RunID) != 1 {
		t.Fatal("second claim duplicated the run")
	}

	other := reopenStore(t, h)
	leased, err := other.Lease(ctx, "default", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(leased) != 1 || leased[0].RunID != child.RunID {
		t.Fatalf("lease %+v", leased)
	}
	assigned, err := h.st.GetRun(ctx, child.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.Status != store.StatusAssigned {
		t.Fatalf("status %s", assigned.Status)
	}
	again, err := h.st.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(again) != 0 {
		t.Fatalf("second lease %+v %v", again, err)
	}

	hash := tokenHash("worker-7")
	exec, err := other.StartExecution(ctx, child.RunID, "worker-7", hash)
	if err != nil {
		t.Fatal(err)
	}
	if exec.WorkerID != "worker-7" || exec.State != store.ExecRunning || exec.RunID != child.RunID {
		t.Fatalf("execution %+v", exec)
	}
	running, err := h.st.GetRun(ctx, child.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != store.StatusRunning || running.WorkerID != "worker-7" {
		t.Fatalf("running %+v", running)
	}
	var boundExec, worker string
	var gotHash []byte
	if err := h.raw.QueryRow(ctx, `
		SELECT a.execution_id, e.worker_id, e.run_token_sha256
		FROM native_activations a
		JOIN executions e ON e.id = a.execution_id
		WHERE a.id = $1`, child.ID).Scan(&boundExec, &worker, &gotHash); err != nil {
		t.Fatal(err)
	}
	if boundExec != exec.ID || worker != "worker-7" || bytes.Equal(gotHash, make([]byte, 32)) || !bytes.Equal(gotHash, hash) {
		t.Fatalf("bound exec %s worker %s", boundExec, worker)
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE worker_id = $1`, "native-activation") != 0 {
		t.Fatal("bookkeeping worker row")
	}

	applied, err := other.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	assertNativeTerminal(t, h, factoryID, lead.Child.ID, child.RunID, store.NativeCompleted, store.StatusSucceeded)
	may, known, err := h.st.NativeRunMayAdvance(ctx, child.RunID)
	if err != nil || may || !known {
		t.Fatalf("child may %v known %v err %v", may, known, err)
	}
	runsBefore := countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1`, factoryID)
	if _, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID}); err == nil {
		t.Fatal("completed task claimed again")
	}
	if countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1`, factoryID) != runsBefore {
		t.Fatal("completed claim created a run")
	}
}

func TestNativeActivationConcurrentLease(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "lease-race", 1, 4, 2500)
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leased, err := h.st.Lease(ctx, "default", time.Minute, 1)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			for _, row := range leased {
				if row.RunID == child.RunID {
					wins.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		t.Fatal(err)
	}
	if wins.Load() != 1 {
		t.Fatalf("wins %d", wins.Load())
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, child.RunID) != 1 {
		t.Fatal("race duplicated the queue row")
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1`, child.RunID) != 0 {
		t.Fatal("lease inserted an execution")
	}
	run, err := h.st.GetRun(ctx, child.RunID)
	if err != nil || run.Status != store.StatusAssigned {
		t.Fatalf("run %+v %v", run, err)
	}
}

func TestNativeActivationCancelBeforeAndAfterLease(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "cancel-before", 1, 4, 2500)
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.CancelSubtree(ctx, h.st, lead.Child.ID); err != nil {
		t.Fatal(err)
	}
	leased, err := h.st.Lease(ctx, "default", time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range leased {
		if row.RunID == child.RunID {
			t.Fatal("cancelled run leased")
		}
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, child.RunID) != 0 {
		t.Fatal("cancel left a queue row")
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1`, child.RunID) != 0 {
		t.Fatal("cancel before lease inserted an execution")
	}
	assertNativeTerminal(t, h, factoryID, lead.Child.ID, child.RunID, store.NativeCancelled, store.StatusCancelled)
	runsBefore := countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1`, factoryID)
	if _, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{}); !errors.Is(err, store.ErrNoResurrect) {
		t.Fatalf("resurrect %v", err)
	}
	if countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1`, factoryID) != runsBefore {
		t.Fatal("cancelled claim created a run")
	}

	factoryID, invocationID, agentID, rootID = openProof(t, h, "cancel-leased", 1, 4, 2500)
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err = coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err = coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	leased, err = h.st.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(leased) != 1 || leased[0].RunID != child.RunID {
		t.Fatalf("lease %+v %v", leased, err)
	}
	cancelled, err := h.st.RequestCancel(ctx, child.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != store.StatusCancelled {
		t.Fatalf("status %s", cancelled.Status)
	}
	if _, err := h.st.StartExecution(ctx, child.RunID, "worker-7", tokenHash("worker-7")); err == nil {
		t.Fatal("cancelled lease started")
	}
	assertNativeTerminal(t, h, factoryID, lead.Child.ID, child.RunID, store.NativeCancelled, store.StatusCancelled)
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE worker_id = $1`, "native-activation") != 0 {
		t.Fatal("bookkeeping worker row")
	}

	factoryID, invocationID, agentID, rootID = openProof(t, h, "cancel-running", 1, 4, 2500)
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err = coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err = coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, child.RunID, "worker-9", tokenHash("worker-9"))
	if err != nil {
		t.Fatal(err)
	}
	flagged, err := h.st.RequestCancel(ctx, child.RunID)
	if err != nil || !flagged.CancelRequested || flagged.Status != store.StatusRunning {
		t.Fatalf("flag %+v %v", flagged, err)
	}
	applied, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCancelled})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	assertNativeTerminal(t, h, factoryID, lead.Child.ID, child.RunID, store.NativeCancelled, store.StatusCancelled)
	if _, err := coordination.Wake(ctx, h.st, store.WakeInput{TaskID: lead.Child.ID}); !errors.Is(err, store.ErrNoResurrect) {
		t.Fatalf("resurrect %v", err)
	}
}

func TestNativeActivationParkJoinAndFailure(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "join-fail", 1, 4, 2500)
	rootAct, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	exec, err := h.st.StartExecution(ctx, child.RunID, "worker-fail", tokenHash("worker-fail"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecFailed, FailureReason: "tool failed"})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	assertNativeTerminal(t, h, factoryID, lead.Child.ID, child.RunID, store.NativeFailed, store.StatusFailed)
	joined, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || joined.Status != store.NativeFailed || joined.SpawnedReplacement || joined.Synthesized {
		t.Fatalf("join %+v %v", joined, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != 2 {
		t.Fatal("failure spawned a replacement")
	}
	assertNativeTerminal(t, h, factoryID, rootID, rootAct.RunID, store.NativeFailed, store.StatusFailed)
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE worker_id = $1`, "native-activation") != 0 {
		t.Fatal("bookkeeping worker row")
	}
	if countWhere(t, h, `SELECT count(*) FROM runs WHERE factory_id = $1 AND status = 'running'`, factoryID) != 0 {
		t.Fatal("running run remained")
	}

	factoryID, invocationID, agentID, rootID = openProof(t, h, "join-ok", 1, 4, 2500)
	rootAct, err = coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err = coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinBlockParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err = coordination.ClaimActivation(ctx, h.st, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	exec, err = h.st.StartExecution(ctx, child.RunID, "worker-ok", tokenHash("worker-ok"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err = h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	resumed, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || resumed.Status != store.NativeRunning || resumed.RunID != rootAct.RunID || len(resumed.Unresolved) != 0 {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, rootAct.RunID) != 1 {
		t.Fatal("resumed parent was not queued")
	}
	parent, err := h.st.GetRun(ctx, rootAct.RunID)
	if err != nil || parent.Status != store.StatusQueued {
		t.Fatalf("parent run %+v %v", parent, err)
	}
	may, known, err := h.st.NativeRunMayAdvance(ctx, child.RunID)
	if err != nil || may || !known {
		t.Fatalf("child may %v known %v", may, known)
	}
	ownerMay, ownerKnown, err := h.st.NativeRunMayAdvance(ctx, rootAct.RunID)
	if err != nil || !ownerMay || !ownerKnown {
		t.Fatalf("owner may %v known %v", ownerMay, ownerKnown)
	}
	leased, err := h.st.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(leased) != 1 || leased[0].RunID != rootAct.RunID {
		t.Fatalf("parent lease %+v %v", leased, err)
	}
}

func TestNativePauseFencesQueuedAssignedAndRunning(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	_, invocationID, agentID, rootID := openProof(t, h, "pause-queued", 1, 4, 2500)
	act, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.PauseSubtree(ctx, h.st, rootID); err != nil {
		t.Fatal(err)
	}
	assertPausedFenced(t, h, invocationID, rootID, act.RunID, "")

	_, invocationID, agentID, rootID = openProof(t, h, "pause-assigned", 1, 4, 2500)
	act, err = coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := h.st.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(leased) != 1 || leased[0].RunID != act.RunID {
		t.Fatalf("lease %+v %v", leased, err)
	}
	if _, err := coordination.PauseSubtree(ctx, h.st, rootID); err != nil {
		t.Fatal(err)
	}
	assertPausedFenced(t, h, invocationID, rootID, act.RunID, "")
	if _, err := h.st.StartExecution(ctx, act.RunID, "worker-paused", tokenHash("worker-paused")); err == nil {
		t.Fatal("paused assigned run started")
	}

	_, invocationID, agentID, rootID = openProof(t, h, "pause-running", 1, 4, 2500)
	act, err = coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	exec := startNativeRun(t, h, act.RunID, "worker-paused")
	hash := tokenHash("worker-paused")
	if _, err := coordination.PauseSubtree(ctx, h.st, rootID); err != nil {
		t.Fatal(err)
	}
	assertPausedFenced(t, h, invocationID, rootID, act.RunID, exec.ID)
	applied, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || applied {
		t.Fatalf("paused finish applied %v %v", applied, err)
	}
	run, err := h.st.GetRun(ctx, act.RunID)
	if err != nil || run.Status == store.StatusSucceeded || run.Status == store.StatusRunning || run.Status == store.StatusAssigned {
		t.Fatalf("paused run %+v %v", run, err)
	}
	if _, err := h.st.AppendEvents(ctx, act.RunID, exec.ID, 0, []json.RawMessage{
		json.RawMessage(`{"type":"message.delta","ts":"2026-11-07T00:00:00Z"}`),
	}); err == nil {
		t.Fatal("paused worker appended an event")
	}
	var got []byte
	if err := h.raw.QueryRow(ctx, `SELECT run_token_sha256 FROM executions WHERE id = $1`, exec.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, hash) {
		t.Fatal("paused execution kept the worker token")
	}
	if _, err := coordination.ResumePark(ctx, h.st, store.ClaimInput{TaskID: rootID}); err == nil {
		t.Fatal("pause resumed through park")
	}
	if _, err := coordination.BoardResume(ctx, h.st, store.BoardResumeInput{TaskID: rootID, Principal: store.CeilingPrincipal}); err == nil {
		t.Fatal("pause used board resume")
	}
}

func TestNativeRebindAfterOrphanAndRetryRevokesToken(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	_, _, agentID, rootID := openProof(t, h, "rebind-expire", 1, 4, 2500)
	act, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	exec := startNativeRun(t, h, act.RunID, "worker-old")
	hash := tokenHash("worker-old")
	if _, err := h.st.AppendEvents(ctx, act.RunID, exec.ID, 0, []json.RawMessage{
		json.RawMessage(`{"type":"message.delta","ts":"2026-11-07T00:00:00Z"}`),
	}); err != nil {
		t.Fatal(err)
	}
	leased, err := h.st.Lease(ctx, "default", time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = leased
	var until time.Time
	if err := h.raw.QueryRow(ctx, `SELECT lease_until FROM run_queue WHERE run_id = $1`, act.RunID).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ExpireLeases(ctx, until.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var bound, state string
	var got []byte
	if err := h.raw.QueryRow(ctx, `
		SELECT a.execution_id, e.state, e.run_token_sha256
		FROM native_activations a
		JOIN executions e ON e.id = a.execution_id
		WHERE a.id = $1`, act.ID).Scan(&bound, &state, &got); err != nil {
		t.Fatal(err)
	}
	if bound != exec.ID || state != store.ExecOrphaned || bytes.Equal(got, hash) {
		t.Fatalf("orphan bind %s state %s", bound, state)
	}
	applied, err := h.st.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || applied {
		t.Fatalf("orphan finish %v %v", applied, err)
	}
	if _, err := h.st.AppendEvents(ctx, act.RunID, exec.ID, 1, []json.RawMessage{
		json.RawMessage(`{"type":"message.delta","ts":"2026-11-07T00:00:01Z"}`),
	}); err == nil {
		t.Fatal("orphaned worker appended an event")
	}
	next := startNativeRun(t, h, act.RunID, "worker-new")
	if err := h.raw.QueryRow(ctx, `SELECT execution_id FROM native_activations WHERE id = $1`, act.ID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != next.ID || next.ID == exec.ID {
		t.Fatalf("rebind %s next %s old %s", bound, next.ID, exec.ID)
	}
	run, err := h.st.GetRun(ctx, act.RunID)
	if err != nil || run.Status != store.StatusRunning || run.WorkerID != "worker-new" {
		t.Fatalf("live run %+v %v", run, err)
	}

	_, _, agentID, rootID = openProof(t, h, "retry-token", 1, 4, 2500)
	act, err = coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	exec = startNativeRun(t, h, act.RunID, "worker-retry")
	hash = tokenHash("worker-retry")
	retried, err := coordination.RetryExecution(ctx, h.st, rootID)
	if err != nil || retried.RunID != act.RunID {
		t.Fatalf("retry %+v %v", retried, err)
	}
	if err := h.raw.QueryRow(ctx, `SELECT state, run_token_sha256 FROM executions WHERE id = $1`, exec.ID).Scan(&state, &got); err != nil {
		t.Fatal(err)
	}
	if state != store.ExecOrphaned || bytes.Equal(got, hash) {
		t.Fatalf("retry left state %s", state)
	}
	if _, err := h.st.AppendEvents(ctx, act.RunID, exec.ID, 0, []json.RawMessage{
		json.RawMessage(`{"type":"message.delta","ts":"2026-11-07T00:00:00Z"}`),
	}); err == nil {
		t.Fatal("retried worker appended an event")
	}
}

func TestNativeCompleteBeforeStartIsNotSuccess(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _, agentID, rootID := openProof(t, h, "complete-queued", 1, 4, 2500)
	act, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Complete(ctx, h.st, rootID, false); !errors.Is(err, store.ErrInputInvalid) {
		t.Fatalf("complete %v", err)
	}
	run, err := h.st.GetRun(ctx, act.RunID)
	if err != nil || run.Status != store.StatusQueued {
		t.Fatalf("run %+v %v", run, err)
	}
	task, err := h.st.GetNativeTask(ctx, factoryID, rootID)
	if err != nil || task.Status != store.NativeRunning {
		t.Fatalf("task %+v %v", task, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_terminal_evidence WHERE run_id = $1`, act.RunID) != 0 {
		t.Fatal("queued complete wrote evidence")
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1`, act.RunID) != 0 {
		t.Fatal("queued complete inserted an execution")
	}
	if _, err := coordination.CancelSubtree(ctx, h.st, rootID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := h.st.GetRun(ctx, act.RunID)
	if err != nil || cancelled.Status != store.StatusCancelled {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1`, act.RunID) != 0 {
		t.Fatal("cancel before start required an execution")
	}

	_, _, agentID, rootID = openProof(t, h, "complete-started", 1, 4, 2500)
	act, err = coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	exec := startNativeRun(t, h, act.RunID, "worker-done")
	if _, err := coordination.Complete(ctx, h.st, rootID, false); err != nil {
		t.Fatal(err)
	}
	var evidenceExec, evidenceStatus string
	if err := h.raw.QueryRow(ctx, `SELECT execution_id, status FROM native_terminal_evidence WHERE run_id = $1`, act.RunID).Scan(&evidenceExec, &evidenceStatus); err != nil {
		t.Fatal(err)
	}
	if evidenceExec != exec.ID || evidenceStatus != store.StatusSucceeded {
		t.Fatalf("evidence %s %s", evidenceExec, evidenceStatus)
	}
}

func TestNativeJoinTwice(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, invocationID, agentID, rootID := openProof(t, h, "join-twice-fail", 1, 4, 2500)
	if _, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err := coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	finishChild(t, h, ctx, lead.Child.ID, agentID, true)
	first, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || first.Status != store.NativeFailed || first.Synthesized || first.SpawnedReplacement {
		t.Fatalf("join %+v %v", first, err)
	}
	tasks := countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	second, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || second.Status != store.NativeFailed || second.Synthesized || second.SpawnedReplacement {
		t.Fatalf("second %+v %v", second, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != tasks {
		t.Fatal("second join synthesized a task")
	}

	factoryID, invocationID, agentID, rootID = openProof(t, h, "join-twice-ok", 1, 4, 2500)
	rootAct, err := coordination.ClaimActivation(ctx, h.st, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, h.st, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err = coordination.Delegate(ctx, h.st, store.DelegateInput{
		FactoryID: factoryID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinBlockParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	finishChild(t, h, ctx, lead.Child.ID, agentID, false)
	acts := countWhere(t, h, `SELECT count(*) FROM native_activations WHERE task_id = $1`, rootID)
	resumed, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || resumed.Status != store.NativeRunning || resumed.RunID != rootAct.RunID {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	again, err := coordination.Join(ctx, h.st, store.JoinInput{TaskID: rootID})
	if err != nil || again.Status != store.NativeRunning || again.RunID != rootAct.RunID || again.Synthesized {
		t.Fatalf("second resume %+v %v", again, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM native_activations WHERE task_id = $1`, rootID) != acts+1 {
		t.Fatal("second join opened another activation")
	}
	if countWhere(t, h, `SELECT count(*) FROM runs WHERE id = $1`, rootAct.RunID) != 1 {
		t.Fatal("second join duplicated the run")
	}
}

func startNativeRun(t *testing.T, h *harness, runID, worker string) store.Execution {
	t.Helper()
	ctx := context.Background()
	leased, err := h.st.Lease(ctx, "default", time.Minute, 8)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range leased {
		if row.RunID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("lease missed %s in %+v", runID, leased)
	}
	exec, err := h.st.StartExecution(ctx, runID, worker, tokenHash(worker))
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func assertPausedFenced(t *testing.T, h *harness, invocationID, taskID, runID, execID string) {
	t.Helper()
	ctx := context.Background()
	task, err := h.st.GetNativeTask(ctx, countFactory(t, h, invocationID), taskID)
	if err != nil || task.Status != store.NativePaused {
		t.Fatalf("task %+v %v", task, err)
	}
	run, err := h.st.GetRun(ctx, runID)
	if err != nil || run.Status != store.StatusQueued || run.Status == store.StatusSucceeded {
		t.Fatalf("run %+v %v", run, err)
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, runID) != 0 {
		t.Fatal("paused run stayed leasable")
	}
	held, _, err := h.st.CapacityHeld(ctx, invocationID)
	if err != nil || held != 0 {
		t.Fatalf("held %d %v", held, err)
	}
	if execID != "" {
		var state string
		if err := h.raw.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, execID).Scan(&state); err != nil || state != store.ExecCancelled {
			t.Fatalf("execution %s %v", state, err)
		}
	}
	if _, err := h.st.StartExecution(ctx, runID, "worker-after-pause", tokenHash("worker-after-pause")); err == nil {
		t.Fatal("paused run started")
	}
}

func assertQueuedNativeRun(t *testing.T, h *harness, act store.Activation, agentID, invocationID, repo string, owner bool) {
	t.Helper()
	ctx := context.Background()
	if act.StageOwner != owner || act.InvocationID != invocationID || act.RunID == "" {
		t.Fatalf("activation %+v", act)
	}
	run, err := h.st.GetRun(ctx, act.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusQueued || run.AgentID != agentID || run.Repo != repo || run.WorkerID != "" {
		t.Fatalf("run %+v", run)
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, act.RunID) != 1 {
		t.Fatal("run is not queued")
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1`, act.RunID) != 0 {
		t.Fatal("activation inserted an execution")
	}
	var linkedInvocation, linkedAgent string
	if err := h.raw.QueryRow(ctx, `
		SELECT a.invocation_id, COALESCE(r.agent_id, '')
		FROM native_activations a
		JOIN runs r ON r.id = a.run_id
		WHERE a.id = $1`, act.ID).Scan(&linkedInvocation, &linkedAgent); err != nil {
		t.Fatal(err)
	}
	if linkedInvocation != invocationID || linkedAgent != agentID {
		t.Fatalf("link invocation %s agent %s", linkedInvocation, linkedAgent)
	}
}

func assertNativeTerminal(t *testing.T, h *harness, factoryID, taskID, runID, taskStatus, runStatus string) {
	t.Helper()
	ctx := context.Background()
	task, err := h.st.GetNativeTask(ctx, factoryID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != taskStatus {
		t.Fatalf("task %s", task.Status)
	}
	run, err := h.st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != runStatus || run.EndedAt == nil {
		t.Fatalf("run %+v", run)
	}
	var evidence string
	if err := h.raw.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, runID).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence != runStatus {
		t.Fatalf("evidence %s", evidence)
	}
	if countWhere(t, h, `SELECT count(*) FROM run_queue WHERE run_id = $1`, runID) != 0 {
		t.Fatal("terminal run stayed queued")
	}
	if countWhere(t, h, `SELECT count(*) FROM executions WHERE run_id = $1 AND state IN ('assigned', 'running')`, runID) != 0 {
		t.Fatal("live execution remained")
	}
}
