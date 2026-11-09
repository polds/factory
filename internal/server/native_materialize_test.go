package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

func TestNativeAssignmentMaterializesTrustedTask(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	repo, first, headSHA := serverTwoCommitRepo(t)
	body := "goal-bytes-d3m"
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "")
	}})
	ctx := context.Background()
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": "native:bundle:materialize",
		"intent":              "new",
		"priority":            "interactive",
		"inputs":              map[string]any{"instruction": body},
		"target":              map[string]any{"repository": "example/fixture", "ref": headSHA},
	}, http.StatusCreated)
	invocationID, _ := inv["invocationId"].(string)
	if invocationID == "" || inv["workItemId"] == "" || inv["pinnedHeadSha"] != headSHA {
		t.Fatalf("receipt %+v", inv)
	}
	dir, pins := rewriteBundle(t)
	tree := loadBundleTree(t, dir, pins)
	c := &client.Client{BaseURL: env.base, APIKey: env.apiKey}
	fac, err := env.srv.factory(ctx, "pipe")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := env.srv.store.ListAgents(ctx, fac.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("agents %+v %v", agents, err)
	}
	openReq := clientBundleRequest(tree)
	openReq.AgentID = agents[0].ID
	opened, err := c.OpenNativeTree(ctx, "pipe", invocationID, openReq)
	if err != nil || opened.RootTaskID == "" {
		t.Fatalf("open %+v %v", opened, err)
	}
	env.restart(t)
	c.BaseURL = env.base
	act, err := c.ActivateNative(ctx, "pipe", opened.RootTaskID, client.NativeActivateRequest{AgentID: agents[0].ID})
	if err != nil || act.RunID == "" {
		t.Fatalf("activate %+v %v", act, err)
	}
	env.restart(t)
	c.BaseURL = env.base
	run, err := env.srv.store.GetRun(ctx, act.RunID)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := env.srv.store.Lease(ctx, "default", time.Minute, 4)
	if err != nil || !leasedRun(leased, act.RunID) {
		t.Fatalf("lease %+v %v", leased, err)
	}
	exec := startNativeWorker(t, env, store.Activation{ID: act.ID, RunID: act.RunID, TaskID: act.TaskID})
	run, err = env.srv.store.GetRun(ctx, act.RunID)
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := env.srv.resolveBound(ctx, env.srv.snapshot(), run.AgentID)
	if !ok {
		t.Fatal("binding")
	}
	taskEnv, err := env.srv.taskEnv(ctx, run, bound)
	if err != nil {
		t.Fatal(err)
	}
	asg := env.srv.assignment(ctx, run, exec, bound, taskEnv)
	if taskEnv[harness.EnvPreHarnessSetup] != harness.PreHarnessSetupDisabled || taskEnv[harness.EnvBundleMode] != definition.BundleModeRoleFiles {
		t.Fatalf("proof env %+v", taskEnv)
	}
	if taskEnv["FACTORY_TASK_PROMPT"] != run.Prompt || asg.Task == nil || asg.Task.Definition.Prompt != run.Prompt {
		t.Fatal("assignment dropped the queued prompt")
	}
	if _, ok := taskEnv["GH_TOKEN"]; ok || strings.Contains(run.Prompt, "roster-only-marker") {
		t.Fatal("private roster or token entered the assignment")
	}
	ovs := asg.Task.AgentConfigSnapshot.RepositoryHeadOverrides
	if len(ovs) != 1 || ovs[0].Head.Value != headSHA || ovs[0].RepoOwner != "example" || ovs[0].RepoName != "fixture" {
		t.Fatalf("override %+v", ovs)
	}
	assertSeatPrompt(t, run.Prompt, "nils-aaberg", pins["nils-aaberg"])
	legacyID := openServerTree(t, env, fac.ID, nativeInvocation(t, env, fac.ID, "", "native:materialize:legacy"), agents[0].ID, "", store.StagePlanning, 1)
	legacyAct, err := coordination.ClaimActivation(ctx, env.srv.store, legacyID, store.ClaimInput{AgentID: agents[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	legacyRun, err := env.srv.store.GetRun(ctx, legacyAct.RunID)
	if err != nil {
		t.Fatal(err)
	}
	legacyEnv, err := env.srv.taskEnv(ctx, legacyRun, bound)
	if err != nil {
		t.Fatal(err)
	}
	if legacyEnv[harness.EnvPreHarnessSetup] != "" || legacyEnv[harness.EnvBundleMode] != "" {
		t.Fatalf("legacy env %s %s", legacyEnv[harness.EnvPreHarnessSetup], legacyEnv[harness.EnvBundleMode])
	}

	workspace := filepath.Join(t.TempDir(), "workspace")
	runtime := t.TempDir()
	opt := assignedProofOptions(t, asg, workspace, runtime, repo)
	invParsed := harness.ParseArgs(opt.Args)
	task, err := harness.MaterializeAssigned(ctx, opt, invParsed)
	if err != nil {
		t.Fatal(err)
	}
	if headOf(t, workspace) != first || headOf(t, repo) != first {
		t.Fatalf("workspace %s source %s", headOf(t, workspace), headOf(t, repo))
	}
	if task.Prompt != run.Prompt || task.PinnedHeadSHA != headSHA || task.BundleMode != definition.BundleModeRoleFiles || task.SetupCommand != "" || task.PreHarnessSetup != harness.PreHarnessSetupDisabled {
		t.Fatalf("task file %+v", task)
	}
	raw, err := os.ReadFile(filepath.Join(runtime, "task.json"))
	if err != nil || strings.Contains(string(raw), "roster-only-marker") || strings.Contains(string(raw), "setup_command") {
		t.Fatalf("task.json %s %v", raw, err)
	}
	info, err := os.Stat(filepath.Join(runtime, "task.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	assertSeatPrompt(t, task.Prompt, "nils-aaberg", pins["nils-aaberg"])
	if goalBodyText(t, task.Prompt) != body {
		t.Fatal("goal dropped")
	}
	code, calls := runAssigned(t, opt)
	if code != harness.ExitOK || calls != 1 || headOf(t, workspace) != headSHA || headOf(t, repo) != first {
		t.Fatalf("code %d calls %d head %s source %s", code, calls, headOf(t, workspace), headOf(t, repo))
	}

	marker := filepath.Join(t.TempDir(), "setup-ran")
	hostile := assignedProofOptions(t, asg, filepath.Join(t.TempDir(), "hostile"), t.TempDir(), repo)
	hostile.Env = append(hostile.Env, harness.EnvUntrustedSetup+"=touch "+marker)
	code, calls = runAssigned(t, hostile)
	if _, err := os.Stat(marker); !os.IsNotExist(err) || code == harness.ExitOK || calls != 0 || headOf(t, repo) != first {
		t.Fatalf("code %d calls %d err %v", code, calls, err)
	}

	missingSrc, missingHead, _ := serverTwoCommitRepo(t)
	missingWS := filepath.Join(t.TempDir(), "missing")
	missing := assignedProofOptions(t, asg, missingWS, t.TempDir(), missingSrc)
	code, calls = runAssigned(t, missing)
	if _, err := os.Stat(missingWS); !os.IsNotExist(err) || code == harness.ExitOK || calls != 0 || headOf(t, missingSrc) != missingHead {
		t.Fatalf("code %d calls %d err %v", code, calls, err)
	}
}

func assignedProofOptions(t *testing.T, asg protocol.TaskAssignment, workspace, runtime, source string) harness.Options {
	t.Helper()
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.ModelID == nil {
		t.Fatal("assignment")
	}
	raw, err := json.Marshal(asg.Task.AgentConfigSnapshot.RepositoryHeadOverrides)
	if err != nil {
		t.Fatal(err)
	}
	model := *asg.Task.AgentConfigSnapshot.ModelID
	args := []string{"agent", "run", "--task-id", asg.TaskID, "--model", model, "--idle-on-complete", "0s", "--repository-head-override-json", string(raw)}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"FACTORY_TASK_PROMPT=" + asg.EnvVars["FACTORY_TASK_PROMPT"],
		harness.EnvPreHarnessSetup + "=" + asg.EnvVars[harness.EnvPreHarnessSetup],
		harness.EnvBundleMode + "=" + asg.EnvVars[harness.EnvBundleMode],
	}
	if source != "" {
		env = append(env, harness.EnvLocalSource+"="+source)
	}
	return harness.Options{Args: args, Env: env, Workspace: workspace, RuntimeDir: runtime}
}

func runAssigned(t *testing.T, opt harness.Options) (int, int) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	opt.Env = append(opt.Env, "OPENAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=test")
	code := harness.Run(context.Background(), opt)
	return code, int(calls.Load())
}
