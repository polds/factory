package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

// TestE2EAskInputReachesNextTurn runs the harness against the offline model.
// The model calls ask_input, the inbox stores that question, the answer API
// resumes the parked run, and the next model request contains the answer.
func TestE2EAskInputReachesNextTurn(t *testing.T) {
	e2eAskInputResume(t, "0s")
}

// TestE2EAskInputSkipsIdleAndResumes uses a positive idle duration. The
// harness must still return as soon as the question is emitted, and the
// answer API must resume that same run on the next assignment.
func TestE2EAskInputSkipsIdleAndResumes(t *testing.T) {
	e2eAskInputResume(t, "1m")
}

func e2eAskInputResume(t *testing.T, idle string) {
	t.Helper()
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	const question = "Which staging env?"
	const answer = "use-staging-2"
	chats := &chatLog{}
	model := httptest.NewServer(askThenDone(chats, question))
	t.Cleanup(model.Close)

	env := boot(t, bootOpts{
		definition: func(t *testing.T) string { return writePipeline(t, 3) },
		configure: func(cfg *Config) {
			cfg.InferenceBaseURL = model.URL
		},
	})
	w := dialWorker(t, env, "local-ask")
	defer w.close()

	task := env.createTask(t, "Ask from the harness", "pick a region")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-ask"})

	code, stderr, elapsed := runAssignedHarness(t, asg, idle)
	if code != harness.ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if idle != "0s" && elapsed >= 5*time.Second {
		t.Fatalf("harness waited %s with --idle-on-complete=%s", elapsed, idle)
	}
	if n := len(chats.snapshot()); n != 1 {
		t.Fatalf("model calls before the answer %d, want 1", n)
	}
	env.waitTask(t, task.ID, store.WorkItemAwaitingInput, "triage")
	assertOneQuestion(t, env, asg.TaskID, question)
	assertInboxQuestion(t, env, task.ID, asg.TaskID, question)
	// The worker reports process exit after the question is ingested.
	// Parking already ended the execution, so this must not finish the run.
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: "asked",
	})
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st := env.status(t, asg.TaskID); st == store.StatusSucceeded || st == store.StatusFailed || st == store.StatusCancelled {
			t.Fatalf("task_completed finished parked run: %s", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	status, raw := env.authed(t, http.MethodPost, "/api/v1/work-items/"+task.ID+"/input", `{"text":`+jsonString(answer)+`}`)
	if status != http.StatusOK {
		t.Fatalf("answer %d %s", status, raw)
	}
	next := w.readAssignment(t)
	if next.TaskID != asg.TaskID {
		t.Fatalf("resume run %s, want %s", next.TaskID, asg.TaskID)
	}
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: next.TaskID, WorkerID: "local-ask"})
	prompt := next.EnvVars["FACTORY_TASK_PROMPT"]
	if !strings.Contains(prompt, question) || !strings.Contains(prompt, answer) {
		t.Fatal("resumed prompt is missing the question and answer")
	}

	code, stderr, _ = runAssignedHarness(t, next, "0s")
	if code != harness.ExitOK {
		t.Fatalf("resume exit %d\n%s", code, stderr)
	}
	seen := chats.snapshot()
	if len(seen) != 2 {
		t.Fatalf("model calls %d, want 2", len(seen))
	}
	if userSaw(t, seen[0], answer) {
		t.Fatal("answer was visible before it was given")
	}
	if !userSaw(t, seen[1], question) || !userSaw(t, seen[1], answer) {
		t.Fatal("model next turn did not see the question and answer")
	}
	assertOneQuestion(t, env, asg.TaskID, question)
}

func assertOneQuestion(t *testing.T, env *env, runID, question string) {
	t.Helper()
	var n int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM run_events WHERE run_id = $1 AND type = 'question'`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("question events %d, want 1", n)
	}
	var text, gate string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT payload_json->>'text', COALESCE(payload_json->>'gate', '')
		FROM run_events
		WHERE run_id = $1 AND type = 'question'`, runID).Scan(&text, &gate); err != nil {
		t.Fatal(err)
	}
	if text != question || gate != "clarification" {
		t.Fatalf("stored question text %q gate %q", text, gate)
	}
}

func assertInboxQuestion(t *testing.T, env *env, workItemID, runID, question string) {
	t.Helper()
	item, err := env.srv.store.GetWorkItem(context.Background(), workItemID)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := env.srv.store.Inbox(context.Background(), item.FactoryID)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.InboxItem
	for i := range inbox {
		if inbox[i].Kind == store.InboxQuestion && inbox[i].RunID == runID {
			got = &inbox[i]
			break
		}
	}
	if got == nil || got.WorkItemID != workItemID || got.Detail != question || got.Action != store.InboxActionAnswer {
		t.Fatalf("inbox %+v", inbox)
	}
}

func runAssignedHarness(t *testing.T, asg protocol.TaskAssignment, idle string) (int, string, time.Duration) {
	t.Helper()
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.ModelID == nil || *asg.Task.AgentConfigSnapshot.ModelID == "" {
		t.Fatal("assignment has no model")
	}
	ws := t.TempDir()
	rt := t.TempDir()
	envVars := make([]string, 0, len(asg.EnvVars))
	for k, v := range asg.EnvVars {
		envVars = append(envVars, k+"="+v)
	}
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	began := time.Now()
	code := harness.Run(ctx, harness.Options{
		Args: []string{
			"agent", "run",
			"--task-id", asg.TaskID,
			"--model", *asg.Task.AgentConfigSnapshot.ModelID,
			"--idle-on-complete", idle,
		},
		Env:        envVars,
		Workspace:  ws,
		RuntimeDir: rt,
		Stderr:     &errBuf,
	})
	elapsed := time.Since(began)
	if ctx.Err() != nil {
		t.Fatalf("harness timed out\n%s", errBuf.String())
	}
	return code, errBuf.String(), elapsed
}

type chatLog struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (c *chatLog) add(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, append([]byte(nil), b...))
}

func (c *chatLog) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.bodies))
	copy(out, c.bodies)
	return out
}

func askThenDone(log *chatLog, question string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		log.add(body)
		w.Header().Set("Content-Type", "application/json")
		if len(log.snapshot()) == 1 {
			args, err := json.Marshal(map[string]string{"text": question})
			if err != nil {
				http.Error(w, "bad request", http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, askToolResponse(string(args)))
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"used the answer"}}]}`)
	})
}

func askToolResponse(args string) string {
	body := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   "call_ask",
				"type": "function",
				"function": map[string]any{
					"name":      "ask_input",
					"arguments": args,
				},
			}},
		}}},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func userSaw(t *testing.T, body []byte, needle string) bool {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("model request: %v", err)
	}
	for _, msg := range req.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, needle) {
			return true
		}
	}
	return false
}
