package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBuildAssignmentM0Shape(t *testing.T) {
	now := time.Date(2026, 10, 13, 3, 20, 59, 0, time.UTC)
	const prompt = "M0 protocol spike: confirm the harness contract is reachable."
	a := buildAssignment(now, assignOpts{
		Prefix:       "run_m0_",
		Title:        "M0 protocol spike",
		Prompt:       prompt,
		Model:        "m0-stub-model",
		DockerImage:  "alpine:3.20",
		SidecarImage: "factory-m0-harness:dev",
	})
	if a.TaskID != "run_m0_20261013T032059" {
		t.Fatal(a.TaskID)
	}
	if a.ExecutionID != "exec_20261013T032059" {
		t.Fatal(a.ExecutionID)
	}
	if a.Task.Title != "M0 protocol spike" || a.Task.Definition.Prompt != prompt {
		t.Fatalf("task %+v", a.Task)
	}
	if a.Task.AgentConfigSnapshot == nil || a.Task.AgentConfigSnapshot.BasePrompt == nil ||
		*a.Task.AgentConfigSnapshot.BasePrompt != prompt {
		t.Fatal("base prompt")
	}
	if a.DockerImage != "alpine:3.20" || a.SidecarImage != "factory-m0-harness:dev" {
		t.Fatalf("images %s %s", a.DockerImage, a.SidecarImage)
	}
	if a.InstanceShape == nil || a.InstanceShape.Vcpus != 2 || a.InstanceShape.MemoryGb != 4 {
		t.Fatalf("shape %+v", a.InstanceShape)
	}
	if a.EnvVars["FACTORY_TASK_PROMPT"] != prompt || a.EnvVars["FACTORY_M0_MARKER"] != a.TaskID || len(a.EnvVars) != 2 {
		t.Fatal(a.EnvVars)
	}
}

func TestBuildAssignmentPromptAndMarkerPrecedence(t *testing.T) {
	now := time.Date(2026, 10, 13, 3, 20, 59, 0, time.UTC)
	in := map[string]string{
		"FACTORY_TASK_PROMPT": "from-env",
		"FACTORY_M0_MARKER":   "override-me",
		"X":                   "y",
	}
	a := buildAssignment(now, assignOpts{
		Prefix: "run_m1_",
		Title:  "M1 real agent run",
		Prompt: "from-flag",
		Model:  "gpt-test",
		Env:    in,
	})
	if a.TaskID != "run_m1_20261013T032059" {
		t.Fatal(a.TaskID)
	}
	if a.Task.Title != "M1 real agent run" || *a.Task.AgentConfigSnapshot.ModelID != "gpt-test" {
		t.Fatalf("title/model %+v", a.Task)
	}
	if a.EnvVars["FACTORY_TASK_PROMPT"] != "from-env" {
		t.Fatal(a.EnvVars["FACTORY_TASK_PROMPT"])
	}
	if a.EnvVars["FACTORY_M0_MARKER"] != a.TaskID {
		t.Fatal(a.EnvVars["FACTORY_M0_MARKER"])
	}
	if _, ok := in["FACTORY_TASK_PROMPT"]; !ok || in["FACTORY_M0_MARKER"] != "override-me" || len(in) != 3 {
		t.Fatalf("input env mutated: %v", in)
	}
}

func TestBuildAssignmentEmptyPromptSkipsTaskPrompt(t *testing.T) {
	now := time.Date(2026, 10, 13, 3, 20, 59, 0, time.UTC)
	a := buildAssignment(now, assignOpts{Prefix: "run_m0_", Env: map[string]string{"X": "y"}})
	if _, ok := a.EnvVars["FACTORY_TASK_PROMPT"]; ok {
		t.Fatalf("empty prompt set FACTORY_TASK_PROMPT: %v", a.EnvVars)
	}
	if a.EnvVars["FACTORY_M0_MARKER"] != a.TaskID || a.EnvVars["X"] != "y" || len(a.EnvVars) != 2 {
		t.Fatal(a.EnvVars)
	}
}

func TestFlagDefaultsMatchM0(t *testing.T) {
	fs := flag.NewFlagSet("m0-spike", flag.ContinueOnError)
	cfg := defineFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if *cfg.model != "m0-stub-model" || *cfg.title != "M0 protocol spike" || *cfg.taskIDPrefix != "run_m0_" {
		t.Fatalf("model=%s title=%s prefix=%s", *cfg.model, *cfg.title, *cfg.taskIDPrefix)
	}
	if *cfg.dockerImage != "alpine:3.20" || *cfg.sidecarImage != "factory-m0-harness:dev" {
		t.Fatalf("images %s %s", *cfg.dockerImage, *cfg.sidecarImage)
	}
	if *cfg.prompt != "M0 protocol spike: confirm the harness contract is reachable." {
		t.Fatal(*cfg.prompt)
	}
	if *cfg.envFile != "" || len(cfg.env.m) != 0 {
		t.Fatalf("env file %q env %v", *cfg.envFile, cfg.env.m)
	}
}

func TestServeLogsEnvKeysNotValues(t *testing.T) {
	const (
		secret = "sk-test-secret-value"
		prompt = "prompt-secret-do-not-log"
	)
	var buf safeLog
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				log.Printf("close websocket: %v", err)
			}
		}()
		res := &result{done: make(chan struct{})}
		serve(conn, "worker", res, assignOpts{
			Prefix:       "run_m0_",
			Title:        "M0 protocol spike",
			Prompt:       prompt,
			Model:        "m0-stub-model",
			DockerImage:  "alpine:3.20",
			SidecarImage: "factory-m0-harness:dev",
			Env:          map[string]string{"OPENAI_API_KEY": secret},
		}, 0)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close websocket: %v", err)
		}
	}()
	if resp != nil && resp.Body != nil {
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Logf("close response: %v", err)
			}
		}()
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var got taskAssignment
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.EnvVars["OPENAI_API_KEY"] != secret || got.EnvVars["FACTORY_TASK_PROMPT"] != prompt {
		t.Fatal("assignment did not carry env on the wire")
	}
	if *got.Task.AgentConfigSnapshot.ModelID != "m0-stub-model" || got.Task.Title != "M0 protocol spike" {
		t.Fatalf("model/title: %+v", got.Task)
	}

	// The assignment is logged after the socket write. Wait until that line
	// is visible; closing the client (via defer) then unblocks serve.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(buf.String(), "env_keys=") {
		time.Sleep(5 * time.Millisecond)
	}
	line := buf.String()
	if !strings.Contains(line, "env_keys=") || !strings.Contains(line, "OPENAI_API_KEY") || !strings.Contains(line, "FACTORY_M0_MARKER") {
		t.Fatalf("log missing keys: %s", line)
	}
	if strings.Contains(line, secret) || strings.Contains(line, prompt) {
		t.Fatalf("log leaked env values: %s", line)
	}
	wantKeys := "[FACTORY_M0_MARKER FACTORY_TASK_PROMPT OPENAI_API_KEY]"
	if !strings.Contains(line, wantKeys) {
		t.Fatalf("keys not sorted in log: %s", line)
	}
}

// safeLog is a bytes.Buffer the test can read while log.Printf writes it
// from the websocket handler goroutine.
type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
