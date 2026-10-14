package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const (
	testAPIKey   = "factory-api-key-test"
	testRunToken = "run-token-test-value"
)

func TestHappyPaths(t *testing.T) {
	f := newFake()
	events := &eventsRec{}
	h := NewHandler(f, testAPIKey, events)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		header map[string]string
		status int
	}{
		{name: "list factories", method: http.MethodGet, path: "/api/v1/factory", status: http.StatusOK},
		{name: "get factory", method: http.MethodGet, path: "/api/v1/factory/fac_1", status: http.StatusOK},
		{name: "validate", method: http.MethodPost, path: "/api/v1/factory/fac_1/definition:validate", body: "tarball-bytes", status: http.StatusOK},
		{name: "apply", method: http.MethodPost, path: "/api/v1/factory/fac_1/definition:apply", status: http.StatusOK},
		{name: "agents", method: http.MethodGet, path: "/api/v1/factory/fac_1/agents", status: http.StatusOK},
		{name: "factory runs", method: http.MethodGet, path: "/api/v1/factory/fac_1/runs", status: http.StatusOK},
		{name: "start run", method: http.MethodPost, path: "/api/v1/agent/run", body: `{"factory":"acme","agent":"implementer","prompt":"fix the docs","title":"docs","priority":"interactive","repo":"polds/factory"}`, status: http.StatusCreated},
		{name: "list runs", method: http.MethodGet, path: "/api/v1/agent/runs?agent=implementer&status=running&worker=w1&model=m1", status: http.StatusOK},
		{name: "get run", method: http.MethodGet, path: "/api/v1/agent/runs/run_1", status: http.StatusOK},
		{name: "cancel", method: http.MethodPost, path: "/api/v1/agent/runs/run_1/cancel", status: http.StatusOK},
		{name: "events", method: http.MethodGet, path: "/api/v1/agent/runs/run_1/events?from_seq=3", status: http.StatusOK},
		{name: "healthz", method: http.MethodGet, path: "/healthz", status: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hdr := apiHeaders()
			if tc.path == "/healthz" {
				hdr = nil
			}
			res := call(t, h, tc.method, tc.path, tc.body, hdr)
			body := res.body
			if res.status != tc.status {
				t.Fatalf("status %d, want %d, body %s", res.status, tc.status, body)
			}
			switch tc.name {
			case "events":
				if res.header.Get("Content-Type") != "text/event-stream" {
					t.Fatalf("content-type %q", res.header.Get("Content-Type"))
				}
			default:
				if res.header.Get("Content-Type") != "application/json" {
					t.Fatalf("content-type %q", res.header.Get("Content-Type"))
				}
				assertNoSecretKeys(t, body)
			}
		})
	}

	if f.validateBody == nil || string(f.validateBody) != "tarball-bytes" {
		t.Fatalf("validate body %q", f.validateBody)
	}
	if f.validateID != "fac_1" {
		t.Fatalf("validate factory %q", f.validateID)
	}
	if f.applyID != "fac_1" {
		t.Fatalf("apply factory %q", f.applyID)
	}
	if f.lastStart.Factory != "acme" || f.lastStart.Agent != "implementer" || f.lastStart.Prompt != "fix the docs" || f.lastStart.Title != "docs" || f.lastStart.Priority != "interactive" || f.lastStart.Repo != "polds/factory" {
		t.Fatalf("start request %+v", f.lastStart)
	}
	if f.lastFilter.FactoryID != "fac_1" {
		t.Fatalf("factory runs filter %+v", f.lastFilter)
	}
	if events.called != 1 || events.runID != "run_1" || events.query != "from_seq=3" || events.path != "/api/v1/agent/runs/run_1/events" {
		t.Fatalf("events %+v", events)
	}
}

func TestListPayloads(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)

	res := call(t, h, http.MethodGet, "/api/v1/factory", "", apiHeaders())
	factories := decode[[]Factory](t, res, http.StatusOK)
	if len(factories) != 1 || factories[0].ID != "fac_1" || factories[0].Name != "acme" {
		t.Fatalf("%+v", factories)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/agents", "", apiHeaders())
	agents := decode[[]Agent](t, res, http.StatusOK)
	if len(agents) != 1 || agents[0].Name != "implementer" || agents[0].FactoryID != "fac_1" {
		t.Fatalf("%+v", agents)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/definition:validate", "", apiHeaders())
	body := res.body
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)
}

func TestValidateAndApplyPayloads(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)

	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/definition:validate", "blob", apiHeaders())
	got := decode[ValidateResponse](t, res, http.StatusOK)
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].File != "factory.yaml" || got.Diagnostics[0].Line != 3 {
		t.Fatalf("%+v", got)
	}

	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/definition:apply", "", apiHeaders())
	applied := decode[ApplyResponse](t, res, http.StatusOK)
	if applied.ID != "def_1" || len(applied.Diagnostics) != 1 || applied.Diagnostics[0].Message != "not applied in M2" {
		t.Fatalf("%+v", applied)
	}
}

func TestRunPayloads(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)

	res := call(t, h, http.MethodPost, "/api/v1/agent/run", `{"factory":"acme","prompt":"ship it"}`, apiHeaders())
	created := decode[Run](t, res, http.StatusCreated)
	if created.ID != "run_new" || created.Status != "queued" {
		t.Fatalf("%+v", created)
	}

	res = call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1", "", apiHeaders())
	got := decode[Run](t, res, http.StatusOK)
	if got.ID != "run_1" || got.Status != "running" {
		t.Fatalf("%+v", got)
	}

	res = call(t, h, http.MethodGet, "/api/v1/agent/runs?agent=implementer&status=running&worker=w1&model=m1", "", apiHeaders())
	_ = decode[[]Run](t, res, http.StatusOK)
	if f.queryFilter.Agent != "implementer" || f.queryFilter.Status != "running" || f.queryFilter.Worker != "w1" || f.queryFilter.Model != "m1" || f.queryFilter.FactoryID != "" {
		t.Fatalf("filter %+v", f.queryFilter)
	}

	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/cancel", "", apiHeaders())
	cancelled := decode[Run](t, res, http.StatusOK)
	if cancelled.ID != "run_1" || cancelled.Status != "cancelled" {
		t.Fatalf("%+v", cancelled)
	}
}

func TestAuthRejected(t *testing.T) {
	f := newFake()
	events := &eventsRec{}
	h := NewHandler(f, testAPIKey, events)
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/factory"},
		{http.MethodGet, "/api/v1/factory/fac_1"},
		{http.MethodPost, "/api/v1/factory/fac_1/definition:validate"},
		{http.MethodPost, "/api/v1/factory/fac_1/definition:apply"},
		{http.MethodGet, "/api/v1/factory/fac_1/agents"},
		{http.MethodGet, "/api/v1/factory/fac_1/runs"},
		{http.MethodPost, "/api/v1/agent/run"},
		{http.MethodGet, "/api/v1/agent/runs"},
		{http.MethodGet, "/api/v1/agent/runs/run_1"},
		{http.MethodPost, "/api/v1/agent/runs/run_1/cancel"},
		{http.MethodGet, "/api/v1/agent/runs/run_1/events"},
	}
	for _, tc := range paths {
		for _, hdr := range []map[string]string{
			nil,
			{"Authorization": "Bearer wrong-key-value"},
			{"Authorization": "Basic " + testAPIKey},
			{"Authorization": testAPIKey},
		} {
			res := call(t, h, tc.method, tc.path, `{"factory":"acme","prompt":"x"}`, hdr)
			body := res.body
			if res.status != http.StatusUnauthorized {
				t.Fatalf("%s %s status %d body %s", tc.method, tc.path, res.status, body)
			}
			assertProblem(t, res)
			if bytes.Contains(body, []byte(testAPIKey)) || bytes.Contains(body, []byte("wrong-key-value")) {
				t.Fatalf("auth response leaked credential: %s", body)
			}
		}
	}
	if f.startCalls != 0 || f.validateCalls != 0 || events.called != 0 {
		t.Fatalf("backend called without auth: start %d validate %d events %d", f.startCalls, f.validateCalls, events.called)
	}
}

func TestIngestRunToken(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)
	body := "{\"n\":1}\n\n{\"n\":2}\n"
	hdr := map[string]string{
		"Authorization":         "Bearer " + testRunToken,
		"X-Factory-Event-Index": "4",
	}
	res := call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/events:ingest", body, hdr)
	raw := res.body
	if res.status != http.StatusNoContent {
		t.Fatalf("status %d body %s", res.status, raw)
	}
	if len(raw) != 0 {
		t.Fatalf("ingest body %q", raw)
	}
	if len(f.ingests) != 1 {
		t.Fatalf("ingests %d", len(f.ingests))
	}
	got := f.ingests[0]
	if got.runID != "run_1" || got.bearer != testRunToken || got.startIdx != 4 {
		t.Fatalf("ingest %+v", got)
	}
	if len(got.lines) != 2 || string(got.lines[0]) != `{"n":1}` || string(got.lines[1]) != `{"n":2}` {
		t.Fatalf("lines %#v", got.lines)
	}
	if bytes.Contains(raw, []byte(testRunToken)) {
		t.Fatalf("response contains run token")
	}
}

func TestIngestRejectsBadRequest(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)

	cases := []struct {
		name   string
		header map[string]string
		status int
	}{
		{name: "missing bearer", header: map[string]string{"X-Factory-Event-Index": "0"}, status: http.StatusUnauthorized},
		{name: "missing index", header: map[string]string{"Authorization": "Bearer " + testRunToken}, status: http.StatusBadRequest},
		{name: "bad index", header: map[string]string{"Authorization": "Bearer " + testRunToken, "X-Factory-Event-Index": "nope"}, status: http.StatusBadRequest},
		{name: "negative index", header: map[string]string{"Authorization": "Bearer " + testRunToken, "X-Factory-Event-Index": "-1"}, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/events:ingest", "{}\n", tc.header)
			body := res.body
			if res.status != tc.status {
				t.Fatalf("status %d body %s", res.status, body)
			}
			assertProblem(t, res)
			if bytes.Contains(body, []byte(testRunToken)) {
				t.Fatalf("leaked token: %s", body)
			}
		})
	}
	if len(f.ingests) != 0 {
		t.Fatalf("backend ingest calls %d", len(f.ingests))
	}
}

func TestIngestBackendUnauthorized(t *testing.T) {
	f := newFake()
	f.ingestErr = ErrUnauthorized
	h := NewHandler(f, testAPIKey, nil)
	hdr := map[string]string{
		"Authorization":         "Bearer " + testRunToken,
		"X-Factory-Event-Index": "0",
	}
	res := call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/events:ingest", "{}\n", hdr)
	body := res.body
	if res.status != http.StatusUnauthorized {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)
	if bytes.Contains(body, []byte(testRunToken)) {
		t.Fatalf("leaked token: %s", body)
	}
}

func TestStartRunValidation(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)
	cases := []struct {
		name string
		body string
	}{
		{name: "empty prompt", body: `{"factory":"acme","prompt":""}`},
		{name: "blank prompt", body: `{"factory":"acme","prompt":"  "}`},
		{name: "missing prompt", body: `{"factory":"acme"}`},
		{name: "missing factory", body: `{"prompt":"hi"}`},
		{name: "invalid json", body: `{`},
		{name: "empty body", body: ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, http.MethodPost, "/api/v1/agent/run", tc.body, apiHeaders())
			body := res.body
			if res.status != http.StatusBadRequest {
				t.Fatalf("status %d body %s", res.status, body)
			}
			assertProblem(t, res)
		})
	}
	if f.startCalls != 0 {
		t.Fatalf("start calls %d", f.startCalls)
	}
}

func TestUnknownRunAndFactory(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/agent/runs/missing"},
		{http.MethodPost, "/api/v1/agent/runs/missing/cancel"},
		{http.MethodGet, "/api/v1/factory/missing"},
		{http.MethodGet, "/api/v1/factory/missing/agents"},
	}
	for _, tc := range paths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := call(t, h, tc.method, tc.path, "", apiHeaders())
			body := res.body
			if res.status != http.StatusNotFound {
				t.Fatalf("status %d body %s", res.status, body)
			}
			assertProblem(t, res)
		})
	}
}

func TestCancelTerminalRun(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)
	res := call(t, h, http.MethodPost, "/api/v1/agent/runs/run_done/cancel", "", apiHeaders())
	body := res.body
	if res.status != http.StatusConflict {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)
	if f.runs["run_done"].Status != "succeeded" {
		t.Fatalf("status changed to %s", f.runs["run_done"].Status)
	}
}

func TestBodyLimits(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)

	ok := sizedRunJSON(1 << 20)
	res := callBytes(t, h, http.MethodPost, "/api/v1/agent/run", ok, apiHeaders())
	raw := res.body
	if res.status != http.StatusCreated {
		t.Fatalf("1 MiB status %d body %s", res.status, raw)
	}

	beforeStart := f.startCalls
	over := sizedRunJSON((1 << 20) + 1)
	res = callBytes(t, h, http.MethodPost, "/api/v1/agent/run", over, apiHeaders())
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MiB+1 status %d", res.status)
	}
	assertProblem(t, res)
	if f.startCalls != beforeStart {
		t.Fatal("oversized run reached backend")
	}

	beforeValidate := f.validateCalls
	res = callBytes(t, h, http.MethodPost, "/api/v1/factory/fac_1/definition:validate", bytes.Repeat([]byte("z"), (1<<20)+1), apiHeaders())
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("validate over limit status %d", res.status)
	}
	assertProblem(t, res)
	if f.validateCalls != beforeValidate {
		t.Fatal("oversized definition reached backend")
	}

	ingestHdr := map[string]string{
		"Authorization":         "Bearer " + testRunToken,
		"X-Factory-Event-Index": "0",
	}
	res = callBytes(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/events:ingest", bytes.Repeat([]byte("a"), 2<<20), ingestHdr)
	raw = res.body
	if res.status != http.StatusNoContent {
		t.Fatalf("2 MiB ingest status %d body %s", res.status, raw)
	}

	res = callBytes(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/events:ingest", bytes.Repeat([]byte("b"), 4<<20), ingestHdr)
	raw = res.body
	if res.status != http.StatusNoContent {
		t.Fatalf("4 MiB ingest status %d body %s", res.status, raw)
	}

	beforeIngest := len(f.ingests)
	res = callBytes(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/events:ingest", bytes.Repeat([]byte("c"), (4<<20)+1), ingestHdr)
	if res.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("4 MiB+1 ingest status %d", res.status)
	}
	assertProblem(t, res)
	if len(f.ingests) != beforeIngest {
		t.Fatal("oversized ingest reached backend")
	}
}

func TestResponseStructsExcludeSecretNames(t *testing.T) {
	types := []any{
		Factory{},
		Agent{},
		Diagnostic{},
		DefinitionRef{},
		Run{},
		ValidateResponse{},
		ApplyResponse{},
		Problem{},
		Health{},
	}
	seen := map[reflect.Type]bool{}
	for _, v := range types {
		walkType(t, reflect.TypeOf(v), seen)
	}
}

func TestInternalErrorDoesNotLeak(t *testing.T) {
	f := newFake()
	f.force = errors.New("postgres token supersecretvalue")
	h := NewHandler(f, testAPIKey, nil)
	res := call(t, h, http.MethodGet, "/api/v1/factory", "", apiHeaders())
	body := res.body
	if res.status != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)
	if bytes.Contains(bytes.ToLower(body), []byte("supersecretvalue")) || bytes.Contains(bytes.ToLower(body), []byte("postgres")) {
		t.Fatalf("leaked backend error: %s", body)
	}
}

func TestHealthzIgnoresAuth(t *testing.T) {
	h := NewHandler(newFake(), testAPIKey, nil)
	for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong-key-value"}} {
		res := call(t, h, http.MethodGet, "/healthz", "", hdr)
		got := decode[Health](t, res, http.StatusOK)
		if got.Status != "ok" {
			t.Fatalf("%+v", got)
		}
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := NewHandler(newFake(), testAPIKey, nil)

	res := call(t, h, http.MethodGet, "/api/v1/missing", "", apiHeaders())
	body := res.body
	if res.status != http.StatusNotFound {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)

	res = call(t, h, http.MethodPost, "/api/v1/factory", "", apiHeaders())
	body = res.body
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)
	if !strings.Contains(res.header.Get("Allow"), http.MethodGet) {
		t.Fatalf("Allow %q", res.header.Get("Allow"))
	}

	res = call(t, h, http.MethodDelete, "/healthz", "", nil)
	body = res.body
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("healthz status %d body %s", res.status, body)
	}
	assertProblem(t, res)
}

func TestNilEventsHandler(t *testing.T) {
	h := NewHandler(newFake(), testAPIKey, nil)
	res := call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1/events", "", apiHeaders())
	body := res.body
	if res.status != http.StatusNotFound {
		t.Fatalf("status %d body %s", res.status, body)
	}
	assertProblem(t, res)
}

type fakeBackend struct {
	factories    []Factory
	agents       map[string][]Agent
	runs         map[string]Run
	listRuns     []Run
	validateDiag []Diagnostic
	applyRef     DefinitionRef
	applyDiag    []Diagnostic
	force        error
	ingestErr    error

	validateBody  []byte
	validateID    string
	validateCalls int
	applyID       string
	lastStart     RunRequest
	startCalls    int
	lastFilter    RunFilter
	queryFilter   RunFilter
	ingests       []ingestRec
}

type ingestRec struct {
	runID    string
	bearer   string
	startIdx int
	lines    [][]byte
}

func newFake() *fakeBackend {
	return &fakeBackend{
		factories:    []Factory{{ID: "fac_1", Name: "acme", Alias: "acme"}},
		agents:       map[string][]Agent{"fac_1": {{ID: "agt_1", Name: "implementer", FactoryID: "fac_1"}}},
		runs:         map[string]Run{"run_1": {ID: "run_1", Status: "running", FactoryID: "fac_1", Agent: "implementer"}, "run_done": {ID: "run_done", Status: "succeeded"}},
		listRuns:     []Run{{ID: "run_1", Status: "running"}},
		validateDiag: []Diagnostic{{File: "factory.yaml", Line: 3, Severity: "warning", Message: "not applied in M2"}},
		applyRef:     DefinitionRef{ID: "def_1"},
		applyDiag:    []Diagnostic{{File: "factory.yaml", Line: 3, Severity: "warning", Message: "not applied in M2"}},
	}
}

func (f *fakeBackend) ListFactories(ctx context.Context) ([]Factory, error) {
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.factories, nil
}

func (f *fakeBackend) GetFactory(ctx context.Context, id string) (Factory, error) {
	if err := f.blocked(ctx); err != nil {
		return Factory{}, err
	}
	for _, fac := range f.factories {
		if fac.ID == id {
			return fac, nil
		}
	}
	return Factory{}, ErrNotFound
}

func (f *fakeBackend) ValidateDefinition(ctx context.Context, factoryID string, tarball io.Reader) ([]Diagnostic, error) {
	f.validateCalls++
	body, err := io.ReadAll(tarball)
	if err != nil {
		return nil, err
	}
	f.validateBody = body
	f.validateID = factoryID
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	if factoryID == "missing" {
		return nil, ErrNotFound
	}
	return f.validateDiag, nil
}

func (f *fakeBackend) ApplyDefinition(ctx context.Context, factoryID string) (DefinitionRef, []Diagnostic, error) {
	f.applyID = factoryID
	if err := f.blocked(ctx); err != nil {
		return DefinitionRef{}, nil, err
	}
	if factoryID == "missing" {
		return DefinitionRef{}, nil, ErrNotFound
	}
	return f.applyRef, f.applyDiag, nil
}

func (f *fakeBackend) ListAgents(ctx context.Context, factoryID string) ([]Agent, error) {
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	agents, ok := f.agents[factoryID]
	if !ok {
		return nil, ErrNotFound
	}
	return agents, nil
}

func (f *fakeBackend) StartRun(ctx context.Context, req RunRequest) (Run, error) {
	f.startCalls++
	f.lastStart = req
	if err := f.blocked(ctx); err != nil {
		return Run{}, err
	}
	return Run{ID: "run_new", Status: "queued"}, nil
}

func (f *fakeBackend) GetRun(ctx context.Context, id string) (Run, error) {
	if err := f.blocked(ctx); err != nil {
		return Run{}, err
	}
	run, ok := f.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	return run, nil
}

func (f *fakeBackend) ListRuns(ctx context.Context, filter RunFilter) ([]Run, error) {
	if filter.FactoryID != "" {
		f.lastFilter = filter
	} else {
		f.queryFilter = filter
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.listRuns, nil
}

func (f *fakeBackend) CancelRun(ctx context.Context, id string) (Run, error) {
	if err := f.blocked(ctx); err != nil {
		return Run{}, err
	}
	run, ok := f.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	switch run.Status {
	case "succeeded", "failed", "cancelled":
		return Run{}, ErrConflict
	}
	run.Status = "cancelled"
	f.runs[id] = run
	return run, nil
}

func (f *fakeBackend) IngestEvents(ctx context.Context, runID, bearer string, startIdx int, lines [][]byte) error {
	if err := f.blocked(ctx); err != nil {
		return err
	}
	if f.ingestErr != nil {
		return f.ingestErr
	}
	copied := make([][]byte, len(lines))
	for i, line := range lines {
		copied[i] = append([]byte(nil), line...)
	}
	f.ingests = append(f.ingests, ingestRec{runID: runID, bearer: bearer, startIdx: startIdx, lines: copied})
	return nil
}

func (f *fakeBackend) blocked(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	return f.force
}

type eventsRec struct {
	called int
	path   string
	query  string
	runID  string
}

func (e *eventsRec) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.called++
	e.path = r.URL.Path
	e.query = r.URL.RawQuery
	e.runID = r.PathValue("runId")
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "id: 4\ndata: {}\n\n")
}

func apiHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testAPIKey}
}

// httpResult is the handler response with its body already read and closed.
type httpResult struct {
	status int
	header http.Header
	body   []byte
}

func call(t *testing.T, h http.Handler, method, target, body string, hdr map[string]string) httpResult {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	return callReader(t, h, method, target, r, hdr)
}

func callBytes(t *testing.T, h http.Handler, method, target string, body []byte, hdr map[string]string) httpResult {
	t.Helper()
	return callReader(t, h, method, target, bytes.NewReader(body), hdr)
}

func callReader(t *testing.T, h http.Handler, method, target string, body io.Reader, hdr map[string]string) httpResult {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close body: %v", err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{status: resp.StatusCode, header: resp.Header.Clone(), body: b}
}

func decode[T any](t *testing.T, res httpResult, status int) T {
	t.Helper()
	if res.status != status {
		t.Fatalf("status %d, want %d, body %s", res.status, status, res.body)
	}
	if ct := res.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	var v T
	if err := json.Unmarshal(res.body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func assertProblem(t *testing.T, res httpResult) {
	t.Helper()
	if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q body %s", ct, res.body)
	}
	var p Problem
	if err := json.Unmarshal(res.body, &p); err != nil {
		t.Fatalf("problem json: %s", res.body)
	}
	if p.Status != res.status || p.Title == "" || p.Type == "" || p.Detail == "" {
		t.Fatalf("problem %+v", p)
	}
	var raw map[string]any
	if err := json.Unmarshal(res.body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["status"]; !ok {
		t.Fatalf("missing status: %s", res.body)
	}
}

func assertNoSecretKeys(t *testing.T, body []byte) {
	t.Helper()
	if len(bytes.TrimSpace(body)) == 0 {
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("json: %s", body)
	}
	walkJSON(t, v)
}

func walkJSON(t *testing.T, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			checkName(t, k)
			walkJSON(t, child)
		}
	case []any:
		for _, child := range x {
			walkJSON(t, child)
		}
	}
}

func walkType(t *testing.T, typ reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || seen[typ] || typ.PkgPath() == "time" {
		return
	}
	seen[typ] = true
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		checkName(t, typ.Name()+"."+f.Name)
		tag := f.Tag.Get("json")
		if tag != "" && tag != "-" {
			checkName(t, typ.Name()+"."+strings.Split(tag, ",")[0])
		}
		ft := f.Type
		kind := ft.Kind()
		if kind == reflect.Pointer || kind == reflect.Struct {
			walkType(t, ft, seen)
		}
		if kind == reflect.Slice || kind == reflect.Array || kind == reflect.Map {
			walkType(t, ft.Elem(), seen)
		}
	}
}

func checkName(t *testing.T, name string) {
	t.Helper()
	low := strings.ToLower(name)
	for _, bad := range []string{"env", "token", "secret"} {
		if strings.Contains(low, bad) {
			t.Errorf("name %q contains %q", name, bad)
		}
	}
}

func sizedRunJSON(n int) []byte {
	prefix := `{"factory":"acme","prompt":"`
	suffix := `"}`
	pad := n - len(prefix) - len(suffix)
	if pad < 1 {
		panic("size too small")
	}
	b := make([]byte, 0, n)
	b = append(b, prefix...)
	b = append(b, bytes.Repeat([]byte("a"), pad)...)
	b = append(b, suffix...)
	if len(b) != n {
		panic("size mismatch")
	}
	return b
}
