package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHelpListsConsoleCommands(t *testing.T) {
	var stdout, stderr strings.Builder
	code := execute([]string{"--help"}, &stdout, &stderr, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{
		"runs show RUN_ID",
		"runs interrupt RUN_ID",
		"runs followup RUN_ID --text TEXT",
		"inbox --factory NAME",
		"workers",
		"automations list --factory NAME",
		"automations pause --factory NAME AUTOMATION",
		"automations resume --factory NAME AUTOMATION",
		"secrets list --factory NAME",
		"definition show --factory NAME",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q\n%s", want, text)
		}
	}
}

func TestConsoleCommands(t *testing.T) {
	const planted = "planted-secret-value-9f3a"
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		gotPath = r.URL.Path
		gotMethod = r.Method
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/agent/runs/run_1" && r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"id":"run_1","status":"failed","failure":{"class":"agent","code":"agent_reported","summary":"The agent reported the run as failed","retryable":false,"evidence":["exit code 10","harness status failed_by_agent"]},"queue_wait_ms":1,"exec_ms":2,"steer":{"enabled":false,"reason":"run finished"},"value":%q}`, planted)
		case r.URL.Path == "/api/v1/agent/runs/run_1/interrupt":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"steer_id":"steer_01"}`))
		case r.URL.Path == "/api/v1/agent/runs/run_1/followups":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"steer_id":"steer_02"}`))
		case r.URL.Path == "/api/v1/factory/acme/inbox":
			_, _ = w.Write([]byte(`[{"kind":"approval","id":"appr_1","work_item_id":"task_01","run_id":"","created_at":"2026-04-02T15:00:00Z","title":"Spec gate","detail":"planning","action":"decide"}]`))
		case r.URL.Path == "/api/v1/agent/connected-self-hosted-workers":
			_, _ = w.Write([]byte(`{"workers":[{"id":"w1","pool":"default","platform":"linux/amd64","backend":"docker","version":"1.0.0","in_flight":1,"capacity":2,"heartbeat_age_ms":500,"reconnects":0,"rejected_1h":1}],"pools":[{"pool":"default","queue_depth":3,"p50_time_to_assign_ms":800,"rejected_1h":1}]}`))
		case r.URL.Path == "/api/v1/factory/acme/automations" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"name":"on-label","enabled":true,"runtime_paused":false,"triggers":["github.label"],"match_count":4,"runs_started":2}]`))
		case r.URL.Path == "/api/v1/factory/acme/automations/on-label/pause":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/factory/acme/automations/on-label/resume":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v1/factory/acme/secrets":
			_, _ = fmt.Fprintf(w, `[{"name":"OPENAI_API_KEY","provider":"env","present":true,"used_by":[{"kind":"agent","name":"implementer"}],"value":%q}]`, planted)
		case r.URL.Path == "/api/v1/factory/acme/definition":
			_, _ = fmt.Fprintf(w, `{"source":"git","git_sha":"abc","production_ref":"main","editable":false,"files":[{"path":"factory.yaml","content":"name: acme\n"}],"value":%q}`, planted)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		switch k {
		case "FACTORY_URL":
			return srv.URL
		case "FACTORY_API_KEY":
			return "cli-key"
		default:
			return ""
		}
	}

	var stdout, stderr strings.Builder
	code := execute([]string{"runs", "show", "run_1"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("show exit %d stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"class: agent", "summary: The agent reported the run as failed", "evidence: exit code 10", "evidence: harness status failed_by_agent"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, planted) || strings.Contains(stderr.String(), planted) {
		t.Fatal("show printed secret material")
	}

	stdout.Reset()
	code = execute([]string{"runs", "interrupt", "run_1"}, &stdout, &stderr, getenv)
	if code != 0 || stdout.String() != "steer_01\n" || gotMethod != http.MethodPost || gotPath != "/api/v1/agent/runs/run_1/interrupt" {
		t.Fatalf("interrupt exit %d stdout %q %s %s stderr %s", code, stdout.String(), gotMethod, gotPath, stderr.String())
	}

	stdout.Reset()
	code = execute([]string{"runs", "followup", "run_1", "--text", "try the other file"}, &stdout, &stderr, getenv)
	if code != 0 || stdout.String() != "steer_02\n" || !strings.Contains(gotBody, "try the other file") {
		t.Fatalf("followup exit %d stdout %q body %s stderr %s", code, stdout.String(), gotBody, stderr.String())
	}

	stdout.Reset()
	code = execute([]string{"inbox", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "approval appr_1") || !strings.Contains(stdout.String(), "Spec gate") {
		t.Fatalf("inbox exit %d stdout %q", code, stdout.String())
	}

	stdout.Reset()
	code = execute([]string{"workers"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "worker w1") || !strings.Contains(stdout.String(), "queue_depth=3") {
		t.Fatalf("workers exit %d stdout %q", code, stdout.String())
	}

	stdout.Reset()
	code = execute([]string{"automations", "list", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "on-label enabled=true runtime_paused=false") {
		t.Fatalf("automations exit %d stdout %q", code, stdout.String())
	}
	stdout.Reset()
	code = execute([]string{"automations", "pause", "--factory", "acme", "on-label"}, &stdout, &stderr, getenv)
	if code != 0 || stdout.String() != "on-label paused\n" {
		t.Fatalf("pause exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	code = execute([]string{"automations", "resume", "--factory", "acme", "on-label"}, &stdout, &stderr, getenv)
	if code != 0 || stdout.String() != "on-label resumed\n" {
		t.Fatalf("resume exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	code = execute([]string{"secrets", "list", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "OPENAI_API_KEY provider=env present=true") {
		t.Fatalf("secrets exit %d stdout %q", code, stdout.String())
	}
	if strings.Contains(stdout.String(), planted) {
		t.Fatal("secrets printed a value")
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"definition", "show", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "source: git") || !strings.Contains(stdout.String(), "--- factory.yaml") || !strings.Contains(stdout.String(), "name: acme") {
		t.Fatalf("definition exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), planted) || strings.Contains(stderr.String(), planted) {
		t.Fatalf("definition printed secret material\nstdout %s\nstderr %s", stdout.String(), stderr.String())
	}
}

func TestConsoleCommandUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	getenv := func(string) string { return "" }
	if code := execute([]string{"runs", "followup", "run_1"}, &stdout, &stderr, getenv); code != 2 || !strings.Contains(stderr.String(), "--text is required") {
		t.Fatalf("followup exit %d stderr %s", code, stderr.String())
	}
	stderr.Reset()
	if code := execute([]string{"inbox"}, &stdout, &stderr, getenv); code != 2 || !strings.Contains(stderr.String(), "--factory is required") {
		t.Fatalf("inbox exit %d stderr %s", code, stderr.String())
	}
}
