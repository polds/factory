package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWorkspaceHeadMatchesRevParseAndBundleTip(t *testing.T) {
	repo := evidenceRepo(t)
	sha := head(t, repo)
	var posted []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "head.bundle") {
			body, _ := io.ReadAll(r.Body)
			posted = body
			if r.Header.Get("X-Factory-Head") != sha || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	log := filepath.Join(t.TempDir(), "git.log")
	restore := wrapGit(t, log)
	defer restore()
	runtime := t.TempDir()
	var buf bytes.Buffer
	opt := Options{
		Workspace:  repo,
		RuntimeDir: runtime,
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"FACTORY_EVENTS_URL=" + srv.URL + "/api/v1/agent/runs/run/events:ingest",
			"FACTORY_RUN_TOKEN=run-token",
		},
	}
	if err := RecordWorkspaceHead(context.Background(), opt, NewLog(&buf, opt.Now)); err != nil {
		t.Fatal(err)
	}
	events := parseEvidence(t, &buf)
	if len(events) != 1 || events[0].Type != EventWorkspaceHead || events[0].Text != sha || strings.TrimSpace(events[0].Text) != sha {
		t.Fatalf("event %+v", events)
	}
	if _, err := os.Stat(filepath.Join(runtime, "head.bundle")); !os.IsNotExist(err) {
		t.Fatal("bundle file left in the runtime dir")
	}
	if err := VerifyBundleTip(context.Background(), writeTemp(t, posted), sha); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	gitCmd(t, other, "init")
	gitCmd(t, other, "config", "user.email", "t@example.com")
	gitCmd(t, other, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, other, "add", "other.txt")
	gitCmd(t, other, "commit", "-m", "other")
	otherSHA := head(t, other)
	if otherSHA == sha {
		t.Fatal("fixture SHAs collided")
	}
	if err := VerifyBundleTip(context.Background(), writeTemp(t, posted), otherSHA); err == nil || !strings.Contains(err.Error(), "tip") {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(recorded), "push") || strings.Contains(string(recorded), "fetch") {
		t.Fatalf("git log %s %v", recorded, err)
	}
}

func TestReviewerCheckoutUsesBundleAndSkipsModel(t *testing.T) {
	repo := evidenceRepo(t)
	sha := head(t, repo)
	bundle := filepath.Join(t.TempDir(), "head.bundle")
	gitCmd(t, repo, "bundle", "create", bundle, "HEAD")
	body, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var deleted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write(body)
		case http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	source, _, _ := twoCommitRepo(t, "https://github.com/example/other.git")
	marker := []byte("local-source-marker\n")
	if err := os.WriteFile(filepath.Join(source, "LOCAL_SOURCE"), marker, 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	opt := proofOptions(t, workspace, "", source, sha, "go test ./stolen")
	opt.Env = append(opt.Env,
		EnvReviewHead+"="+sha,
		EnvBundleURL+"="+srv.URL+"/head.bundle",
		EnvOriginURL+"=https://github.com/example/fixture.git",
		"FACTORY_RUN_TOKEN=run-token",
		EnvEvidencePackage+"=./oracle",
		EnvEvidenceMutation+"=oracle/oracle.go",
		"HOME="+os.Getenv("HOME"),
		"GOTOOLCHAIN=local",
	)
	before := factoryPorcelain(t)
	task, err := MaterializeAssigned(context.Background(), opt, ParseArgs(opt.Args))
	if err != nil {
		t.Fatal(err)
	}
	if head(t, workspace) != sha || task.EvidencePackage != "./oracle" || task.PinnedHeadSHA != sha {
		t.Fatalf("head %s task %+v", head(t, workspace), task)
	}
	if _, err := os.Stat(filepath.Join(workspace, "LOCAL_SOURCE")); !os.IsNotExist(err) {
		t.Fatal("local object source was copied")
	}
	if !deleted.Load() {
		t.Fatal("bundle was not deleted after checkout")
	}
	if after := factoryPorcelain(t); after != before {
		t.Fatalf("factory status changed\n%s", after)
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(model.Close)
	opt.Env = append(opt.Env, "OPENAI_BASE_URL="+model.URL, "OPENAI_API_KEY=test")
	code := Run(context.Background(), opt)
	events, err := os.ReadFile(filepath.Join(opt.RuntimeDir, "events.ndjson"))
	if err != nil || code != ExitOK || calls.Load() != 0 {
		t.Fatalf("code %d calls %d %v %s", code, calls.Load(), err, events)
	}
	text := string(events)
	if !strings.Contains(text, `"type":"evidence.test"`) || !strings.Contains(text, `"status":"tests_failed"`) || !strings.Contains(text, `"status":"tests_passed"`) {
		t.Fatalf("events %s", text)
	}
	if strings.Contains(text, `"type":"workspace.head"`) || strings.Contains(text, `"type":"tool.result"`) {
		t.Fatalf("events %s", text)
	}
	if head(t, workspace) != sha || head(t, repo) != sha {
		t.Fatal("HEAD moved")
	}
	porcelain, err := os.ReadFile(filepath.Join(workspace, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	_ = porcelain
	out := gitOut(t, workspace, "status", "--porcelain=v1")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("dirty %s", out)
	}

	missing := proofOptions(t, filepath.Join(t.TempDir(), "missing"), "", source, sha, "prompt")
	missing.Env = append(missing.Env,
		EnvReviewHead+"="+sha,
		EnvBundleURL+"="+srv.URL+"/missing-bundle",
		EnvOriginURL+"=https://github.com/example/fixture.git",
		"FACTORY_RUN_TOKEN=run-token",
		EnvLocalSource+"="+source,
	)
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(gone.Close)
	for i, env := range missing.Env {
		if strings.HasPrefix(env, EnvBundleURL+"=") {
			missing.Env[i] = EnvBundleURL + "=" + gone.URL + "/head.bundle"
		}
	}
	if _, err := MaterializeAssigned(context.Background(), missing, ParseArgs(missing.Args)); err == nil {
		t.Fatal("missing bundle checked out")
	}
	if _, err := os.Stat(filepath.Join(missing.Workspace, "LOCAL_SOURCE")); !os.IsNotExist(err) {
		t.Fatal("missing bundle copied the local source")
	}
}

func TestImplementerRecordsHeadAfterModel(t *testing.T) {
	repo := evidenceRepo(t)
	sha := head(t, repo)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") || r.URL.Path == "/chat/completions" {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	runtime := t.TempDir()
	opt := Options{
		Args:       []string{"agent", "run", "--task-id", "impl", "--model", "fake-model", "--idle-on-complete", "0s"},
		Workspace:  repo,
		RuntimeDir: runtime,
		Env: []string{
			"PATH=" + os.Getenv("PATH"),
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=test",
			"FACTORY_TASK_PROMPT=implement",
			"FACTORY_EVENTS_URL=" + srv.URL + "/api/v1/agent/runs/run/events:ingest",
			"FACTORY_RUN_TOKEN=run-token",
		},
	}
	if err := os.WriteFile(filepath.Join(runtime, "task.json"), []byte("{\"prompt\":\"implement\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := Run(context.Background(), opt)
	events, err := os.ReadFile(filepath.Join(runtime, "events.ndjson"))
	if err != nil || code != ExitOK || calls.Load() == 0 {
		t.Fatalf("code %d calls %d %v %s", code, calls.Load(), err, events)
	}
	if !strings.Contains(string(events), `"type":"workspace.head"`) || !strings.Contains(string(events), `"text":"`+sha+`"`) {
		t.Fatalf("events %s", events)
	}
	var saw bool
	for _, line := range bytes.Split(bytes.TrimSpace(events), []byte("\n")) {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == EventWorkspaceHead {
			saw = ev.Text == sha
		}
		if ev.Type == "tool.result" && ev.Text != "" {
			t.Fatal("tool.result carried text")
		}
	}
	if !saw {
		t.Fatal("workspace.head text was not the SHA")
	}
	if _, err := os.Stat(filepath.Join(runtime, "head.bundle")); !os.IsNotExist(err) {
		t.Fatal("bundle left behind")
	}
}

func writeTemp(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "head.bundle")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func factoryPorcelain(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
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
