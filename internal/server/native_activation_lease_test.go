package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

func TestNativeQueuedAssignmentStageAndCancel(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	ctx := context.Background()
	fac, agentID, itemID, invocationID := nativeLeaseFixture(t, env, "pipe", "native:lease:1", store.StagePlanning)
	rootID := openServerTree(t, env, fac.ID, invocationID, agentID, itemID, store.StagePlanning, 1)
	rootAct, err := coordination.ClaimActivation(ctx, env.srv.store, rootID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, env.srv.store, store.ParkInput{TaskID: rootID}); err != nil {
		t.Fatal(err)
	}
	lead, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: rootID,
		Assignee: "lead", IdempotencyKey: "lead", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := coordination.ClaimActivation(ctx, env.srv.store, lead.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := env.srv.store.GetRun(ctx, child.RunID)
	if err != nil || queued.Status != store.StatusQueued || queued.AgentID != agentID || queued.Repo != "example/fixture" {
		t.Fatalf("queued %+v %v", queued, err)
	}

	env.restart(t)
	leased, err := env.srv.store.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(leased) != 1 || leased[0].RunID != child.RunID {
		t.Fatalf("lease %+v %v", leased, err)
	}
	exec := startNativeWorker(t, env, child)
	if exec.WorkerID == "native-activation" {
		t.Fatal("assignment used the bookkeeping worker")
	}
	run, err := env.srv.store.GetRun(ctx, child.RunID)
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := env.srv.resolveBound(ctx, env.srv.snapshot(), run.AgentID)
	if !ok {
		t.Fatal("agent binding missing")
	}
	asg := env.srv.assignment(ctx, run, exec, bound, map[string]string{"FACTORY_RUN_TOKEN": "native-lease-token"})
	if asg.TaskID != run.ID || asg.ExecutionID != exec.ID || asg.Task == nil || asg.Task.Definition.Prompt != run.Prompt {
		t.Fatal("assignment did not carry the queued run")
	}
	if asg.EnvVars["FACTORY_RUN_TOKEN"] == "" {
		t.Fatal("assignment omitted the run token")
	}
	env.srv.finish(ctx, run.ID, exec.ID, store.ExecCompleted, "", "done", nil, nil, nil)
	done, err := env.srv.store.GetRun(ctx, run.ID)
	if err != nil || done.Status != store.StatusSucceeded || done.FailureReason != "" {
		t.Fatalf("child terminal %+v %v", done, err)
	}
	childTask, err := env.srv.store.GetNativeTask(ctx, fac.ID, lead.Child.ID)
	if err != nil || childTask.Status != store.NativeCompleted {
		t.Fatalf("child task %+v %v", childTask, err)
	}
	item, err := env.srv.store.GetWorkItem(ctx, itemID)
	if err != nil || item.Status != store.WorkItemOpen || item.Stage != store.StagePlanning {
		t.Fatalf("child advanced %+v %v", item, err)
	}
	var evidence string
	if err := env.raw.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, run.ID).Scan(&evidence); err != nil || evidence != store.StatusSucceeded {
		t.Fatalf("evidence %s %v", evidence, err)
	}

	joined, err := env.srv.JoinNative(ctx, "pipe", rootID)
	if err != nil || joined.Status != store.NativeRunning || joined.RunID != rootAct.RunID {
		t.Fatalf("join %+v %v", joined, err)
	}
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	ownerExec := startNativeWorker(t, env, rootAct)
	env.srv.finish(ctx, rootAct.RunID, ownerExec.ID, store.ExecCompleted, "", "done", nil, nil, nil)
	item, err = env.srv.store.GetWorkItem(ctx, itemID)
	if err != nil || item.Status != store.WorkItemComplete {
		t.Fatalf("owner left %+v %v", item, err)
	}

	cancelID := openServerTree(t, env, fac.ID, nativeInvocation(t, env, fac.ID, itemID, "native:lease:cancel"), agentID, itemID, store.StagePlanning, 2)
	// The work item already completed. This tree only checks cancel.
	cancelAct, err := coordination.ClaimActivation(ctx, env.srv.store, cancelID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	leased, err = env.srv.store.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(leased) != 1 || leased[0].RunID != cancelAct.RunID {
		t.Fatalf("cancel lease %+v %v", leased, err)
	}
	cancelled, err := env.srv.cancelRun(ctx, cancelAct.RunID)
	if err != nil || cancelled.Status != store.StatusCancelled {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	if _, err := env.srv.store.StartExecution(ctx, cancelAct.RunID, "worker-late", tokenHash("worker-late")); err == nil {
		t.Fatal("cancelled assignment started")
	}
	cancelTask, err := env.srv.store.GetNativeTask(ctx, fac.ID, cancelID)
	if err != nil || cancelTask.Status != store.NativeCancelled {
		t.Fatalf("cancel task %+v %v", cancelTask, err)
	}

	runningID := openServerTree(t, env, fac.ID, nativeInvocation(t, env, fac.ID, "", "native:lease:running-cancel"), agentID, "", store.StagePlanning, 1)
	runningAct, err := coordination.ClaimActivation(ctx, env.srv.store, runningID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	runningExec := startNativeWorker(t, env, runningAct)
	flagged, err := env.srv.cancelRun(ctx, runningAct.RunID)
	if err != nil || !flagged.CancelRequested || flagged.Status != store.StatusRunning {
		t.Fatalf("flag %+v %v", flagged, err)
	}
	env.srv.settleCancel(ctx, runningAct.RunID)
	settled, err := env.srv.store.GetRun(ctx, runningAct.RunID)
	if err != nil || settled.Status != store.StatusCancelled {
		t.Fatalf("settled %+v %v", settled, err)
	}
	settledTask, err := env.srv.store.GetNativeTask(ctx, fac.ID, runningID)
	if err != nil || settledTask.Status != store.NativeCancelled {
		t.Fatalf("settled task %+v %v", settledTask, err)
	}
	var workers int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM executions WHERE worker_id = 'native-activation'`).Scan(&workers); err != nil || workers != 0 {
		t.Fatalf("bookkeeping workers %d %v", workers, err)
	}
	_ = runningExec
}

func TestNativeReportWithoutPullRequest(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeReportDefinition})
	ctx := context.Background()
	fac, agentID, itemID, invocationID := nativeLeaseFixture(t, env, "scan", "native:report:missing", store.StagePlanning)
	missingID := openServerTree(t, env, fac.ID, invocationID, agentID, itemID, store.StageBuilding, 1)
	missingAct, err := coordination.ClaimActivation(ctx, env.srv.store, missingID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	missingExec := startNativeWorker(t, env, missingAct)
	env.srv.finish(ctx, missingAct.RunID, missingExec.ID, store.ExecCompleted, "", "done", nil, nil, nil)
	missingRun, err := env.srv.store.GetRun(ctx, missingAct.RunID)
	if err != nil || missingRun.Status != store.StatusFailed || missingRun.FailureReason != "report_missing" {
		t.Fatalf("missing %+v %v", missingRun, err)
	}
	missingTask, err := env.srv.store.GetNativeTask(ctx, fac.ID, missingID)
	if err != nil || missingTask.Status != store.NativeFailed {
		t.Fatalf("missing task %+v %v", missingTask, err)
	}

	okItem, err := env.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID, Source: store.SourceManual, Title: "native report", Stage: store.StagePlanning,
	})
	if err != nil {
		t.Fatal(err)
	}
	okInv := nativeInvocation(t, env, fac.ID, okItem.ID, "native:report:present")
	okID := openServerTree(t, env, fac.ID, okInv, agentID, okItem.ID, store.StageBuilding, 1)
	okAct, err := coordination.ClaimActivation(ctx, env.srv.store, okID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	okExec := startNativeWorker(t, env, okAct)
	env.srv.finish(ctx, okAct.RunID, okExec.ID, store.ExecCompleted, "", `{"summary":"no findings","coverageStatus":"full","findingsCount":0,"report":"coverage full"}`, nil, nil, nil)
	okRun, err := env.srv.store.GetRun(ctx, okAct.RunID)
	if err != nil || okRun.Status != store.StatusSucceeded || okRun.FailureReason != "" {
		t.Fatalf("report %+v %v", okRun, err)
	}
	okTask, err := env.srv.store.GetNativeTask(ctx, fac.ID, okID)
	if err != nil || okTask.Status != store.NativeCompleted {
		t.Fatalf("report task %+v %v", okTask, err)
	}
	var reports int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM invocation_results WHERE invocation_id = $1`, okInv).Scan(&reports); err != nil || reports != 1 {
		t.Fatalf("stored reports %d %v", reports, err)
	}
	var pulls int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE run_id = $1 AND payload_json::text LIKE '%github.com%'`, okAct.RunID).Scan(&pulls); err != nil {
		t.Fatal(err)
	}
	if pulls != 0 {
		t.Fatal("report success required a pull request")
	}
}

func nativeLeaseFixture(t *testing.T, env *env, factoryUID, key, itemStage string) (store.Factory, string, string, string) {
	t.Helper()
	ctx := context.Background()
	fac, err := env.srv.factory(ctx, factoryUID)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := env.srv.store.ListAgents(ctx, fac.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("agents %+v %v", agents, err)
	}
	item, err := env.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID, Source: store.SourceManual, Title: "native lease", Stage: itemStage,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fac, agents[0].ID, item.ID, nativeInvocation(t, env, fac.ID, item.ID, key)
}

func nativeInvocation(t *testing.T, env *env, factoryID, workItemID, key string) string {
	t.Helper()
	row, err := env.srv.store.InsertInvocation(context.Background(), store.InsertInvocationParams{
		FactoryID: factoryID, ProfileName: "scan", SourceOccurrenceKey: key,
		CanonicalDigest: "digest-" + key, DefinitionDigest: "def", State: invocation.StateAccepted,
		Intent: "new", Priority: "interactive", WorkItemID: workItemID, NeutralStage: true,
		TargetRepository: "example/fixture",
		PrincipalJSON:    json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	})
	if err != nil || !row.Created {
		t.Fatalf("invocation %+v %v", row, err)
	}
	return row.Row.ID
}

func openServerTree(t *testing.T, env *env, factoryID, invocationID, agentID, workItemID, stage string, slots int) string {
	t.Helper()
	_, task, err := env.srv.store.OpenNativeTree(context.Background(), store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID,
		WorkItemID: workItemID, Stage: stage, Tree: serverProofTree(),
		CeilingCents: 2500, Slots: slots, MaxLive: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func startNativeWorker(t *testing.T, env *env, act store.Activation) store.Execution {
	t.Helper()
	hash := tokenHash("worker-real")
	if bytes.Equal(hash, make([]byte, 32)) {
		t.Fatal("token hash is empty")
	}
	exec, err := env.srv.store.StartExecution(context.Background(), act.RunID, "worker-real", hash)
	if err != nil {
		t.Fatal(err)
	}
	if exec.WorkerID != "worker-real" {
		t.Fatalf("worker %s", exec.WorkerID)
	}
	var bound string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT execution_id FROM native_activations
		WHERE run_id = $1
		ORDER BY fence DESC
		LIMIT 1`, act.RunID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != exec.ID {
		t.Fatalf("activation execution %s worker execution %s", bound, exec.ID)
	}
	return exec
}
