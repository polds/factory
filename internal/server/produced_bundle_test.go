package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/coordination"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

func TestProducedHeadReviewerPipeline(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "")
	}})
	t.Cleanup(func() {
		admin, err := pgx.Connect(context.Background(), env.srv.cfg.DatabaseURL)
		if err != nil {
			return
		}
		defer func() { _ = admin.Close(context.Background()) }()
		dropDreamd(admin)
		restorePublic(admin)
	})
	ctx := context.Background()
	fac, agentID, itemID, invocationID := nativeLeaseFixture(t, env, "pipe", "native:d5:head", store.StagePlanning)
	prepareBundleGoal(t, env, fac.ID, invocationID, itemID)
	rootDir, _, pins := loadBundle(t)
	commitRoleSource(t, rootDir)
	commit := strings.TrimSpace(gitServer(t, rootDir, "rev-parse", "HEAD"))
	tree := loadBundleTree(t, rootDir, pins)
	tree.SourceCommit = commit
	t.Setenv("FACTORY_ROLE_SOURCE", rootDir)
	if env.srv.cfg.Secrets == nil {
		env.srv.cfg.Secrets = map[string]string{}
	}
	env.srv.cfg.Secrets["GH_TOKEN"] = "gh-synthetic-token"
	snap := env.srv.snapshot()
	snap.tree.Factory.Secrets = append(snap.tree.Factory.Secrets, "GH_TOKEN")

	beforeStatus := factoryStatus(t)
	opened, err := env.srv.OpenNativeTree(ctx, "pipe", invocationID, apiTree(tree, agentID, itemID))
	if err != nil || opened.RootTaskID == "" {
		t.Fatal(err)
	}
	if factoryStatus(t) != beforeStatus {
		t.Fatal("open wrote into the factory checkout")
	}
	policy, err := env.srv.store.GetNativePolicy(ctx, invocationID)
	if err != nil || policy.SourceCommit != commit {
		t.Fatalf("stored commit %s %v", policy.SourceCommit, err)
	}
	policies := countQuery(t, env, `SELECT count(*) FROM native_policies`)
	mismatch := tree
	mismatch.Personas = append([]definition.NativePersona{}, tree.Personas...)
	for i := range mismatch.Personas {
		if mismatch.Personas[i].Name != "nils-aaberg" {
			continue
		}
		files := append([]definition.RoleFile{}, mismatch.Personas[i].Files...)
		files[2].Bytes = append([]byte{}, files[2].Bytes...)
		files[2].Bytes[0] = 'Q'
		mismatch.Personas[i].Files = files
	}
	fresh := nativeInvocation(t, env, fac.ID, "", "native:d5:mismatch")
	prepareBundleGoal(t, env, fac.ID, fresh, "")
	if _, err := env.srv.OpenNativeTree(ctx, "pipe", fresh, apiTree(mismatch, agentID, "")); err == nil || !strings.Contains(err.Error(), "org/agents/nils-aaberg/SOUL.md") || !strings.Contains(err.Error(), "byte mismatch") || strings.Contains(err.Error(), "SEAT:nils-aaberg") {
		t.Fatal(err)
	}
	if countQuery(t, env, `SELECT count(*) FROM native_policies`) != policies {
		t.Fatal("mismatch inserted a policy")
	}
	absent := tree
	absent.SourceCommit = strings.Repeat("b", 40)
	absentInv := nativeInvocation(t, env, fac.ID, "", "native:d5:absent")
	prepareBundleGoal(t, env, fac.ID, absentInv, "")
	t.Setenv("FACTORY_ROLE_SOURCE", "")
	if _, err := env.srv.OpenNativeTree(ctx, "pipe", absentInv, apiTree(absent, agentID, "")); err == nil || !strings.Contains(err.Error(), "FACTORY_ROLE_SOURCE") || !strings.Contains(err.Error(), "absent object") {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_ROLE_SOURCE", rootDir)
	blob := strings.TrimSpace(gitServer(t, rootDir, "rev-parse", "HEAD:org/roster.yaml"))
	blobTree := tree
	blobTree.SourceCommit = blob
	blobInv := nativeInvocation(t, env, fac.ID, "", "native:d5:blob")
	prepareBundleGoal(t, env, fac.ID, blobInv, "")
	if _, err := env.srv.OpenNativeTree(ctx, "pipe", blobInv, apiTree(blobTree, agentID, "")); err == nil || !strings.Contains(err.Error(), "not a commit") || strings.Contains(err.Error(), "SEAT:") {
		t.Fatal(err)
	}
	if countQuery(t, env, `SELECT count(*) FROM native_policies`) != policies+0 {
		t.Fatal("failed attestation inserted a policy")
	}
	if factoryStatus(t) != beforeStatus {
		t.Fatal("attestation wrote role bodies into the factory checkout")
	}

	rootAct := claimOnly(t, env, agentID, opened.RootTaskID)
	tasks := countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	if _, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: "oskar-early", JoinPolicy: store.JoinFailParent,
	}); err == nil || !strings.Contains(err.Error(), "implementer") {
		t.Fatal(err)
	}
	if _, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "viktor-halas", IdempotencyKey: "skip-viktor", JoinPolicy: store.JoinFailParent,
	}); err == nil || !strings.Contains(err.Error(), "delegation_denied") {
		t.Fatal(err)
	}
	if countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != tasks {
		t.Fatal("denied delegate inserted a task")
	}
	beatriz, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "beatriz-salgado", IdempotencyKey: "beatriz", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	beatrizAct := claimOnly(t, env, agentID, beatriz.ChildID)
	if _, err := env.srv.DelegateNative(ctx, "pipe", beatriz.ChildID, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: "beatriz-oskar", JoinPolicy: store.JoinFailParent,
	}); err == nil {
		t.Fatal("beatriz delegated oskar")
	}
	viktor, err := env.srv.DelegateNative(ctx, "pipe", beatriz.ChildID, api.NativeDelegateRequest{
		Assignee: "viktor-halas", IdempotencyKey: "viktor", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeOskar := countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	if _, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: "oskar-early", JoinPolicy: store.JoinFailParent,
	}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatal(err)
	}
	if countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != beforeOskar {
		t.Fatal("reviewer delegate inserted a task without a ready SHA")
	}
	viktorAct := claimOnly(t, env, agentID, viktor.ChildID)
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 8); err != nil {
		t.Fatal(err)
	}
	exec, err := env.srv.store.StartExecution(ctx, viktorAct.RunID, "worker-d5", tokenHash("bundle-token"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := env.srv.store.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	if _, err := env.srv.store.JoinNative(ctx, store.JoinInput{TaskID: beatriz.ChildID}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.JoinNative(ctx, "pipe", beatriz.ChildID); err == nil || !strings.Contains(err.Error(), "implementer") || strings.Contains(err.Error(), "evidence.test") {
		t.Fatal(err)
	}

	mem := &memObjects{objs: map[string][]byte{}}
	bundleClients.Store(env.srv, mem)
	t.Cleanup(func() { bundleClients.Delete(env.srv) })
	bundle, tip := tinyBundle(t)
	statusBeforeBundle := factoryStatus(t)
	postBundle(t, env, viktorAct.RunID, "bundle-token", strings.Repeat("c", 40), bytes.Repeat([]byte("x"), bundleCap+1))
	if mem.puts != 0 {
		t.Fatal("oversize bundle called Put")
	}
	postBundle(t, env, viktorAct.RunID, "bundle-token", strings.Repeat("d", 40), []byte("not-a-bundle"))
	if mem.puts != 1 || mem.deletes != 1 {
		t.Fatalf("bad tip puts %d deletes %d", mem.puts, mem.deletes)
	}
	if _, err := env.srv.store.GetProducedHead(ctx, viktor.ChildID); err == nil {
		t.Fatal("bad tip stored a SHA")
	}
	bundleClients.Delete(env.srv)
	postBundle(t, env, viktorAct.RunID, "bundle-token", tip, bundle)
	if _, err := env.srv.store.GetProducedHead(ctx, viktor.ChildID); err == nil {
		t.Fatal("missing artifact client stored a SHA")
	}
	if factoryStatus(t) != statusBeforeBundle {
		t.Fatal("bundle rejection wrote into the factory checkout")
	}
	bundleClients.Store(env.srv, mem)
	if res := postBundle(t, env, viktorAct.RunID, "bundle-token", tip, bundle); res != http.StatusNoContent {
		t.Fatalf("accept %d", res)
	}
	row, err := env.srv.store.GetProducedHead(ctx, viktor.ChildID)
	if err != nil || row.State != store.ProducedReady || row.HeadSHA != tip || row.BundleSHA256 == "" {
		t.Fatalf("row %+v %v", row, err)
	}
	if _, ok := mem.objs["runs/"+viktorAct.RunID+"/"+exec.ID+"/head.bundle"]; !ok {
		t.Fatalf("keys %v", mem.keys())
	}
	joined, err := env.srv.JoinNative(ctx, "pipe", beatriz.ChildID)
	if err != nil || joined.ParentTaskID != beatriz.ChildID {
		t.Fatalf("join %+v %v", joined, err)
	}

	oskarStore, err := env.srv.store.DelegateNative(ctx, store.DelegateInput{
		FactoryID: fac.ID, InvocationID: invocationID, ParentTaskID: opened.RootTaskID,
		Assignee: "oskar-lindgren", IdempotencyKey: "oskar-manual", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	oskarAct := claimOnly(t, env, agentID, oskarStore.Child.ID)
	oskarRun := mustRun(t, env, oskarAct.RunID)
	bound := mustBound(t, env, oskarRun.AgentID)
	if _, err := env.srv.taskEnv(ctx, oskarRun, bound); err == nil || !strings.Contains(err.Error(), "missing SHA") {
		t.Fatal(err)
	}
	var pin string
	if err := env.raw.QueryRow(ctx, `SELECT pinned_head_sha FROM invocations WHERE id = $1`, invocationID).Scan(&pin); err != nil || pin != bundlePin {
		t.Fatalf("pin %s %v", pin, err)
	}
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "./oracle")
	if _, err := env.srv.taskEnv(ctx, oskarRun, bound); err == nil || !strings.Contains(err.Error(), "missing SHA") {
		t.Fatal(err)
	}
	copied, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: "oskar-manual", JoinPolicy: store.JoinFailParent,
	})
	if err != nil || copied.ChildID != oskarStore.Child.ID {
		t.Fatalf("copy %+v %v", copied, err)
	}
	copiedRow, err := env.srv.store.GetProducedHead(ctx, copied.ChildID)
	if err != nil || copiedRow.HeadSHA != tip || copiedRow.HeadSHA == bundlePin {
		t.Fatalf("copied %+v %v", copiedRow, err)
	}
	if _, err := env.srv.taskEnv(ctx, oskarRun, bound); err == nil || !strings.Contains(err.Error(), "FACTORY_EVIDENCE_MUTATION") {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "")
	t.Setenv("FACTORY_EVIDENCE_MUTATION", "oracle/oracle.go")
	if _, err := env.srv.taskEnv(ctx, oskarRun, bound); err == nil || !strings.Contains(err.Error(), "FACTORY_EVIDENCE_PACKAGE") {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_EVIDENCE_PACKAGE", "./oracle")
	reviewEnv, err := env.srv.taskEnv(ctx, oskarRun, bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reviewEnv["GH_TOKEN"]; ok || strings.Contains(reviewEnv["FACTORY_SECRET_VALUES"], "gh-synthetic-token") {
		t.Fatal("reviewer env has GH_TOKEN")
	}
	if reviewEnv[harness.EnvTestDSN] == "" || reviewEnv[harness.EnvReviewHead] != tip || reviewEnv[harness.EnvEvidencePackage] != "./oracle" {
		t.Fatal("reviewer evidence env missing")
	}
	reviewAsg := env.srv.assignment(ctx, oskarRun, exec, bound, reviewEnv)
	ovs := reviewAsg.Task.AgentConfigSnapshot.RepositoryHeadOverrides
	if len(ovs) != 1 || ovs[0].Head.Value != tip || ovs[0].Head.Value == pin || ovs[0].RepoOwner != "example" || ovs[0].RepoName != "fixture" {
		t.Fatalf("review overrides %d", len(ovs))
	}
	if err := env.raw.QueryRow(ctx, `SELECT pinned_head_sha FROM invocations WHERE id = $1`, invocationID).Scan(&pin); err != nil || pin != bundlePin {
		t.Fatalf("pin changed %s", pin)
	}

	for _, runID := range []string{rootAct.RunID, beatrizAct.RunID, viktorAct.RunID} {
		run := mustRun(t, env, runID)
		got, err := env.srv.taskEnv(ctx, run, bound)
		if err != nil {
			t.Fatal(err)
		}
		if got[harness.EnvTestDSN] != "" || got[harness.EnvEvidencePackage] != "" || got[harness.EnvEvidenceMutation] != "" || got[harness.EnvReviewHead] != "" {
			t.Fatalf("%s received evidence env", run.ID)
		}
	}
	nilsRun := mustRun(t, env, rootAct.RunID)
	nilsAsg := env.srv.assignment(ctx, nilsRun, exec, bound, mustEnv(t, env, nilsRun, bound))
	nilsOvs := nilsAsg.Task.AgentConfigSnapshot.RepositoryHeadOverrides
	if len(nilsOvs) != 1 || nilsOvs[0].Head.Value != bundlePin {
		t.Fatalf("nils overrides %d", len(nilsOvs))
	}
	afterTasks := countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID)
	if _, err := env.srv.DelegateNative(ctx, "pipe", oskarStore.Child.ID, api.NativeDelegateRequest{
		Assignee: "viktor-halas", IdempotencyKey: "oskar-child", JoinPolicy: store.JoinFailParent,
	}); err == nil || !strings.Contains(err.Error(), "delegation_denied") {
		t.Fatal(err)
	}
	if _, err := env.srv.DelegateNative(ctx, "pipe", viktor.ChildID, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: "viktor-child", JoinPolicy: store.JoinFailParent,
	}); err == nil {
		t.Fatal("viktor delegated")
	}
	if countQuery(t, env, `SELECT count(*) FROM native_tasks WHERE invocation_id = $1`, invocationID) != afterTasks {
		t.Fatal("leaf delegate inserted a task")
	}

	events := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "events.ndjson") {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(events.Close)
	body := []byte("{\"type\":\"message.delta\"}\n")
	client := artifacts.S3{
		Endpoint: events.URL, Bucket: "factory", AccessKey: "k", SecretKey: "s",
		Region: "us-east-1", PathStyle: true,
	}
	if err := client.Put(ctx, "runs/r/e/events.ndjson", bytes.NewReader(body), int64(len(body)), "application/x-ndjson"); err != nil {
		t.Fatal(err)
	}
}

func apiTree(tree definition.NativeTree, agentID, workID string) api.NativeTreeRequest {
	req := api.NativeTreeRequest{
		MaxDepth: tree.MaxDepth, AbsoluteMaxDepth: tree.AbsoluteMaxDepth,
		Delegation: tree.Delegation, Synthetic: true, NativeHierarchy: true,
		CeilingCents: 2500, Slots: 4, MaxLive: 4, Stage: store.StagePlanning,
		AgentID: agentID, WorkItemID: workID,
		SourceCommit: tree.SourceCommit, RosterSHA256: tree.RosterSHA256,
		RosterGitBlob: tree.RosterGitBlob, BundleMode: tree.BundleMode,
	}
	for _, persona := range tree.Personas {
		files := make([]api.NativeRoleFile, len(persona.Files))
		for i, file := range persona.Files {
			files[i] = api.NativeRoleFile{Name: file.Name, SHA256: file.SHA256, GitBlob: file.GitBlob, Bytes: append([]byte{}, file.Bytes...)}
		}
		req.Personas = append(req.Personas, api.NativePersona{
			Name: persona.Name, ReportsTo: persona.ReportsTo, SpawnableBy: append([]string{}, persona.SpawnableBy...),
			Role: persona.Role, SourceReportsTo: persona.SourceReportsTo, SourceDottedTo: persona.SourceDottedTo, Files: files,
		})
	}
	return req
}

func claimOnly(t *testing.T, env *env, agentID, taskID string) store.Activation {
	t.Helper()
	act, err := coordination.ClaimActivation(context.Background(), env.srv.store, taskID, store.ClaimInput{AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	return act
}

func mustRun(t *testing.T, env *env, runID string) store.Run {
	t.Helper()
	run, err := env.srv.store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustBound(t *testing.T, env *env, agentID string) boundAgent {
	t.Helper()
	bound, ok := env.srv.resolveBound(context.Background(), env.srv.snapshot(), agentID)
	if !ok {
		t.Fatal("binding")
	}
	return bound
}

func mustEnv(t *testing.T, env *env, run store.Run, bound boundAgent) map[string]string {
	t.Helper()
	got, err := env.srv.taskEnv(context.Background(), run, bound)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func postBundle(t *testing.T, env *env, runID, token, sha string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.base+"/api/v1/agent/runs/"+runID+"/head.bundle", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(headHeader, sha)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	return res.StatusCode
}

func commitRoleSource(t *testing.T, dir string) {
	t.Helper()
	gitServer(t, dir, "init")
	gitServer(t, dir, "config", "user.email", "t@example.com")
	gitServer(t, dir, "config", "user.name", "t")
	gitServer(t, dir, "add", "org")
	gitServer(t, dir, "commit", "-m", "roles")
}

func tinyBundle(t *testing.T) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	gitServer(t, dir, "init")
	gitServer(t, dir, "config", "user.email", "t@example.com")
	gitServer(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("tip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitServer(t, dir, "add", "f")
	gitServer(t, dir, "commit", "-m", "tip")
	sha := strings.TrimSpace(gitServer(t, dir, "rev-parse", "HEAD"))
	path := filepath.Join(dir, "head.bundle")
	gitServer(t, dir, "bundle", "create", path, "HEAD")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body, sha
}

func gitServer(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func factoryStatus(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod")
		}
		dir = parent
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

type memObjects struct {
	mu      sync.Mutex
	objs    map[string][]byte
	puts    int
	deletes int
}

func (m *memObjects) Put(_ context.Context, key string, body io.Reader, size int64, _ string) error {
	buf := make([]byte, size)
	_, err := io.ReadFull(body, buf)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.puts++
	m.objs[key] = buf
	return nil
}

func (m *memObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.objs[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (m *memObjects) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	delete(m.objs, key)
	return nil
}

func (m *memObjects) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for key := range m.objs {
		out = append(out, key)
	}
	return out
}
