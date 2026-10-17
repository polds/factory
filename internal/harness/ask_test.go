package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const offlineQuestion = "Which staging env?"

// TestAskInputEmitsQuestionFromOfflineStub is the contract the inbox reads.
// The offline model-stub calls ask_input once. The run writes exactly one
// question event, with the gate and text the control plane stores.
func TestAskInputEmitsQuestionFromOfflineStub(t *testing.T) {
	script := writeStubScript(t, `[
		{"tool":"ask_input","args":{"text":"Which staging env?"}},
		{"final":"should not be asked"}
	]`)
	base := startOfflineStub(t, script)
	code, raw := runAsk(t, base, nil)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	events := decodeAskEvents(t, raw)
	var questions []Event
	for _, ev := range events {
		if ev.Type == "question" {
			questions = append(questions, ev)
		}
		if ev.Text == "should not be asked" || ev.Summary == "should not be asked" {
			t.Fatalf("model continued after ask_input: %+v", ev)
		}
	}
	if len(questions) != 1 {
		t.Fatalf("question events %d, want 1\n%s", len(questions), raw)
	}
	got := questions[0]
	if got.Gate != "clarification" || got.Text != offlineQuestion {
		t.Fatalf("question %+v", got)
	}
	if !strings.Contains(raw, `"type":"question"`) || !strings.Contains(raw, `"text":"Which staging env?"`) || !strings.Contains(raw, `"gate":"clarification"`) {
		t.Fatalf("question event is missing inbox fields\n%s", raw)
	}
}

func TestAskInputRedactsQuestion(t *testing.T) {
	const secret = "planted-secret-value"
	script := writeStubScript(t, `[
		{"tool":"ask_input","args":{"text":"Use token planted-secret-value?"}},
		{"final":"should not be asked"}
	]`)
	base := startOfflineStub(t, script)
	code, raw := runAsk(t, base, []string{"FACTORY_SECRET_VALUES=" + secret})
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("secret leaked into events\n%s", raw)
	}
	events := decodeAskEvents(t, raw)
	var text string
	n := 0
	for _, ev := range events {
		if ev.Type != "question" {
			continue
		}
		n++
		text = ev.Text
	}
	if n != 1 || text != "Use token [REDACTED]?" {
		t.Fatalf("questions %d text %q\n%s", n, text, raw)
	}
}

func TestAskInputEmptyTextDoesNotAsk(t *testing.T) {
	script := writeStubScript(t, `[
		{"tool":"ask_input","args":{}},
		{"final":"continued without a question"}
	]`)
	base := startOfflineStub(t, script)
	code, raw := runAsk(t, base, nil)
	if code != ExitOK {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	events := decodeAskEvents(t, raw)
	for _, ev := range events {
		if ev.Type == "question" {
			t.Fatalf("empty ask_input emitted %+v", ev)
		}
	}
	var result Event
	for _, ev := range events {
		if ev.Type == "tool.result" && ev.Tool == "ask_input" {
			result = ev
		}
	}
	if result.OutputDigest != Digest("ask_input requires text") {
		t.Fatalf("tool.result %+v", result)
	}
	term := events[len(events)-1]
	if term.Type != "terminal" || term.Summary != "continued without a question" {
		t.Fatalf("terminal %+v", term)
	}
}

func writeStubScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runAsk(t *testing.T, baseURL string, extra []string) (int, string) {
	t.Helper()
	ws := t.TempDir()
	rt := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.TempDir(),
		"OPENAI_BASE_URL=" + baseURL,
		"OPENAI_API_KEY=offline-dummy",
		"FACTORY_TASK_PROMPT=ask which staging env",
		"FACTORY_MAX_TURNS=4",
	}
	env = append(env, extra...)
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	code := Run(ctx, Options{
		Args:       []string{"agent", "run", "--task-id", "ask", "--model", "offline-stub", "--idle-on-complete", "0s"},
		Env:        env,
		Workspace:  ws,
		RuntimeDir: rt,
		Stderr:     &errBuf,
	})
	if ctx.Err() != nil {
		t.Fatalf("harness timed out\n%s", errBuf.String())
	}
	b, err := os.ReadFile(filepath.Join(rt, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return code, string(b)
}

func decodeAskEvents(t *testing.T, raw string) []Event {
	t.Helper()
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event %s: %v", line, err)
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("no events")
	}
	return events
}

var (
	stubBinOnce sync.Once
	stubBinPath string
	stubBinErr  error
)

func modelStubBin() (string, error) {
	stubBinOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			stubBinErr = fmt.Errorf("go env GOMOD: %w", err)
			return
		}
		root := filepath.Dir(strings.TrimSpace(string(out)))
		dir, err := os.MkdirTemp("", "factory-model-stub-")
		if err != nil {
			stubBinErr = err
			return
		}
		stubBinPath = filepath.Join(dir, "model-stub")
		cmd := exec.Command("go", "build", "-o", stubBinPath, "./spike/m1/model-stub")
		cmd.Dir = root
		built, err := cmd.CombinedOutput()
		if err != nil {
			stubBinErr = fmt.Errorf("build model-stub: %w\n%s", err, built)
		}
	})
	return stubBinPath, stubBinErr
}

func startOfflineStub(t *testing.T, script string) string {
	t.Helper()
	bin, err := modelStubBin()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-addr", addr, "-script", script)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	base := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for time.Now().Before(deadline) {
		res, err := client.Get(base + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("model-stub did not listen\n%s", logs.String())
	return ""
}
