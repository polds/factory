package harness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSkillTextAppendedToSystemPrompt(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=sk-test",
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=1",
			"FACTORY_SKILL_TEXT=SKILL_BODY_MARKER",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: t.TempDir(),
		Stderr:     io.Discard,
	})
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	got := systemContent(t, body)
	want := systemPrompt + "\n\nSKILL_BODY_MARKER"
	if got != want {
		t.Fatalf("system prompt %q", got)
	}
}

func TestEmptySkillTextLeavesSystemPrompt(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)
	code := Run(context.Background(), Options{
		Args: []string{"--task-id", "t1", "--model", "m"},
		Env: []string{
			"OPENAI_BASE_URL=" + srv.URL,
			"OPENAI_API_KEY=sk-test",
			"FACTORY_TASK_PROMPT=do the thing",
			"FACTORY_MAX_TURNS=1",
		},
		Workspace:  t.TempDir(),
		RuntimeDir: t.TempDir(),
		Stderr:     io.Discard,
	})
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if got := systemContent(t, body); got != systemPrompt {
		t.Fatalf("system prompt %q", got)
	}
}

func systemContent(t *testing.T, body string) string {
	t.Helper()
	var req chatRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) == 0 || req.Messages[0].Role != "system" {
		t.Fatalf("messages %+v", req.Messages)
	}
	return req.Messages[0].Content
}
