package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type scopeBackend struct {
	*fakeBackend
	keys            map[string]string
	schedules       []Schedule
	tasks           map[string]Task
	approvals       map[string]string
	deliveries      map[string]string
	external        ExternalRun
	startCalls      int
	scheduleCreates int
	decides         int
	replays         int
	applyCalls      int
	putCalls        int
}

func newScopeBackend() *scopeBackend {
	base := newFake()
	base.factories = []Factory{{ID: "fac_a", Name: "a"}, {ID: "fac_b", Name: "b"}}
	base.runs = map[string]Run{
		"run_a": {ID: "run_a", Status: "running", FactoryID: "fac_a", Title: "alpha-secret"},
		"run_b": {ID: "run_b", Status: "running", FactoryID: "fac_b", Title: "beta"},
	}
	base.listRuns = []Run{base.runs["run_a"], base.runs["run_b"]}
	return &scopeBackend{
		fakeBackend: base,
		keys:        map[string]string{"key-b": "fac_b"},
		schedules: []Schedule{
			{ID: "sch_a", FactoryID: "fac_a", Automation: "a", Cron: "0 * * * *"},
			{ID: "sch_b", FactoryID: "fac_b", Automation: "b", Cron: "0 * * * *"},
		},
		tasks: map[string]Task{
			"task_a": {ID: "task_a", FactoryID: "fac_a", Title: "alpha-task", Stage: "triage", Status: "open"},
		},
		approvals:  map[string]string{"apr_a": "fac_a"},
		deliveries: map[string]string{"del_a": "fac_a"},
		external: ExternalRun{
			Provider: "github", ExternalRef: "alpha", WorkItemID: "task_a",
			Run: &Run{ID: "run_a", FactoryID: "fac_a", Title: "alpha-secret"},
		},
	}
}

func (b *scopeBackend) FactoryIDForKey(_ context.Context, token string) (string, bool, error) {
	id, ok := b.keys[token]
	return id, ok, nil
}

func (b *scopeBackend) ResolveFactoryID(_ context.Context, nameOrID string) (string, error) {
	for _, factory := range b.factories {
		if factory.ID == nameOrID || factory.Name == nameOrID {
			return factory.ID, nil
		}
	}
	return "", ErrNotFound
}

func (b *scopeBackend) StartRun(ctx context.Context, req RunRequest) (Run, error) {
	b.startCalls++
	return b.fakeBackend.StartRun(ctx, req)
}

func (b *scopeBackend) ListRuns(_ context.Context, filter RunFilter) ([]Run, error) {
	var out []Run
	for _, run := range b.listRuns {
		if filter.FactoryID != "" && run.FactoryID != filter.FactoryID {
			continue
		}
		out = append(out, run)
	}
	if out == nil {
		out = []Run{}
	}
	return out, nil
}

func (b *scopeBackend) ApplyDefinition(ctx context.Context, factoryID string) (DefinitionRef, []Diagnostic, error) {
	b.applyCalls++
	return b.fakeBackend.ApplyDefinition(ctx, factoryID)
}

func (b *scopeBackend) PublishDefinition(context.Context, string, DefinitionPushRequest) (DefinitionPushResponse, error) {
	b.putCalls++
	return DefinitionPushResponse{}, nil
}

func (b *scopeBackend) ListSchedules(_ context.Context, factoryID string) ([]Schedule, error) {
	var out []Schedule
	for _, row := range b.schedules {
		if factoryID != "" && row.FactoryID != factoryID {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (b *scopeBackend) GetSchedule(_ context.Context, id string) (Schedule, error) {
	for _, row := range b.schedules {
		if row.ID == id {
			return row, nil
		}
	}
	return Schedule{}, ErrNotFound
}

func (b *scopeBackend) CreateSchedule(_ context.Context, req ScheduleRequest) (Schedule, error) {
	b.scheduleCreates++
	return Schedule{ID: "sch_new", FactoryID: req.FactoryID, Automation: req.Automation, Cron: req.Cron}, nil
}

func (b *scopeBackend) UpdateSchedule(context.Context, string, SchedulePatch) (Schedule, error) {
	return Schedule{}, nil
}

func (b *scopeBackend) DeleteSchedule(context.Context, string) error { return nil }

func (b *scopeBackend) SetSchedulePaused(context.Context, string, bool) (Schedule, error) {
	return Schedule{}, nil
}

func (b *scopeBackend) CreateTask(context.Context, string, TaskRequest) (Task, error) {
	return Task{}, nil
}

func (b *scopeBackend) GetTask(_ context.Context, _, id string) (Task, error) {
	task, ok := b.tasks[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return task, nil
}

func (b *scopeBackend) ListTasks(context.Context, string, TaskFilter) ([]Task, error) {
	return nil, nil
}

func (b *scopeBackend) CancelTask(context.Context, string, string) (Task, error) {
	return Task{}, nil
}

func (b *scopeBackend) ListApprovals(context.Context, string, bool) ([]Approval, error) {
	return nil, nil
}

func (b *scopeBackend) DecideApproval(context.Context, string, string, string, string) (Approval, error) {
	b.decides++
	return Approval{}, nil
}

func (b *scopeBackend) ApprovalFactory(_ context.Context, id string) (string, error) {
	factoryID, ok := b.approvals[id]
	if !ok {
		return "", ErrNotFound
	}
	return factoryID, nil
}

func (b *scopeBackend) ListDeliveries(context.Context, string, string, int) ([]Delivery, error) {
	return nil, nil
}

func (b *scopeBackend) ReplayDelivery(context.Context, string) (Delivery, error) {
	b.replays++
	return Delivery{}, nil
}

func (b *scopeBackend) DeliveryFactory(_ context.Context, id string) (string, error) {
	factoryID, ok := b.deliveries[id]
	if !ok {
		return "", ErrNotFound
	}
	return factoryID, nil
}

func (b *scopeBackend) RunByExternalReference(context.Context, string, string) (ExternalRun, error) {
	return b.external, nil
}

func TestScopedKeyIsolation(t *testing.T) {
	backend := newScopeBackend()
	srv := httptest.NewServer(NewHandler(backend, "admin-key", nil))
	t.Cleanup(srv.Close)

	status, body := scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/runs/run_a", "")
	assertEmpty(t, status, body, http.StatusNotFound, "alpha-secret")
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/factory/fac_a/tasks/task_a", "")
	assertEmpty(t, status, body, http.StatusNotFound, "alpha-task")
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/approvals/apr_a:decide", `{"decision":"approve"}`)
	assertEmpty(t, status, body, http.StatusNotFound, "")
	if backend.decides != 0 {
		t.Fatal("decide ran")
	}
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/deliveries/del_a:replay", `{}`)
	assertEmpty(t, status, body, http.StatusNotFound, "")
	if backend.replays != 0 {
		t.Fatal("replay ran")
	}
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/schedules/sch_a", "")
	assertEmpty(t, status, body, http.StatusNotFound, "sch_a")

	before := backend.startCalls
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/agent/run", `{"factory":"fac_a","prompt":"go"}`)
	assertCode(t, status, body, http.StatusForbidden)
	if backend.startCalls != before {
		t.Fatal("start ran for another factory")
	}
	before = backend.scheduleCreates
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/agent/schedules", `{"factory_id":"fac_a","automation":"a","cron":"0 * * * *"}`)
	assertCode(t, status, body, http.StatusForbidden)
	if backend.scheduleCreates != before {
		t.Fatal("schedule insert ran")
	}
	before = backend.applyCalls
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/factory/fac_b/definition:apply", "")
	assertCode(t, status, body, http.StatusForbidden)
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/factory/fac_a/definition:apply", "")
	assertCode(t, status, body, http.StatusForbidden)
	if backend.applyCalls != before {
		t.Fatal("apply ran")
	}
	before = backend.putCalls
	status, body = scopeDo(t, srv, "key-b", http.MethodPut, "/api/v1/factory/fac_b/definition", `{"generation":1,"files":[{"path":"factory.yaml","content":"name: b"}]}`)
	assertCode(t, status, body, http.StatusForbidden)
	if backend.putCalls != before {
		t.Fatal("put ran")
	}
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/factory/fac_b/definition:validate", `{}`)
	assertCode(t, status, body, http.StatusOK)
	status, body = scopeDo(t, srv, "key-b", http.MethodPost, "/api/v1/factory/fac_a/definition:validate", `{}`)
	assertCode(t, status, body, http.StatusForbidden)

	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/runs", "")
	if status != http.StatusOK || strings.Contains(body, "run_a") || !strings.Contains(body, "run_b") {
		t.Fatalf("runs %d %s", status, body)
	}
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/runs?factory_id=fac_a", "")
	assertCode(t, status, body, http.StatusForbidden)
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/schedules", "")
	if status != http.StatusOK || strings.Contains(body, "sch_a") || !strings.Contains(body, "sch_b") {
		t.Fatalf("schedules %d %s", status, body)
	}
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/schedules?factory=fac_a", "")
	assertCode(t, status, body, http.StatusForbidden)
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/factory", "")
	if status != http.StatusOK || strings.Contains(body, "fac_a") || !strings.Contains(body, "fac_b") {
		t.Fatalf("factories %d %s", status, body)
	}
	status, body = scopeDo(t, srv, "key-b", http.MethodGet, "/api/v1/agent/run-by-external-reference?provider=github&ref=alpha", "")
	assertEmpty(t, status, body, http.StatusNotFound, "alpha-secret")

	status, body = scopeDo(t, srv, "admin-key", http.MethodGet, "/api/v1/agent/runs", "")
	if status != http.StatusOK || !strings.Contains(body, "run_a") || !strings.Contains(body, "run_b") {
		t.Fatalf("admin runs %d %s", status, body)
	}
	status, body = scopeDo(t, srv, "admin-key", http.MethodGet, "/api/v1/factory", "")
	if status != http.StatusOK || !strings.Contains(body, "fac_a") || !strings.Contains(body, "fac_b") {
		t.Fatalf("admin factories %d %s", status, body)
	}
}

func scopeDo(t *testing.T, srv *httptest.Server, key, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(raw)
}

func assertCode(t *testing.T, status int, body string, want int) {
	t.Helper()
	if status != want {
		t.Fatalf("status %d body %s", status, body)
	}
}

func assertEmpty(t *testing.T, status int, body string, want int, absent string) {
	t.Helper()
	if status != want || len(bytes.TrimSpace([]byte(body))) != 0 || (absent != "" && strings.Contains(body, absent)) {
		t.Fatalf("status %d body %q", status, body)
	}
}
