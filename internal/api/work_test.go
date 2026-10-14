package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCreateTaskDoesNotStartRun(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)
	body := `{"title":"  Ship the gate  ","body":"do the work","source":" issue ","source_ref":" polds/factory#1 ","created_by":"mallory","token":"supersecretvalue","secret":"shh","env":{"GH_TOKEN":"shh"}}`
	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", body, apiHeaders())
	task := decode[Task](t, res, http.StatusCreated)
	if task.ID != "task_1" || task.FactoryID != "fac_1" || task.Title != "Ship the gate" {
		t.Fatalf("%+v", task)
	}
	if task.Source != TaskSourceIssue || task.SourceRef != "polds/factory#1" || task.Body != "do the work" {
		t.Fatalf("%+v", task)
	}
	if task.Stage != TaskStageTriage || task.Status != TaskStatusOpen || task.RevisionCount != 0 {
		t.Fatalf("%+v", task)
	}
	if task.CreatedBy != APIKeyPrincipal || !task.CreatedAt.Equal(w.now) {
		t.Fatalf("%+v", task)
	}
	if !bytes.Contains(res.body, []byte(`"revision_count":0`)) {
		t.Fatalf("revision_count omitted: %s", res.body)
	}
	assertNoPlanted(t, res.body, "supersecretvalue", "mallory", "shh", "GH_TOKEN")
	if w.startCalls != 0 || w.lastStart != (RunRequest{}) {
		t.Fatalf("bare run started: calls %d req %+v", w.startCalls, w.lastStart)
	}
	if w.creates != 1 {
		t.Fatalf("creates %d", w.creates)
	}
	want := Effect{Op: EffectStartRun, Stage: TaskStageTriage}
	if len(w.effects) != 1 || w.effects[0] != want {
		t.Fatalf("effects %+v", w.effects)
	}

	createdBody := append([]byte(nil), res.body...)
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/tasks/task_1", "", apiHeaders())
	got := decode[Task](t, res, http.StatusOK)
	if got.ID != task.ID || got.Title != task.Title || got.Source != task.Source || !got.CreatedAt.Equal(task.CreatedAt) {
		t.Fatalf("get %+v create %+v", got, task)
	}
	if !bytes.Equal(res.body, createdBody) {
		t.Fatalf("get %s create %s", res.body, createdBody)
	}
}

func TestCreateTaskDefaultsAndValidation(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)

	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", `{"title":"manual intake","source":"manual"}`, apiHeaders())
	task := decode[Task](t, res, http.StatusCreated)
	if task.Source != TaskSourceManual || task.Stage != TaskStageTriage {
		t.Fatalf("%+v", task)
	}

	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", `{"title":"from the api"}`, apiHeaders())
	task = decode[Task](t, res, http.StatusCreated)
	if task.Source != TaskSourceAPI || task.ID != "task_2" {
		t.Fatalf("%+v", task)
	}

	cases := []struct {
		name string
		body string
	}{
		{name: "empty title", body: `{"title":""}`},
		{name: "blank title", body: `{"title":"  "}`},
		{name: "missing title", body: `{"source":"api"}`},
		{name: "bad source", body: `{"title":"t","source":"supersecretvalue"}`},
		{name: "invalid json", body: `{`},
		{name: "empty body", body: ``},
	}
	before := w.creates
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", tc.body, apiHeaders())
			if res.status != http.StatusBadRequest {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
			assertNoPlanted(t, res.body, "supersecretvalue")
		})
	}
	if w.creates != before || w.startCalls != 0 {
		t.Fatalf("creates %d start %d", w.creates, w.startCalls)
	}
}

func TestListAndFilterTasks(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)
	w.factories = append(w.factories, Factory{ID: "fac_2", Name: "other"})
	w.seedTask("fac_2", "task_other", TaskStatusOpen, TaskStageTriage, TaskSourceAPI)
	w.seedTask("fac_1", "task_build", TaskStatusOpen, TaskStageBuilding, TaskSourceManual)

	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", `{"title":"issue","source":"issue"}`, apiHeaders())
	created := decode[Task](t, res, http.StatusCreated)

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/tasks", "", apiHeaders())
	all := decode[[]Task](t, res, http.StatusOK)
	if len(all) != 2 || all[0].ID != "task_build" || all[1].ID != created.ID {
		t.Fatalf("%+v", all)
	}
	assertNoSecretKeys(t, res.body)

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/tasks?stage=building&status=open&source=manual", "", apiHeaders())
	filtered := decode[[]Task](t, res, http.StatusOK)
	if len(filtered) != 1 || filtered[0].ID != "task_build" {
		t.Fatalf("%+v", filtered)
	}
	if w.lastTaskFilter != (TaskFilter{Stage: TaskStageBuilding, Status: TaskStatusOpen, Source: TaskSourceManual}) {
		t.Fatalf("filter %+v", w.lastTaskFilter)
	}
	if w.lastFactory != "fac_1" {
		t.Fatalf("factory %q", w.lastFactory)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/tasks?stage=reviewing", "", apiHeaders())
	none := decode[[]Task](t, res, http.StatusOK)
	if len(none) != 0 || bytes.Contains(res.body, []byte("null")) {
		t.Fatalf("body %s", res.body)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/missing/tasks", "", apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/tasks/task_other", "", apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	assertNoPlanted(t, res.body, "task_other")
}

func TestCancelTask(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)
	open := w.seedTask("fac_1", "task_open", TaskStatusOpen, TaskStagePlanning, TaskSourceAPI)
	waiting := w.seedTask("fac_1", "task_wait", TaskStatusAwaitingApproval, TaskStagePlanning, TaskSourceAPI)
	w.seedTask("fac_1", "task_done", TaskStatusComplete, TaskStageReviewing, TaskSourceAPI)
	w.seedTask("fac_1", "task_stop", TaskStatusCancelled, TaskStageTriage, TaskSourceAPI)

	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks/task_open/cancel", "", apiHeaders())
	cancelled := decode[Task](t, res, http.StatusOK)
	if cancelled.Status != TaskStatusCancelled || cancelled.Stage != open.Stage || cancelled.ID != open.ID {
		t.Fatalf("%+v", cancelled)
	}
	assertNoSecretKeys(t, res.body)

	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks/task_wait/cancel", "", apiHeaders())
	cancelled = decode[Task](t, res, http.StatusOK)
	if cancelled.Status != TaskStatusCancelled || cancelled.ID != waiting.ID {
		t.Fatalf("%+v", cancelled)
	}

	for _, id := range []string{"task_done", "task_stop", "task_open"} {
		res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks/"+id+"/cancel", "", apiHeaders())
		if res.status != http.StatusConflict {
			t.Fatalf("%s status %d body %s", id, res.status, res.body)
		}
		assertProblem(t, res)
		if !bytes.Contains(res.body, []byte("work item is terminal")) {
			t.Fatalf("detail %s", res.body)
		}
	}
	if w.tasks["task_done"].Status != TaskStatusComplete || w.tasks["task_stop"].Status != TaskStatusCancelled {
		t.Fatalf("terminal status changed: %+v %+v", w.tasks["task_done"], w.tasks["task_stop"])
	}
	if w.startCalls != 0 {
		t.Fatalf("start calls %d", w.startCalls)
	}

	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks/missing/cancel", "", apiHeaders())
	if res.status != http.StatusNotFound {
		t.Fatalf("status %d", res.status)
	}
	assertProblem(t, res)
}

func TestApprovalsDecide(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)
	w.factories = append(w.factories, Factory{ID: "fac_2", Name: "other"})
	w.seedTask("fac_1", "task_1", TaskStatusAwaitingApproval, TaskStagePlanning, TaskSourceIssue)
	w.seedTask("fac_2", "task_2", TaskStatusAwaitingApproval, TaskStagePlanning, TaskSourceIssue)
	w.seedApproval("task_1", "apr_1", "spec")
	w.seedApproval("task_2", "apr_2", "spec")
	w.seedApproval("task_1", "apr_3", "handoff")

	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/approvals", "", apiHeaders())
	pending := decode[[]Approval](t, res, http.StatusOK)
	if len(pending) != 2 || pending[0].ID != "apr_1" || pending[1].ID != "apr_3" {
		t.Fatalf("%+v", pending)
	}
	if pending[0].Decision != "" || pending[0].DecidedBy != "" || pending[0].Gate != "spec" || pending[0].WorkItemID != "task_1" {
		t.Fatalf("%+v", pending[0])
	}
	assertNoSecretKeys(t, res.body)
	if bytes.Contains(res.body, []byte("decided_at")) || bytes.Contains(res.body, []byte("apr_2")) {
		t.Fatalf("inbox leaked: %s", res.body)
	}

	res = call(t, h, http.MethodPost, "/api/v1/approvals/apr_1:decide", `{"decision":" approve ","note":" lgtm ","decided_by":"mallory","token":"supersecretvalue"}`, apiHeaders())
	decided := decode[Approval](t, res, http.StatusOK)
	if decided.Decision != DecisionApprove || decided.Note != "lgtm" || decided.DecidedBy != APIKeyPrincipal {
		t.Fatalf("%+v", decided)
	}
	if decided.DecidedAt == nil || !decided.DecidedAt.Equal(w.now) {
		t.Fatalf("%+v", decided)
	}
	assertNoPlanted(t, res.body, "supersecretvalue", "mallory")

	res = call(t, h, http.MethodPost, "/api/v1/approvals/apr_1:decide", `{"decision":"approve","note":"changed"}`, apiHeaders())
	again := decode[Approval](t, res, http.StatusOK)
	if !sameApproval(again, decided) {
		t.Fatalf("idempotent %+v want %+v", again, decided)
	}

	res = call(t, h, http.MethodPost, "/api/v1/approvals/apr_1:decide", `{"decision":"reject","note":"no"}`, apiHeaders())
	if res.status != http.StatusConflict {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	if !bytes.Contains(res.body, []byte("approval is decided")) {
		t.Fatalf("detail %s", res.body)
	}
	if w.approvals["apr_1"].Decision != DecisionApprove || w.approvals["apr_1"].Note != "lgtm" {
		t.Fatalf("decision changed: %+v", w.approvals["apr_1"])
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/approvals", "", apiHeaders())
	pending = decode[[]Approval](t, res, http.StatusOK)
	if len(pending) != 1 || pending[0].ID != "apr_3" {
		t.Fatalf("inbox %+v", pending)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/approvals?pending=false", "", apiHeaders())
	all := decode[[]Approval](t, res, http.StatusOK)
	if len(all) != 2 || all[0].ID != "apr_1" || all[0].Decision != DecisionApprove || all[1].ID != "apr_3" {
		t.Fatalf("%+v", all)
	}

	res = call(t, h, http.MethodPost, "/api/v1/approvals/apr_3:decide", `{"decision":"reject","note":"needs a spec"}`, apiHeaders())
	rejected := decode[Approval](t, res, http.StatusOK)
	if rejected.Decision != DecisionReject || rejected.Note != "needs a spec" || rejected.DecidedBy != APIKeyPrincipal {
		t.Fatalf("%+v", rejected)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/approvals?pending=true", "", apiHeaders())
	none := decode[[]Approval](t, res, http.StatusOK)
	if len(none) != 0 || bytes.Contains(res.body, []byte("null")) {
		t.Fatalf("body %s", res.body)
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/approvals?pending=0", "", apiHeaders())
	history := decode[[]Approval](t, res, http.StatusOK)
	if len(history) != 2 || history[0].Decision != DecisionApprove || history[1].Decision != DecisionReject {
		t.Fatalf("%+v", history)
	}
	if w.startCalls != 0 {
		t.Fatalf("start calls %d", w.startCalls)
	}
}

func TestApprovalValidation(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)
	w.seedTask("fac_1", "task_1", TaskStatusAwaitingApproval, TaskStagePlanning, TaskSourceAPI)
	w.seedApproval("task_1", "apr_1", "spec")

	cases := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{name: "bad decision", path: "/api/v1/approvals/apr_1:decide", body: `{"decision":"supersecretvalue"}`, status: http.StatusBadRequest},
		{name: "empty decision", path: "/api/v1/approvals/apr_1:decide", body: `{"decision":"  "}`, status: http.StatusBadRequest},
		{name: "missing decision", path: "/api/v1/approvals/apr_1:decide", body: `{}`, status: http.StatusBadRequest},
		{name: "invalid json", path: "/api/v1/approvals/apr_1:decide", body: `{`, status: http.StatusBadRequest},
		{name: "empty body", path: "/api/v1/approvals/apr_1:decide", body: ``, status: http.StatusBadRequest},
		{name: "unknown approval", path: "/api/v1/approvals/apr_missing:decide", body: `{"decision":"approve"}`, status: http.StatusNotFound},
		{name: "missing suffix", path: "/api/v1/approvals/apr_1", body: `{"decision":"approve"}`, status: http.StatusNotFound},
		{name: "suffix only", path: "/api/v1/approvals/:decide", body: `{"decision":"approve"}`, status: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := w.decides
			res := call(t, h, http.MethodPost, tc.path, tc.body, apiHeaders())
			if res.status != tc.status {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
			assertNoPlanted(t, res.body, "supersecretvalue")
			if tc.status == http.StatusBadRequest && w.decides != before {
				t.Fatalf("backend called for %s", tc.name)
			}
		})
	}
	if w.approvals["apr_1"].Decision != "" {
		t.Fatalf("approval mutated: %+v", w.approvals["apr_1"])
	}

	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/approvals?pending=nope", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	if w.approvalLists != 0 {
		t.Fatalf("list calls %d", w.approvalLists)
	}
}

func TestWorkAuthDoesNotCallBackend(t *testing.T) {
	w := newWork()
	h := NewHandler(w, testAPIKey, nil)
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/factory/fac_1/tasks"},
		{http.MethodGet, "/api/v1/factory/fac_1/tasks"},
		{http.MethodGet, "/api/v1/factory/fac_1/tasks/task_1"},
		{http.MethodPost, "/api/v1/factory/fac_1/tasks/task_1/cancel"},
		{http.MethodGet, "/api/v1/factory/fac_1/approvals"},
		{http.MethodPost, "/api/v1/approvals/apr_1:decide"},
	}
	for _, tc := range paths {
		res := call(t, h, tc.method, tc.path, `{"title":"t","decision":"approve","token":"supersecretvalue"}`, nil)
		if res.status != http.StatusUnauthorized {
			t.Fatalf("%s %s status %d body %s", tc.method, tc.path, res.status, res.body)
		}
		assertProblem(t, res)
		assertNoPlanted(t, res.body, testAPIKey, "supersecretvalue")
	}
	if w.creates != 0 || w.decides != 0 || w.cancels != 0 || w.taskLists != 0 || w.approvalLists != 0 || w.startCalls != 0 {
		t.Fatalf("backend called: %+v", w)
	}
}

func TestWorkRequiresWorkBackend(t *testing.T) {
	h := NewHandler(newFake(), testAPIKey, nil)
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/factory/fac_1/tasks"},
		{http.MethodPost, "/api/v1/factory/fac_1/tasks"},
		{http.MethodGet, "/api/v1/factory/fac_1/tasks/task_1"},
		{http.MethodPost, "/api/v1/factory/fac_1/tasks/task_1/cancel"},
		{http.MethodGet, "/api/v1/factory/fac_1/approvals"},
		{http.MethodPost, "/api/v1/approvals/apr_1:decide"},
	}
	for _, tc := range paths {
		res := call(t, h, tc.method, tc.path, `{"title":"t","decision":"approve"}`, apiHeaders())
		if res.status != http.StatusNotFound {
			t.Fatalf("%s %s status %d body %s", tc.method, tc.path, res.status, res.body)
		}
		assertProblem(t, res)
	}
}

func TestWorkProblemAndLimits(t *testing.T) {
	w := newWork()
	w.force = errors.New("postgres token supersecretvalue")
	h := NewHandler(w, testAPIKey, nil)
	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", `{"title":"t"}`, apiHeaders())
	if res.status != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	assertNoPlanted(t, res.body, "supersecretvalue", "postgres")
	if len(w.effects) != 0 {
		t.Fatalf("effect queued on failure: %+v", w.effects)
	}

	w.force = nil
	before := w.creates
	over := bytes.Repeat([]byte("a"), (1<<20)+1)
	res = callBytes(t, h, http.MethodPost, "/api/v1/factory/fac_1/tasks", over, apiHeaders())
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", res.status)
	}
	assertProblem(t, res)
	if w.creates != before {
		t.Fatalf("oversized create reached backend")
	}

	res = call(t, h, http.MethodPut, "/api/v1/factory/fac_1/tasks", "", apiHeaders())
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	if !bytes.Contains([]byte(res.header.Get("Allow")), []byte(http.MethodGet)) || !bytes.Contains([]byte(res.header.Get("Allow")), []byte(http.MethodPost)) {
		t.Fatalf("Allow %q", res.header.Get("Allow"))
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/tasks/task_1/cancel", "", apiHeaders())
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	if res.header.Get("Allow") != http.MethodPost {
		t.Fatalf("Allow %q", res.header.Get("Allow"))
	}
}

type workBackend struct {
	*fakeBackend
	now            time.Time
	tasks          map[string]Task
	order          []string
	approvals      map[string]Approval
	aprOrder       []string
	effects        []Effect
	next           int
	creates        int
	cancels        int
	decides        int
	taskLists      int
	approvalLists  int
	lastTaskFilter TaskFilter
	lastFactory    string
}

func newWork() *workBackend {
	return &workBackend{
		fakeBackend: newFake(),
		now:         time.Date(2026, 10, 14, 15, 4, 5, 0, time.UTC),
		tasks:       map[string]Task{},
		approvals:   map[string]Approval{},
	}
}

func (w *workBackend) seedTask(factoryID, id, status, stage, source string) Task {
	task := Task{
		ID:        id,
		FactoryID: factoryID,
		Source:    source,
		Title:     "seeded",
		Stage:     stage,
		Status:    status,
		CreatedBy: APIKeyPrincipal,
		CreatedAt: w.now,
	}
	w.tasks[id] = task
	w.order = append(w.order, id)
	return task
}

func (w *workBackend) seedApproval(taskID, id, gate string) Approval {
	apr := Approval{
		ID:          id,
		WorkItemID:  taskID,
		Gate:        gate,
		RequestedAt: w.now,
	}
	w.approvals[id] = apr
	w.aprOrder = append(w.aprOrder, id)
	return apr
}

func (w *workBackend) CreateTask(ctx context.Context, factoryID string, req TaskRequest) (Task, error) {
	w.creates++
	w.lastFactory = factoryID
	if err := w.blocked(ctx); err != nil {
		return Task{}, err
	}
	if !w.knownFactory(factoryID) {
		return Task{}, ErrNotFound
	}
	w.next++
	task := Task{
		ID:            fmt.Sprintf("task_%d", w.next),
		FactoryID:     factoryID,
		Source:        req.Source,
		SourceRef:     req.SourceRef,
		Title:         req.Title,
		Body:          req.Body,
		Stage:         TaskStageTriage,
		Status:        TaskStatusOpen,
		CreatedBy:     req.CreatedBy,
		RevisionCount: 0,
		CreatedAt:     w.now,
	}
	w.tasks[task.ID] = task
	w.order = append(w.order, task.ID)
	w.effects = append(w.effects, Effect{Op: EffectStartRun, Stage: TaskStageTriage})
	return task, nil
}

func (w *workBackend) GetTask(ctx context.Context, factoryID, id string) (Task, error) {
	if err := w.blocked(ctx); err != nil {
		return Task{}, err
	}
	task, ok := w.tasks[id]
	if !ok || task.FactoryID != factoryID {
		return Task{}, ErrNotFound
	}
	return task, nil
}

func (w *workBackend) ListTasks(ctx context.Context, factoryID string, f TaskFilter) ([]Task, error) {
	w.taskLists++
	w.lastFactory = factoryID
	w.lastTaskFilter = f
	if err := w.blocked(ctx); err != nil {
		return nil, err
	}
	if !w.knownFactory(factoryID) {
		return nil, ErrNotFound
	}
	var out []Task
	for _, id := range w.order {
		task := w.tasks[id]
		if task.FactoryID != factoryID {
			continue
		}
		if f.Stage != "" && task.Stage != f.Stage {
			continue
		}
		if f.Status != "" && task.Status != f.Status {
			continue
		}
		if f.Source != "" && task.Source != f.Source {
			continue
		}
		out = append(out, task)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (w *workBackend) CancelTask(ctx context.Context, factoryID, id string) (Task, error) {
	w.cancels++
	if err := w.blocked(ctx); err != nil {
		return Task{}, err
	}
	task, ok := w.tasks[id]
	if !ok || task.FactoryID != factoryID {
		return Task{}, ErrNotFound
	}
	if terminalTask(task.Status) {
		return Task{}, ErrConflict
	}
	task.Status = TaskStatusCancelled
	w.tasks[id] = task
	return task, nil
}

func (w *workBackend) ListApprovals(ctx context.Context, factoryID string, pendingOnly bool) ([]Approval, error) {
	w.approvalLists++
	if err := w.blocked(ctx); err != nil {
		return nil, err
	}
	if !w.knownFactory(factoryID) {
		return nil, ErrNotFound
	}
	var out []Approval
	for _, id := range w.aprOrder {
		apr := w.approvals[id]
		task, ok := w.tasks[apr.WorkItemID]
		if !ok || task.FactoryID != factoryID {
			continue
		}
		if pendingOnly && apr.Decision != "" {
			continue
		}
		out = append(out, apr)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (w *workBackend) DecideApproval(ctx context.Context, id, decision, decidedBy, note string) (Approval, error) {
	w.decides++
	if err := w.blocked(ctx); err != nil {
		return Approval{}, err
	}
	apr, ok := w.approvals[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if apr.Decision != "" {
		if apr.Decision == decision {
			return apr, nil
		}
		return Approval{}, ErrConflict
	}
	now := w.now
	apr.Decision = decision
	apr.DecidedBy = decidedBy
	apr.Note = note
	apr.DecidedAt = &now
	w.approvals[id] = apr
	return apr, nil
}

func (w *workBackend) knownFactory(id string) bool {
	for _, fac := range w.factories {
		if fac.ID == id {
			return true
		}
	}
	return false
}

func terminalTask(status string) bool {
	return status == TaskStatusComplete || status == TaskStatusCancelled
}

func sameApproval(a, b Approval) bool {
	if a.ID != b.ID || a.WorkItemID != b.WorkItemID || a.Gate != b.Gate || a.Decision != b.Decision || a.Note != b.Note || a.DecidedBy != b.DecidedBy {
		return false
	}
	if !a.RequestedAt.Equal(b.RequestedAt) {
		return false
	}
	if (a.DecidedAt == nil) != (b.DecidedAt == nil) {
		return false
	}
	if a.DecidedAt != nil && !a.DecidedAt.Equal(*b.DecidedAt) {
		return false
	}
	return true
}

func assertNoPlanted(t *testing.T, body []byte, planted ...string) {
	t.Helper()
	assertNoSecretKeys(t, body)
	for _, s := range planted {
		if s != "" && bytes.Contains(body, []byte(s)) {
			t.Fatalf("response contains %q: %s", s, body)
		}
	}
}
