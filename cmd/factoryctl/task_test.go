package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cliGetenv(url, key string) func(string) string {
	return func(k string) string {
		switch k {
		case "FACTORY_URL":
			return url
		case "FACTORY_API_KEY":
			return key
		default:
			return ""
		}
	}
}

func TestTaskCreatePrintsID(t *testing.T) {
	var gotBody []byte
	var gotPath, gotMethod, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"task_cli","stage":"triage","status":"open"}`))
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr strings.Builder
	code := execute([]string{
		"task", "create", "--factory", "acme", "--title", "Fix the gate",
		"--body", "details", "--source", "manual", "--ref", "42",
	}, &stdout, &stderr, cliGetenv(srv.URL, "cli-key"))
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if stdout.String() != "task_cli\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/factory/acme/tasks" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer cli-key" {
		t.Fatalf("authorization %q", gotAuth)
	}
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "Fix the gate" || body["body"] != "details" || body["source"] != "manual" || body["source_ref"] != "42" {
		t.Fatalf("body %s", gotBody)
	}
	if strings.Contains(stdout.String(), "cli-key") || strings.Contains(stderr.String(), "cli-key") {
		t.Fatal("api key leaked")
	}
}

func TestTaskCreateRejectsBadSource(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr strings.Builder
	code := execute([]string{"task", "create", "--factory", "acme", "--title", "T", "--source", "webhook"}, &stdout, &stderr, cliGetenv(srv.URL, "cli-key"))
	if code != 2 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if hit {
		t.Fatal("request sent for invalid source")
	}
	if !strings.Contains(stderr.String(), "source") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestApprovalsDecideSendsBody(t *testing.T) {
	var gotBody []byte
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.RequestURI()
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"appr_1","decision":"reject","note":"needs tests","gate":"spec"}`))
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr strings.Builder
	code := execute([]string{"approvals", "decide", "appr_1", "--decision", "reject", "--note", "needs tests"}, &stdout, &stderr, cliGetenv(srv.URL, "cli-key"))
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/approvals/appr_1:decide" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["decision"] != "reject" || body["note"] != "needs tests" {
		t.Fatalf("body %s", gotBody)
	}
	if !strings.Contains(stdout.String(), "appr_1") || !strings.Contains(stdout.String(), "reject") {
		t.Fatalf("stdout %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "cli-key") || strings.Contains(stderr.String(), "cli-key") {
		t.Fatal("api key leaked")
	}
}

func TestApprovalsDecideRejectsDecision(t *testing.T) {
	var stdout, stderr strings.Builder
	code := execute([]string{"approvals", "decide", "appr_1", "--decision", "yes"}, &stdout, &stderr, cliGetenv("http://127.0.0.1:1", "cli-key"))
	if code != 2 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "decision") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestTaskListGetCancelWatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cli-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/factory/acme/tasks" && r.URL.Query().Get("stage") == "building":
			_, _ = w.Write([]byte(`[{"id":"task_cli","stage":"building","status":"open","title":"Fix"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/factory/acme/tasks/task_cli":
			_, _ = w.Write([]byte(`{"id":"task_cli","stage":"reviewing","status":"complete","title":"Fix","runs":[{"id":"run_cli","status":"succeeded"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/factory/acme/tasks/task_cli/cancel":
			_, _ = w.Write([]byte(`{"id":"task_cli","status":"cancelled"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/factory/acme/runs":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/runs/run_cli/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "id: 1\nevent: terminal\ndata: {\"status\":\"succeeded\",\"summary\":\"done\"}\n\n")
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/factory/acme/approvals":
			_, _ = w.Write([]byte(`[{"id":"appr_1","gate":"spec","work_item_id":"task_cli"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	getenv := cliGetenv(srv.URL, "cli-key")

	var stdout, stderr strings.Builder
	code := execute([]string{"task", "list", "--factory", "acme", "--stage", "building"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "task_cli") || !strings.Contains(stdout.String(), "building") {
		t.Fatalf("list exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"task", "get", "task_cli", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "task_cli") || !strings.Contains(stdout.String(), "reviewing") {
		t.Fatalf("get exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"task", "cancel", "--factory", "acme", "task_cli"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "task_cli") || !strings.Contains(stdout.String(), "cancelled") {
		t.Fatalf("cancel exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"task", "watch", "task_cli", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 {
		t.Fatalf("watch exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "stage=reviewing status=complete") || !strings.Contains(stdout.String(), "run=run_cli terminal status=succeeded") {
		t.Fatalf("watch stdout %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = execute([]string{"approvals", "list", "--factory", "acme"}, &stdout, &stderr, getenv)
	if code != 0 || !strings.Contains(stdout.String(), "appr_1") || !strings.Contains(stdout.String(), "spec") {
		t.Fatalf("approvals exit %d stdout %q stderr %s", code, stdout.String(), stderr.String())
	}
}

func TestUsageIncludesTask(t *testing.T) {
	var stdout, stderr strings.Builder
	code := execute(nil, &stdout, &stderr, func(string) string { return "" })
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	text := stderr.String()
	for _, want := range []string{"task create", "task watch", "approvals list", "approvals decide"} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage missing %q\n%s", want, text)
		}
	}
}
