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

	admin := func(method, path, body string) *http.Response {
		t.Helper()
		return scopeDo(t, srv, "admin-key", method, path, body)
	}
	scoped := func(method, path, body string) *http.Response {
		t.Helper()
		return scopeDo(t, srv, "key-b", method, path, body)
	}

	res := scoped(http.MethodGet, "/api/v1/agent/runs/run_a", "")
	assertEmptyStatus(t, res, http.StatusNotFound, "alpha-secret")
	res = scoped(http.MethodGet, "/api/v1/factory/fac_a/tasks/task_a", "")
	assertEmptyStatus(t, res, http.StatusNotFound, "alpha-task")
	res = scoped(http.MethodPost, "/api/v1/approvals/apr_a:decide", `{"decision":"approve"}`)
	assertEmptyStatus(t, res, http.StatusNotFound, "")
	if backend.decides != 0 {
		t.Fatal("decide ran")
	}
	res = scoped(http.MethodPost, "/api/v1/deliveries/del_a:replay", `{}`)
	assertEmptyStatus(t, res, http.StatusNotFound, "")
	if backend.replays != 0 {
		t.Fatal("replay ran")
	}
	res = scoped(http.MethodGet, "/api/v1/agent/schedules/sch_a", "")
	assertEmptyStatus(t, res, http.StatusNotFound, "sch_a")

	before := backend.startCalls
	res = scoped(http.MethodPost, "/api/v1/agent/run", `{"factory":"fac_a","prompt":"go"}`)
	assertStatus(t, res, http.StatusForbidden)
	if backend.startCalls != before {
		t.Fatal("start ran for another factory")
	}
	before = backend.scheduleCreates
	res = scoped(http.MethodPost, "/api/v1/agent/schedules", `{"factory_id":"fac_a","automation":"a","cron":"0 * * * *"}`)
	assertStatus(t, res, http.StatusForbidden)
	if backend.scheduleCreates != before {
		t.Fatal("schedule insert ran")
	}
	before = backend.applyCalls
	res = scoped(http.MethodPost, "/api/v1/factory/fac_b/definition:apply", "")
	assertStatus(t, res, http.StatusForbidden)
	res = scoped(http.MethodPost, "/api/v1/factory/fac_a/definition:apply", "")
	assertStatus(t, res, http.StatusForbidden)
	if backend.applyCalls != before {
		t.Fatal("apply ran")
	}
	before = backend.putCalls
	res = scoped(http.MethodPut, "/api/v1/factory/fac_b/definition", `{"generation":1,"files":[{"path":"factory.yaml","content":"name: b"}]}`)
	assertStatus(t, res, http.StatusForbidden)
	if backend.putCalls != before {
		t.Fatal("put ran")
	}
	res = scoped(http.MethodPost, "/api/v1/factory/fac_b/definition:validate", `{}`)
	assertStatus(t, res, http.StatusOK)
	res = scoped(http.MethodPost, "/api/v1/factory/fac_a/definition:validate", `{}`)
	assertStatus(t, res, http.StatusForbidden)

	res = scoped(http.MethodGet, "/api/v1/agent/runs", "")
	body := readAll(t, res)
	if res.StatusCode != http.StatusOK || strings.Contains(body, "run_a") || !strings.Contains(body, "run_b") {
		t.Fatalf("runs %d %s", res.StatusCode, body)
	}
	res = scoped(http.MethodGet, "/api/v1/agent/runs?factory_id=fac_a", "")
	assertStatus(t, res, http.StatusForbidden)
	res = scoped(http.MethodGet, "/api/v1/agent/schedules", "")
	body = readAll(t, res)
	if res.StatusCode != http.StatusOK || strings.Contains(body, "sch_a") || !strings.Contains(body, "sch_b") {
		t.Fatalf("schedules %d %s", res.StatusCode, body)
	}
	res = scoped(http.MethodGet, "/api/v1/agent/schedules?factory=fac_a", "")
	assertStatus(t, res, http.StatusForbidden)
	res = scoped(http.MethodGet, "/api/v1/factory", "")
	body = readAll(t, res)
	if res.StatusCode != http.StatusOK || strings.Contains(body, "fac_a") || !strings.Contains(body, "fac_b") {
		t.Fatalf("factories %d %s", res.StatusCode, body)
	}
	res = scoped(http.MethodGet, "/api/v1/agent/run-by-external-reference?provider=github&ref=alpha", "")
	assertEmptyStatus(t, res, http.StatusNotFound, "alpha-secret")

	res = admin(http.MethodGet, "/api/v1/agent/runs", "")
	body = readAll(t, res)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "run_a") || !strings.Contains(body, "run_b") {
		t.Fatalf("admin runs %d %s", res.StatusCode, body)
	}
	res = admin(http.MethodGet, "/api/v1/factory", "")
	body = readAll(t, res)
	if !strings.Contains(body, "fac_a") || !strings.Contains(body, "fac_b") {
		t.Fatalf("admin factories %s", body)
	}
}

func scopeDo(t *testing.T, srv *httptest.Server, key, method, path, body string) *http.Response {
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
	return res
}

func assertStatus(t *testing.T, res *http.Response, status int) {
	t.Helper()
	defer res.Body.Close()
	if res.StatusCode != status {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, raw)
	}
}

func assertEmptyStatus(t *testing.T, res *http.Response, status int, absent string) {
	t.Helper()
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != status || len(bytes.TrimSpace(raw)) != 0 || (absent != "" && strings.Contains(string(raw), absent)) {
		t.Fatalf("status %d body %q", res.StatusCode, raw)
	}
}

func readAll(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
