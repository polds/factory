package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateTaskSendsBearerAndJSON(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"task_abc","stage":"triage","status":"open","title":"Fix the gate"}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	item, err := c.CreateTask(context.Background(), "acme", TaskRequest{
		Title:     "Fix the gate",
		Body:      "details",
		Source:    "issue",
		SourceRef: "42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "task_abc" || item.Stage != "triage" || item.Status != "open" {
		t.Fatalf("item %+v", item)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/factory/acme/tasks" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization %q", gotAuth)
	}
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"title":      "Fix the gate",
		"body":       "details",
		"source":     "issue",
		"source_ref": "42",
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%s]=%q want %q (full %s)", k, body[k], v, gotBody)
		}
	}
	if _, ok := body["ref"]; ok {
		t.Fatalf("ref must be sent as source_ref, body %s", gotBody)
	}
	if strings.Contains(string(gotBody), "test-key") {
		t.Fatal("token leaked into body")
	}
}

func TestCreateTaskRequiresKey(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1"}
	_, err := c.CreateTask(context.Background(), "acme", TaskRequest{Title: "T"})
	if err == nil || !strings.Contains(err.Error(), "FACTORY_API_KEY") {
		t.Fatalf("error %v", err)
	}
}

func TestListTasksFilters(t *testing.T) {
	var gotPath, gotStage, gotStatus string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotStage = r.URL.Query().Get("stage")
		gotStatus = r.URL.Query().Get("status")
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"task_1","stage":"planning","status":"awaiting_approval","title":"T","pending_gate":"spec"}]`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	items, err := c.ListTasks(context.Background(), "acme", TaskFilter{Stage: "planning", Status: "awaiting_approval"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/acme/tasks" || gotStage != "planning" || gotStatus != "awaiting_approval" {
		t.Fatalf("path %s stage %q status %q", gotPath, gotStage, gotStatus)
	}
	if len(items) != 1 || items[0].ID != "task_1" || items[0].PendingGate != "spec" {
		t.Fatalf("items %+v", items)
	}
}

func TestGetAndCancelTask(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"task_abc","stage":"building","status":"open","title":"T"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"):
			_, _ = w.Write([]byte(`{"id":"task_abc","stage":"building","status":"cancelled","title":"T"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	item, err := c.GetTask(context.Background(), "acme", "task_abc")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "task_abc" || item.Status != "open" {
		t.Fatalf("get %+v", item)
	}
	cancelled, err := c.CancelTask(context.Background(), "acme", "task_abc")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancel %+v", cancelled)
	}
	if len(paths) != 2 ||
		paths[0] != "GET /api/v1/factory/acme/tasks/task_abc" ||
		paths[1] != "POST /api/v1/factory/acme/tasks/task_abc/cancel" {
		t.Fatalf("paths %q", paths)
	}
}

func TestDecideApprovalSendsBody(t *testing.T) {
	var gotAuth, gotPath, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"appr_1","work_item_id":"task_abc","gate":"spec","decision":"approve","note":"ship it"}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	got, err := c.DecideApproval(context.Background(), "appr_1", "approve", "ship it")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "appr_1" || got.Decision != "approve" || got.Gate != "spec" || got.WorkItemID != "task_abc" {
		t.Fatalf("approval %+v", got)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/approvals/appr_1:decide" {
		t.Fatalf("request %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization %q", gotAuth)
	}
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["decision"] != "approve" || body["note"] != "ship it" {
		t.Fatalf("body %s", gotBody)
	}
	if strings.Contains(string(gotBody), "test-key") {
		t.Fatal("token leaked into body")
	}
}

func TestListApprovals(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"appr_1","work_item_id":"task_abc","gate":"spec"}]`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	items, err := c.ListApprovals(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/acme/approvals" {
		t.Fatalf("path %s", gotPath)
	}
	if len(items) != 1 || items[0].ID != "appr_1" || items[0].Gate != "spec" {
		t.Fatalf("items %+v", items)
	}
}

func TestWatchTaskStagesAndChildTranscript(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/factory/acme/tasks/task_abc":
			n := polls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			switch n {
			case 1:
				_, _ = w.Write([]byte(`{"id":"task_abc","stage":"triage","status":"open"}`))
			case 2:
				_, _ = w.Write([]byte(`{"id":"task_abc","stage":"planning","status":"awaiting_approval","pending_gate":"spec","runs":[{"id":"run_child","status":"succeeded"}]}`))
			default:
				_, _ = w.Write([]byte(`{"id":"task_abc","stage":"reviewing","status":"complete","runs":[{"id":"run_child","status":"succeeded"}]}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/runs/run_child/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "id: 1\nevent: message.delta\ndata: {\"role\":\"assistant\",\"text\":\"hello\"}\n\n")
			_, _ = io.WriteString(w, "id: 2\nevent: terminal\ndata: {\"status\":\"succeeded\",\"summary\":\"done\"}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	c := &Client{BaseURL: srv.URL, APIKey: "test-key", PollInterval: time.Millisecond}
	code, err := c.WatchTask(ctx, "acme", "task_abc", &out)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	text := out.String()
	triage := strings.Index(text, "stage=triage status=open")
	planning := strings.Index(text, "stage=planning status=awaiting_approval gate=spec")
	line := strings.Index(text, "run=run_child message.delta role=assistant text=hello")
	term := strings.Index(text, "run=run_child terminal status=succeeded summary=done")
	done := strings.Index(text, "stage=reviewing status=complete")
	if triage < 0 || planning < 0 || line < 0 || term < 0 || done < 0 {
		t.Fatalf("stdout %q", text)
	}
	if triage >= planning || planning >= line || line >= term || term >= done {
		t.Fatalf("order %q", text)
	}
	if strings.Contains(text, "test-key") {
		t.Fatal("api key leaked")
	}
}

func TestWatchTaskCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_abc","stage":"building","status":"cancelled"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var out strings.Builder
	c := &Client{BaseURL: srv.URL, APIKey: "test-key", PollInterval: time.Millisecond}
	code, err := c.WatchTask(ctx, "acme", "task_abc", &out)
	if err != nil {
		t.Fatal(err)
	}
	if code != 2 {
		t.Fatalf("exit %d stdout %q", code, out.String())
	}
	if !strings.Contains(out.String(), "stage=building status=cancelled") {
		t.Fatalf("stdout %q", out.String())
	}
}
