package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	"github.com/polds/factory/internal/harness"
)

// chatResponse is the subset of a chat completion that
// internal/harness/openai.go unmarshals (chatResponse, chatMessage, toolCall).
type chatResponse struct {
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

func TestScriptedSteps(t *testing.T) {
	steps := mustSteps(t, `[
		{"tool":"bash","args":{"command":"echo hi"}},
		{"final":"done"}
	]`)
	srv := httptest.NewServer(newHandler(steps))
	t.Cleanup(srv.Close)

	first := postChat(t, srv.URL+"/chat/completions", `{"model":"m"}`)
	calls := first.Choices[0].Message.ToolCalls
	if len(calls) != 1 {
		t.Fatalf("tool calls %+v", first.Choices[0].Message)
	}
	call := calls[0]
	if call.Function.Name != "bash" {
		t.Fatalf("function name %q", call.Function.Name)
	}
	if call.Type != "function" || call.ID != "call_1" {
		t.Fatalf("tool call id=%q type=%q", call.ID, call.Type)
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments %q: %v", call.Function.Arguments, err)
	}
	if args.Command != "echo hi" {
		t.Fatalf("command %q", args.Command)
	}

	second := postChat(t, srv.URL+"/chat/completions", `{"model":"m"}`)
	msg := second.Choices[0].Message
	if msg.Content != "done" || len(msg.ToolCalls) != 0 {
		t.Fatalf("final message %+v", msg)
	}
}

func TestPathsHealthzReplayAndToolName(t *testing.T) {
	steps := mustSteps(t, `[
		{"tool":"read","args":{"path":"a.txt"}},
		{"final":"done"}
	]`)
	srv := httptest.NewServer(newHandler(steps))
	t.Cleanup(srv.Close)

	health, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(health)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", res.StatusCode)
	}

	first := postChat(t, srv.URL+"/v1/chat/completions", `{}`)
	if first.Choices[0].Message.ToolCalls[0].Function.Name != "read" {
		t.Fatalf("tool %+v", first.Choices[0].Message)
	}
	if first.Choices[0].Message.ToolCalls[0].ID != "call_1" {
		t.Fatalf("id %s", first.Choices[0].Message.ToolCalls[0].ID)
	}
	second := postChat(t, srv.URL+"/chat/completions", `{}`)
	if second.Choices[0].Message.Content != "done" {
		t.Fatalf("content %q", second.Choices[0].Message.Content)
	}
	third := postChat(t, srv.URL+"/v1/chat/completions", `{}`)
	if third.Choices[0].Message.Content != "done" || len(third.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("replay %+v", third.Choices[0].Message)
	}

	missing, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/other", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(missing)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path %d", res.StatusCode)
	}
}

func TestPastEndWithoutFinal(t *testing.T) {
	steps := mustSteps(t, `[{"tool":"bash","args":{"command":"true"}}]`)
	srv := httptest.NewServer(newHandler(steps))
	t.Cleanup(srv.Close)
	_ = postChat(t, srv.URL+"/chat/completions", `{}`)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/chat/completions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError || !strings.Contains(string(raw), "no final") {
		t.Fatalf("status %d body %s", res.StatusCode, raw)
	}
}

func TestRequestBodyNotLogged(t *testing.T) {
	const marker = "model-stub-body-7f3a9c"
	steps := mustSteps(t, `[{"final":"done"}]`)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	srv := httptest.NewServer(newHandler(steps))
	t.Cleanup(srv.Close)
	health, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(health)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	body := `{"messages":[{"role":"user","content":"` + marker + `"}]}`
	msg := postChat(t, srv.URL+"/chat/completions", body)
	if msg.Choices[0].Message.Content != "done" {
		t.Fatalf("content %q", msg.Choices[0].Message.Content)
	}
	logged := buf.String()
	if strings.Contains(logged, marker) || strings.Contains(logged, "messages") {
		t.Fatalf("request body logged:\n%s", logged)
	}
	if strings.Count(logged, "request count=") != 1 {
		t.Fatalf("log %q", logged)
	}
}

func TestParseScript(t *testing.T) {
	ok := []string{
		`[{"tool":"bash","args":{"command":"true"}},{"final":"done"}]`,
		`[{"tool":"bash"},{"final":""}]`,
		`[]`,
	}
	for _, raw := range ok {
		if _, err := parseScript([]byte(raw)); err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
	}
	bad := []string{
		`{`,
		`{}`,
		`[{}]`,
		`[{"tool":"bash","final":"x"}]`,
		`[{"final":"x","args":{}}]`,
		`[{"tool":"","args":{}}]`,
		`[{"tool":"bash","args":[]}]`,
		`[{"tool":"bash","note":1}]`,
		`[{"final":1}]`,
	}
	for _, raw := range bad {
		if _, err := parseScript([]byte(raw)); err == nil {
			t.Fatalf("parse %s: want error", raw)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "script.json")
	if err := os.WriteFile(path, []byte(`[{"final":"done"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	steps, err := loadScript(path)
	if err != nil || len(steps) != 1 {
		t.Fatalf("loadScript %v %v", steps, err)
	}
	if _, err := loadScript(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file: want error")
	}
}

func TestBinaryServesScript(t *testing.T) {
	const marker = "model-stub-bin-body-7f3a9c"
	bin := buildBin(t, "./spike/m1/model-stub", "model-stub")
	script := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(script, []byte(`[{"tool":"bash","args":{"command":"true"}},{"final":"done"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	var logs lockedBuf
	cmd := exec.Command(bin, "-addr", addr, "-script", script)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	base := "http://" + addr
	waitHealth(t, base+"/healthz")
	msg := postChat(t, base+"/v1/chat/completions", `{"secret":"`+marker+`"}`)
	if msg.Choices[0].Message.ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("message %+v", msg.Choices[0].Message)
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatalf("binary logged request body:\n%s", logs.String())
	}

	missing := exec.Command(bin)
	if err := missing.Run(); err == nil {
		t.Fatal("missing -script: want non-zero exit")
	}
}

// defaultHarnessTurns is the generic-openai budget when FACTORY_MAX_TURNS is
// unset (internal/harness maxTurns). The round trip must finish inside it.
const defaultHarnessTurns = 8

func TestHarnessRoundTripAtDefaultTurnLimit(t *testing.T) {
	const marker = "model-stub-harness-body-7f3a9c"
	steps := mustSteps(t, `[
		{"tool":"bash","args":{"command":"echo hi > out.txt"}},
		{"final":"done"}
	]`)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	srv := httptest.NewServer(newHandler(steps))
	t.Cleanup(srv.Close)

	bin := buildBin(t, "./cmd/factory-harness", "factory-harness")
	ws := t.TempDir()
	rt := t.TempDir()
	task, err := json.Marshal(map[string]string{
		"prompt": "create out.txt. marker " + marker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt, "task.json"), task, 0o600); err != nil {
		t.Fatal(err)
	}

	// FACTORY_MAX_TURNS is left unset so the harness keeps its default budget.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"OPENAI_BASE_URL=" + srv.URL,
		"OPENAI_API_KEY=model-stub-key-7f3a9c",
		"FACTORY_RUNTIME_DIR=" + rt,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"agent", "run",
		"--task-id", "stub-roundtrip",
		"--model", "scripted",
		"--idle-on-complete", "0s",
	)
	cmd.Dir = ws
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness: %v\n%s", err, out)
	}
	body, err := os.ReadFile(filepath.Join(ws, "out.txt"))
	if err != nil {
		t.Fatalf("out.txt: %v\nharness:\n%s", err, out)
	}
	if string(body) != "hi\n" {
		t.Fatalf("out.txt %q", body)
	}

	raw, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	turns := 0
	var term harness.Event
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e harness.Event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "turn.started" {
			turns++
		}
		if e.Type == "terminal" {
			term = e
		}
	}
	if turns != 2 {
		t.Fatalf("got %d turns; default budget is %d\n%s", turns, defaultHarnessTurns, raw)
	}
	if term.Status != "succeeded" || term.Summary != "done" {
		t.Fatalf("terminal %+v\n%s", term, out)
	}
	logged := logs.String()
	if strings.Contains(logged, marker) || strings.Contains(logged, "model-stub-key-7f3a9c") {
		t.Fatalf("stub logged request material:\n%s", logged)
	}
}

func mustSteps(t *testing.T, raw string) []step {
	t.Helper()
	steps, err := parseScript([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func postChat(t *testing.T, url, body string) chatResponse {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d body %s", url, res.StatusCode, raw)
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("json: %v body %s", err, raw)
	}
	if len(parsed.Choices) == 0 {
		t.Fatalf("no choices: %s", raw)
	}
	return parsed
}

func buildBin(t *testing.T, pkg, name string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = moduleRoot(t)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, raw)
	}
	return out
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	mod := strings.TrimSpace(string(raw))
	if mod == "" || mod == os.DevNull {
		t.Fatal("GOMOD empty")
	}
	return filepath.Dir(mod)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitHealth(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
			last = fmt.Errorf("status %d", res.StatusCode)
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("healthz %s: %v", url, last)
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
