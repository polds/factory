package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestNativeBundleHTTPHarnessCheckout(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	repo, first, headSHA := serverTwoCommitRepo(t)
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "")
	}})
	ctx := context.Background()
	body := "# File AGENTS.md\nbytes: 4\nNOPE"
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": "native:bundle:http",
		"intent":              "new",
		"priority":            "interactive",
		"inputs":              map[string]any{"instruction": body},
		"target":              map[string]any{"repository": "example/fixture", "ref": headSHA},
	}, http.StatusCreated)
	invocationID, _ := inv["invocationId"].(string)
	workItemID, _ := inv["workItemId"].(string)
	if inv["pinnedHeadSha"] != headSHA || invocationID == "" || workItemID == "" {
		t.Fatalf("receipt %+v", inv)
	}
	dir, pins := rewriteBundle(t)
	tree := loadBundleTree(t, dir, pins)
	pinRoleSource(t, dir, &tree)
	c := &client.Client{BaseURL: env.base, APIKey: env.apiKey}
	wire, err := json.Marshal(clientBundleRequest(tree))
	if err != nil || !strings.Contains(string(wire), `"bundleMode":"role_files"`) || !strings.Contains(string(wire), `"sourceCommit"`) {
		t.Fatal("client dropped bundle fields")
	}
	emptyID := httpInvocation(t, env, "native:bundle:empty-files", headSHA, body)
	emptyReq := clientBundleRequest(tree)
	for i := range emptyReq.Personas {
		emptyReq.Personas[i].Files = nil
	}
	if _, err := c.OpenNativeTree(ctx, "pipe", emptyID, emptyReq); err == nil {
		t.Fatal("all-empty open")
	}
	if countQuery(t, env, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, emptyID) != 0 {
		t.Fatal("all-empty inserted a policy")
	}
	unknownID := httpInvocation(t, env, "native:bundle:unknown-mode", headSHA, body)
	unknown := clientBundleRequest(tree)
	unknown.BundleMode = "other"
	if _, err := c.OpenNativeTree(ctx, "pipe", unknownID, unknown); err == nil || countQuery(t, env, `SELECT count(*) FROM native_policies WHERE invocation_id = $1`, unknownID) != 0 {
		t.Fatal("unknown mode opened")
	}
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
	if err != nil || opened.RootTaskID == "" || opened.StageOwner != "nils-aaberg" {
		t.Fatalf("open %+v %v", opened, err)
	}
	mutated := clientBundleRequest(tree)
	mutated.Personas[0].Files[0].Bytes = []byte("changed")
	again, err := c.OpenNativeTree(ctx, "pipe", invocationID, mutated)
	if err != nil || again.Digest != opened.Digest {
		t.Fatalf("replay %+v %v", again, err)
	}
	env.restart(t)
	c.BaseURL = env.base
	policy, err := env.srv.store.GetNativePolicy(ctx, invocationID)
	if err != nil || policy.BundleMode != definition.BundleModeRoleFiles || policy.SourceCommit != tree.SourceCommit || policy.RosterSHA256 != tree.RosterSHA256 {
		t.Fatalf("reloaded %+v %v", policy, err)
	}
	if !equalSeat(policy.Personas, tree.Personas) {
		t.Fatal("reload lost seat bytes")
	}
	act, err := c.ActivateNative(ctx, "pipe", opened.RootTaskID, client.NativeActivateRequest{AgentID: agents[0].ID})
	if err != nil || act.RunID == "" {
		t.Fatalf("activate %+v %v", act, err)
	}
	env.restart(t)
	c.BaseURL = env.base
	run, err := env.srv.store.GetRun(ctx, act.RunID)
	if err != nil || !strings.Contains(run.Prompt, "task: "+opened.RootTaskID+"\n") || goalBodyText(t, run.Prompt) != body {
		t.Fatalf("queued prompt %v", err)
	}
	if run.ParentRunID != "" {
		t.Fatal("root prompt used a stage predecessor")
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
	if asg.Task == nil || asg.Task.Definition.Prompt != run.Prompt || taskEnv["FACTORY_TASK_PROMPT"] != run.Prompt {
		t.Fatal("assignment dropped the queued prompt")
	}
	if _, ok := taskEnv["GH_TOKEN"]; ok || len(bound.agent.Secrets) != 0 || definition.RequiresPullRequest(bound.agent) {
		t.Fatal("goal text changed grants")
	}
	ovs := asg.Task.AgentConfigSnapshot.RepositoryHeadOverrides
	if len(ovs) != 1 || ovs[0].Head.Value != headSHA || ovs[0].RepoOwner != "example" || ovs[0].RepoName != "fixture" || ovs[0].Head.Type != "COMMIT_SHA" {
		t.Fatalf("override %+v", ovs)
	}
	if headOf(t, repo) != first {
		t.Fatal("fixture did not start at the other commit")
	}
	calls := runAssignment(t, asg, repo, headSHA)
	if calls != 1 || headOf(t, repo) != headSHA {
		t.Fatalf("calls %d head %s", calls, headOf(t, repo))
	}
	assertSeatPrompt(t, run.Prompt, "nils-aaberg", pins["nils-aaberg"])
	if _, err := coordination.Park(ctx, env.srv.store, store.ParkInput{TaskID: opened.RootTaskID}); err != nil {
		t.Fatal(err)
	}
	child, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: policy.FactoryID, InvocationID: invocationID, ParentTaskID: opened.RootTaskID,
		Assignee: "beatriz-salgado", IdempotencyKey: "beatriz-http", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	childAct, err := coordination.ClaimActivation(ctx, env.srv.store, child.Child.ID, store.ClaimInput{AgentID: agents[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	childRun, err := env.srv.store.GetRun(ctx, childAct.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if goalBodyText(t, childRun.Prompt) != body || !strings.Contains(childRun.Prompt, "task: "+child.Child.ID+"\n") || strings.Contains(childRun.Prompt, "task: "+opened.RootTaskID+"\n") {
		t.Fatal("child goal")
	}
	assertSeatPrompt(t, childRun.Prompt, "beatriz-salgado", pins["beatriz-salgado"])
	if strings.Contains(childRun.Prompt, "SEAT:nils-aaberg") || strings.Contains(run.Prompt, "SEAT:beatriz-salgado") {
		t.Fatal("seat files crossed")
	}
	blankID := httpInvocation(t, env, "native:bundle:blank-goal", headSHA, "")
	if _, err := c.OpenNativeTree(ctx, "pipe", blankID, clientBundleRequest(tree)); err != nil {
		t.Fatal(err)
	}
	queueBefore := countQuery(t, env, `SELECT count(*) FROM run_queue`)
	var blankTask string
	if err := env.raw.QueryRow(ctx, `SELECT id FROM native_tasks WHERE invocation_id = $1 AND parent_task_id IS NULL`, blankID).Scan(&blankTask); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ActivateNative(ctx, "pipe", blankTask, client.NativeActivateRequest{}); err == nil || countQuery(t, env, `SELECT count(*) FROM run_queue`) != queueBefore {
		t.Fatal("empty goal queued a run")
	}
}

func httpInvocation(t *testing.T, env *env, key, sha, body string) string {
	t.Helper()
	inputs := map[string]any{}
	if body != "" {
		inputs["instruction"] = body
	}
	payload := map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
		"target":              map[string]any{"repository": "example/fixture", "ref": sha},
	}
	if body != "" {
		payload["inputs"] = inputs
	}
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", payload, http.StatusCreated)
	id, _ := inv["invocationId"].(string)
	if id == "" || inv["pinnedHeadSha"] != sha {
		t.Fatalf("invocation %+v", inv)
	}
	return id
}

func clientBundleRequest(tree definition.NativeTree) client.NativeTreeRequest {
	req := client.NativeTreeRequest{
		MaxDepth: tree.MaxDepth, AbsoluteMaxDepth: tree.AbsoluteMaxDepth,
		Delegation: tree.Delegation, Synthetic: true, NativeHierarchy: true,
		CeilingCents: 2500, Slots: 1, MaxLive: 4, Stage: store.StagePlanning,
		SourceCommit: tree.SourceCommit, RosterSHA256: tree.RosterSHA256,
		RosterGitBlob: tree.RosterGitBlob, BundleMode: tree.BundleMode,
	}
	for _, persona := range tree.Personas {
		files := make([]client.NativeRoleFile, len(persona.Files))
		for i, file := range persona.Files {
			files[i] = client.NativeRoleFile{Name: file.Name, SHA256: file.SHA256, GitBlob: file.GitBlob, Bytes: append([]byte{}, file.Bytes...)}
		}
		req.Personas = append(req.Personas, client.NativePersona{
			Name: persona.Name, ReportsTo: persona.ReportsTo, SpawnableBy: persona.SpawnableBy, Role: persona.Role,
			SourceReportsTo: persona.SourceReportsTo, SourceDottedTo: persona.SourceDottedTo, Files: files,
		})
	}
	return req
}

func equalSeat(got, want []definition.NativePersona) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Name != want[i].Name || len(got[i].Files) != len(want[i].Files) {
			return false
		}
		for j := range got[i].Files {
			if string(got[i].Files[j].Bytes) != string(want[i].Files[j].Bytes) {
				return false
			}
		}
	}
	return true
}

func goalBodyText(t *testing.T, prompt string) string {
	t.Helper()
	const header = "# Body\nbytes: "
	i := strings.LastIndex(prompt, header)
	if i < 0 {
		t.Fatal("missing body")
	}
	rest := prompt[i+len(header):]
	nl := strings.IndexByte(rest, '\n')
	var n int
	for _, c := range rest[:nl] {
		n = n*10 + int(c-'0')
	}
	return rest[nl+1 : nl+1+n]
}

func runAssignment(t *testing.T, asg protocol.TaskAssignment, repo, want string) int {
	t.Helper()
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.ModelID == nil {
		t.Fatal("assignment")
	}
	raw, err := json.Marshal(asg.Task.AgentConfigSnapshot.RepositoryHeadOverrides)
	if err != nil {
		t.Fatal(err)
	}
	rt := t.TempDir()
	setupLog := filepath.Join(t.TempDir(), "setup-head")
	model := *asg.Task.AgentConfigSnapshot.ModelID
	task, err := json.Marshal(map[string]string{
		"prompt":        asg.Task.Definition.Prompt,
		"model_id":      model,
		"setup_command": "git rev-parse HEAD > " + setupLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), task, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := os.ReadFile(setupLog)
		if err != nil || strings.TrimSpace(string(got)) != want {
			t.Errorf("model ran before checkout %q %v", got, err)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	code := harness.Run(context.Background(), harness.Options{
		Args:       []string{"agent", "run", "--task-id", asg.TaskID, "--model", model, "--idle-on-complete", "0s", "--repository-head-override-json", string(raw)},
		Env:        []string{"PATH=" + os.Getenv("PATH"), "OPENAI_BASE_URL=" + srv.URL, "OPENAI_API_KEY=test"},
		Workspace:  repo,
		RuntimeDir: rt,
	})
	if code != harness.ExitOK {
		t.Fatalf("harness %d", code)
	}
	return int(calls.Load())
}

func serverTwoCommitRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a")
	run("commit", "-m", "a")
	first := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b")
	run("commit", "-m", "b")
	second := run("rev-parse", "HEAD")
	run("remote", "add", "origin", "https://github.com/example/fixture.git")
	run("checkout", "--detach", first)
	return dir, first, second
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
