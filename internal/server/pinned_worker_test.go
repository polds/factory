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
	"testing"
	"time"

	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/store"
)

const pinnedWorker = "63dbb8ae8ccb3941392691f5c628e3365d8e248e"

// TestPinnedWorkerExecuteTask runs the pinned oz-agent-worker direct backend.
// Empty setup does not create a marker. A configured setup command does, even
// when the assignment sets FACTORY_PRE_HARNESS_SETUP=disabled. The empty
// setup case uses factory-harness so the assigned prompt and pin are what
// the checkout gate sees.
func TestPinnedWorkerExecuteTask(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	bin := pinnedWorkerBinary(t)
	repo, first, pin := serverTwoCommitRepo(t)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(model.Close)
	env := boot(t, bootOpts{
		definition: func(t *testing.T) string { return writeBundleDefinition(t, "") },
		configure: func(cfg *Config) {
			cfg.InferenceBaseURL = model.URL
			cfg.InferenceAPIKey = "test"
		},
	})
	body := "goal-bytes-worker"
	runID, prompt := queueRoleFiles(t, env, "native:worker:materialize", body, pin)
	cancelOtherRuns(t, env, runID)
	runtime := t.TempDir()
	workspace := t.TempDir()
	marker := filepath.Join(t.TempDir(), "setup-ran")
	harnessBin := buildFactoryHarness(t)
	wrapper := wrapHarness(t, harnessBin, filepath.Join(runtime, "env.txt"))
	code, log := runPinnedWorker(t, env, bin, workerRun{
		id: "proof-empty", config: workerConfig{
			id: "proof-empty", workspace: workspace, oz: wrapper, runtime: runtime, source: repo,
		},
	})
	if code != 0 || strings.Contains(log, "Task execution failed") {
		envDump, _ := os.ReadFile(filepath.Join(runtime, "env.txt"))
		kind, _ := exec.Command("git", "-C", repo, "cat-file", "-t", pin).CombinedOutput()
		t.Fatalf("empty setup worker %d pin %s kind %q\n%s\nenv:\n%s", code, pin, kind, log, envDump)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("empty setup created a marker")
	}
	if headOf(t, workspace) != pin || headOf(t, repo) != first {
		t.Fatalf("workspace %s source %s\n%s", headOf(t, workspace), headOf(t, repo), log)
	}
	raw, err := os.ReadFile(filepath.Join(runtime, "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task harness.TaskFile
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	if task.Prompt != prompt || task.PinnedHeadSHA != pin || task.BundleMode != "role_files" || task.PreHarnessSetup != harness.PreHarnessSetupDisabled || task.SetupCommand != "" {
		t.Fatalf("task file %+v", task)
	}
	if strings.Contains(string(raw), "roster-only-marker") || goalBodyText(t, task.Prompt) != body {
		t.Fatal("task file lost the goal or stored the roster")
	}

	setupRun, _ := queueRoleFiles(t, env, "native:worker:setup", body, pin)
	cancelOtherRuns(t, env, setupRun)
	workspace = t.TempDir()
	runtime = t.TempDir()
	flag := filepath.Join(t.TempDir(), "flag")
	stub := writeStubHarness(t)
	code, log = runPinnedWorker(t, env, bin, workerRun{
		id: "proof-setup", config: workerConfig{
			id: "proof-setup", workspace: workspace, oz: stub, runtime: runtime, source: repo,
			setup: "touch " + marker + "; printf '%s\\n' \"$FACTORY_PRE_HARNESS_SETUP\" > " + flag,
		},
	})
	if code != 0 {
		t.Fatalf("setup worker %d\n%s", code, log)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("configured setup did not run before the harness")
	}
	got, err := os.ReadFile(flag)
	if err != nil || strings.TrimSpace(string(got)) != harness.PreHarnessSetupDisabled {
		t.Fatalf("flag %q %v", got, err)
	}
}

type workerConfig struct {
	id, workspace, oz, runtime, source, setup string
}

type workerRun struct {
	id     string
	config workerConfig
}

func queueRoleFiles(t *testing.T, env *env, key, body, sha string) (string, string) {
	t.Helper()
	ctx := context.Background()
	inv := env.postJSON(t, "/api/v1/factory/pipe/invocations", map[string]any{
		"sourceOccurrenceKey": key,
		"intent":              "new",
		"priority":            "interactive",
		"inputs":              map[string]any{"instruction": body},
		"target":              map[string]any{"repository": "example/fixture", "ref": sha},
	}, http.StatusCreated)
	invocationID, _ := inv["invocationId"].(string)
	if invocationID == "" || inv["pinnedHeadSha"] != sha {
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
	act, err := c.ActivateNative(ctx, "pipe", opened.RootTaskID, client.NativeActivateRequest{AgentID: agents[0].ID})
	if err != nil || act.RunID == "" {
		t.Fatalf("activate %+v %v", act, err)
	}
	run, err := env.srv.store.GetRun(ctx, act.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.Prompt, "SEAT:nils-aaberg") {
		t.Fatal("queued prompt is not the seat")
	}
	return act.RunID, run.Prompt
}

func runPinnedWorker(t *testing.T, env *env, bin string, run workerRun) (int, string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "worker.yaml")
	var setup string
	if run.config.setup != "" {
		setup = "\n    setup_command: " + yamlString(run.config.setup)
	}
	body := "worker_id: " + run.config.id + "\n" +
		"cleanup: false\n" +
		"max_concurrent_tasks: 1\n" +
		"one_shot: true\n" +
		"backend:\n" +
		"  direct:\n" +
		"    target_dir: " + yamlString(run.config.workspace) + "\n" +
		"    oz_path: " + yamlString(run.config.oz) + setup + "\n" +
		"    environment:\n" +
		"      - name: FACTORY_LOCAL_OBJECT_SOURCE\n" +
		"        value: " + yamlString(run.config.source) + "\n" +
		"      - name: FACTORY_RUNTIME_DIR\n" +
		"        value: " + yamlString(run.config.runtime) + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"--config-file", cfg,
		"--api-key", env.workerKey,
		"--web-socket-url", "ws"+strings.TrimPrefix(env.base, "http")+"/api/v1/selfhosted/worker/ws",
		"--server-root-url", env.base,
		"--backend", "direct",
		"--one-shot",
		"--no-cleanup",
		"--worker-id", run.config.id,
		"--log-level", "info",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		} else {
			code = 1
		}
	}
	return code, string(out)
}

func cancelOtherRuns(t *testing.T, env *env, keep string) {
	t.Helper()
	ctx := context.Background()
	runs, err := env.srv.store.ListRuns(ctx, store.RunFilter{Status: store.StatusQueued, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.ID == keep {
			continue
		}
		if _, err := env.srv.store.RequestCancel(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func yamlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func wrapHarness(t *testing.T, bin, envFile string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wrap-harness")
	body := "#!/bin/sh\n{ env | sort; echo '--- args ---'; printf '%s\\n' \"$@\"; } > " + envFile + "\nexec " + bin + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeStubHarness(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub-harness")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildFactoryHarness(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factory-harness")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/factory-harness")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness build: %v %s", err, out)
	}
	return path
}

func pinnedWorkerBinary(t *testing.T) string {
	t.Helper()
	dest := filepath.Join(os.TempDir(), "oz-agent-worker-"+pinnedWorker)
	if info, err := os.Stat(dest); err == nil && info.Mode()&0o111 != 0 {
		return dest
	}
	src := filepath.Join(os.TempDir(), "oz-agent-worker-src-"+pinnedWorker)
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		git("init")
		git("fetch", "--depth", "1", "https://github.com/warpdotdev/oz-agent-worker.git", pinnedWorker)
		git("checkout", "FETCH_HEAD")
	}
	cmd := exec.Command("go", "build", "-o", dest, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned worker %s did not build: %v %s", pinnedWorker, err, out)
	}
	return dest
}
