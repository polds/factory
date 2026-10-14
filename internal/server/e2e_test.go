package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/artifacts"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/scheduler"
)

const (
	plantedGH     = "planted-secret-value"
	plantedNPM    = "planted-npm-token-value"
	plantedOpenAI = "planted-openai-key"
)

func TestDefaultCapsAdmitRuns(t *testing.T) {
	caps := defaultCaps()
	if caps.DefaultPerFactory <= 0 || caps.DefaultPerAgent <= 0 {
		t.Fatalf("caps %+v admit nothing; zero is a closed gate", caps)
	}
	plan := scheduler.Plan(time.Now(), []scheduler.Pending{{
		RunID: "run", FactoryID: "fac", AgentID: "agt", Pool: "standard", Platform: "linux",
		Priority: scheduler.Interactive, EnqueuedAt: time.Now(),
	}}, []scheduler.Candidate{{
		WorkerID: "w", Pool: "standard", Platform: "linux",
		LastHeartbeat: time.Now(), IdleSince: time.Now(), MaxConcurrent: 1,
	}}, nil, caps)
	if len(plan) != 1 {
		t.Fatal("positive defaults did not place a run")
	}
}

func TestLeaseTTLIsTwiceHeartbeat(t *testing.T) {
	if defaultLeaseTTL != 2*scheduler.HeartbeatWindow {
		t.Fatalf("lease ttl %s, heartbeat %s", defaultLeaseTTL, scheduler.HeartbeatWindow)
	}
}

func TestWorkerSeenDoesNotBlock(t *testing.T) {
	s := &Server{pings: make(chan beat, 1)}
	s.pings <- beat{workerID: "w"}
	done := make(chan struct{})
	go func() {
		s.WorkerSeen(context.Background(), "w", time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WorkerSeen blocked; pong is queued after it returns")
	}
}

func TestSecretAllowlistUnionsFactoryAndAgent(t *testing.T) {
	got := secretNames([]string{"GH_TOKEN", "SHARED"}, []string{"SHARED", "NPM_TOKEN"})
	want := []string{"GH_TOKEN", "SHARED", "NPM_TOKEN"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("allowlist %v", got)
	}
}

// TestE2E is the T12 integration test: a real store, the worker gateway, and
// the REST API in-process. Subtest names are the docs/04 server obligations.
func TestE2E(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}

	t.Run("lifecycle_fresh_connect_idempotent_terminal", func(t *testing.T) {
		env := boot(t, bootOpts{})
		w := dialWorker(t, env, "local-1")
		defer w.close()

		runID := env.startRun(t, "fix the docs")
		asg := w.readAssignment(t)
		if asg.TaskID != runID {
			t.Fatalf("task id %s", asg.TaskID)
		}
		if asg.ExecutionID == "" || asg.DockerImage != "factory-m1-task:dev" || asg.SidecarImage != "factory-harness:dev" {
			t.Fatalf("assignment images or execution missing, task %s", asg.TaskID)
		}
		if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.ModelID == nil || *asg.Task.AgentConfigSnapshot.ModelID != "test-model" {
			t.Fatal("model_id")
		}
		if asg.EnvVars["FACTORY_MAX_TURNS"] != "7" {
			t.Fatal("max turns")
		}
		if asg.EnvVars["GH_TOKEN"] != plantedGH || asg.EnvVars["NPM_TOKEN"] != plantedNPM {
			t.Fatal("secret allowlist was not the union of factory and agent secrets")
		}
		if asg.EnvVars["OPENAI_BASE_URL"] == "" || asg.EnvVars["OPENAI_API_KEY"] != plantedOpenAI {
			t.Fatal("inference env")
		}
		token := asg.EnvVars["FACTORY_RUN_TOKEN"]
		if token == "" || !strings.Contains(asg.EnvVars["FACTORY_EVENTS_URL"], runID) {
			t.Fatal("event push env")
		}
		if !strings.Contains(asg.EnvVars["FACTORY_SECRET_VALUES"], token) || !strings.Contains(asg.EnvVars["FACTORY_SECRET_VALUES"], plantedGH) {
			t.Fatal("FACTORY_SECRET_VALUES")
		}
		env.forbidSecrets(t, token)

		sseLines := make(chan string, 16)
		sseCtx, sseCancel := context.WithCancel(context.Background())
		defer sseCancel()
		go env.readSSE(t, sseCtx, runID, sseLines)

		w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: runID, WorkerID: "local-1"})
		postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], token, 0, []string{
			`{"type":"message.delta","text":"hi"}`,
			`{"type":"tool.call","tool":"bash","redacted_input":"ls"}`,
			`{"type":"usage","input_tokens":1,"output_tokens":2}`,
		})
		w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: runID, ExecutionID: asg.ExecutionID, Message: "done"})
		w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: runID, ExecutionID: asg.ExecutionID, Message: "done"})

		env.waitStatus(t, runID, "succeeded")
		metricsRes, err := http.Get(env.base + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		metricsBody, _ := io.ReadAll(metricsRes.Body)
		_ = metricsRes.Body.Close()
		if metricsRes.StatusCode != http.StatusOK || !bytes.Contains(metricsBody, []byte("factory_time_to_assignment_seconds")) || !bytes.Contains(metricsBody, []byte("factory_reject_rate")) {
			t.Fatalf("metrics %d %s", metricsRes.StatusCode, metricsBody)
		}
		var sawTerm bool
		deadline := time.After(5 * time.Second)
		for !sawTerm {
			select {
			case line := <-sseLines:
				if strings.Contains(line, "event: run.terminal") {
					sawTerm = true
				}
			case <-deadline:
				t.Fatal("SSE did not deliver run.terminal")
			}
		}
		rows := env.eventRows(t, runID)
		if len(rows) < 4 {
			t.Fatalf("events %+v", rows)
		}
		if rows[0].seq != 1 || rows[3].seq != 4 || rows[3].typ != "run.terminal" {
			t.Fatalf("events %+v", rows)
		}
		var terminals int
		for _, row := range rows {
			if row.typ == "run.terminal" {
				terminals++
			}
		}
		if terminals != 1 {
			t.Fatalf("duplicate terminal events %+v", rows)
		}
		t.Run("obligation_env_redacted", func(t *testing.T) {
			env.forbidSecrets(t, token)
		})
		if os.Getenv("FACTORY_TEST_S3_ENDPOINT") != "" {
			body := env.archived(t, runID, asg.ExecutionID)
			if !bytes.Contains(body, []byte("run.terminal")) || !bytes.Contains(body, []byte("message.delta")) {
				t.Fatal("archive missing events")
			}
		}
	})

	t.Run("obligation_stale_heartbeat", func(t *testing.T) {
		var ahead atomic.Int64
		env := boot(t, bootOpts{now: func() time.Time {
			return time.Now().Add(time.Duration(ahead.Load()))
		}})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		w.ping(t)
		ahead.Store(int64(scheduler.HeartbeatWindow + time.Second))

		runID := env.startRun(t, "should stay queued")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if env.status(t, runID) != "queued" {
				t.Fatal("assigned to a worker whose heartbeat is older than 90s")
			}
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("obligation_reject_requeue", func(t *testing.T) {
		env := boot(t, bootOpts{})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "reject me")
		first := w.readAssignment(t)
		w.send(t, protocol.TypeTaskRejected, protocol.TaskRejected{TaskID: runID, Reason: "busy"})
		second := w.readAssignment(t)
		if second.ExecutionID == "" || second.ExecutionID == first.ExecutionID {
			t.Fatal("reject did not assign a new execution")
		}
		w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: runID, WorkerID: "local-1"})
		var attempts int
		if err := env.raw.QueryRow(context.Background(), `SELECT attempts FROM run_queue WHERE run_id = $1`, runID).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		if attempts < 1 {
			t.Fatalf("attempts %d", attempts)
		}
		w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: runID, ExecutionID: second.ExecutionID, Message: "ok"})
		env.waitStatus(t, runID, "succeeded")
		states := env.execStates(t, runID)
		if len(states) < 2 {
			t.Fatalf("executions %+v", states)
		}
		for _, st := range states {
			if st == "failed" {
				t.Fatalf("reject wrote a failure record: %+v", states)
			}
		}
	})

	t.Run("obligation_orphan_new_execution", func(t *testing.T) {
		env := boot(t, bootOpts{})
		w := dialWorker(t, env, "local-1")
		runID := env.startRun(t, "disconnect me")
		first := w.readAssignment(t)
		w.close()
		env.waitStatus(t, runID, "queued")
		if env.execState(t, first.ExecutionID) != "orphaned" {
			t.Fatal("prior execution was not orphaned")
		}
		w2 := dialWorker(t, env, "local-1")
		defer w2.close()
		second := w2.readAssignment(t)
		if second.ExecutionID == first.ExecutionID {
			t.Fatal("reused execution id")
		}
		w2.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: runID, ExecutionID: second.ExecutionID, Message: "ok"})
		env.waitStatus(t, runID, "succeeded")
	})

	t.Run("cancel_delivers_task_cancellation", func(t *testing.T) {
		env := boot(t, bootOpts{cancelGrace: 200 * time.Millisecond})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "cancel me")
		asg := w.readAssignment(t)
		env.cancel(t, runID)
		typ, raw := w.readEnvelope(t)
		if typ != protocol.TypeTaskCancellation {
			t.Fatalf("message %s %s", typ, raw)
		}
		var cancel protocol.TaskCancellation
		if err := json.Unmarshal(raw, &cancel); err != nil || cancel.TaskID != runID {
			t.Fatalf("cancellation %+v %v", cancel, err)
		}
		w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{TaskID: runID, ExecutionID: asg.ExecutionID, Message: "cancelled", ExitCode: 130})
		env.waitStatus(t, runID, "cancelled")
	})

	t.Run("cancel_grace_without_terminal", func(t *testing.T) {
		env := boot(t, bootOpts{cancelGrace: 200 * time.Millisecond})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "grace")
		_ = w.readAssignment(t)
		env.cancel(t, runID)
		env.waitStatus(t, runID, "cancelled")
	})

	t.Run("cancel_queued_writes_run_terminal", func(t *testing.T) {
		env := boot(t, bootOpts{})
		runID := env.startRun(t, "cancel before a worker")
		env.waitStatus(t, runID, "queued")
		env.cancel(t, runID)
		env.waitStatus(t, runID, "cancelled")
		env.requireOneTerminal(t, runID, "cancelled")
		if n := env.queueRows(t, runID); n != 0 {
			t.Fatalf("queue rows %d", n)
		}
	})

	t.Run("cancel_then_disconnect_stays_cancelled", func(t *testing.T) {
		env := boot(t, bootOpts{cancelGrace: 200 * time.Millisecond})
		w := dialWorker(t, env, "local-1")
		runID := env.startRun(t, "cancel then drop")
		asg := w.readAssignment(t)
		env.cancel(t, runID)
		w.close()
		env.waitStatus(t, runID, "cancelled")
		if env.execState(t, asg.ExecutionID) != "cancelled" {
			t.Fatalf("execution %s", env.execState(t, asg.ExecutionID))
		}
		env.requireOneTerminal(t, runID, "cancelled")
		if n := env.queueRows(t, runID); n != 0 {
			t.Fatalf("disconnect requeued the cancelled run (%d rows)", n)
		}
		w2 := dialWorker(t, env, "local-2")
		defer w2.close()
		if _, ok := w2.readAssignmentWithin(t, 400*time.Millisecond); ok {
			t.Fatal("cancelled run was assigned again")
		}
	})

	t.Run("cancel_then_reject_stays_cancelled", func(t *testing.T) {
		env := boot(t, bootOpts{cancelGrace: 200 * time.Millisecond})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "cancel then reject")
		asg := w.readAssignment(t)
		env.cancel(t, runID)
		w.send(t, protocol.TypeTaskRejected, protocol.TaskRejected{TaskID: runID, Reason: "busy"})
		env.waitStatus(t, runID, "cancelled")
		if env.execState(t, asg.ExecutionID) != "cancelled" {
			t.Fatalf("execution %s", env.execState(t, asg.ExecutionID))
		}
		env.requireOneTerminal(t, runID, "cancelled")
		if n := env.queueRows(t, runID); n != 0 {
			t.Fatalf("reject requeued the cancelled run (%d rows)", n)
		}
	})

	t.Run("cancel_grace_without_tracked_execution", func(t *testing.T) {
		env := boot(t, bootOpts{cancelGrace: 200 * time.Millisecond})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "grace without memory")
		asg := w.readAssignment(t)
		env.cancel(t, runID)
		env.srv.untrackRun(runID)
		env.waitStatus(t, runID, "cancelled")
		if env.execState(t, asg.ExecutionID) != "cancelled" {
			t.Fatalf("execution %s", env.execState(t, asg.ExecutionID))
		}
		env.requireOneTerminal(t, runID, "cancelled")
	})

	t.Run("wrong_run_token_unauthorized", func(t *testing.T) {
		env := boot(t, bootOpts{})
		res := env.postEventsStatus(t, "run_does_not_exist", "not-the-token", 0, `{"type":"message.delta","text":"x"}`)
		if res != http.StatusUnauthorized {
			t.Fatalf("missing run status %d", res)
		}
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "auth")
		asg := w.readAssignment(t)
		res = env.postEventsStatus(t, runID, "not-the-token", 0, `{"type":"message.delta","text":"x"}`)
		if res != http.StatusUnauthorized {
			t.Fatalf("bad bearer status %d", res)
		}
		res = env.postEventsStatus(t, runID, asg.EnvVars["FACTORY_RUN_TOKEN"], 0, `{"type":"message.delta","text":"ok"}`)
		if res != http.StatusNoContent {
			t.Fatalf("good bearer status %d", res)
		}
	})

	t.Run("sse_prestream_problem_json", func(t *testing.T) {
		env := boot(t, bootOpts{})
		req, err := http.NewRequest(http.MethodGet, env.base+"/api/v1/agent/runs/run_missing/events?from_seq=-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+env.apiKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d %s", res.StatusCode, body)
		}
		if !strings.Contains(res.Header.Get("Content-Type"), "application/problem+json") {
			t.Fatalf("content-type %q body %s", res.Header.Get("Content-Type"), body)
		}
	})

	t.Run("archive_transport_error_is_hard_failure", func(t *testing.T) {
		var trips atomic.Int32
		env := boot(t, bootOpts{s3: &artifacts.S3{
			Endpoint:  "http://127.0.0.1:9",
			Bucket:    "factory",
			AccessKey: "minio",
			SecretKey: "secretsecret",
			Region:    "us-east-1",
			PathStyle: true,
			HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				trips.Add(1)
				return nil, errors.New("dial tcp: connection refused")
			})},
		}})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		runID := env.startRun(t, "archive fails")
		asg := w.readAssignment(t)
		w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{TaskID: runID, ExecutionID: asg.ExecutionID, Message: "ok"})
		env.waitStatus(t, runID, "succeeded")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if env.hasEventType(t, runID, "transcript_unavailable") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !env.hasEventType(t, runID, "transcript_unavailable") {
			t.Fatal("archive failure was not marked transcript_unavailable")
		}
		if trips.Load() != 1 {
			t.Fatalf("archive transport attempts %d", trips.Load())
		}
	})

	t.Run("upstream_worker_dials", func(t *testing.T) {
		bin := os.Getenv("FACTORY_TEST_WORKER_BIN")
		if bin == "" {
			t.Skip("FACTORY_TEST_WORKER_BIN unset")
		}
		env := boot(t, bootOpts{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cmd := exec.CommandContext(ctx, bin,
			"--worker-id", "upstream-1",
			"--backend", "docker",
			"--api-key", env.workerKey,
			"--log-level", "error",
			"--max-concurrent-tasks", "1",
			"--web-socket-url", "ws"+strings.TrimPrefix(env.base, "http")+"/api/v1/selfhosted/worker/ws",
			"--server-root-url", env.base,
		)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.Stdout = &stderr
		cmd.Env = append(os.Environ(), "WARP_API_KEY="+env.workerKey)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cancel()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			for _, info := range env.srv.gw.Connected() {
				if info.WorkerID == "upstream-1" {
					return
				}
			}
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		out := strings.ReplaceAll(stderr.String(), env.workerKey, "[REDACTED]")
		t.Fatalf("upstream worker did not connect\n%s", out)
	})
}

type bootOpts struct {
	now         func() time.Time
	cancelGrace time.Duration
	s3          *artifacts.S3
}

type env struct {
	t         *testing.T
	srv       *Server
	base      string
	apiKey    string
	workerKey string
	logs      *bytes.Buffer
	raw       *pgx.Conn
	s3        *artifacts.S3
}

func boot(t *testing.T, opts bootOpts) *env {
	t.Helper()
	ctx := context.Background()
	base := os.Getenv("FACTORY_TEST_DATABASE_URL")
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "t_" + hex.EncodeToString(entropy[:])
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	rawDSN, err := withSearchPath(base, schema, false)
	if err != nil {
		t.Fatal(err)
	}
	poolDSN, err := withSearchPath(base, schema, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := pgx.Connect(ctx, rawDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = raw.Close(context.Background())
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
		_ = admin.Close(context.Background())
	})

	def := writeDefinition(t)
	var logs bytes.Buffer
	cfg := Config{
		DatabaseURL:         poolDSN,
		APIKey:              "test-api-key",
		WorkerAPIKey:        "test-worker-key",
		DefinitionPath:      def,
		TaskServerURL:       "http://127.0.0.1:1",
		InferenceBaseURL:    "http://model.example/v1",
		InferenceAPIKey:     plantedOpenAI,
		Secrets:             map[string]string{"GH_TOKEN": plantedGH, "NPM_TOKEN": plantedNPM},
		WorkerPool:          "standard",
		WorkerPlatform:      "linux",
		WorkerMaxConcurrent: 2,
		CancelGrace:         opts.cancelGrace,
		Now:                 opts.now,
		Log:                 newTestLogger(&logs),
	}
	var s3copy artifacts.S3
	if opts.s3 != nil {
		s3copy = *opts.s3
		cfg.S3 = s3copy
	} else if os.Getenv("FACTORY_TEST_S3_ENDPOINT") != "" {
		s3copy = artifacts.S3{
			Endpoint:  os.Getenv("FACTORY_TEST_S3_ENDPOINT"),
			Bucket:    "factory",
			AccessKey: os.Getenv("FACTORY_TEST_S3_KEY"),
			SecretKey: os.Getenv("FACTORY_TEST_S3_SECRET"),
			Region:    "us-east-1",
			PathStyle: true,
		}
		cfg.S3 = s3copy
	}
	srv, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	var handler http.Handler
	ready := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-ready
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	srv.cfg.TaskServerURL = ts.URL
	handler = srv.Handler()
	close(ready)

	e := &env{t: t, srv: srv, base: ts.URL, apiKey: cfg.APIKey, workerKey: cfg.WorkerAPIKey, logs: &logs, raw: raw}
	if cfg.S3.Endpoint != "" {
		held := cfg.S3
		e.s3 = &held
	}
	return e
}

func writeDefinition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "factory.yaml"), `
schemaVersion: v1alpha1
name: m2
secrets:
  - GH_TOKEN
agentDefaults:
  model: auto
  runner: local
  pool: standard
`)
	mustWrite(t, filepath.Join(root, "runners", "local.yaml"), `
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`)
	mustWrite(t, filepath.Join(root, "agents", "implementer", "agent.md"), `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
maxTurns: 7
---

Do the task.
`)
	return root
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) startRun(t *testing.T, prompt string) string {
	t.Helper()
	body := `{"factory":"m2","agent":"implementer","prompt":` + jsonString(prompt) + `,"priority":"interactive"}`
	req, err := http.NewRequest(http.MethodPost, e.base+"/api/v1/agent/run", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("start %d %s", res.StatusCode, raw)
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
		t.Fatalf("start body %s", raw)
	}
	return run.ID
}

func (e *env) status(t *testing.T, runID string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.base+"/api/v1/agent/runs/"+runID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get %d %s", res.StatusCode, raw)
	}
	if strings.Contains(string(raw), plantedGH) || strings.Contains(string(raw), plantedNPM) || strings.Contains(string(raw), plantedOpenAI) {
		t.Fatal("API response contains a secret value")
	}
	var run struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	return run.Status
}

func (e *env) waitStatus(t *testing.T, runID, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = e.status(t, runID)
		if last == want {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("status %s, want %s", last, want)
}

func (e *env) cancel(t *testing.T, runID string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.base+"/api/v1/agent/runs/"+runID+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("cancel %d %s", res.StatusCode, raw)
	}
}

func (e *env) postEventsStatus(t *testing.T, runID, token string, idx int, body string) int {
	t.Helper()
	res, err := http.DefaultClient.Do(eventRequest(t, e.base+"/api/v1/agent/runs/"+runID+"/events:ingest", token, idx, body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func (e *env) readSSE(t *testing.T, ctx context.Context, runID string, out chan<- string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/api/v1/agent/runs/"+runID+"/events", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = res.Body.Close() }()
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		case out <- sc.Text():
		}
	}
}

func (e *env) forbidSecrets(t *testing.T, token string) {
	t.Helper()
	text := e.logs.String()
	for _, secret := range []string{plantedGH, plantedNPM, plantedOpenAI, token} {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("log contains secret material (%d bytes)", len(secret))
		}
	}
	var n int
	err := e.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT prompt || ' ' || title AS hay FROM runs
			UNION ALL
			SELECT payload_json::text FROM run_events
			UNION ALL
			SELECT encode(run_token_sha256, 'escape') FROM executions
		) s
		WHERE hay LIKE '%' || $1 || '%'
		   OR hay LIKE '%' || $2 || '%'
		   OR hay LIKE '%' || $3 || '%'
		   OR ($4 <> '' AND hay LIKE '%' || $4 || '%')`,
		plantedGH, plantedNPM, plantedOpenAI, token).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("database text contains a secret or run token")
	}
}

type eventRow struct {
	seq int64
	typ string
}

func (e *env) eventRows(t *testing.T, runID string) []eventRow {
	t.Helper()
	rows, err := e.raw.Query(context.Background(), `SELECT seq, type FROM run_events WHERE run_id = $1 ORDER BY seq`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []eventRow
	for rows.Next() {
		var row eventRow
		if err := rows.Scan(&row.seq, &row.typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) requireOneTerminal(t *testing.T, runID, status string) {
	t.Helper()
	rows, err := e.raw.Query(context.Background(), `SELECT payload_json::text FROM run_events WHERE run_id = $1 AND type = 'run.terminal' ORDER BY seq`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("run.terminal payloads %+v", payloads)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(payloads[0]), &body); err != nil || body.Status != status {
		t.Fatalf("run.terminal payloads %+v", payloads)
	}
}

func (e *env) queueRows(t *testing.T, runID string) int {
	t.Helper()
	var n int
	if err := e.raw.QueryRow(context.Background(), `SELECT count(*) FROM run_queue WHERE run_id = $1`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) hasEventType(t *testing.T, runID, typ string) bool {
	t.Helper()
	for _, row := range e.eventRows(t, runID) {
		if row.typ == typ {
			return true
		}
	}
	return false
}

func (e *env) execStates(t *testing.T, runID string) []string {
	t.Helper()
	rows, err := e.raw.Query(context.Background(), `SELECT state FROM executions WHERE run_id = $1 ORDER BY attempt`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			t.Fatal(err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) execState(t *testing.T, execID string) string {
	t.Helper()
	var st string
	if err := e.raw.QueryRow(context.Background(), `SELECT state FROM executions WHERE id = $1`, execID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (e *env) archived(t *testing.T, runID, execID string) []byte {
	t.Helper()
	if e.s3 == nil {
		t.Fatal("s3 not configured")
	}
	rc, err := e.s3.Get(context.Background(), "runs/"+runID+"/"+execID+"/events.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type wsWorker struct {
	t    *testing.T
	conn *websocket.Conn
	mu   sync.Mutex
}

func dialWorker(t *testing.T, env *env, workerID string) *wsWorker {
	t.Helper()
	rawURL := "ws" + strings.TrimPrefix(env.base, "http") + "/api/v1/selfhosted/worker/ws?worker_id=" + url.QueryEscape(workerID)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+env.workerKey)
	conn, resp, err := websocket.DefaultDialer.Dial(rawURL, hdr)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			t.Fatalf("dial %v %s", err, body)
		}
		t.Fatal(err)
	}
	w := &wsWorker{t: t, conn: conn}
	return w
}

func (w *wsWorker) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.conn != nil {
		_ = w.conn.Close()
		w.conn = nil
	}
}

func (w *wsWorker) readEnvelope(t *testing.T) (string, json.RawMessage) {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	_, raw, err := w.conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.Type, env.Data
}

func (w *wsWorker) readAssignmentWithin(t *testing.T, d time.Duration) (protocol.TaskAssignment, bool) {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(d))
	_, raw, err := w.conn.ReadMessage()
	if err != nil {
		return protocol.TaskAssignment{}, false
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Type != protocol.TypeTaskAssignment {
		t.Fatalf("message %s %v", env.Type, err)
	}
	var asg protocol.TaskAssignment
	if err := json.Unmarshal(env.Data, &asg); err != nil {
		t.Fatal(err)
	}
	return asg, true
}

func (w *wsWorker) readAssignment(t *testing.T) protocol.TaskAssignment {
	t.Helper()
	typ, raw := w.readEnvelope(t)
	if typ != protocol.TypeTaskAssignment {
		t.Fatalf("want assignment, got %s", typ)
	}
	var asg protocol.TaskAssignment
	if err := json.Unmarshal(raw, &asg); err != nil {
		t.Fatal(err)
	}
	return asg
}

func (w *wsWorker) send(t *testing.T, typ string, payload any) {
	t.Helper()
	raw, err := protocol.Marshal(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := w.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatal(err)
	}
}

func (w *wsWorker) ping(t *testing.T) {
	t.Helper()
	got := make(chan struct{}, 1)
	w.conn.SetPongHandler(func(string) error {
		select {
		case got <- struct{}{}:
		default:
		}
		return nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := w.conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		w.close()
		<-done
	})
	w.mu.Lock()
	err := w.conn.WriteControl(websocket.PingMessage, []byte("x"), time.Now().Add(time.Second))
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("pong")
	}
}

func postEvents(t *testing.T, eventsURL, token string, idx int, lines []string) {
	t.Helper()
	res, err := http.DefaultClient.Do(eventRequest(t, eventsURL, token, idx, strings.Join(lines, "\n")+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("ingest %d %s", res.StatusCode, raw)
	}
}

func eventRequest(t *testing.T, eventsURL, token string, idx int, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, eventsURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Factory-Event-Index", jsonNumber(idx))
	req.Header.Set("Content-Type", "application/x-ndjson")
	return req
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func withSearchPath(raw, schema string, pool bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("search_path", schema)
	if pool {
		q.Set("pool_max_conns", "8")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
