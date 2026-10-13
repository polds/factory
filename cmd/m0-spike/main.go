// Command m0-spike is the M0 protocol spike described in docs/12-build-sequence.md.
//
// It implements the smallest control-plane surface that the real, unmodified
// oz-agent-worker will talk to: a WebSocket endpoint that authenticates a
// worker, assigns exactly one task, and waits for a terminal message.
//
// Its purpose is to falsify or confirm the central assumption of this project
// (docs/01-evidence-and-provenance.md, O2): that the MIT-licensed worker can be
// pointed at a control plane we run. It exits 0 only if a real worker claimed
// our task, launched our harness sidecar, and reported task_completed.
//
// This is deliberately a single file with one dependency. It is a probe, not a
// foundation; factoryd proper begins at M2.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Wire protocol.
//
// Mirrors oz-agent-worker's internal/types/messages.go. Field names and JSON
// tags are observed, not invented; see docs/04-protocols-and-apis.md.
// Only the subset M0 needs is modelled.
// ---------------------------------------------------------------------------

type messageType string

const (
	msgTaskAssignment   messageType = "task_assignment"
	msgTaskClaimed      messageType = "task_claimed"
	msgTaskCompleted    messageType = "task_completed"
	msgTaskFailed       messageType = "task_failed"
	msgTaskRejected     messageType = "task_rejected"
	msgTaskCancellation messageType = "task_cancellation"
)

type envelope struct {
	Type messageType     `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type taskDefinition struct {
	Prompt string `json:"prompt"`
}

type agentConfigSnapshot struct {
	BasePrompt *string `json:"base_prompt,omitempty"`
	ModelID    *string `json:"model_id,omitempty"`
}

type task struct {
	ID                  string               `json:"id"`
	Title               string               `json:"title"`
	Definition          taskDefinition       `json:"task_definition"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	AgentConfigSnapshot *agentConfigSnapshot `json:"agent_config_snapshot,omitempty"`
}

type instanceShape struct {
	Vcpus    int `json:"vcpus,omitempty"`
	MemoryGb int `json:"memory_gb,omitempty"`
}

type taskAssignment struct {
	TaskID        string            `json:"task_id"`
	ExecutionID   string            `json:"execution_id,omitempty"`
	Task          *task             `json:"task"`
	DockerImage   string            `json:"docker_image,omitempty"`
	SidecarImage  string            `json:"sidecar_image,omitempty"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
	InstanceShape *instanceShape    `json:"instance_shape,omitempty"`
}

type taskClaimed struct {
	TaskID   string `json:"task_id"`
	WorkerID string `json:"worker_id"`
}

type taskCompleted struct {
	TaskID      string `json:"task_id"`
	ExecutionID string `json:"execution_id,omitempty"`
	Message     string `json:"message"`
}

type taskFailed struct {
	TaskID        string `json:"task_id"`
	ExecutionID   string `json:"execution_id,omitempty"`
	Message       string `json:"message"`
	FailureReason string `json:"failure_reason,omitempty"`
	ExitCode      int    `json:"exit_code,omitempty"`
}

type taskRejected struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

// ---------------------------------------------------------------------------
// Result ledger
// ---------------------------------------------------------------------------

// result records what the spike actually observed, so the verdict is evidence
// rather than an absence of errors.
type result struct {
	mu            sync.Mutex
	connected     bool
	workerID      string
	assigned      bool
	claimed       bool
	claimedBy     string
	pings         int
	heartbeatReq  bool // we held the connection open long enough to expect one
	terminal      messageType
	terminalMsg   string
	failureReason string
	exitCode      int
	done          chan struct{}
	closeOnce     sync.Once
}

func (r *result) finish() { r.closeOnce.Do(func() { close(r.done) }) }

func (r *result) ok() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !(r.connected && r.assigned && r.claimed && r.terminal == msgTaskCompleted) {
		return false
	}
	// Only gate on the heartbeat when we actually waited for one.
	return !r.heartbeatReq || r.pings > 0
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	apiKey := flag.String("api-key", "m0-spike-key", "expected worker bearer token")
	dockerImage := flag.String("docker-image", "alpine:3.20", "task container image")
	sidecarImage := flag.String("sidecar-image", "factory-m0-harness:dev", "harness sidecar image mounted at /agent")
	prompt := flag.String("prompt", "M0 protocol spike: confirm the harness contract is reachable.", "task prompt")
	timeout := flag.Duration("timeout", 3*time.Minute, "overall deadline")
	hold := flag.Duration("hold-for-heartbeat", 0,
		"after the terminal message, keep the connection open this long to observe a heartbeat ping (the worker's interval is 30s)")
	flag.Parse()

	res := &result{done: make(chan struct{}), heartbeatReq: *hold > 30*time.Second}

	// gorilla's default ping handler replies with a pong automatically. The
	// worker's heartbeat is a WebSocket PING control frame every 30s (not a
	// JSON message, despite the heartbeat constant in its message enum), and
	// it closes the connection if no pong arrives within 60s. We install our
	// own handler only to count heartbeats, then delegate.
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1 << 14,
		WriteBufferSize: 1 << 14,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/selfhosted/worker/ws", func(w http.ResponseWriter, r *http.Request) {
		// The worker authenticates on the upgrade request and declares its
		// identity as a query parameter: ?worker_id=<id>.
		if got := r.Header.Get("Authorization"); got != "Bearer "+*apiKey {
			log.Printf("auth: rejected upgrade (authorization=%q)", redact(got))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		workerID := r.URL.Query().Get("worker_id")
		if workerID == "" {
			http.Error(w, "worker_id required", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		res.mu.Lock()
		res.connected = true
		res.workerID = workerID
		res.mu.Unlock()
		log.Printf("worker connected: worker_id=%s remote=%s", workerID, r.RemoteAddr)

		prev := conn.PingHandler()
		conn.SetPingHandler(func(appData string) error {
			res.mu.Lock()
			res.pings++
			n := res.pings
			res.mu.Unlock()
			log.Printf("heartbeat: ping #%d from %s (replying pong)", n, workerID)
			return prev(appData)
		})

		serve(conn, workerID, res, *dockerImage, *sidecarImage, *prompt, *hold)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("m0-spike listening on %s", *addr)
		log.Printf("point a worker at: ws://127.0.0.1%s/api/v1/selfhosted/worker/ws", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-res.done:
	case <-time.After(*timeout):
		log.Printf("timeout after %s", *timeout)
	case <-ctx.Done():
		log.Printf("interrupted")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	report(res)
	if !res.ok() {
		os.Exit(1)
	}
}

// serve assigns one task on the established connection and reads until a
// terminal message arrives.
func serve(conn *websocket.Conn, workerID string, res *result, dockerImage, sidecarImage, prompt string, hold time.Duration) {
	now := time.Now().UTC()
	taskID := "run_m0_" + now.Format("20060102T150405")
	model := "m0-stub-model"
	base := prompt

	assignment := taskAssignment{
		TaskID:      taskID,
		ExecutionID: "exec_" + now.Format("20060102T150405"),
		Task: &task{
			ID:         taskID,
			Title:      "M0 protocol spike",
			Definition: taskDefinition{Prompt: prompt},
			CreatedAt:  now,
			UpdatedAt:  now,
			AgentConfigSnapshot: &agentConfigSnapshot{
				BasePrompt: &base,
				ModelID:    &model,
			},
		},
		DockerImage:  dockerImage,
		SidecarImage: sidecarImage,
		EnvVars: map[string]string{
			// Stands in for the scoped, short-TTL credentials a real control
			// plane mints per run (docs/11-security-model.md). The spike's
			// harness asserts it arrives, proving the injection path works.
			"FACTORY_M0_MARKER": taskID,
		},
		InstanceShape: &instanceShape{Vcpus: 2, MemoryGb: 4},
	}

	if err := writeMessage(conn, msgTaskAssignment, assignment); err != nil {
		log.Printf("assign: %v", err)
		res.finish()
		return
	}
	res.mu.Lock()
	res.assigned = true
	res.mu.Unlock()
	log.Printf("assigned task_id=%s image=%s sidecar=%s", taskID, dockerImage, sidecarImage)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("read: %v", err)
			}
			res.finish()
			return
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			log.Printf("decode envelope: %v (%s)", err, truncate(string(raw), 200))
			continue
		}

		switch env.Type {
		case msgTaskClaimed:
			var m taskClaimed
			_ = json.Unmarshal(env.Data, &m)
			res.mu.Lock()
			res.claimed = true
			res.claimedBy = m.WorkerID
			res.mu.Unlock()
			log.Printf("<- task_claimed task_id=%s worker_id=%s", m.TaskID, m.WorkerID)

		case msgTaskCompleted:
			var m taskCompleted
			_ = json.Unmarshal(env.Data, &m)
			res.mu.Lock()
			res.terminal = msgTaskCompleted
			res.terminalMsg = m.Message
			res.mu.Unlock()
			log.Printf("<- task_completed task_id=%s message=%q", m.TaskID, truncate(m.Message, 400))
			// The worker's heartbeat is a ping every 30s, so a 4-second task
			// finishes before one is ever sent. Optionally hold the connection
			// open to observe the liveness half of the protocol too.
			if hold > 0 {
				log.Printf("holding connection %s to observe a heartbeat ping…", hold)
				_ = conn.SetReadDeadline(time.Now().Add(hold))
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						break
					}
				}
			}
			res.finish()
			return

		case msgTaskFailed:
			var m taskFailed
			_ = json.Unmarshal(env.Data, &m)
			res.mu.Lock()
			res.terminal = msgTaskFailed
			res.terminalMsg = m.Message
			res.failureReason = m.FailureReason
			res.exitCode = m.ExitCode
			res.mu.Unlock()
			log.Printf("<- task_failed task_id=%s reason=%s exit=%d message=%q",
				m.TaskID, m.FailureReason, m.ExitCode, truncate(m.Message, 600))
			res.finish()
			return

		case msgTaskRejected:
			// Backpressure, not failure: a real control plane requeues here
			// rather than recording a failed run (O4). The spike has nowhere
			// to requeue to, so it reports and stops.
			var m taskRejected
			_ = json.Unmarshal(env.Data, &m)
			res.mu.Lock()
			res.terminal = msgTaskRejected
			res.terminalMsg = m.Reason
			res.mu.Unlock()
			log.Printf("<- task_rejected task_id=%s reason=%q (a real scheduler would requeue)", m.TaskID, m.Reason)
			res.finish()
			return

		default:
			log.Printf("<- %s (unhandled) %s", env.Type, truncate(string(env.Data), 200))
		}
	}
}

func writeMessage(conn *websocket.Conn, t messageType, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", t, err)
	}
	raw, err := json.Marshal(envelope{Type: t, Data: data})
	if err != nil {
		return fmt.Errorf("marshal envelope %s: %w", t, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, raw)
}

func report(r *result) {
	r.mu.Lock()
	defer r.mu.Unlock()

	line := func(name string, pass bool, detail string) {
		mark := "FAIL"
		if pass {
			mark = "PASS"
		}
		fmt.Printf("  %-4s  %-28s %s\n", mark, name, detail)
	}

	fmt.Printf("\n── M0 protocol spike ──────────────────────────────────────────\n")
	line("worker authenticated", r.connected, detailOr(r.workerID, "no worker connected"))
	line("task assignment accepted", r.assigned, "task_assignment written to socket")
	line("task claimed", r.claimed, detailOr(r.claimedBy, "no task_claimed received"))
	if r.pings == 0 && !r.heartbeatReq {
		// Not a failure: the task finished before the worker's 30s interval.
		fmt.Printf("  %-4s  %-28s %s\n", "n/a", "heartbeat answered",
			"not exercised — run finished inside the 30s interval (-hold-for-heartbeat to force)")
	} else {
		line("heartbeat answered", r.pings > 0, fmt.Sprintf("%d ping/pong exchange(s)", r.pings))
	}
	line("terminal state", r.terminal == msgTaskCompleted, terminalDetail(r))

	fmt.Printf("──────────────────────────────────────────────────────────────\n")
	if r.connected && r.assigned && r.claimed && r.terminal == msgTaskCompleted &&
		(!r.heartbeatReq || r.pings > 0) {
		fmt.Printf("VERDICT: confirmed — an unmodified worker ran our harness against our server.\n\n")
		return
	}
	fmt.Printf("VERDICT: not confirmed.\n\n")
}

func terminalDetail(r *result) string {
	switch r.terminal {
	case "":
		return "none received"
	case msgTaskCompleted:
		return "task_completed: " + truncate(r.terminalMsg, 120)
	case msgTaskFailed:
		return fmt.Sprintf("task_failed (%s, exit %d): %s", r.failureReason, r.exitCode, truncate(r.terminalMsg, 120))
	default:
		return fmt.Sprintf("%s: %s", r.terminal, truncate(r.terminalMsg, 120))
	}
}

func detailOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// redact keeps the scheme but drops the credential, so a failed handshake is
// diagnosable without writing a token to the log.
func redact(authz string) string {
	scheme, _, found := strings.Cut(authz, " ")
	if !found {
		return "<malformed>"
	}
	return scheme + " <redacted>"
}
