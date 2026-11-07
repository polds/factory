package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

func TestPinnedRoleBundleAssignment(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "")
	}})
	ctx := context.Background()
	fac, agentID, itemID, invocationID := nativeLeaseFixture(t, env, "pipe", "native:bundle:assign", store.StagePlanning)
	prepareBundleGoal(t, env, fac.ID, invocationID, itemID)
	rootDir, tree, original := loadBundle(t)
	policies := countQuery(t, env, `SELECT count(*) FROM native_policies`)
	queued := countQuery(t, env, `SELECT count(*) FROM run_queue`)
	bad := tree
	bad.RosterSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := definition.LoadPinnedRoles(bad); err == nil {
		t.Fatal("old roster pin loaded")
	}
	if countQuery(t, env, `SELECT count(*) FROM native_policies`) != policies || countQuery(t, env, `SELECT count(*) FROM run_queue`) != queued {
		t.Fatal("rejected pin inserted a policy or queue row")
	}
	policy, task := openBundle(t, env, fac.ID, invocationID, agentID, itemID, loadBundleTree(t, rootDir, original), 1, 4)
	if policy.StageOwner != "nils-aaberg" || policy.SourceCommit != "source-commit" || policy.RosterSHA256 == "" || policy.RosterGitBlob == "" {
		t.Fatalf("policy %+v", policy)
	}
	reloaded, err := definition.ValidateNativeTree(policyTree(policy))
	if err != nil || reloaded.Digest != policy.Digest || policyTree(policy).AtumRoster || !policyTree(policy).Synthetic {
		t.Fatalf("reload digest %s stored %s %v", reloaded.Digest, policy.Digest, err)
	}
	if reloaded.Depth["nils-aaberg"] != 0 || reloaded.Depth["beatriz-salgado"] != 1 || reloaded.Depth["oskar-lindgren"] != 1 || reloaded.Depth["viktor-halas"] != 2 {
		t.Fatalf("depths %+v", reloaded.Depth)
	}
	nils := bundlePersona(policy.Personas, "nils-aaberg")
	if nils.ReportsTo != "" || nils.SourceReportsTo != "quinn" || nils.Role != "manager" || nils.SourceDottedTo != "" || bundlePersona(policy.Personas, "quinn").Name != "" || bundlePersona(policy.Personas, "ida").Name != "" {
		t.Fatalf("stored root %+v", nils)
	}
	var raw string
	if err := env.raw.QueryRow(ctx, `SELECT personas::text FROM native_policies WHERE invocation_id = $1`, invocationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(raw), []byte(`"sourceCommit"`)) || bytes.Contains([]byte(raw), []byte("roster-only-marker")) {
		t.Fatal("personas jsonb dropped provenance or stored the roster")
	}
	legacyID := openServerTree(t, env, fac.ID, nativeInvocation(t, env, fac.ID, "", "native:bundle:legacy"), agentID, "", store.StagePlanning, 1)
	var legacy string
	if err := env.raw.QueryRow(ctx, `
		SELECT personas::text FROM native_policies p
		JOIN native_tasks t ON t.invocation_id = p.invocation_id
		WHERE t.id = $1`, legacyID).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy) == 0 || legacy[0] != '[' {
		t.Fatal("legacy policy was not stored as a persona array")
	}
	legacyTask, err := env.srv.store.GetNativeTask(ctx, fac.ID, legacyID)
	if err != nil {
		t.Fatal(err)
	}
	legacyAct, err := coordination.ClaimActivation(ctx, env.srv.store, legacyID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	legacyRun, err := env.srv.store.GetRun(ctx, legacyAct.RunID)
	if err != nil || legacyRun.Prompt != "native activation" || legacyRun.ModelID != "" {
		t.Fatalf("legacy run %+v %v", legacyRun, err)
	}
	if _, err := coordination.CancelSubtree(ctx, env.srv.store, legacyTask.ID); err != nil {
		t.Fatal(err)
	}

	mutatedPath := filepath.Join(rootDir, "org", "agents", "nils-aaberg", "SOUL.md")
	if err := os.WriteFile(mutatedPath, []byte("mutated-after-open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := definition.LoadPinnedRoles(pinnedRequest(rootDir, original)); err == nil {
		t.Fatal("mutated directory matched the old pin")
	}
	changed := policyTree(policy)
	changed.Personas = append([]definition.NativePersona{}, policy.Personas...)
	changed.Personas[0].Files = append([]definition.RoleFile{}, policy.Personas[0].Files...)
	changed.Personas[0].Files[0].Bytes = []byte("different")
	again, _, err := env.srv.store.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: fac.ID, InvocationID: invocationID, AgentID: agentID, WorkItemID: itemID,
		Stage: store.StagePlanning, Tree: changed, CeilingCents: 2500, Slots: 1, MaxLive: 4,
	})
	if err != nil || again.Digest != policy.Digest {
		t.Fatalf("second open %s %v", again.Digest, err)
	}

	rootAct, err := coordination.ClaimActivation(ctx, env.srv.store, task.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	rootRun, err := env.srv.store.GetRun(ctx, rootAct.RunID)
	if err != nil || rootRun.Status != store.StatusQueued || rootRun.AgentID != agentID || rootRun.ModelID != "" || rootRun.Title != "native activation" {
		t.Fatalf("queued %+v %v", rootRun, err)
	}
	assertSeatPrompt(t, rootRun.Prompt, "nils-aaberg", original["nils-aaberg"])
	env.restart(t)
	stored, err := env.srv.store.GetNativePolicy(ctx, invocationID)
	if err != nil || stored.Digest != policy.Digest || stored.SourceCommit != "source-commit" || stored.RosterSHA256 != policy.RosterSHA256 {
		t.Fatalf("restart policy %+v %v", stored, err)
	}
	if !bytes.Equal(bundlePersona(stored.Personas, "nils-aaberg").Files[2].Bytes, original["nils-aaberg"]["SOUL.md"]) {
		t.Fatal("restart lost the frozen soul bytes")
	}
	leased, err := env.srv.store.Lease(ctx, "default", time.Minute, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !leasedRun(leased, rootAct.RunID) {
		t.Fatalf("lease %+v", leased)
	}
	exec := startNativeWorker(t, env, rootAct)
	if exec.WorkerID == "native-activation" {
		t.Fatal("assignment used the bookkeeping worker")
	}
	rootRun, err = env.srv.store.GetRun(ctx, rootAct.RunID)
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := env.srv.resolveBound(ctx, env.srv.snapshot(), rootRun.AgentID)
	if !ok {
		t.Fatal("agent binding missing")
	}
	taskEnv, err := env.srv.taskEnv(ctx, rootRun, bound)
	if err != nil {
		t.Fatal(err)
	}
	asg := env.srv.assignment(ctx, rootRun, exec, bound, taskEnv)
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.BasePrompt == nil || asg.Task.AgentConfigSnapshot.ModelID == nil {
		t.Fatal("assignment snapshot")
	}
	base := *asg.Task.AgentConfigSnapshot.BasePrompt
	if asg.Task.Definition.Prompt != rootRun.Prompt || base != rootRun.Prompt || taskEnv["FACTORY_TASK_PROMPT"] != rootRun.Prompt {
		t.Fatal("assignment prompt diverged from the queued run")
	}
	assertSeatPrompt(t, rootRun.Prompt, "nils-aaberg", original["nils-aaberg"])
	if _, ok := taskEnv["GH_TOKEN"]; ok || len(bound.agent.Secrets) != 0 || len(env.srv.snapshot().tree.Factory.Secrets) != 0 {
		t.Fatal("role text granted a secret")
	}
	if definition.RequiresPullRequest(bound.agent) || definition.RequiresReport(bound.agent) {
		t.Fatal("role text changed requires")
	}
	if bound.agent.CredentialStrategy == "HOSTILE" || bound.agent.Pool == "hostile" || len(bound.agent.RequiredRepos) != 0 || len(bound.agent.MCPServers) != 0 {
		t.Fatalf("role text changed grants %+v", bound.agent)
	}
	if rootRun.ModelID != "" || *asg.Task.AgentConfigSnapshot.ModelID != "bound-model" {
		t.Fatalf("model run %q assignment %q", rootRun.ModelID, *asg.Task.AgentConfigSnapshot.ModelID)
	}
	if countQuery(t, env, `SELECT count(*) FROM schedules WHERE factory_id = $1`, fac.ID) != 0 {
		t.Fatal("role text created a schedule")
	}
	views, err := env.srv.store.ListNativeTrees(ctx, fac.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := json.Marshal(views)
	if err != nil || bytes.Contains(listed, []byte("SEAT:")) || bytes.Contains(listed, []byte("sourceCommit")) {
		t.Fatal("list view grew persona bytes")
	}

	if _, err := coordination.Park(ctx, env.srv.store, store.ParkInput{TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	before := countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	if _, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: task.ID,
		Assignee: "viktor-halas", IdempotencyKey: "skip-level",
	}); !errors.Is(err, definition.ErrDelegationDenied) {
		t.Fatalf("skip %v", err)
	}
	if countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != before {
		t.Fatal("denied delegate inserted a child")
	}
	beatriz, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: task.ID,
		Assignee: "beatriz-salgado", IdempotencyKey: "beatriz", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: task.ID,
		Assignee: "beatriz-salgado", IdempotencyKey: "beatriz", JoinPolicy: store.JoinFailParent,
	})
	if err != nil || !replay.Replay || replay.Child.ID != beatriz.Child.ID {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1 AND assignee = 'beatriz-salgado'`, invocationID) != 1 {
		t.Fatal("replay inserted a second child")
	}
	claimSeat(t, env, agentID, beatriz.Child.ID, "beatriz-salgado", original)
	oskar, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: task.ID,
		Assignee: "oskar-lindgren", IdempotencyKey: "oskar", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimSeat(t, env, agentID, oskar.Child.ID, "oskar-lindgren", original)
	if _, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: oskar.Child.ID,
		Assignee: "viktor-halas", IdempotencyKey: "general-child",
	}); !errors.Is(err, definition.ErrDelegationDenied) {
		t.Fatalf("general delegate %v", err)
	}
	viktor, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: beatriz.Child.ID,
		Assignee: "viktor-halas", IdempotencyKey: "viktor", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimSeat(t, env, agentID, viktor.Child.ID, "viktor-halas", original)
	var depth int
	if err := env.raw.QueryRow(ctx, `SELECT task_depth FROM native_tasks WHERE id = $1`, viktor.Child.ID).Scan(&depth); err != nil || depth != 2 {
		t.Fatalf("task depth %d %v", depth, err)
	}

	freshDir, freshPins := rewriteBundle(t)
	one := freshPins["nils-aaberg"]["SOUL.md"]
	one = append(append([]byte{}, one...), 'x')
	if err := os.WriteFile(filepath.Join(freshDir, "org", "agents", "nils-aaberg", "SOUL.md"), one, 0o644); err != nil {
		t.Fatal(err)
	}
	freshInv := nativeInvocation(t, env, fac.ID, "", "native:bundle:old-pin")
	beforePolicies := countQuery(t, env, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, freshInv)
	beforeQueue := countQuery(t, env, `SELECT count(*) FROM run_queue`)
	if _, err := definition.LoadPinnedRoles(pinnedRequest(freshDir, freshPins)); err == nil {
		t.Fatal("stale pin")
	}
	if countQuery(t, env, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, freshInv) != beforePolicies || countQuery(t, env, `SELECT count(*) FROM run_queue`) != beforeQueue {
		t.Fatal("stale pin inserted rows")
	}
	updatedPins := copyPins(freshPins)
	updatedPins["nils-aaberg"]["SOUL.md"] = one
	updated, err := definition.LoadPinnedRoles(pinnedRequest(freshDir, updatedPins))
	if err != nil {
		t.Fatal(err)
	}
	updatedChecked, err := definition.ValidateNativeTree(updated)
	if err != nil || updatedChecked.Digest == policy.Digest {
		t.Fatal("updated bytes did not change the digest")
	}
	updatedInv := nativeInvocation(t, env, fac.ID, "", "native:bundle:new-pin")
	updatedPolicy, _ := openBundle(t, env, fac.ID, updatedInv, agentID, "", updated, 1, 4)
	if updatedPolicy.Digest != updatedChecked.Digest {
		t.Fatalf("stored updated %s", updatedPolicy.Digest)
	}

	broken := policyTree(policy)
	broken.Personas = append([]definition.NativePersona{}, policy.Personas...)
	for i := range broken.Personas {
		if broken.Personas[i].Name == "nils-aaberg" {
			broken.Personas[i].Files = nil
		}
	}
	brokenInv := nativeInvocation(t, env, fac.ID, "", "native:bundle:missing-files")
	policiesBefore := countQuery(t, env, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, brokenInv)
	queueBefore := countQuery(t, env, `SELECT count(*) FROM run_queue`)
	if _, _, err := env.srv.store.OpenNativeTree(ctx, store.OpenTreeInput{
		FactoryID: fac.ID, InvocationID: brokenInv, AgentID: agentID,
		Stage: store.StagePlanning, Tree: broken, CeilingCents: 2500, Slots: 1, MaxLive: 4,
	}); err == nil {
		t.Fatal("short seat opened a policy")
	}
	if countQuery(t, env, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, brokenInv) != policiesBefore || countQuery(t, env, `SELECT count(*) FROM run_queue`) != queueBefore {
		t.Fatal("short seat inserted a policy or queue row")
	}
}

func TestPinnedRoleBundleLifecycle(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "")
	}})
	ctx := context.Background()
	fac, agentID, itemID, invocationID := nativeLeaseFixture(t, env, "pipe", "native:bundle:cancel", store.StagePlanning)
	prepareBundleGoal(t, env, fac.ID, invocationID, itemID)
	dir, pins := rewriteBundle(t)
	tree := loadBundleTree(t, dir, pins)
	_, root := openBundle(t, env, fac.ID, invocationID, agentID, itemID, tree, 1, 4)
	act, err := coordination.ClaimActivation(ctx, env.srv.store, root.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.CancelSubtree(ctx, env.srv.store, root.ID); err != nil {
		t.Fatal(err)
	}
	leased, err := env.srv.store.Lease(ctx, "default", time.Minute, 8)
	if err != nil {
		t.Fatal(err)
	}
	if leasedRun(leased, act.RunID) {
		t.Fatal("cancelled run leased")
	}
	if countQuery(t, env, `SELECT count(*) FROM executions WHERE run_id = $1`, act.RunID) != 0 {
		t.Fatal("cancel before lease executed")
	}
	if _, err := coordination.ClaimActivation(ctx, env.srv.store, root.ID, store.ClaimInput{AgentID: agentID}); !errors.Is(err, store.ErrNoResurrect) {
		t.Fatalf("resurrect claim %v", err)
	}
	if _, err := coordination.Wake(ctx, env.srv.store, store.WakeInput{TaskID: root.ID}); !errors.Is(err, store.ErrNoResurrect) {
		t.Fatalf("resurrect wake %v", err)
	}

	parkInv := nativeInvocation(t, env, fac.ID, "", "native:bundle:park")
	prepareBundleGoal(t, env, fac.ID, parkInv, "")
	_, parkRoot := openBundle(t, env, fac.ID, parkInv, agentID, "", tree, 1, 4)
	parkAct, err := coordination.ClaimActivation(ctx, env.srv.store, parkRoot.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	parkedRun, err := env.srv.store.GetRun(ctx, parkAct.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, env.srv.store, store.ParkInput{TaskID: parkRoot.ID}); err != nil {
		t.Fatal(err)
	}
	resumed, err := coordination.ClaimActivation(ctx, env.srv.store, parkRoot.ID, store.ClaimInput{AgentID: agentID})
	if err != nil || resumed.RunID != parkAct.RunID {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	resumedRun, err := env.srv.store.GetRun(ctx, resumed.RunID)
	if err != nil || resumedRun.Prompt != parkedRun.Prompt {
		t.Fatal("park resume changed the prompt")
	}
	env.restart(t)
	leased, err = env.srv.store.Lease(ctx, "default", time.Minute, 8)
	if err != nil || !leasedRun(leased, resumed.RunID) {
		t.Fatalf("resume lease %+v %v", leased, err)
	}

	joinInv := nativeInvocation(t, env, fac.ID, "", "native:bundle:join")
	prepareBundleGoal(t, env, fac.ID, joinInv, "")
	_, joinRoot := openBundle(t, env, fac.ID, joinInv, agentID, "", tree, 1, 4)
	joinAct, err := coordination.ClaimActivation(ctx, env.srv.store, joinRoot.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	joinRun, err := env.srv.store.GetRun(ctx, joinAct.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordination.Park(ctx, env.srv.store, store.ParkInput{TaskID: joinRoot.ID}); err != nil {
		t.Fatal(err)
	}
	child, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: joinInv, ParentTaskID: joinRoot.ID,
		Assignee: "beatriz-salgado", IdempotencyKey: "join-child", JoinPolicy: store.JoinBlockParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	childAct, err := coordination.ClaimActivation(ctx, env.srv.store, child.Child.ID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 8); err != nil {
		t.Fatal(err)
	}
	childExec, err := env.srv.store.StartExecution(ctx, childAct.RunID, "worker-join", tokenHash("worker-join"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := env.srv.store.FinishExecution(ctx, childExec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	joined, err := coordination.Join(ctx, env.srv.store, store.JoinInput{TaskID: joinRoot.ID})
	if err != nil || joined.Status != store.NativeRunning || joined.RunID != joinAct.RunID {
		t.Fatalf("join %+v %v", joined, err)
	}
	parent, err := env.srv.store.GetRun(ctx, joinAct.RunID)
	if err != nil || parent.Prompt != joinRun.Prompt || parent.Status != store.StatusQueued {
		t.Fatalf("joined parent %+v %v", parent, err)
	}

	prEnv := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "pullRequest")
	}})
	prFac, prAgent, prItem, prInv := nativeLeaseFixture(t, prEnv, "pipe", "native:bundle:pr", store.StagePlanning)
	prepareBundleGoal(t, prEnv, prFac.ID, prInv, prItem)
	_, prRoot := openBundle(t, prEnv, prFac.ID, prInv, prAgent, prItem, tree, 1, 4)
	prAct, err := coordination.ClaimActivation(ctx, prEnv.srv.store, prRoot.ID, store.ClaimInput{AgentID: prAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prEnv.srv.store.Lease(ctx, "default", time.Minute, 4); err != nil {
		t.Fatal(err)
	}
	prExec := startNativeWorker(t, prEnv, prAct)
	prEnv.srv.finish(ctx, prAct.RunID, prExec.ID, store.ExecCompleted, "", "done", nil, nil, nil)
	prRun, err := prEnv.srv.store.GetRun(ctx, prAct.RunID)
	if err != nil || prRun.Status != store.StatusFailed || prRun.FailureReason != "no_work_product" {
		t.Fatalf("pull request %+v %v", prRun, err)
	}

	reportEnv := boot(t, bootOpts{definition: writeReportDefinition})
	reportFac, reportAgent, reportItem, reportInv := nativeLeaseFixture(t, reportEnv, "scan", "native:bundle:report-missing", store.StagePlanning)
	prepareBundleGoal(t, reportEnv, reportFac.ID, reportInv, reportItem)
	_, reportRoot := openBundle(t, reportEnv, reportFac.ID, reportInv, reportAgent, reportItem, tree, 1, 4)
	missingAct, err := coordination.ClaimActivation(ctx, reportEnv.srv.store, reportRoot.ID, store.ClaimInput{AgentID: reportAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reportEnv.srv.store.Lease(ctx, "default", time.Minute, 4); err != nil {
		t.Fatal(err)
	}
	missingExec := startNativeWorker(t, reportEnv, missingAct)
	reportEnv.srv.finish(ctx, missingAct.RunID, missingExec.ID, store.ExecCompleted, "", "done", nil, nil, nil)
	missingRun, err := reportEnv.srv.store.GetRun(ctx, missingAct.RunID)
	if err != nil || missingRun.Status != store.StatusFailed || missingRun.FailureReason != "report_missing" {
		t.Fatalf("missing report %+v %v", missingRun, err)
	}
	okItem, err := reportEnv.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: reportFac.ID, Source: store.SourceManual, Title: "bundle report", Stage: store.StagePlanning,
	})
	if err != nil {
		t.Fatal(err)
	}
	okInv := nativeInvocation(t, reportEnv, reportFac.ID, okItem.ID, "native:bundle:report-present")
	prepareBundleGoal(t, reportEnv, reportFac.ID, okInv, okItem.ID)
	_, okRoot := openBundle(t, reportEnv, reportFac.ID, okInv, reportAgent, okItem.ID, tree, 1, 4)
	okAct, err := coordination.ClaimActivation(ctx, reportEnv.srv.store, okRoot.ID, store.ClaimInput{AgentID: reportAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reportEnv.srv.store.Lease(ctx, "default", time.Minute, 4); err != nil {
		t.Fatal(err)
	}
	okExec := startNativeWorker(t, reportEnv, okAct)
	reportEnv.srv.finish(ctx, okAct.RunID, okExec.ID, store.ExecCompleted, "", `{"summary":"no findings","coverageStatus":"full","findingsCount":0,"report":"coverage full"}`, nil, nil, nil)
	okRun, err := reportEnv.srv.store.GetRun(ctx, okAct.RunID)
	if err != nil || okRun.Status != store.StatusSucceeded || okRun.FailureReason != "" {
		t.Fatalf("report %+v %v", okRun, err)
	}
	var pulls int
	if err := reportEnv.raw.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE run_id = $1 AND payload_json::text LIKE '%github.com%'`, okAct.RunID).Scan(&pulls); err != nil {
		t.Fatal(err)
	}
	if pulls != 0 {
		t.Fatal("report success required a pull request")
	}
}

func writeBundleDefinition(t *testing.T, requires string) string {
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
  stages: [planning]
  agents:
    planning: worker
`)
	extra := "model: bound-model\n"
	if requires != "" {
		extra += "requires:\n  - " + requires + "\n"
	}
	writeAgent(t, root, "worker", "IMPLEMENT", extra)
	return root
}

func loadBundle(t *testing.T) (string, definition.PinnedRoles, map[string]map[string][]byte) {
	t.Helper()
	dir, pins := rewriteBundle(t)
	return dir, pinnedRequest(dir, pins), pins
}

func rewriteBundle(t *testing.T) (string, map[string]map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	pins := map[string]map[string][]byte{}
	roster := []byte(`meta:
  org: synthetic
  note: roster-only-marker
agents:
  - slug: ida
    role: ceo
  - slug: quinn
    role: cto
    reports_to: ida
  - slug: nils-aaberg
    role: manager
    reports_to: quinn
    interval_s: 14400
  - slug: beatriz-salgado
    role: manager
    reports_to: nils-aaberg
  - slug: oskar-lindgren
    role: general
    reports_to: nils-aaberg
  - slug: viktor-halas
    role: general
    reports_to: beatriz-salgado
  - slug: nour
    role: general
    reports_to: nils-aaberg
`)
	if err := os.MkdirAll(filepath.Join(root, "org"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "org", "roster.yaml"), roster, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"nils-aaberg", "beatriz-salgado", "oskar-lindgren", "viktor-halas"} {
		pins[slug] = map[string][]byte{}
		dir := filepath.Join(root, "org", "agents", slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"} {
			body := []byte("SEAT:" + slug + "\nFILE:" + name + "\nsecrets: [GH_TOKEN]\nrequires: [pullRequest]\nmodel: cursor-private-model\npool: hostile\ncredentialStrategy: HOSTILE\ninterval_s: 14400\n")
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
			pins[slug][name] = body
		}
	}
	return root, pins
}

func loadBundleTree(t *testing.T, root string, pins map[string]map[string][]byte) definition.NativeTree {
	t.Helper()
	tree, err := definition.LoadPinnedRoles(pinnedRequest(root, pins))
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func pinnedRequest(root string, pins map[string]map[string][]byte) definition.PinnedRoles {
	body, err := os.ReadFile(filepath.Join(root, "org", "roster.yaml"))
	if err != nil {
		return definition.PinnedRoles{}
	}
	sum := sha256.Sum256(body)
	files := map[string]map[string]definition.PinnedFile{}
	var slugs []string
	for _, slug := range []string{"nils-aaberg", "beatriz-salgado", "oskar-lindgren", "viktor-halas"} {
		slugs = append(slugs, slug)
		files[slug] = map[string]definition.PinnedFile{}
		for _, name := range []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"} {
			files[slug][name] = bundleFilePin(pins[slug][name])
		}
	}
	return definition.PinnedRoles{
		Root: root, SourceCommit: "source-commit", RosterSHA256: hex.EncodeToString(sum[:]), RosterGitBlob: bundleGitBlob(body),
		Slugs: slugs, ProofRoot: "nils-aaberg", Files: files,
	}
}

func bundleFilePin(body []byte) definition.PinnedFile {
	sum := sha256.Sum256(body)
	return definition.PinnedFile{SHA256: hex.EncodeToString(sum[:]), GitBlob: bundleGitBlob(body), Size: len(body)}
}

func bundleGitBlob(body []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(body))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func copyPins(in map[string]map[string][]byte) map[string]map[string][]byte {
	out := map[string]map[string][]byte{}
	for slug, files := range in {
		out[slug] = map[string][]byte{}
		for name, body := range files {
			out[slug][name] = append([]byte{}, body...)
		}
	}
	return out
}

const bundlePin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func prepareBundleGoal(t *testing.T, env *env, factoryID, invocationID, workItemID string) {
	t.Helper()
	ctx := context.Background()
	if workItemID == "" {
		item, err := env.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
			FactoryID: factoryID, Source: store.SourceManual, Title: "bundle title",
			Body: "bundle-objective", Stage: store.StagePlanning,
		})
		if err != nil {
			t.Fatal(err)
		}
		workItemID = item.ID
		if _, err := env.raw.Exec(ctx, `UPDATE invocations SET work_item_id = $2 WHERE id = $1`, invocationID, workItemID); err != nil {
			t.Fatal(err)
		}
	} else if _, err := env.raw.Exec(ctx, `UPDATE work_items SET title = 'bundle title', body = 'bundle-objective' WHERE id = $1`, workItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `UPDATE invocations SET pinned_head_sha = $2, target_repository = 'example/fixture' WHERE id = $1`, invocationID, bundlePin); err != nil {
		t.Fatal(err)
	}
}

func openBundle(t *testing.T, env *env, factoryID, invocationID, agentID, workItemID string, tree definition.NativeTree, slots, maxLive int) (store.NativePolicy, store.NativeTask) {
	t.Helper()
	// Slots stays 1 so the seats are not leased together. MaxLive stays at
	// least 4 because a parked ancestor still counts as live.
	policy, task, err := env.srv.store.OpenNativeTree(context.Background(), store.OpenTreeInput{
		FactoryID: factoryID, InvocationID: invocationID, AgentID: agentID, WorkItemID: workItemID,
		Stage: store.StagePlanning, Tree: tree, CeilingCents: 2500, Slots: slots, MaxLive: maxLive,
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy, task
}

func claimSeat(t *testing.T, env *env, agentID, taskID, slug string, pins map[string]map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	act, err := coordination.ClaimActivation(ctx, env.srv.store, taskID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := env.srv.store.GetRun(ctx, act.RunID)
	if err != nil || run.Status != store.StatusQueued || run.AgentID != agentID || run.ModelID != "" {
		t.Fatalf("seat run %+v %v", run, err)
	}
	assertSeatPrompt(t, run.Prompt, slug, pins[slug])
	for _, other := range []string{"nils-aaberg", "beatriz-salgado", "oskar-lindgren", "viktor-halas"} {
		if other != slug && bytes.Contains([]byte(run.Prompt), []byte("SEAT:"+other)) {
			t.Fatalf("%s prompt contains %s", slug, other)
		}
	}
	if _, err := coordination.Park(ctx, env.srv.store, store.ParkInput{TaskID: taskID}); err != nil {
		t.Fatal(err)
	}
}

func assertSeatPrompt(t *testing.T, prompt, slug string, files map[string][]byte) {
	t.Helper()
	if !bytes.HasPrefix([]byte(prompt), []byte("# Seat\n"+slug+"\n")) {
		t.Fatalf("seat header %s", slug)
	}
	for _, name := range []string{"AGENTS.md", "HEARTBEAT.md", "SOUL.md", "TOOLS.md"} {
		got := bundleSection(t, prompt, name)
		if !bytes.Equal(got, files[name]) {
			t.Fatalf("%s %s", slug, name)
		}
	}
}

func bundleSection(t *testing.T, prompt, name string) []byte {
	t.Helper()
	header := "# File " + name + "\nbytes: "
	i := bytes.Index([]byte(prompt), []byte(header))
	if i < 0 {
		t.Fatalf("missing %s", name)
	}
	rest := prompt[i+len(header):]
	nl := bytes.IndexByte([]byte(rest), '\n')
	if nl < 0 {
		t.Fatal("length")
	}
	var n int
	if _, err := fmt.Sscan(rest[:nl], &n); err != nil {
		t.Fatal(err)
	}
	body := []byte(rest[nl+1:])
	if len(body) < n {
		t.Fatalf("short %s", name)
	}
	return body[:n]
}

func policyTree(policy store.NativePolicy) definition.NativeTree {
	return definition.NativeTree{
		NativeHierarchy:       policy.NativeHierarchy,
		Personas:              policy.Personas,
		MaxDepth:              policy.MaxDepth,
		AbsoluteMaxDepth:      policy.AbsoluteMaxDepth,
		AllowAncestorShortcut: policy.AllowAncestorShortcut,
		Delegation:            policy.Delegation,
		Synthetic:             true,
		SourceCommit:          policy.SourceCommit,
		RosterSHA256:          policy.RosterSHA256,
		RosterGitBlob:         policy.RosterGitBlob,
		BundleMode:            policy.BundleMode,
	}
}

func bundlePersona(personas []definition.NativePersona, name string) definition.NativePersona {
	for _, persona := range personas {
		if persona.Name == name {
			return persona
		}
	}
	return definition.NativePersona{}
}

func leasedRun(leased []store.QueuedRun, runID string) bool {
	for _, row := range leased {
		if row.RunID == runID {
			return true
		}
	}
	return false
}

func countQuery(t *testing.T, env *env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := env.raw.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
