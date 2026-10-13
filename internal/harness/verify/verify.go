package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/polds/factory/internal/harness"
)

// Result is one conformance case.
type Result struct {
	Name string
	Err  error
}

// All runs the Harness Contract v1 checks that do not need a live vendor.
func All(bin string) []Result {
	cases := []struct {
		name string
		fn   func(string) error
	}{
		{"flags", checkFlags},
		{"success", checkSuccess},
		{"redact", checkRedact},
		{"agent-failure", checkAgentFailure},
		{"invalid-model", checkInvalidModel},
		{"unreachable", checkUnreachable},
		{"setup-failed", checkSetupFailed},
		{"outside-write", checkOutsideWrite},
		{"auth-scrub", checkAuthScrub},
		{"skip-initial", checkSkipInitial},
		{"follow-up", checkFollowUp},
		{"bare-idle", checkBareIdle},
	}
	out := make([]Result, 0, len(cases))
	for _, c := range cases {
		out = append(out, Result{Name: c.name, Err: c.fn(bin)})
	}
	return out
}

// Report writes the spike-style verdict. It returns an error when any case failed.
func Report(w io.Writer, results []Result) error {
	fmt.Fprintln(w, "── harness verify ─────────────────────────────────────────")
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(w, "  FAIL  %-28s %s\n", r.Name, r.Err)
			failed = append(failed, r.Name)
			continue
		}
		fmt.Fprintf(w, "  PASS  %s\n", r.Name)
	}
	fmt.Fprintln(w, "────────────────────────────────────────────────────────────")
	if len(failed) > 0 {
		fmt.Fprintf(w, "VERDICT: failed (%s)\n", strings.Join(failed, ", "))
		return fmt.Errorf("failed: %s", strings.Join(failed, ", "))
	}
	fmt.Fprintln(w, "VERDICT: conformant")
	return nil
}

type runOut struct {
	Code   int
	Stdout string
	Stderr string
	Events []harness.Event
	Raw    string
	Dir    string
}

func runHarness(bin string, args []string, extraEnv []string, task any, timeout time.Duration) (runOut, error) {
	ws, err := os.MkdirTemp("", "factory-ws-")
	if err != nil {
		return runOut{}, err
	}
	rt, err := os.MkdirTemp("", "factory-rt-")
	if err != nil {
		return runOut{}, err
	}
	if task != nil {
		b, err := json.Marshal(task)
		if err != nil {
			return runOut{}, err
		}
		if err := os.WriteFile(filepath.Join(rt, "task.json"), b, 0o600); err != nil {
			return runOut{}, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = ws
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"FACTORY_RUNTIME_DIR=" + rt,
	}, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	if err != nil {
		if ctx.Err() != nil {
			return runOut{Stderr: stderr.String(), Dir: ws}, fmt.Errorf("timed out after %s\n%s", timeout, stderr.String())
		}
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return runOut{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		return runOut{}, err
	}
	events, err := decodeEvents(raw)
	if err != nil {
		return runOut{}, err
	}
	return runOut{Code: code, Stdout: stdout.String(), Stderr: stderr.String(), Events: events, Raw: string(raw), Dir: ws}, nil
}

func decodeEvents(raw []byte) ([]harness.Event, error) {
	var events []harness.Event
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e harness.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("event %s: %w", line, err)
		}
		events = append(events, e)
	}
	if len(events) == 0 || events[len(events)-1].Type != "terminal" {
		return events, fmt.Errorf("last event is not terminal")
	}
	n := 0
	var prev time.Time
	for _, e := range events {
		if e.Type == "terminal" {
			n++
		}
		ts, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			return events, err
		}
		if !prev.IsZero() && !ts.After(prev) {
			return events, fmt.Errorf("timestamp %s is not after %s", e.TS, prev.Format(time.RFC3339Nano))
		}
		prev = ts
	}
	if n != 1 {
		return events, fmt.Errorf("%d terminal events", n)
	}
	return events, nil
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func serveChat(fn func(model string, n int) (status int, body string)) *httptest.Server {
	var n int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		n++
		status, body := fn(req.Model, n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func assistant(content string, promptTokens, completionTokens int) string {
	body := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role":    "assistant",
			"content": content,
		}}},
	}
	if promptTokens > 0 || completionTokens > 0 {
		body["usage"] = map[string]int{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
		}
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func toolCall(name, args, id string) string {
	body := map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"role":    "assistant",
			"content": "",
			"tool_calls": []any{map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			}},
		}}},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func checkFlags(bin string) error {
	srv := serveChat(func(string, int) (int, string) {
		return 200, assistant("ok", 0, 0)
	})
	defer srv.Close()
	args := []string{
		"agent", "run",
		"--not-a-real-flag", "bogus",
		"--task-id", "flags",
		"--model", "verify-ok",
		"--harness", "generic-openai",
		"--profile", "prof",
		"--skill", "repository-conventions",
		"--mcp", "{}",
		"--environment", "env",
		"--server-root-url", "http://127.0.0.1:9",
		"--session-sharing-server-url", "http://127.0.0.1:9",
		"--share", "public:VIEWER",
		"--sandboxed",
		"--bare",
		"--computer-use",
		"--computer-use-model", "vision",
		"--no-computer-use",
		"--repository-head-override-json", "{}",
		"--remove-repository-origins",
		"--no-snapshot",
		"--snapshot-upload-timeout", "1s",
		"--snapshot-script-timeout", "1s",
		"--bedrock-inference-role", "arn:aws:iam::1:role/x",
		"--bedrock-role-region", "us-east-1",
		"--idle-on-complete", "0s",
		"positional-extra",
	}
	got, err := runHarness(bin, args, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "say ok"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != 0 {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	wantErr := strings.Join([]string{
		"factory-harness: ignoring positional argument agent",
		"factory-harness: ignoring positional argument run",
		"factory-harness: ignoring unknown flag --not-a-real-flag bogus",
		"factory-harness: ignoring positional argument positional-extra",
		"factory-harness: ok",
		"",
	}, "\n")
	if got.Stderr != wantErr {
		return fmt.Errorf("stderr:\n%s", got.Stderr)
	}
	term := got.Events[len(got.Events)-1]
	if term.Status != "succeeded" || term.Summary != "ok" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}

func checkSuccess(bin string) error {
	argsJSON, _ := json.Marshal(map[string]string{"content": "hello\n", "path": "hello.txt"})
	var calls int
	srv := serveChat(func(string, int) (int, string) {
		calls++
		if calls == 1 {
			return 200, toolCall("write", string(argsJSON), "call_write")
		}
		return 200, assistant("edited hello.txt", 3, 4)
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "success", "--model", "verify-success", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "write hello.txt"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != 0 {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	body, err := os.ReadFile(filepath.Join(got.Dir, "hello.txt"))
	if err != nil {
		return err
	}
	if string(body) != "hello\n" {
		return fmt.Errorf("hello.txt %q", body)
	}
	var sawCall, sawFile, sawUsage, sawMsg bool
	for _, e := range got.Events {
		switch e.Type {
		case "tool.call":
			if e.Tool != "write" || e.RedactedInput != string(argsJSON) || e.InputDigest != sha(string(argsJSON)) {
				return fmt.Errorf("tool.call %+v", e)
			}
			sawCall = true
		case "tool.result":
			if e.Tool != "write" || e.OK == nil || !*e.OK || e.OutputDigest != sha("wrote hello.txt") || e.DurationMS == nil {
				return fmt.Errorf("tool.result %+v", e)
			}
		case "file.changed":
			if e.Path != "hello.txt" || e.Additions != 1 || e.Deletions != 0 {
				return fmt.Errorf("file.changed %+v", e)
			}
			sawFile = true
		case "usage":
			if e.InputTokens != 3 || e.OutputTokens != 4 || e.ModelID != "verify-success" {
				return fmt.Errorf("usage %+v", e)
			}
			sawUsage = true
		case "message.delta":
			if e.Text != "edited hello.txt" {
				return fmt.Errorf("delta %+v", e)
			}
			sawMsg = true
		}
	}
	term := got.Events[len(got.Events)-1]
	if !sawCall || !sawFile || !sawUsage || !sawMsg || term.Summary != "edited hello.txt" {
		return fmt.Errorf("missing event call=%v file=%v usage=%v msg=%v term=%+v", sawCall, sawFile, sawUsage, sawMsg, term)
	}
	return nil
}

func checkRedact(bin string) error {
	const secret = "planted-secret-value"
	const command = "echo planted-secret-value"
	argsJSON, _ := json.Marshal(map[string]string{"command": command})
	var calls int
	srv := serveChat(func(string, int) (int, string) {
		calls++
		if calls == 1 {
			return 200, toolCall("bash", string(argsJSON), "call_bash")
		}
		return 200, assistant("done", 0, 0)
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "redact", "--model", "verify-redact", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]any{
		"prompt":  "echo the secret",
		"secrets": []string{secret},
	}, 5*time.Second)
	if err != nil {
		return err
	}
	if strings.Contains(got.Raw, secret) || strings.Contains(got.Stderr, secret) {
		return fmt.Errorf("secret leaked into events")
	}
	var call harness.Event
	for _, e := range got.Events {
		if e.Type == "tool.call" {
			call = e
		}
	}
	if call.RedactedInput != "echo [REDACTED]" || call.InputDigest != sha(command) {
		return fmt.Errorf("tool.call %+v", call)
	}
	return nil
}

func checkAgentFailure(bin string) error {
	srv := serveChat(func(string, int) (int, string) {
		return 200, assistant(`{"status":"failed_by_agent","summary":"tests failed"}`, 0, 0)
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "agent", "--model", "verify-fail", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "run tests"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != harness.ExitAgentReported {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	term := got.Events[len(got.Events)-1]
	if term.Status != "failed_by_agent" || term.Summary != "tests failed" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}

func checkInvalidModel(bin string) error {
	srv := serveChat(func(string, int) (int, string) {
		return 400, `{"error":{"message":"model not found"}}`
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "badmodel", "--model", "missing-model", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "hi"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != harness.ExitConfigInvalid {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	term := got.Events[len(got.Events)-1]
	if term.Status != "config_invalid" || term.Summary != "provider HTTP 400: model not found" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}

func checkUnreachable(bin string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	ln.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "down", "--model", "m", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=http://" + addr}, map[string]string{"prompt": "hi"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != harness.ExitModelUnavailable {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	term := got.Events[len(got.Events)-1]
	if term.Status != "model_unavailable" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}

func checkSetupFailed(bin string) error {
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "setup", "--model", "m", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=http://127.0.0.1:9"}, map[string]string{
		"prompt":        "hi",
		"setup_command": "exit 9",
	}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != harness.ExitEnvironment {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	term := got.Events[len(got.Events)-1]
	if term.Status != "environment_setup_failed" || term.Summary != "setup command failed: exit status 9" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}

func checkOutsideWrite(bin string) error {
	outside := filepath.Join(os.TempDir(), "factory-harness-outside-proof")
	_ = os.Remove(outside)
	argsJSON, _ := json.Marshal(map[string]string{"content": "nope", "path": outside})
	var calls int
	srv := serveChat(func(string, int) (int, string) {
		calls++
		if calls == 1 {
			return 200, toolCall("write", string(argsJSON), "call_out")
		}
		return 200, assistant("blocked", 0, 0)
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "jail", "--model", "verify-outside", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "escape"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != 0 {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		return fmt.Errorf("outside stat %v", err)
	}
	var saw bool
	for _, e := range got.Events {
		if e.Type == "tool.result" && e.Tool == "write" && e.OK != nil && !*e.OK {
			saw = true
		}
	}
	if !saw {
		return fmt.Errorf("write was not refused")
	}
	return nil
}

func checkAuthScrub(bin string) error {
	script := "printf %s \"$OPENAI_API_KEY\" > key.txt; printf %s \"$FACTORY_HARNESS_TOKEN\" > token.txt; printf %s \"$FACTORY_VISIBLE\" > vis.txt"
	argsJSON, _ := json.Marshal(map[string]string{"command": script})
	var calls int
	srv := serveChat(func(string, int) (int, string) {
		calls++
		if calls == 1 {
			return 200, toolCall("bash", string(argsJSON), "call_env")
		}
		return 200, assistant("done", 0, 0)
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "auth", "--model", "verify-scrub", "--idle-on-complete", "0s",
	}, []string{
		"OPENAI_BASE_URL=" + srv.URL,
		"OPENAI_API_KEY=sk-test-secret-key",
		"FACTORY_HARNESS_TOKEN=harness-token-value",
		"FACTORY_VISIBLE=yes",
	}, map[string]any{
		"prompt":           "print env",
		"harness_auth_env": []string{"FACTORY_HARNESS_TOKEN"},
	}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != 0 {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	key, _ := os.ReadFile(filepath.Join(got.Dir, "key.txt"))
	token, _ := os.ReadFile(filepath.Join(got.Dir, "token.txt"))
	vis, _ := os.ReadFile(filepath.Join(got.Dir, "vis.txt"))
	if string(key) != "" || string(token) != "" || string(vis) != "yes" {
		return fmt.Errorf("key %q token %q vis %q", key, token, vis)
	}
	if strings.Contains(got.Raw, "sk-test-secret-key") || strings.Contains(got.Raw, "harness-token-value") {
		return fmt.Errorf("credential leaked into events")
	}
	return nil
}

func checkSkipInitial(bin string) error {
	srv := serveChat(func(string, int) (int, string) {
		return 500, `{"error":{"message":"should not be called"}}`
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "skip", "--model", "m", "--skip-initial-turn", "--idle-on-complete", "0s",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "nope"}, 5*time.Second)
	if err != nil {
		return err
	}
	if got.Code != 0 {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	for _, e := range got.Events {
		if e.Type == "turn.started" {
			return fmt.Errorf("model was called")
		}
	}
	term := got.Events[len(got.Events)-1]
	if term.Summary != "skipped initial turn" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}

func checkFollowUp(bin string) error {
	argsJSON, _ := json.Marshal(map[string]string{"content": "hello\n", "path": "hello.txt"})
	var calls int
	srv := serveChat(func(string, int) (int, string) {
		calls++
		if calls == 1 {
			return 200, toolCall("write", string(argsJSON), "call_fu")
		}
		return 200, assistant("edited hello.txt", 0, 0)
	})
	defer srv.Close()

	ws, err := os.MkdirTemp("", "factory-ws-")
	if err != nil {
		return err
	}
	rt, err := os.MkdirTemp("", "factory-rt-")
	if err != nil {
		return err
	}
	task, _ := json.Marshal(map[string]string{"prompt": "ignored"})
	if err := os.WriteFile(filepath.Join(rt, "task.json"), task, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "run",
		"--task-id", "follow",
		"--model", "verify-success",
		"--skip-initial-turn",
		"--idle-on-complete", "3s",
	)
	cmd.Dir = ws
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"FACTORY_RUNTIME_DIR=" + rt,
		"OPENAI_BASE_URL=" + srv.URL,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	raw, _ := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if bytes.Contains(raw, []byte(`"turn.started"`)) {
		_ = cmd.Process.Kill()
		return fmt.Errorf("model ran before the follow-up")
	}
	fu, _ := json.Marshal(map[string]string{"prompt": "write hello.txt"})
	tmp := filepath.Join(rt, "followup.json.tmp")
	if err := os.WriteFile(tmp, fu, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(rt, "followup.json")); err != nil {
		return err
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("timed out\n%s", stderr.String())
	}
	code := 0
	if waitErr != nil {
		ee, ok := waitErr.(*exec.ExitError)
		if !ok {
			return waitErr
		}
		code = ee.ExitCode()
	}
	if code != 0 {
		return fmt.Errorf("exit %d\n%s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(ws, "hello.txt"))
	if err != nil {
		return err
	}
	if string(body) != "hello\n" {
		return fmt.Errorf("hello.txt %q", body)
	}
	evRaw, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		return err
	}
	events, err := decodeEvents(evRaw)
	if err != nil {
		return err
	}
	if events[len(events)-1].Summary != "edited hello.txt" {
		return fmt.Errorf("terminal %+v", events[len(events)-1])
	}
	return nil
}

func checkBareIdle(bin string) error {
	srv := serveChat(func(string, int) (int, string) {
		return 200, assistant("ok", 0, 0)
	})
	defer srv.Close()
	got, err := runHarness(bin, []string{
		"agent", "run", "--task-id", "bare", "--model", "m", "--idle-on-complete",
	}, []string{"OPENAI_BASE_URL=" + srv.URL}, map[string]string{"prompt": "hi"}, 2*time.Second)
	if err != nil {
		return err
	}
	if got.Code != 0 {
		return fmt.Errorf("exit %d\n%s", got.Code, got.Stderr)
	}
	term := got.Events[len(got.Events)-1]
	if term.Summary != "ok" {
		return fmt.Errorf("terminal %+v", term)
	}
	return nil
}
