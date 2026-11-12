package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

const publishToken = "ghs_PUBLISH_TEST_TOKEN_VALUE_NOT_A_SECRET"

func TestDraftPublishRequiresReviewerPass(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env, tree, roleDir := bootPublication(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		reason string
		full   bool
		mode   string
	}{
		{name: "root not completed", reason: reasonRootOpen},
		{name: "cancelled", reason: reasonRootCancelled, mode: "cancel"},
		{name: "paused", reason: reasonRootPaused, mode: "pause"},
		{name: "evidence missing", reason: reasonEvidence, full: true, mode: "no-evidence"},
		{name: "red unnamed", reason: reasonRed, full: true, mode: "red-unnamed"},
		{name: "green skip", reason: reasonGreen, full: true, mode: "green-skip"},
		{name: "green unnamed", reason: reasonGreen, full: true, mode: "green-unnamed"},
		{name: "truncated", reason: reasonTruncated, full: true, mode: "truncated"},
		{name: "porcelain", reason: reasonPorcelain, full: true, mode: "porcelain"},
		{name: "head", reason: reasonHead, full: true, mode: "head"},
		{name: "ready sha", reason: reasonReady, full: true, mode: "ready-sha"},
		{name: "sentinel", reason: reasonSentinel, full: true, mode: "tool-error"},
		{name: "sentinel missing at x", reason: reasonSentinel, full: true, mode: "no-sentinel"},
		{name: "spend paused", reason: reasonSpend, full: true, mode: "spend-paused"},
		{name: "spend denied", reason: reasonSpend, full: true, mode: "spend-missing"},
		{name: "repo", reason: reasonRepo, full: true, mode: "repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := preparePublication(t, env, tree, roleDir, tc.name, tc.full, tc.mode)
			if tc.mode == "repo" {
				t.Setenv(publishEnvRepo, "other/repo")
			} else {
				t.Setenv(publishEnvRepo, publishRepo)
			}
			if tc.mode != "no-sentinel" && tc.mode != "tool-error" {
				t.Setenv(harness.EnvEvidenceMutation, "oracle/tenant_test.go")
			}
			if tc.mode == "no-sentinel" {
				t.Setenv(harness.EnvEvidenceMutation, "oracle/tenant_test.go")
			}
			err := env.srv.publishDraft(ctx, "pipe", fix.rootID)
			assertReason(t, err, tc.reason)
			if strings.Contains(err.Error(), publishToken) {
				t.Fatal("refusal contains the publication token")
			}
			var n int
			if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM draft_publications WHERE invocation_id = $1`, fix.invocationID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refusal inserted %d rows", n)
			}
		})
	}
}

func TestPublisherRouteAllowlist(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env, tree, roleDir := bootPublication(t)
	key := publishAppKey(t)
	logBuf := &bytes.Buffer{}
	env.srv.log = slog.New(slog.NewTextHandler(logBuf, nil))
	t.Run("success", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "success", true, "open")
		fake := newPublishGitHub(t, fix.sha, "")
		armPublisher(t, env, key, fake)
		wrap := gitArgLog(t)
		before := deleteBundle(t, env, fix)
		if before != http.StatusNoContent {
			t.Fatalf("delete %d", before)
		}
		if _, ok := fix.mem.objs[fix.objectKey]; !ok {
			t.Fatal("delete removed the bundle before publication")
		}
		fix = finishRoot(t, env, fix, false)
		if _, err := env.srv.CompleteNative(context.Background(), "pipe", fix.rootID, false); err != nil {
			t.Fatal(err)
		}
		assertOpened(t, env, fix, fake)
		fake.assertRoutes(t)
		if _, ok := fix.mem.objs[fix.objectKey]; ok {
			t.Fatal("bundle remained after open")
		}
		if strings.Contains(logBuf.String(), publishToken) || strings.Contains(wrap.text(t), "LEAK") || strings.Contains(wrap.text(t), "BAD_KEY") || strings.Contains(wrap.text(t), "BEARER") || strings.Contains(wrap.text(t), "--force") {
			t.Fatal("token, bearer auth, parent git config, or force push")
		}
		if !strings.Contains(wrap.text(t), "KEY_OK") || !strings.Contains(wrap.text(t), "VALUE_OK") || !strings.Contains(wrap.text(t), "core.hooksPath=") {
			t.Fatal("git env config or hooks path missing")
		}
		tip := strings.TrimSpace(gitServer(t, filepath.Join(fake.project, "northstar-ai-labs", "dreaming.git"), "rev-parse", "refs/heads/"+fix.ref(t, env)))
		if tip != fix.sha {
			t.Fatalf("tip %s", tip)
		}
		posts := fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls")
		packs := fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack")
		_, _ = env.srv.CompleteNative(context.Background(), "pipe", fix.rootID, false)
		if err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID); err != nil {
			t.Fatal(err)
		}
		if fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls") != posts || fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != packs {
			t.Fatal("replay pushed or opened again")
		}
		second := preparePublication(t, env, tree, roleDir, "second", true, "repo")
		err := env.srv.publishDraft(context.Background(), "pipe", second.rootID)
		assertReason(t, err, reasonIssue)
		if fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != packs {
			t.Fatal("second invocation pushed")
		}
		assertNoToken(t, env, fix, logBuf.String())
	})
	t.Run("before push", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "before-push", true, "open")
		fake := newPublishGitHub(t, fix.sha, "")
		fake.onToken = func() {
			if _, err := env.srv.CancelNativeSubtree(context.Background(), "pipe", fix.rootID, true); err != nil {
				t.Errorf("pause %v", err)
			}
		}
		fix = finishRoot(t, env, fix, false)
		armPublisher(t, env, key, fake)
		if _, err := env.srv.CompleteNative(context.Background(), "pipe", fix.rootID, false); err == nil {
			t.Fatal("paused root published without an error")
		}
		if fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != 0 || fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls") != 0 {
			t.Fatal("pause before push still wrote")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
		assertDetail(t, env, fix.invocationID, "root paused")
	})
	t.Run("before post", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "before-post", true, "open")
		fake := newPublishGitHub(t, fix.sha, "")
		fake.onReceive = func() {
			if _, err := env.srv.CancelNativeSubtree(context.Background(), "pipe", fix.rootID, false); err != nil {
				t.Errorf("cancel %v", err)
			}
		}
		fix = finishRoot(t, env, fix, false)
		armPublisher(t, env, key, fake)
		if _, err := env.srv.CompleteNative(context.Background(), "pipe", fix.rootID, false); err == nil {
			t.Fatal("cancelled root published without an error")
		}
		if fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls") != 0 {
			t.Fatal("cancel before post still created a pull")
		}
		if fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != 1 {
			t.Fatal("push did not happen before the post gate")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
	})
	t.Run("pause during pre-post list", func(t *testing.T) {
		stopDuringPrePostList(t, env, tree, roleDir, key, "pause-list", true, false)
	})
	t.Run("cancel during pre-post list", func(t *testing.T) {
		stopDuringPrePostList(t, env, tree, roleDir, key, "cancel-list", false, false)
	})
	t.Run("pause during adopt list", func(t *testing.T) {
		stopDuringPrePostList(t, env, tree, roleDir, key, "pause-adopt", true, true)
	})
	t.Run("cancel during adopt list", func(t *testing.T) {
		stopDuringPrePostList(t, env, tree, roleDir, key, "cancel-adopt", false, true)
	})
	t.Run("existing ref", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "existing-ref", true, "good")
		fake := newPublishGitHub(t, fix.sha, "ref")
		armPublisher(t, env, key, fake)
		err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID)
		assertReason(t, err, reasonRef)
		if fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != 0 {
			t.Fatal("existing ref was forced")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
	})
	t.Run("existing pr", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "existing-pr", true, "good")
		fake := newPublishGitHub(t, fix.sha, "pr")
		armPublisher(t, env, key, fake)
		err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID)
		assertReason(t, err, reasonPR)
		if fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != 0 {
			t.Fatal("existing pull still pushed")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
	})
	t.Run("pulls forbidden", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "forbidden", true, "good")
		fake := newPublishGitHub(t, fix.sha, "403")
		armPublisher(t, env, key, fake)
		err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID)
		assertReason(t, err, reasonGitHub)
		if strings.Contains(err.Error(), publishToken) {
			t.Fatal("github error contains the token")
		}
		if fake.count("POST", "/northstar-ai-labs/dreaming.git/git-receive-pack") != 0 {
			t.Fatal("403 still pushed")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
	})
	t.Run("not draft", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "not-draft", true, "good")
		fake := newPublishGitHub(t, fix.sha, "not-draft")
		armPublisher(t, env, key, fake)
		err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID)
		assertReason(t, err, reasonNotDraft)
		if err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID); err != nil {
			t.Fatal(err)
		}
		if fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls") != 1 {
			t.Fatal("not-draft retried")
		}
		if fake.sawReady(t) {
			t.Fatal("ready fallback")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
	})
	t.Run("head mismatch", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "mismatch", true, "good")
		fake := newPublishGitHub(t, fix.sha, "mismatch")
		armPublisher(t, env, key, fake)
		err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID)
		assertReason(t, err, reasonHeadMismatch)
		assertState(t, env, fix.invocationID, store.DraftFailed)
	})
	t.Run("unprocessable", func(t *testing.T) {
		fix := preparePublication(t, env, tree, roleDir, "422", true, "good")
		fake := newPublishGitHub(t, fix.sha, "422")
		armPublisher(t, env, key, fake)
		err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID)
		assertReason(t, err, reasonGitHub)
		if err := env.srv.publishDraft(context.Background(), "pipe", fix.rootID); err != nil {
			t.Fatal(err)
		}
		if fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls") != 1 || fake.sawReady(t) {
			t.Fatal("422 retried or marked ready")
		}
		assertState(t, env, fix.invocationID, store.DraftFailed)
		if _, ok := fix.mem.objs[fix.objectKey]; ok {
			t.Fatal("bundle remained after failure")
		}
	})
}

type pubFix struct {
	rootID       string
	rootRun      string
	invocationID string
	viktorRun    string
	sha          string
	objectKey    string
	mem          *memObjects
}

func (f pubFix) ref(t *testing.T, env *env) string {
	t.Helper()
	var ref string
	if err := env.raw.QueryRow(context.Background(), `SELECT git_ref FROM draft_publications WHERE invocation_id = $1`, f.invocationID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func bootPublication(t *testing.T) (*env, definition.NativeTree, string) {
	t.Helper()
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writeBundleDefinition(t, "")
	}})
	rootDir, _, pins := loadBundle(t)
	commitRoleSource(t, rootDir)
	tree := loadBundleTree(t, rootDir, pins)
	tree.SourceCommit = strings.TrimSpace(gitServer(t, rootDir, "rev-parse", "HEAD"))
	t.Setenv("FACTORY_ROLE_SOURCE", rootDir)
	return env, tree, rootDir
}

func preparePublication(t *testing.T, env *env, tree definition.NativeTree, roleDir, key string, full bool, mode string) pubFix {
	t.Helper()
	ctx := context.Background()
	fac, agentID, itemID, invocationID := nativeLeaseFixture(t, env, "pipe", "native:d6b:"+key, store.StagePlanning)
	prepareBundleGoal(t, env, fac.ID, invocationID, itemID)
	env.srv.PutRoleCache(fac.ID, tree.SourceCommit, roleDir)
	opened, err := env.srv.OpenNativeTree(ctx, "pipe", invocationID, apiTree(tree, agentID, itemID))
	if err != nil || opened.RootTaskID == "" {
		t.Fatal(err)
	}
	fix := pubFix{rootID: opened.RootTaskID, invocationID: invocationID}
	if mode == "cancel" {
		if _, err := env.srv.CancelNativeSubtree(ctx, "pipe", opened.RootTaskID, false); err != nil {
			t.Fatal(err)
		}
		return fix
	}
	if mode == "pause" {
		if _, err := env.srv.CancelNativeSubtree(ctx, "pipe", opened.RootTaskID, true); err != nil {
			t.Fatal(err)
		}
		return fix
	}
	if !full {
		return fix
	}
	rootAct := claimOnly(t, env, agentID, opened.RootTaskID)
	fix.rootRun = rootAct.RunID
	beatriz, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "beatriz-salgado", IdempotencyKey: key + "-beatriz", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimOnly(t, env, agentID, beatriz.ChildID)
	viktor, err := env.srv.DelegateNative(ctx, "pipe", beatriz.ChildID, api.NativeDelegateRequest{
		Assignee: "viktor-halas", IdempotencyKey: key + "-viktor", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	viktorAct := claimOnly(t, env, agentID, viktor.ChildID)
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 8); err != nil {
		t.Fatal(err)
	}
	exec, err := env.srv.store.StartExecution(ctx, viktorAct.RunID, "worker-d6b", tokenHash("bundle-token-"+key))
	if err != nil {
		t.Fatal(err)
	}
	body := "package oracle\n\nfunc Marker() string { return \"factoryEvidencePass\" }\n"
	if mode == "no-sentinel" {
		body = "package oracle\n\nfunc Marker() string { return \"other\" }\n"
	}
	bundle, sha := fileBundle(t, "oracle/tenant_test.go", body)
	fix.sha = sha
	fix.viktorRun = viktorAct.RunID
	fix.objectKey = headKey(viktorAct.RunID, exec.ID)
	mem := &memObjects{objs: map[string][]byte{}}
	bundleClients.Store(env.srv, mem)
	t.Cleanup(func() { bundleClients.Delete(env.srv) })
	fix.mem = mem
	if res := postBundle(t, env, viktorAct.RunID, "bundle-token-"+key, sha, bundle); res != http.StatusNoContent {
		t.Fatalf("bundle %d", res)
	}
	oskar, err := env.srv.DelegateNative(ctx, "pipe", opened.RootTaskID, api.NativeDelegateRequest{
		Assignee: "oskar-lindgren", IdempotencyKey: key + "-oskar", JoinPolicy: store.JoinFailParent,
	})
	if err != nil {
		t.Fatal(err)
	}
	oskarAct := claimOnly(t, env, agentID, oskar.ChildID)
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 8); err != nil {
		t.Fatal(err)
	}
	oskarExec, err := env.srv.store.StartExecution(ctx, oskarAct.RunID, "worker-d6b-oskar", tokenHash("oskar-token-"+key))
	if err != nil {
		t.Fatal(err)
	}
	if mode != "no-evidence" {
		if _, err := env.srv.store.AppendEvents(ctx, oskarAct.RunID, oskarExec.ID, 0, evidencePayloads(mode)); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "ready-sha" {
		if _, err := env.raw.Exec(ctx, `UPDATE native_produced_heads SET head_sha = $2 WHERE task_id = $1`, viktor.ChildID, strings.Repeat("b", 40)); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "spend-paused" || mode == "spend-missing" {
		paused := mode == "spend-paused"
		missing := mode == "spend-missing"
		if _, err := env.raw.Exec(ctx, `
			INSERT INTO budget_scopes (scope_id, invocation_id, kind, ceiling_cents, paused, missing_usage)
			VALUES ($1, $2, 'invocation', 2500, $3, $4)`,
			"scope-"+key, invocationID, paused, missing); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "porcelain" {
		publishWorktreeHook = func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "dirt"), []byte("x"), 0o644); err != nil {
				t.Errorf("dirt %v", err)
			}
		}
		t.Cleanup(func() { publishWorktreeHook = nil })
	}
	if mode == "head" {
		publishWorktreeHook = func(dir string) {
			gitServer(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "move")
		}
		t.Cleanup(func() { publishWorktreeHook = nil })
	}
	if mode == "open" {
		return fix
	}
	return finishRoot(t, env, fix, true)
}

func finishRoot(t *testing.T, env *env, fix pubFix, complete bool) pubFix {
	t.Helper()
	ctx := context.Background()
	if _, err := env.srv.store.Lease(ctx, "default", time.Minute, 8); err != nil {
		t.Fatal(err)
	}
	exec, err := env.srv.store.StartExecution(ctx, fix.rootRun, "worker-d6b-root", tokenHash("root-token"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := env.srv.store.FinishExecution(ctx, exec.ID, store.Terminal{State: store.ExecCompleted})
	if err != nil || !applied {
		t.Fatalf("finish %v %v", applied, err)
	}
	if complete {
		if _, err := env.srv.store.CompleteNative(ctx, fix.rootID, false); err != nil {
			t.Fatal(err)
		}
	}
	return fix
}

func stopDuringPrePostList(t *testing.T, env *env, tree definition.NativeTree, roleDir string, key *rsa.PrivateKey, name string, pause, adopt bool) {
	t.Helper()
	fix := preparePublication(t, env, tree, roleDir, name, true, "open")
	fake := newPublishGitHub(t, fix.sha, "")
	if adopt {
		ref, err := publicationRef(fix.invocationID)
		if err != nil {
			t.Fatal(err)
		}
		fake.adoptRef = ref
	}
	fake.onListed = func() {
		if _, err := env.srv.CancelNativeSubtree(context.Background(), "pipe", fix.rootID, pause); err != nil {
			t.Errorf("gate %v", err)
		}
	}
	fix = finishRoot(t, env, fix, false)
	armPublisher(t, env, key, fake)
	if _, err := env.srv.CompleteNative(context.Background(), "pipe", fix.rootID, false); err == nil {
		t.Fatal("stopped root published without an error")
	}
	if fake.count("POST", "/repos/northstar-ai-labs/dreaming/pulls") != 0 {
		t.Fatal("pre-post list gate still posted")
	}
	assertState(t, env, fix.invocationID, store.DraftFailed)
}

func evidencePayloads(mode string) []json.RawMessage {
	red := testLine("fail", "TestTenantIsolation")
	green := testLine("pass", "TestTenantIsolation")
	redStatus := harness.EvidenceTestsFailed
	greenStatus := harness.EvidenceTestsPassed
	switch mode {
	case "red-unnamed":
		red = "{\"Action\":\"fail\",\"Package\":\"oracle\"}\n"
	case "green-skip":
		green = testLine("skip", "TestTenantIsolation")
		greenStatus = harness.EvidenceSkipOnly
	case "green-unnamed":
		green = "{\"Action\":\"pass\",\"Package\":\"oracle\"}\n"
	case "truncated":
		green = testLine("pass", "TestTenantIsolation") + harness.EvidenceTruncated
	case "tool-error":
		return []json.RawMessage{eventRaw(harness.EvidenceToolError, "tool_error")}
	case "no-sentinel", "porcelain", "head", "ready-sha", "spend-paused", "spend-missing", "repo", "good", "open":
	default:
	}
	return []json.RawMessage{eventRaw(redStatus, red), eventRaw(greenStatus, green)}
}

func testLine(action, name string) string {
	return fmt.Sprintf("{\"Action\":%q,\"Package\":\"oracle\",\"Test\":%q}\n", action, name)
}

func eventRaw(status, text string) json.RawMessage {
	raw, err := json.Marshal(map[string]string{"type": harness.EventEvidenceTest, "status": status, "text": text})
	if err != nil {
		panic(err)
	}
	return raw
}

func fileBundle(t *testing.T, rel, body string) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	gitServer(t, dir, "init")
	gitServer(t, dir, "config", "user.email", "t@example.com")
	gitServer(t, dir, "config", "user.name", "t")
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitServer(t, dir, "add", rel)
	gitServer(t, dir, "commit", "-m", "tip")
	sha := strings.TrimSpace(gitServer(t, dir, "rev-parse", "HEAD"))
	bundle := filepath.Join(dir, "head.bundle")
	gitServer(t, dir, "bundle", "create", bundle, "HEAD")
	raw, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sha
}

func publishAppKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func armPublisher(t *testing.T, env *env, key *rsa.PrivateKey, fake *publishGitHub) {
	t.Helper()
	t.Setenv(publishEnvRepo, publishRepo)
	t.Setenv(harness.EnvEvidenceMutation, "oracle/tenant_test.go")
	env.srv.cfg.GitHubAppID = 4242
	env.srv.cfg.GitHubAppKey = key
	env.srv.cfg.GitHubAPIBase = fake.srv.URL
	if _, err := env.raw.Exec(context.Background(), `DELETE FROM draft_publications`); err != nil {
		t.Fatal(err)
	}
}

func assertReason(t *testing.T, err error, reason string) {
	t.Helper()
	var pe *publishError
	if !errors.As(err, &pe) || pe.Reason != reason {
		t.Fatalf("got %v want %s", err, reason)
	}
}

func assertState(t *testing.T, env *env, invocationID, state string) {
	t.Helper()
	var got string
	if err := env.raw.QueryRow(context.Background(), `SELECT state FROM draft_publications WHERE invocation_id = $1`, invocationID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != state {
		t.Fatalf("state %s", got)
	}
}

func assertOpened(t *testing.T, env *env, fix pubFix, fake *publishGitHub) {
	t.Helper()
	var state, sha, ref, detail string
	var url *string
	var draft *bool
	var number *int
	if err := env.raw.QueryRow(context.Background(), `
		SELECT state, head_sha, git_ref, pr_number, pr_url, draft, detail
		FROM draft_publications WHERE invocation_id = $1`, fix.invocationID).Scan(
		&state, &sha, &ref, &number, &url, &draft, &detail); err != nil {
		t.Fatal(err)
	}
	if state != store.DraftOpened {
		t.Fatalf("state %s detail %s", state, detail)
	}
	if draft == nil || url == nil || number == nil || !*draft || sha != fix.sha || *number <= 0 || !strings.HasPrefix(ref, publishRefPrefix) {
		t.Fatalf("row %s %s %s %v %v", state, sha, ref, number, draft)
	}
	if strings.Contains(detail, publishToken) || strings.Contains(*url, publishToken) {
		t.Fatal("row contains the token")
	}
	body := fake.createBody()
	for _, needle := range []string{fix.sha, "factoryEvidencePass", "Refs #454", "Draft by Factory; human review required; Factory will not merge.", "Spend counted cents:"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("body missing %s", needle)
		}
	}
	if !strings.Contains(body, `"draft":true`) {
		t.Fatal("request was not draft")
	}
}

func assertNoToken(t *testing.T, env *env, fix pubFix, logs string) {
	t.Helper()
	if strings.Contains(logs, publishToken) {
		t.Fatal("log contains the token")
	}
	var payload string
	if err := env.raw.QueryRow(context.Background(), `SELECT coalesce(string_agg(payload_json::text, ''), '') FROM run_events WHERE run_id = $1`, fix.viktorRun).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, publishToken) {
		t.Fatal("events contain the token")
	}
}

func assertDetail(t *testing.T, env *env, invocationID, needle string) {
	t.Helper()
	var detail string
	if err := env.raw.QueryRow(context.Background(), `SELECT detail FROM draft_publications WHERE invocation_id = $1`, invocationID).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, needle) {
		t.Fatalf("detail %s", detail)
	}
}

func deleteBundle(t *testing.T, env *env, fix pubFix) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, env.base+"/api/v1/agent/runs/"+fix.viktorRun+"/head.bundle", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer bundle-token-success")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

type argLog struct {
	path string
}

func gitArgLog(t *testing.T) argLog {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args")
	real := os.Getenv("PATH")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"case \"$*\" in\n" +
		"*" + publishToken + "*) echo LEAK >> " + shellQuote(logPath) + "; exit 97 ;;\n" +
		"esac\n" +
		"if [ \"$GIT_CONFIG_KEY_0\" = \"http.extraheader\" ]; then echo KEY_OK >> " + shellQuote(logPath) + "; fi\n" +
		"if [ \"$GIT_CONFIG_VALUE_0\" = " + shellQuote("Authorization: Basic "+basicGitToken()) + " ]; then echo VALUE_OK >> " + shellQuote(logPath) + "; fi\n" +
		"case \"$GIT_CONFIG_VALUE_0\" in\n" +
		"*'Bearer '*) echo BEARER >> " + shellQuote(logPath) + " ;;\n" +
		"esac\n" +
		"if [ -n \"$GIT_CONFIG_KEY_0\" ] && [ \"$GIT_CONFIG_KEY_0\" != \"http.extraheader\" ]; then echo BAD_KEY >> " + shellQuote(logPath) + "; fi\n" +
		"export PATH=" + shellQuote(real) + "\n" +
		"exec git \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_KEY_0", "filter.pwn.clean")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+real)
	return argLog{path: logPath}
}

func (a argLog) text(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type publishGitHub struct {
	srv       *httptest.Server
	project   string
	mu        sync.Mutex
	calls     []string
	body      []byte
	mode      string
	wantSHA   string
	onToken   func()
	onReceive func()
	onListed  func()
	adoptRef  string
	pushed    bool
}

func newPublishGitHub(t *testing.T, sha, mode string) *publishGitHub {
	t.Helper()
	project := t.TempDir()
	repo := filepath.Join(project, "northstar-ai-labs", "dreaming.git")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	gitServer(t, project, "init", "--bare", "-b", "main", repo)
	gitServer(t, repo, "config", "http.receivepack", "true")
	seedMain(t, repo)
	if mode == "ref" {
		seedRef(t, repo)
	}
	fake := &publishGitHub{project: project, mode: mode, wantSHA: sha}
	fake.srv = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.srv.Close)
	return fake
}

func seedMain(t *testing.T, bare string) {
	t.Helper()
	dir := t.TempDir()
	gitServer(t, dir, "init", "-b", "main")
	gitServer(t, dir, "config", "user.email", "t@example.com")
	gitServer(t, dir, "config", "user.name", "t")
	gitServer(t, dir, "commit", "--allow-empty", "-m", "main")
	gitServer(t, dir, "push", bare, "HEAD:refs/heads/main")
}

func seedRef(t *testing.T, bare string) {
	t.Helper()
	dir := t.TempDir()
	gitServer(t, dir, "init")
	gitServer(t, dir, "config", "user.email", "t@example.com")
	gitServer(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitServer(t, dir, "add", "f")
	gitServer(t, dir, "commit", "-m", "old")
	gitServer(t, dir, "push", bare, "HEAD:refs/heads/factory/dreaming-454-old")
}

func (f *publishGitHub) serve(w http.ResponseWriter, r *http.Request) {
	buf, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
	if r.Method == http.MethodPost && r.URL.Path == "/repos/northstar-ai-labs/dreaming/pulls" {
		f.body = append([]byte(nil), buf...)
	}
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/installation":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7}`))
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/access_tokens"):
		if f.onToken != nil {
			f.onToken()
		}
		var in struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		if json.Unmarshal(buf, &in) != nil || len(in.Repositories) != 1 || in.Repositories[0] != "dreaming" {
			http.Error(w, "repos", http.StatusBadRequest)
			return
		}
		if in.Permissions["contents"] != "write" || in.Permissions["pull_requests"] != "write" || in.Permissions["metadata"] != "read" || len(in.Permissions) != 3 {
			http.Error(w, "perms", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, publishToken, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/northstar-ai-labs/dreaming/pulls":
		if r.URL.Query().Get("state") != "all" || r.URL.Query().Has("head") {
			http.Error(w, "query", http.StatusBadRequest)
			return
		}
		if f.mode == "403" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"denied ` + publishToken + `"}`))
			return
		}
		if f.mode == "pr" {
			_, _ = w.Write([]byte(`[{"number":8,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/8","draft":false,"state":"closed","head":{"ref":"factory/dreaming-454-old","sha":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}]`))
			return
		}
		f.mu.Lock()
		pushed := f.pushed
		listed := f.onListed
		if pushed {
			f.onListed = nil
		}
		adopt := f.adoptRef
		f.mu.Unlock()
		if pushed && listed != nil {
			listed()
		}
		if pushed && adopt != "" {
			_, _ = fmt.Fprintf(w, `[{"number":11,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/11","draft":true,"state":"open","head":{"ref":%q,"sha":%q}}]`, adopt, f.wantSHA)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	case r.Method == http.MethodPost && r.URL.Path == "/repos/northstar-ai-labs/dreaming/pulls":
		if f.mode == "422" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"draft refused"}`))
			return
		}
		var requested struct {
			Head string `json:"head"`
		}
		_ = json.Unmarshal(buf, &requested)
		draft := f.mode != "not-draft"
		sha := f.wantSHA
		if f.mode == "mismatch" {
			sha = strings.Repeat("c", 40)
		}
		f.mu.Lock()
		f.body = append([]byte(nil), buf...)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":%t,"state":"open","head":{"ref":%q,"sha":%q}}`, draft, requested.Head, sha)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/northstar-ai-labs/dreaming/pulls/"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"number":9,"html_url":"https://github.com/northstar-ai-labs/dreaming/pull/9","draft":true,"state":"open","head":{"ref":%q,"sha":%q}}`, f.headRef(r), f.wantSHA)
	case strings.Contains(r.URL.Path, "/northstar-ai-labs/dreaming.git/"):
		f.gitHTTP(w, r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			f.mu.Lock()
			f.pushed = true
			f.mu.Unlock()
			if f.onReceive != nil {
				f.onReceive()
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *publishGitHub) headRef(r *http.Request) string {
	_ = r
	f.mu.Lock()
	defer f.mu.Unlock()
	var body struct {
		Head string `json:"head"`
	}
	_ = json.Unmarshal(f.body, &body)
	if body.Head != "" {
		return body.Head
	}
	return publishRefPrefix + "inv_xxxx"
}

func (f *publishGitHub) gitHTTP(w http.ResponseWriter, r *http.Request) {
	if !gitSmartAuthorized(r) {
		rejectGitAuth(w)
		return
	}
	backend := filepath.Join("/usr/lib/git-core", "git-http-backend")
	cmd := exec.Command(backend)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_PROJECT_ROOT=" + f.project,
		"GIT_HTTP_EXPORT_ALL=1",
		"REQUEST_METHOD=" + r.Method,
		"PATH_INFO=" + r.URL.Path,
		"QUERY_STRING=" + r.URL.RawQuery,
		"CONTENT_TYPE=" + r.Header.Get("Content-Type"),
		"REMOTE_USER=git",
		"REMOTE_ADDR=127.0.0.1",
	}
	cmd.Stdin = r.Body
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		http.Error(w, "git backend", http.StatusBadGateway)
		return
	}
	raw := stdout.Bytes()
	head, body, ok := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !ok {
		head, body, ok = bytes.Cut(raw, []byte("\n\n"))
	}
	if !ok {
		http.Error(w, "git cgi", http.StatusBadGateway)
		return
	}
	status := http.StatusOK
	for _, line := range bytes.Split(head, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		name, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			continue
		}
		if strings.EqualFold(string(name), "Status") {
			_, _ = fmt.Sscanf(strings.TrimSpace(string(value)), "%d", &status)
			continue
		}
		w.Header().Add(string(bytes.TrimSpace(name)), strings.TrimSpace(string(value)))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (f *publishGitHub) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		gotMethod, rest, _ := strings.Cut(call, " ")
		gotPath, _, _ := strings.Cut(rest, "?")
		if gotMethod == method && gotPath == path {
			n++
		}
	}
	return n
}

func (f *publishGitHub) createBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.body)
}

func (f *publishGitHub) assertRoutes(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	allowed := func(method, path, raw string) bool {
		switch {
		case method == http.MethodGet && path == "/repos/northstar-ai-labs/dreaming/installation":
			return true
		case method == http.MethodPost && strings.Contains(path, "/access_tokens"):
			return true
		case method == http.MethodGet && strings.HasSuffix(path, "/info/refs"):
			return strings.Contains(raw, "service=git-upload-pack") || strings.Contains(raw, "service=git-receive-pack")
		case method == http.MethodPost && strings.HasSuffix(path, "/git-receive-pack"):
			return true
		case method == http.MethodGet && path == "/repos/northstar-ai-labs/dreaming/pulls":
			return strings.Contains(raw, "state=all") && !strings.Contains(raw, "head=")
		case method == http.MethodPost && path == "/repos/northstar-ai-labs/dreaming/pulls":
			return true
		case method == http.MethodGet && strings.HasPrefix(path, "/repos/northstar-ai-labs/dreaming/pulls/"):
			return true
		default:
			return false
		}
	}
	for _, call := range f.calls {
		method, rest, _ := strings.Cut(call, " ")
		path, raw, _ := strings.Cut(rest, "?")
		if !allowed(method, path, raw) {
			t.Fatalf("route %s %s", method, path)
		}
	}
}

func (f *publishGitHub) sawReady(t *testing.T) bool {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if strings.Contains(call, "merge") || strings.Contains(call, "requested_reviewers") || strings.Contains(call, "/labels") || strings.Contains(call, "/comments") {
			return true
		}
	}
	return false
}

func basicGitToken() string {
	return base64.StdEncoding.EncodeToString([]byte("x-access-token:" + publishToken))
}

func gitSmartAuthorized(r *http.Request) bool {
	user, pass, ok := basicCredentials(r.Header.Get("Authorization"))
	return ok && user == "x-access-token" && pass == publishToken
}

func basicCredentials(header string) (string, string, bool) {
	const prefix = "Basic "
	if !strings.HasPrefix(header, prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok && user != "" && pass != ""
}

func rejectGitAuth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, GitBearerRejected+"\n")
}

func TestPublishGitRejectsBearer(t *testing.T) {
	fake := newPublishGitHub(t, strings.Repeat("ab", 20), "")
	bearer, err := http.NewRequest(http.MethodGet, fake.srv.URL+"/northstar-ai-labs/dreaming.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatal(err)
	}
	bearer.Header.Set("Authorization", "Bearer "+publishToken)
	res, err := http.DefaultClient.Do(bearer)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), GitBearerRejected) {
		t.Fatalf("bearer status %d body %q", res.StatusCode, body)
	}
	basic, err := http.NewRequest(http.MethodGet, fake.srv.URL+"/northstar-ai-labs/dreaming.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		t.Fatal(err)
	}
	basic.Header.Set("Authorization", "Basic "+basicGitToken())
	okRes, err := http.DefaultClient.Do(basic)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, okRes.Body)
	_ = okRes.Body.Close()
	if okRes.StatusCode != http.StatusOK {
		t.Fatalf("basic status %d", okRes.StatusCode)
	}
}

func TestGitHTTPAuthHeader(t *testing.T) {
	got, err := GitHTTPAuthHeader(publishToken)
	if err != nil {
		t.Fatal(err)
	}
	want := "Authorization: Basic " + basicGitToken()
	if got != want {
		t.Fatalf("header %s", got)
	}
	if strings.Contains(got, "Bearer") || strings.Contains(got, publishToken) {
		t.Fatal("header kept bearer or the raw token")
	}
	if _, err := GitHTTPAuthHeader("bad\ntoken"); err == nil {
		t.Fatal("newline token was accepted")
	}
}
