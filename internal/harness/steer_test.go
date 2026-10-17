package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Steering tests drive harness.Run, the function the binary calls, against an
// httptest control plane and an OpenAI-compatible model.

func TestSteerInterruptStopsTurnAndFollowupKeepsMessages(t *testing.T) {
	const (
		token    = "steer-interrupt-token"
		prompt   = "fix the flaky parser"
		follow   = "continue from the parser work"
		steerInt = "int-1"
		steerFu  = "fu-1"
	)
	var mu sync.Mutex
	var interruptSent time.Time
	var turnStopped time.Time
	var followBody string
	modelStarted := make(chan struct{})
	var started sync.Once
	var calls int

	model := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			started.Do(func() { close(modelStarted) })
			select {
			case <-r.Context().Done():
				mu.Lock()
				turnStopped = time.Now()
				mu.Unlock()
				return
			case <-time.After(3 * time.Second):
				t.Errorf("model call was not canceled")
				return
			}
		}
		mu.Lock()
		followBody = string(body)
		mu.Unlock()
		writeAssistant(w, "parser fixed")
	}

	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-modelStarted:
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			return
		}
		mu.Lock()
		interruptSent = time.Now()
		mu.Unlock()
		writeSteers(w, []map[string]string{
			{"id": steerInt, "kind": "interrupt", "text": "stop the turn"},
			{"id": steerFu, "kind": "followup", "text": follow},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	code, events, stderr := runSteer(t, ctx, plane.eventsURL, token, prompt, "0s", model, nil)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s\nevents:\n%s", code, stderr, events)
	}
	mu.Lock()
	sent, stopped, body := interruptSent, turnStopped, followBody
	mu.Unlock()
	if sent.IsZero() {
		t.Fatal("steer endpoint was not polled")
	}
	if stopped.IsZero() || stopped.Sub(sent) > time.Second {
		t.Fatalf("turn stopped %s after interrupt, want <= 1s", stopped.Sub(sent))
	}
	if !strings.Contains(body, prompt) || !strings.Contains(body, follow) {
		t.Fatalf("follow-up request lost earlier turns:\n%s", body)
	}
	got := decodeSteerEvents(t, events)
	if countType(got, "run.capabilities") != 1 || !got.hasCapability() {
		t.Fatalf("capability events:\n%s", events)
	}
	if !got.hasInterrupted(steerInt) {
		t.Fatalf("missing turn.interrupted %s:\n%s", steerInt, events)
	}
	if !got.hasFollowup(steerFu, follow) {
		t.Fatalf("missing followup.received %s:\n%s", steerFu, events)
	}
}

func TestSteerFollowupDuringIdleResumes(t *testing.T) {
	const (
		token  = "steer-idle-token"
		prompt = "sort the crates"
		follow = "add a test for the sorter"
	)
	firstDone := make(chan struct{})
	var mu sync.Mutex
	var calls int
	var secondBody string

	model := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		if n == 2 {
			secondBody = string(body)
		}
		mu.Unlock()
		if n == 1 {
			writeAssistant(w, "crates sorted")
			close(firstDone)
			return
		}
		writeAssistant(w, "test added")
	}
	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-firstDone:
		case <-r.Context().Done():
			return
		}
		// The first turn has finished. Wait so the idle loop, not the turn
		// itself, is what observes this follow-up.
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
		writeSteers(w, []map[string]string{
			{"id": "idle-1", "kind": "followup", "text": follow},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	code, events, stderr := runSteer(t, ctx, plane.eventsURL, token, prompt, "500ms", model, nil)
	elapsed := time.Since(start)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s\nevents:\n%s", code, stderr, events)
	}
	mu.Lock()
	n, body := calls, secondBody
	mu.Unlock()
	if n != 2 {
		t.Fatalf("model calls %d, want 2", n)
	}
	if strings.Contains(body, prompt) || !strings.Contains(body, follow) {
		t.Fatalf("idle follow-up did not start a fresh turn through pollFollowup:\n%s", body)
	}
	got := decodeSteerEvents(t, events)
	if !got.hasFollowup("idle-1", follow) {
		t.Fatalf("events:\n%s", events)
	}
	term := got[len(got)-1]
	if term.Type != "terminal" || term.Text == "" && term.Status != "succeeded" {
		t.Fatalf("terminal %+v", term)
	}
	if term.Status != "succeeded" || term.Summary != "test added" {
		t.Fatalf("terminal %+v", term)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("idle follow-up took %s", elapsed)
	}
}

func TestSteerMissingEndpointLeavesRunUnchanged(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const token = "steer-missing-token"
			const prompt = "leave me alone"
			var mu sync.Mutex
			hits := 0
			plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hits++
				mu.Unlock()
				w.WriteHeader(status)
			})
			model := func(w http.ResponseWriter, r *http.Request) {
				writeAssistant(w, "untouched")
			}
			baseCode, baseEvents, _ := runSteer(t, context.Background(), "", "", prompt, "300ms", model, nil)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			code, events, stderr := runSteer(t, ctx, plane.eventsURL, token, prompt, "300ms", model, nil)
			if code != baseCode {
				t.Fatalf("exit %d, baseline %d, stderr %s", code, baseCode, stderr)
			}
			if typesOf(decodeSteerEvents(t, events)) != typesOf(decodeSteerEvents(t, baseEvents)) {
				t.Fatalf("events changed\nbase:\n%s\ngot:\n%s", baseEvents, events)
			}
			if strings.Contains(events, "run.capabilities") || strings.Contains(events, `"steer"`) {
				t.Fatalf("capability emitted:\n%s", events)
			}
			mu.Lock()
			n := hits
			mu.Unlock()
			if n != 1 {
				t.Fatalf("steer polls %d, want 1 (stop after %d)", n, status)
			}
		})
	}
}

func TestSteerIDAppliedOnceAcrossReconnect(t *testing.T) {
	const (
		token  = "steer-once-token"
		prompt = "first pass"
		follow = "once please"
		id     = "s1"
	)
	firstDone := make(chan struct{})
	var mu sync.Mutex
	var afters []string
	var bodies []string
	var calls int

	model := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		bodies = append(bodies, string(body))
		n := calls
		mu.Unlock()
		if n == 1 {
			writeAssistant(w, "first done")
			close(firstDone)
			return
		}
		writeAssistant(w, "followed once")
	}
	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		afters = append(afters, r.URL.Query().Get("after"))
		mu.Unlock()
		select {
		case <-firstDone:
		default:
			// Hold the long-poll until the first turn finishes so the
			// follow-up lands in the idle window, then replay s1 forever.
			select {
			case <-firstDone:
			case <-r.Context().Done():
				return
			}
		}
		writeSteers(w, []map[string]string{
			{"id": id, "kind": "followup", "text": follow},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	code, events, stderr := runSteer(t, ctx, plane.eventsURL, token, prompt, "400ms", model, nil)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s\nevents:\n%s", code, stderr, events)
	}
	got := decodeSteerEvents(t, events)
	if countSteer(got, "followup.received", id) != 1 {
		t.Fatalf("followup events:\n%s", events)
	}
	seen := 0
	mu.Lock()
	defer mu.Unlock()
	for _, body := range bodies {
		if strings.Contains(body, follow) {
			seen++
			if strings.Count(body, follow) != 1 {
				t.Fatalf("follow-up text repeated inside one request:\n%s", body)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("requests containing %q: %d", follow, seen)
	}
	acked := false
	for _, a := range afters {
		if a == id {
			acked = true
		}
	}
	if !acked {
		t.Fatalf("no reconnect carried after=%s: %q", id, afters)
	}
	if countType(got, "run.capabilities") != 1 {
		t.Fatalf("capability count:\n%s", events)
	}
}

func TestSteerTokenAbsentFromLogs(t *testing.T) {
	const token = "factory-run-token-planted"
	follow := "please use " + token + " carefully"
	modelStarted := make(chan struct{})
	var once sync.Once
	var calls int
	var mu sync.Mutex
	model := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			once.Do(func() { close(modelStarted) })
			select {
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
				t.Errorf("model call was not canceled")
				return
			}
		}
		writeAssistant(w, "done")
	}
	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" && strings.Contains(r.URL.String(), token) {
			t.Errorf("token leaked into steer URL %s", r.URL.String())
		}
		select {
		case <-modelStarted:
		case <-r.Context().Done():
			return
		}
		writeSteers(w, []map[string]string{
			{"id": "int-tok", "kind": "interrupt", "text": token},
			{"id": "fu-tok", "kind": "followup", "text": follow},
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	code, events, stderr := runSteer(t, ctx, plane.eventsURL, token, "do the thing", "0s", model, nil)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if strings.Contains(events, token) || strings.Contains(stderr, token) {
		t.Fatalf("token leaked\nevents:\n%s\nstderr:\n%s", events, stderr)
	}
	got := decodeSteerEvents(t, events)
	if !got.hasFollowup("fu-tok", "[REDACTED]") && !strings.Contains(events, `"text":"please use [REDACTED] carefully"`) {
		t.Fatalf("steer text was not redacted:\n%s", events)
	}
	if strings.Contains(events, token) {
		t.Fatalf("token still present:\n%s", events)
	}
}

func TestSteerInterruptKillsToolProcessGroup(t *testing.T) {
	const token = "steer-kill-token"
	ws := t.TempDir()
	pidPath := filepath.Join(ws, "child.pid")
	var mu sync.Mutex
	var interruptSent time.Time
	died := make(chan time.Time, 1)
	var watchOnce sync.Once
	var calls int

	command := "sleep 60 & echo $! > child.pid; wait"
	model := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			writeTool(w, "bash", `{"command":`+jsonString(command)+`}`)
			return
		}
		writeAssistant(w, "tool stopped")
	}
	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for {
			if _, err := os.Stat(pidPath); err == nil {
				break
			}
			select {
			case <-deadline.C:
				return
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			return
		}
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); err != nil || pid <= 0 {
			return
		}
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		watchOnce.Do(func() {
			go func() {
				for {
					if err := syscall.Kill(pid, 0); err != nil {
						died <- time.Now()
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()
		})
		mu.Lock()
		if interruptSent.IsZero() {
			interruptSent = time.Now()
		}
		mu.Unlock()
		writeSteers(w, []map[string]string{
			{"id": "int-kill", "kind": "interrupt", "text": "stop"},
			{"id": "fu-kill", "kind": "followup", "text": "continue"},
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	code, events, stderr := runSteerIn(t, ctx, plane.eventsURL, token, "run the sleeper", "0s", model, nil, ws)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s\nevents:\n%s", code, stderr, events)
	}
	mu.Lock()
	sent := interruptSent
	mu.Unlock()
	if sent.IsZero() {
		t.Fatal("steer endpoint was not polled after the tool started")
	}
	var childDied time.Time
	select {
	case childDied = <-died:
	case <-time.After(time.Second):
		t.Fatal("tool child was still alive 1s after the run returned")
	}
	if childDied.Sub(sent) > time.Second {
		t.Fatalf("child died %s after interrupt", childDied.Sub(sent))
	}
	if !decodeSteerEvents(t, events).hasInterrupted("int-kill") {
		t.Fatalf("events:\n%s", events)
	}
}

func TestSteerPollDoesNotBlockAgentLoop(t *testing.T) {
	const token = "steer-block-token"
	var mu sync.Mutex
	hits := 0
	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	code, _, stderr := runSteer(t, ctx, plane.eventsURL, token, "be quick", "0s", func(w http.ResponseWriter, r *http.Request) {
		writeAssistant(w, "quick")
	}, nil)
	elapsed := time.Since(start)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if elapsed > time.Second {
		t.Fatalf("run took %s while steer was held", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits < 1 {
		t.Fatal("steer poll never started")
	}
}

func TestSteerEndpointFromIngestURL(t *testing.T) {
	got, err := steerEndpoint("http://factoryd:8080/api/v1/agent/runs/run_1/events:ingest")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://factoryd:8080/api/v1/agent/runs/run_1/steer" {
		t.Fatalf("endpoint %s", got)
	}
	if _, err := steerEndpoint("http://127.0.0.1:9"); err == nil {
		t.Fatal("events URL without the ingest suffix was accepted")
	}
	got, err = steerEndpoint("http://factoryd:8080/api/v1/agent/runs/run_1/events:ingest?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://factoryd:8080/api/v1/agent/runs/run_1/steer" {
		t.Fatalf("query was kept: %s", got)
	}
}

func TestSteerSecretRedactsEveryFormat(t *testing.T) {
	const planted = "super-secret-token-value"
	tok := secretValue(planted)
	samples := []string{
		fmt.Sprintf("%v", tok),
		fmt.Sprintf("%+v", tok),
		fmt.Sprintf("%#v", tok),
		fmt.Sprintf("%v", &tok),
		fmt.Sprintf("%+v", &tok),
		fmt.Sprintf("%#v", &tok),
		fmt.Sprint(tok),
		tok.String(),
		tok.GoString(),
	}
	holder := struct{ Token secretValue }{Token: tok}
	samples = append(samples,
		fmt.Sprintf("%v", holder),
		fmt.Sprintf("%+v", holder),
		fmt.Sprintf("%#v", holder),
		fmt.Sprintf("%v", &holder),
		fmt.Sprintf("%+v", &holder),
		fmt.Sprintf("%#v", &holder),
	)
	poller := steerPoller{token: tok, endpoint: "http://127.0.0.1/steer"}
	samples = append(samples,
		fmt.Sprintf("%v", poller),
		fmt.Sprintf("%+v", poller),
		fmt.Sprintf("%#v", poller),
		fmt.Sprintf("%v", &poller),
		fmt.Sprintf("%+v", &poller),
		fmt.Sprintf("%#v", &poller),
	)
	raw, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	samples = append(samples, string(raw))
	text, err := tok.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	samples = append(samples, string(text))
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("check", "token", tok, "ptr", &tok, "holder", holder, "poller", poller)
	logger = slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("check", "token", tok, "ptr", &tok, "holder", holder, "poller", poller)
	samples = append(samples, buf.String())
	for _, sample := range samples {
		if strings.Contains(sample, planted) {
			t.Fatalf("secret leaked: %s", sample)
		}
	}
}

func TestSteerBackoffThenCapability(t *testing.T) {
	const token = "steer-backoff-token"
	var mu sync.Mutex
	var hits []time.Time
	plane := newSteerPlane(t, token, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, time.Now())
		n := len(hits)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeSteers(w, nil)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	code, events, stderr := runSteer(t, ctx, plane.eventsURL, token, "keep going", "400ms", func(w http.ResponseWriter, r *http.Request) {
		writeAssistant(w, "kept going")
	}, nil)
	if code != ExitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) < 2 {
		t.Fatalf("polls %d, want a retry after 500", len(hits))
	}
	if gap := hits[1].Sub(hits[0]); gap < 80*time.Millisecond {
		t.Fatalf("retry gap %s, want backoff", gap)
	}
	if countType(decodeSteerEvents(t, events), "run.capabilities") != 1 {
		t.Fatalf("events:\n%s", events)
	}
}

type steerEvent struct {
	Type    string `json:"type"`
	Steer   *bool  `json:"steer"`
	SteerID string `json:"steer_id"`
	Turn    int    `json:"turn"`
	Text    string `json:"text"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type steerEvents []steerEvent

func (e steerEvents) hasCapability() bool {
	for _, ev := range e {
		if ev.Type == "run.capabilities" && ev.Steer != nil && *ev.Steer {
			return true
		}
	}
	return false
}

func (e steerEvents) hasInterrupted(id string) bool {
	for _, ev := range e {
		if ev.Type == "turn.interrupted" && ev.SteerID == id && ev.Turn > 0 {
			return true
		}
	}
	return false
}

func (e steerEvents) hasFollowup(id, text string) bool {
	for _, ev := range e {
		if ev.Type == "followup.received" && ev.SteerID == id && strings.Contains(ev.Text, text) {
			return true
		}
	}
	return false
}

func countType(events steerEvents, typ string) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func countSteer(events steerEvents, typ, id string) int {
	n := 0
	for _, ev := range events {
		if ev.Type == typ && ev.SteerID == id {
			n++
		}
	}
	return n
}

func typesOf(events steerEvents) string {
	var b strings.Builder
	for _, ev := range events {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(ev.Type)
		if ev.Status != "" {
			b.WriteByte(':')
			b.WriteString(ev.Status)
		}
	}
	return b.String()
}

func decodeSteerEvents(t *testing.T, raw string) steerEvents {
	t.Helper()
	var out steerEvents
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var ev steerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

type steerPlane struct {
	eventsURL string
}

func newSteerPlane(t *testing.T, token string, steer http.HandlerFunc) *steerPlane {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/runs/run-1/events:ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("ingest auth %q", r.Header.Get("Authorization"))
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/agent/runs/run-1/steer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("steer method %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("steer auth %q", r.Header.Get("Authorization"))
		}
		steer(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &steerPlane{eventsURL: srv.URL + "/api/v1/agent/runs/run-1/events:ingest"}
}

func writeSteers(w http.ResponseWriter, items []map[string]string) {
	if items == nil {
		items = []map[string]string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func writeAssistant(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+jsonString(text)+`}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
}

func writeTool(w http.ResponseWriter, name, args string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":`+jsonString(name)+`,"arguments":`+jsonString(args)+`}}]}}]}`)
}

func runSteer(t *testing.T, ctx context.Context, eventsURL, token, prompt, idle string, model http.HandlerFunc, extra []string) (int, string, string) {
	t.Helper()
	return runSteerIn(t, ctx, eventsURL, token, prompt, idle, model, extra, t.TempDir())
}

func runSteerIn(t *testing.T, ctx context.Context, eventsURL, token, prompt, idle string, model http.HandlerFunc, extra []string, ws string) (int, string, string) {
	t.Helper()
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)
	rt := t.TempDir()
	var errBuf bytes.Buffer
	env := []string{
		"OPENAI_BASE_URL=" + modelSrv.URL,
		"FACTORY_TASK_PROMPT=" + prompt,
		"PATH=" + os.Getenv("PATH"),
	}
	if eventsURL != "" {
		env = append(env, "FACTORY_EVENTS_URL="+eventsURL)
	}
	if token != "" {
		env = append(env, "FACTORY_RUN_TOKEN="+token)
	}
	env = append(env, extra...)
	code := Run(ctx, Options{
		Args:       []string{"--task-id", "t1", "--model", "m", "--idle-on-complete", idle},
		Env:        env,
		Workspace:  ws,
		RuntimeDir: rt,
		Stderr:     &errBuf,
	})
	b, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return code, string(b), errBuf.String()
}
