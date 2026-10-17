package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/protocol"
)

var (
	harnessOnce sync.Once
	harnessBin  string
	harnessErr  error
	stubOnce    sync.Once
	stubBin     string
	stubErr     error
)

func TestE2EM5(t *testing.T) {
	skipNoDB(t)
	t.Run("agent_reported", func(t *testing.T) { testE2EM5Agent(t) })
	t.Run("worker_killed", func(t *testing.T) { testE2EM5Killed(t) })
	t.Run("steer", func(t *testing.T) { testE2EM5Steer(t) })
	t.Run("no_capability", func(t *testing.T) { testE2EM5NoCapability(t) })
	t.Run("sse", func(t *testing.T) {
		env := boot(t, bootOpts{})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		resumeFactorySSE(t, env, w)
	})
	t.Run("secrets", func(t *testing.T) {
		env := boot(t, bootOpts{})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "secrets")
		asg := w.readAssignment(t)
		if asg.TaskID != runID {
			t.Fatalf("assignment %s", asg.TaskID)
		}
		walkPlanted(t, env, asg.EnvVars["FACTORY_RUN_TOKEN"])
	})
	t.Run("backfill", func(t *testing.T) {
		env := boot(t, bootOpts{})
		assertBackfillParity(t, env)
	})
}

func testE2EM5Agent(t *testing.T) {
	t.Helper()
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "make the tests fail")
	asg := w.readAssignment(t)
	model := startModelStub(t, `[{"final":"{\"status\":\"failed_by_agent\",\"summary\":\"tests failed\"}"}]`)
	cmd := startHarness(t, asg, model, "0s")
	waitHarness(t, cmd, asg.EnvVars["FACTORY_RUN_TOKEN"], 10)
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: runID, ExecutionID: asg.ExecutionID, Message: "container exited",
		FailureReason: "container_exit", ExitCode: 10,
	})
	env.waitStatus(t, runID, "failed")
	detail := env.runDetail(t, runID)
	if detail.Failure.Class != "agent" || detail.Failure.Code != "agent_reported" {
		t.Fatalf("class %s/%s", detail.Failure.Class, detail.Failure.Code)
	}
	var exit *int
	if err := env.raw.QueryRow(context.Background(), `SELECT exit_code FROM runs WHERE id = $1`, runID).Scan(&exit); err != nil {
		t.Fatal(err)
	}
	if exit == nil || *exit != 10 {
		t.Fatalf("exit %v", exit)
	}
	walkPlanted(t, env, asg.EnvVars["FACTORY_RUN_TOKEN"])
}

func testE2EM5Killed(t *testing.T) {
	t.Helper()
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "killed")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: runID, ExecutionID: asg.ExecutionID, Message: "killed",
		FailureReason: "container_exit", ExitCode: 137,
	})
	env.waitStatus(t, runID, "failed")
	detail := env.runDetail(t, runID)
	if detail.Failure.Class != "infrastructure" || detail.Failure.Code != "container_killed" {
		t.Fatalf("class %s/%s", detail.Failure.Class, detail.Failure.Code)
	}
}

func testE2EM5NoCapability(t *testing.T) {
	t.Helper()
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "no steer")
	_ = w.readAssignment(t)
	env.waitStatus(t, runID, "running")
	code, raw := env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+runID+"/interrupt", "")
	assertSteerReason(t, code, raw, "worker/harness does not support steering")
}

func testE2EM5Steer(t *testing.T) {
	t.Helper()
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "steer me")
	asg := w.readAssignment(t)
	token := asg.EnvVars["FACTORY_RUN_TOKEN"]
	started := make(chan struct{})
	var once sync.Once
	var calls int
	var mu sync.Mutex
	model := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			once.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(rw, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	t.Cleanup(model.Close)
	// The steer route holds an empty poll for 25s, and the harness emits
	// run.capabilities only after that first 200. Plant the capability the
	// harness is about to send so the interrupt is accepted while the poller
	// is already up, then let the harness emit the real events.
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], token, 100, []string{
		`{"type":"run.capabilities","steer":true}`,
	})
	cmd := startHarness(t, asg, model.URL, "0s")
	select {
	case <-started:
	case <-time.After(8 * time.Second):
		t.Fatal(harnessOutput(cmd, token))
	}
	code, raw := env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+runID+"/interrupt", "")
	if code != http.StatusAccepted {
		t.Fatalf("interrupt %d %s\n%s", code, raw, harnessOutput(cmd, token))
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+runID+"/followups", `{"text":"continue"}`)
	if code != http.StatusAccepted {
		t.Fatalf("followup %d %s\n%s", code, raw, harnessOutput(cmd, token))
	}
	waitEvent(t, env, runID, "turn.interrupted")
	waitEvent(t, env, runID, "followup.received")
	waitHarness(t, cmd, token, 0)
	walkPlanted(t, env, token)
}

func waitEvent(t *testing.T, env *env, runID, typ string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if env.hasEventType(t, runID, typ) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("missing event %s", typ)
}

func walkPlanted(t *testing.T, env *env, token string) {
	t.Helper()
	paths := []string{
		"/api/v1/factory/m2/definition",
		"/api/v1/agent/runs",
		"/api/v1/factory/m2/secrets",
		"/api/v1/agent/identities",
		"/api/v1/agent/connected-self-hosted-workers",
		"/api/v1/factory/m2/webhooks",
		"/api/v1/factory/m2/automations",
		"/api/v1/factory/m2/inbox",
		"/api/v1/factory/m2/summary?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z",
	}
	for _, path := range paths {
		code, raw := env.authed(t, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("%s %d %s", path, code, raw)
		}
		forbidPlanted(t, path, string(raw), token)
	}
}

type harnessCmd struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	done   chan error
}

func startHarness(t *testing.T, asg protocol.TaskAssignment, modelURL, idle string) *harnessCmd {
	t.Helper()
	bin := ensureHarness(t)
	rt := t.TempDir()
	ws := t.TempDir()
	task := map[string]string{"prompt": "do the task", "model_id": "test-model"}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"FACTORY_RUNTIME_DIR=" + rt,
		"OPENAI_BASE_URL=" + modelURL,
	}
	for key, value := range asg.EnvVars {
		if key == "OPENAI_BASE_URL" {
			continue
		}
		env = append(env, key+"="+value)
	}
	cmd := exec.Command(bin, "--task-id", asg.TaskID, "--model", "test-model", "--idle-on-complete", idle)
	cmd.Dir = ws
	cmd.Env = env
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out := &harnessCmd{cmd: cmd, stderr: stderr, done: make(chan error, 1)}
	go func() { out.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return out
}

func waitHarness(t *testing.T, cmd *harnessCmd, token string, want int) {
	t.Helper()
	select {
	case err := <-cmd.done:
		if cmd.cmd.ProcessState == nil || cmd.cmd.ProcessState.ExitCode() != want {
			code := -1
			if cmd.cmd.ProcessState != nil {
				code = cmd.cmd.ProcessState.ExitCode()
			}
			t.Fatalf("harness exit %d (%v)\n%s", code, err, harnessText(cmd.stderr.String(), token))
		}
	case <-time.After(20 * time.Second):
		t.Fatal(harnessOutput(cmd, token))
	}
}

func harnessOutput(cmd *harnessCmd, token string) string {
	return harnessText(cmd.stderr.String(), token)
}

func harnessText(text, token string) string {
	if token != "" {
		text = strings.ReplaceAll(text, token, "[REDACTED]")
	}
	if strings.TrimSpace(text) == "" {
		return "harness produced no stderr"
	}
	return text
}

func ensureHarness(t *testing.T) string {
	t.Helper()
	harnessOnce.Do(func() {
		dir, err := os.MkdirTemp("", "factory-harness-bin")
		if err != nil {
			harnessErr = err
			return
		}
		harnessBin = filepath.Join(dir, "factory-harness")
		cmd := exec.Command("go", "build", "-o", harnessBin, "./cmd/factory-harness")
		cmd.Dir = moduleRoot(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			harnessErr = err
			harnessBin = string(out)
		}
	})
	if harnessErr != nil {
		t.Fatalf("build harness: %v %s", harnessErr, harnessBin)
	}
	return harnessBin
}

func startModelStub(t *testing.T, script string) string {
	t.Helper()
	bin := ensureStub(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-addr", addr, "-script", path)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return "http://" + addr
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("model stub did not listen")
	return ""
}

func ensureStub(t *testing.T) string {
	t.Helper()
	stubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "factory-model-stub")
		if err != nil {
			stubErr = err
			return
		}
		stubBin = filepath.Join(dir, "model-stub")
		cmd := exec.Command("go", "build", "-o", stubBin, "./spike/m1/model-stub")
		cmd.Dir = moduleRoot(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			stubErr = err
			stubBin = string(out)
		}
	})
	if stubErr != nil {
		t.Fatalf("build model stub: %v %s", stubErr, stubBin)
	}
	return stubBin
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestM5PoolsNilP50(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/m2/summary?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z", "")
	if code != http.StatusOK {
		t.Fatalf("summary %d %s", code, raw)
	}
	if !strings.Contains(string(raw), `"queue_wait_p50_ms":0`) {
		t.Fatalf("summary %s", raw)
	}
}
