package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	plantedSecret  = "planted-secret-value-9f3a"
	runTokenValue  = "run-token-value"
	echoDefinition = "echo-definition-content"
	echoEventText  = "echo-event-text"
)

func TestRunListConsoleFilters(t *testing.T) {
	f := newFake()
	h := NewHandler(f, testAPIKey, nil)
	target := "/api/v1/agent/runs?agent=implementer&status=running&worker=w1&model=m1&failure_class=agent&source=api&parent_run_id=run_p&work_item_id=task_1"
	res := call(t, h, http.MethodGet, target, "", apiHeaders())
	_ = decode[[]Run](t, res, http.StatusOK)
	got := f.queryFilter
	want := RunFilter{
		Agent:        "implementer",
		Status:       "running",
		Worker:       "w1",
		Model:        "m1",
		FailureClass: "agent",
		Source:       "api",
		ParentRunID:  "run_p",
		WorkItemID:   "task_1",
	}
	if got != want {
		t.Fatalf("filter %+v", got)
	}
	if bytes.Contains(res.body, []byte("queue_wait_ms")) || bytes.Contains(res.body, []byte("failure_class")) || bytes.Contains(res.body, []byte("exec_ms")) || bytes.Contains(res.body, []byte("cost_cents")) {
		t.Fatalf("unwired list grew console fields: %s", res.body)
	}
}

func TestConsoleGoldens(t *testing.T) {
	f := newConsole(t)
	h := NewHandler(f, testAPIKey, nil)
	for _, rt := range consoleRoutes() {
		if rt.name == "events" {
			continue
		}
		res := call(t, h, rt.method, rt.sample, rt.body, consoleAuth(rt))
		if res.status != rt.status {
			t.Fatalf("%s status %d body %s", rt.name, res.status, res.body)
		}
		if rt.golden != "" {
			assertGolden(t, res.body, rt.golden)
		}
		if rt.status == http.StatusNoContent && len(bytes.TrimSpace(res.body)) != 0 {
			t.Fatalf("%s body %s", rt.name, res.body)
		}
	}
	if f.actor != APIKeyPrincipal || f.actor == testAPIKey {
		t.Fatalf("actor %q", f.actor)
	}
	if f.followText != "try the other file" {
		t.Fatalf("follow text %q", f.followText)
	}
	if f.steerToken != runTokenValue || f.steerToken == testAPIKey {
		t.Fatalf("steer token %q", f.steerToken)
	}
	if f.steerAfter != "steer_00" {
		t.Fatalf("after %q", f.steerAfter)
	}
	if !f.sumFrom.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) || !f.sumTo.Equal(time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("summary window %s %s", f.sumFrom, f.sumTo)
	}
	if len(f.paused) != 1 || f.paused[0] != "on-label" || len(f.resumed) != 1 || f.resumed[0] != "on-label" {
		t.Fatalf("pause %v resume %v", f.paused, f.resumed)
	}

	res := call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1", "", apiHeaders())
	assertGolden(t, res.body, "run.json")
	if f.detailCalls != 1 {
		t.Fatalf("detail calls %d", f.detailCalls)
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs?failure_class=agent", "", apiHeaders())
	assertGolden(t, res.body, "runs.json")
	if bytes.Contains(res.body, []byte("pr_url")) || bytes.Contains(res.body, []byte("steer")) {
		t.Fatalf("list row carries detail-only fields: %s", res.body)
	}
	if f.queryFilter.FailureClass != "agent" {
		t.Fatalf("list filter %+v", f.queryFilter)
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs?factory_id=fac_b", "", apiHeaders())
	_ = decode[[]RunRow](t, res, http.StatusOK)
	if f.lastFilter.FactoryID != "fac_b" {
		t.Fatalf("factory query %+v", f.lastFilter)
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/runs?factory_id=fac_b", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("mismatch status %d body %s", res.status, res.body)
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/runs?factory_id=fac_1", "", apiHeaders())
	_ = decode[[]RunRow](t, res, http.StatusOK)
	if f.lastFilter.FactoryID != "fac_1" {
		t.Fatalf("matching factory query %+v", f.lastFilter)
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/runs", "", apiHeaders())
	assertGolden(t, res.body, "runs.json")
	if f.lastFilter.FactoryID != "fac_1" {
		t.Fatalf("factory list filter %+v", f.lastFilter)
	}
}

func TestConsoleAuthNotFoundAndUnwired(t *testing.T) {
	for _, rt := range consoleRoutes() {
		t.Run(rt.name+"/401", func(t *testing.T) {
			h := NewHandler(newConsole(t), testAPIKey, nil)
			res := call(t, h, rt.method, rt.sample, rt.body, nil)
			if res.status != http.StatusUnauthorized {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
			assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
		})
		t.Run(rt.name+"/404", func(t *testing.T) {
			f := newConsole(t)
			f.notFound = true
			h := NewHandler(f, testAPIKey, nil)
			res := call(t, h, rt.method, rt.sample, rt.body, consoleAuth(rt))
			if res.status != http.StatusNotFound {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
		})
		t.Run(rt.name+"/501", func(t *testing.T) {
			h := NewHandler(newFake(), testAPIKey, nil)
			res := call(t, h, rt.method, rt.sample, rt.body, consoleAuth(rt))
			want := http.StatusNotImplemented
			if rt.unwired != 0 {
				want = rt.unwired
			}
			if res.status != want {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
			if want != http.StatusNotImplemented {
				return
			}
			var p Problem
			if err := json.Unmarshal(res.body, &p); err != nil {
				t.Fatal(err)
			}
			if p.Detail != consoleUnwiredDetail {
				t.Fatalf("detail %q", p.Detail)
			}
		})
	}

	unwired := NewHandler(newFake(), testAPIKey, nil)
	res := call(t, unwired, http.MethodGet, "/api/v1/agent/runs/run_1", "", apiHeaders())
	got := decode[Run](t, res, http.StatusOK)
	if got.ID != "run_1" || bytes.Contains(res.body, []byte("queue_wait_ms")) {
		t.Fatalf("unwired get run changed: %s", res.body)
	}
}

func TestSteerConflictReason(t *testing.T) {
	f := newConsole(t)
	f.interruptErr = fmt.Errorf("queue: %w", ErrSteerUnavailable{Reason: "worker/harness does not support steering"})
	f.followErr = fmt.Errorf("queue: %w", ErrSteerUnavailable{Reason: "run has not started"})
	h := NewHandler(f, testAPIKey, nil)

	res := call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/interrupt", "", apiHeaders())
	assertSteerReason(t, res, "worker/harness does not support steering")
	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/followups", `{"text":"hello"}`, apiHeaders())
	assertSteerReason(t, res, "run has not started")

	f.followCalls = 0
	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/followups", `{"text":"  "}`, apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("status %d", res.status)
	}
	if f.followCalls != 0 {
		t.Fatal("empty followup reached the backend")
	}
}

func TestSteerRunTokenAndLongPoll(t *testing.T) {
	f := newConsole(t)
	h := NewHandler(f, testAPIKey, nil)

	res := call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1/steer", "", map[string]string{"Authorization": "Bearer " + testAPIKey})
	if res.status != http.StatusOK {
		t.Fatalf("api key as bearer status %d body %s", res.status, res.body)
	}
	if f.steerToken != testAPIKey {
		t.Fatalf("token was not forwarded: %q", f.steerToken)
	}

	f.unauth = true
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1/steer", "", map[string]string{"Authorization": "Bearer " + runTokenValue})
	if res.status != http.StatusUnauthorized {
		t.Fatalf("rejected token status %d", res.status)
	}
	f.unauth = false

	withSteerWait(t, time.Hour)
	f.steerWait = 0
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1/steer", "", consoleAuth(consoleRoute{runToken: true}))
	if res.status != http.StatusOK {
		t.Fatalf("capped poll status %d", res.status)
	}
	if f.steerWait != maxSteerWait {
		t.Fatalf("wait %s, want cap %s", f.steerWait, maxSteerWait)
	}

	withSteerWait(t, 40*time.Millisecond)
	f.blockSteer = true
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1/steer", "", consoleAuth(consoleRoute{runToken: true}))
	if res.status != http.StatusOK || strings.TrimSpace(string(res.body)) != "[]" {
		t.Fatalf("timeout status %d body %s", res.status, res.body)
	}

	f.blockSteer = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/runs/run_1/steer", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+runTokenValue)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
		t.Fatalf("disconnect wrote %d bytes type %q", rr.Body.Len(), rr.Header().Get("Content-Type"))
	}
}

func TestFactoryEventsResume(t *testing.T) {
	f := newConsole(t)
	f.events = []FactoryEvent{
		{GID: 1, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":1}`)},
		{GID: 2, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":2}`)},
		{GID: 3, RunID: "run_1", Type: "run.terminal", Data: json.RawMessage(`{"seq":3,"status":"failed"}`)},
		{GID: 4, RunID: "run_2", Type: "message.delta", Data: json.RawMessage(`{"seq":1,"text":"next"}`)},
	}
	f.gate = make(chan struct{})
	h := NewHandler(f, testAPIKey, nil)

	rd, stop := openConsoleStream(t, h, "/api/v1/factory/fac_1/events", apiHeaders())
	releaseGate(t, f.gate)
	first := readConsoleSSE(t, rd, 1)
	releaseGate(t, f.gate)
	second := readConsoleSSE(t, rd, 1)
	if from := <-f.started; from != 0 {
		t.Fatalf("first cursor %d", from)
	}
	stop()

	f.gate = make(chan struct{})
	hdr := apiHeaders()
	hdr["Last-Event-ID"] = "2"
	rd, stop = openConsoleStream(t, h, "/api/v1/factory/fac_1/events?from_seq=1", hdr)
	defer stop()
	releaseGate(t, f.gate)
	third := readConsoleSSE(t, rd, 1)
	releaseGate(t, f.gate)
	fourth := readConsoleSSE(t, rd, 1)
	if from := <-f.started; from != 2 {
		t.Fatalf("resume cursor %d, Last-Event-ID must win", from)
	}
	got := first[0].id + "," + second[0].id + "," + third[0].id + "," + fourth[0].id
	if got != "1,2,3,4" {
		t.Fatalf("ids %s, want 1,2,3,4 with no gap or duplicate", got)
	}
	if third[0].event != "run.terminal" {
		t.Fatalf("terminal did not resume: %+v", third[0])
	}
}

func TestFactoryEventGoldenAndExclusive(t *testing.T) {
	f := newConsole(t)
	f.closeAfter = true
	f.events = []FactoryEvent{{
		GID:   10,
		RunID: "run_1",
		Type:  "message.delta",
		Data:  json.RawMessage(`{"seq":4,"turn":1,"role":"assistant","text":"hello"}`),
	}}
	h := NewHandler(f, testAPIKey, nil)
	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/events", "", apiHeaders())
	if res.status != http.StatusOK {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	if ct := res.header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	parts := splitSSE(res.body)
	if len(parts) != 1 || parts[0].id != "10" || parts[0].event != "message.delta" {
		t.Fatalf("%+v", parts)
	}
	assertGolden(t, []byte(parts[0].data), "factory-event.json")

	f.inclusive = true
	f.events = []FactoryEvent{
		{GID: 2, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":2}`)},
		{GID: 3, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":3}`)},
	}
	hdr := apiHeaders()
	hdr["Last-Event-ID"] = "2"
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/events?from_seq=0", "", hdr)
	parts = splitSSE(res.body)
	if len(parts) != 1 || parts[0].id != "3" {
		t.Fatalf("exclusive ids %+v", parts)
	}

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/events?from_seq=-1", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("bad cursor status %d", res.status)
	}
	assertProblem(t, res)
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/events?from_seq=-1", "", hdr)
	parts = splitSSE(res.body)
	if res.status != http.StatusOK || len(parts) != 1 || parts[0].id != "3" {
		t.Fatalf("Last-Event-ID should win: status %d parts %+v", res.status, parts)
	}
}

func TestInboxOldestFirst(t *testing.T) {
	f := newConsole(t)
	f.inbox = []InboxItem{f.inbox[2], f.inbox[0], f.inbox[1]}
	h := NewHandler(f, testAPIKey, nil)
	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/inbox", "", apiHeaders())
	assertGolden(t, res.body, "inbox.json")
}

func TestSummaryTimeValidation(t *testing.T) {
	f := newConsole(t)
	h := NewHandler(f, testAPIKey, nil)
	res := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/summary?from=yesterday", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("status %d", res.status)
	}
	if f.summaryCalls != 0 {
		t.Fatal("invalid from reached the backend")
	}
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/summary?to=nope", "", apiHeaders())
	if res.status != http.StatusBadRequest {
		t.Fatalf("status %d", res.status)
	}
}

func TestConsoleNoSecretMaterial(t *testing.T) {
	assertNoValueField(t, reflect.TypeOf(Secret{}), reflect.TypeOf(SecretUse{}), reflect.TypeOf(Identity{}), reflect.TypeOf(Webhook{}))

	f := newConsole(t)
	plantResponseSecrets(f)
	h := NewHandler(f, testAPIKey, nil)
	for _, rt := range consoleRoutes() {
		res := call(t, h, rt.method, rt.sample, rt.body, consoleAuth(rt))
		if res.status != rt.status {
			t.Fatalf("%s status %d body %s", rt.name, res.status, res.body)
		}
		if rt.name == "events" {
			assertEventFrameClean(t, res.body)
			continue
		}
		assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
		if rt.name == "definition" && !bytes.Contains(res.body, []byte(echoDefinition)) {
			t.Fatalf("definition walk missed file content: %s", res.body)
		}
	}
	res := call(t, h, http.MethodGet, "/api/v1/agent/runs/run_1", "", apiHeaders())
	if res.status != http.StatusOK {
		t.Fatalf("run status %d", res.status)
	}
	assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
	if !bytes.Contains(res.body, []byte("The agent reported the run as failed")) {
		t.Fatalf("run walk missed failure summary: %s", res.body)
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs", "", apiHeaders())
	if res.status != http.StatusOK {
		t.Fatalf("list status %d", res.status)
	}
	assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
	if !bytes.Contains(res.body, []byte("failure_class")) {
		t.Fatalf("list walk missed the row: %s", res.body)
	}

	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/followups", `{"text":"hello","value":"`+plantedSecret+`"}`, apiHeaders())
	if res.status != http.StatusAccepted {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertBodyClean(t, res.body, plantedSecret)

	f.leakErr = true
	for _, rt := range consoleRoutes() {
		res := call(t, h, rt.method, rt.sample, rt.body, consoleAuth(rt))
		if res.status == http.StatusBadRequest {
			continue
		}
		if bytes.Contains(res.body, []byte(plantedSecret)) || bytes.Contains(res.body, []byte(testAPIKey)) {
			t.Fatalf("%s leaked backend error: %s", rt.name, res.body)
		}
	}
	res = call(t, h, http.MethodGet, "/api/v1/agent/runs", "", apiHeaders())
	if bytes.Contains(res.body, []byte(plantedSecret)) {
		t.Fatalf("list leaked backend error: %s", res.body)
	}
}

func TestHiddenRedacts(t *testing.T) {
	run := Run{ID: "run_1", Held: hidden(plantedSecret)}
	view := DefinitionView{HeldValue: hidden(testAPIKey), HeldSign: hidden(plantedSecret)}
	for _, v := range []any{run, &run, view, &view} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			printed := fmt.Sprintf(format, v)
			if strings.Contains(printed, plantedSecret) || strings.Contains(printed, testAPIKey) {
				t.Fatalf("%s printed a secret: %s", format, printed)
			}
		}
	}
	for _, v := range []any{run, view} {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(plantedSecret)) || bytes.Contains(body, []byte(testAPIKey)) {
			t.Fatalf("json %s", body)
		}
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("run", "run", run, "view", view)
	if strings.Contains(buf.String(), plantedSecret) || strings.Contains(buf.String(), testAPIKey) {
		t.Fatalf("slog %s", buf.String())
	}
}

func assertSteerReason(t *testing.T, res httpResult, reason string) {
	t.Helper()
	if res.status != http.StatusConflict {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	var raw map[string]any
	if err := json.Unmarshal(res.body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["reason"] != reason || raw["detail"] != reason {
		t.Fatalf("problem %+v", raw)
	}
	if bytes.Contains(res.body, []byte("queue:")) {
		t.Fatalf("wrapped error text leaked: %s", res.body)
	}
}

func assertNoValueField(t *testing.T, types ...reflect.Type) {
	t.Helper()
	banned := []string{"value", "token", "secret", "password", "apikey", "api_key", "key", "credential", "authorization", "env"}
	for _, typ := range types {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			for _, bad := range banned {
				if strings.EqualFold(f.Name, bad) || strings.EqualFold(tag, bad) {
					t.Errorf("%s.%s carries %s", typ.Name(), f.Name, bad)
				}
			}
		}
	}
}

func assertBodyClean(t *testing.T, body []byte, planted ...string) {
	t.Helper()
	assertNoMaterialKeys(t, body)
	for _, s := range planted {
		if s != "" && bytes.Contains(body, []byte(s)) {
			t.Fatalf("response contains %q: %s", s, body)
		}
	}
}

func assertNoMaterialKeys(t *testing.T, body []byte) {
	t.Helper()
	if len(bytes.TrimSpace(body)) == 0 {
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("json: %s", body)
	}
	var walk func(any)
	walk = func(node any) {
		switch x := node.(type) {
		case map[string]any:
			for k, child := range x {
				switch strings.ToLower(k) {
				case "value", "token", "secret", "password", "api_key", "authorization", "credential", "env":
					t.Errorf("response key %q", k)
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
}

func plantResponseSecrets(f *consoleFake) {
	f.definition.HeldValue = hidden(testAPIKey)
	f.definition.HeldSign = hidden(plantedSecret)
	f.definition.Files[0].Content = echoDefinition + "\n"
	f.detail.Held = hidden(plantedSecret)
	for i := range f.rows {
		f.rows[i].Held = hidden(plantedSecret)
	}
	for i := range f.hooks {
		f.hooks[i].HeldSign = hidden(plantedSecret)
	}
	f.closeAfter = true
	f.events = []FactoryEvent{{
		GID:   10,
		RunID: "run_1",
		Type:  "message.delta",
		Held:  hidden(runTokenValue),
		Data:  json.RawMessage(`{"seq":4,"turn":1,"role":"assistant","text":"` + echoEventText + `"}`),
	}}
}

func assertEventFrameClean(t *testing.T, body []byte) {
	t.Helper()
	parts := splitSSE(body)
	if len(parts) != 1 {
		t.Fatalf("events %+v body %s", parts, body)
	}
	if !strings.Contains(parts[0].data, echoEventText) {
		t.Fatalf("event frame missed transcript text: %s", parts[0].data)
	}
	assertBodyClean(t, []byte(parts[0].data), plantedSecret, testAPIKey, runTokenValue)
	if bytes.Contains(body, []byte(plantedSecret)) || bytes.Contains(body, []byte(testAPIKey)) || bytes.Contains(body, []byte(runTokenValue)) {
		t.Fatalf("event frame leaked: %s", body)
	}
}

func consoleAuth(rt consoleRoute) map[string]string {
	if rt.runToken {
		return map[string]string{"Authorization": "Bearer " + runTokenValue}
	}
	return apiHeaders()
}

func releaseGate(t *testing.T, gate chan struct{}) {
	t.Helper()
	select {
	case gate <- struct{}{}:
	case <-time.After(2 * time.Second):
		t.Fatal("producer did not take the next event")
	}
}

func withSteerWait(t *testing.T, d time.Duration) {
	t.Helper()
	prev := steerWait
	steerWait = d
	t.Cleanup(func() { steerWait = prev })
}

func assertGolden(t *testing.T, body []byte, name string) {
	t.Helper()
	got := normalizeJSON(t, body)
	want := normalizeJSON(t, readGolden(t, name))
	if !bytes.Equal(got, want) {
		t.Fatalf("golden %s\n got %s\nwant %s", name, got, want)
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "console", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeGolden[T any](t *testing.T, name string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(readGolden(t, name), &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

func normalizeJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("json: %v\n%s", err, b)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(buf.Bytes())
}

type consoleFake struct {
	*fakeBackend
	mu       sync.Mutex
	planted  string
	notFound bool
	leakErr  bool
	unauth   bool

	definition     DefinitionView
	detail         RunDetail
	rows           []RunRow
	detailCalls    int
	timeline       []Phase
	usage          Usage
	steerID        string
	followID       string
	interruptErr   error
	followErr      error
	actor          string
	followText     string
	followCalls    int
	steers         []Steer
	steerToken     string
	steerAfter     string
	steerRun       string
	steerWait      time.Duration
	blockSteer     bool
	events         []FactoryEvent
	gate           chan struct{}
	closeAfter     bool
	inclusive      bool
	started        chan int64
	inbox          []InboxItem
	autos          []Automation
	paused         []string
	resumed        []string
	hooks          []Webhook
	workers        WorkersView
	secrets        []Secret
	identities     []Identity
	summary        Summary
	steerHistory   []SteerRecord
	hookDeliveries []Delivery
	sumFrom        time.Time
	sumTo          time.Time
	summaryCalls   int
}

func newConsole(t *testing.T) *consoleFake {
	t.Helper()
	f := &consoleFake{
		fakeBackend:    newFake(),
		planted:        plantedSecret,
		started:        make(chan int64, 4),
		definition:     decodeGolden[DefinitionView](t, "definition.json"),
		detail:         decodeGolden[RunDetail](t, "run.json"),
		rows:           decodeGolden[[]RunRow](t, "runs.json"),
		timeline:       decodeGolden[[]Phase](t, "timeline.json"),
		usage:          decodeGolden[Usage](t, "harness-usage.json"),
		steerID:        decodeGolden[SteerAck](t, "interrupt.json").SteerID,
		followID:       decodeGolden[SteerAck](t, "followup.json").SteerID,
		steers:         decodeGolden[[]Steer](t, "steer.json"),
		inbox:          decodeGolden[[]InboxItem](t, "inbox.json"),
		autos:          decodeGolden[[]Automation](t, "automations.json"),
		hooks:          decodeGolden[[]Webhook](t, "webhooks.json"),
		workers:        decodeGolden[WorkersView](t, "workers.json"),
		secrets:        decodeGolden[[]Secret](t, "secrets.json"),
		identities:     decodeGolden[[]Identity](t, "identities.json"),
		summary:        decodeGolden[Summary](t, "summary.json"),
		steerHistory:   decodeGolden[[]SteerRecord](t, "steers.json"),
		hookDeliveries: decodeGolden[[]Delivery](t, "webhook-deliveries.json"),
	}
	return f
}

func (f *consoleFake) fail(id string) error {
	if f.leakErr {
		return fmt.Errorf("backend: %s", f.planted)
	}
	if f.notFound || id == "missing" {
		return ErrNotFound
	}
	return nil
}

func (f *consoleFake) Definition(ctx context.Context, factory string) (DefinitionView, error) {
	if err := f.fail(factory); err != nil {
		return DefinitionView{}, err
	}
	if err := f.blocked(ctx); err != nil {
		return DefinitionView{}, err
	}
	return f.definition, nil
}

func (f *consoleFake) ListRunRows(ctx context.Context, filter RunFilter) ([]RunRow, error) {
	if filter.FactoryID != "" {
		f.lastFilter = filter
	} else {
		f.queryFilter = filter
	}
	if err := f.fail(""); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.rows, nil
}

func (f *consoleFake) RunDetail(ctx context.Context, id string) (RunDetail, error) {
	f.detailCalls++
	if err := f.fail(id); err != nil {
		return RunDetail{}, err
	}
	if err := f.blocked(ctx); err != nil {
		return RunDetail{}, err
	}
	return f.detail, nil
}

func (f *consoleFake) RunTimeline(ctx context.Context, id string) ([]Phase, error) {
	if err := f.fail(id); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.timeline, nil
}

func (f *consoleFake) RunUsage(ctx context.Context, id string) (Usage, error) {
	if err := f.fail(id); err != nil {
		return Usage{}, err
	}
	if err := f.blocked(ctx); err != nil {
		return Usage{}, err
	}
	return f.usage, nil
}

func (f *consoleFake) Interrupt(ctx context.Context, id, actor string) (string, error) {
	f.actor = actor
	if err := f.fail(id); err != nil {
		return "", err
	}
	if f.interruptErr != nil {
		return "", f.interruptErr
	}
	if err := f.blocked(ctx); err != nil {
		return "", err
	}
	return f.steerID, nil
}

func (f *consoleFake) Followup(ctx context.Context, id, actor, text string) (string, error) {
	f.followCalls++
	f.actor = actor
	f.followText = text
	if err := f.fail(id); err != nil {
		return "", err
	}
	if f.followErr != nil {
		return "", f.followErr
	}
	if err := f.blocked(ctx); err != nil {
		return "", err
	}
	return f.followID, nil
}

func (f *consoleFake) NextSteers(ctx context.Context, runToken, runID, after string, wait time.Duration) ([]Steer, error) {
	f.mu.Lock()
	f.steerToken = runToken
	f.steerAfter = after
	f.steerRun = runID
	f.steerWait = wait
	block := f.blockSteer
	f.mu.Unlock()
	if f.leakErr {
		return nil, fmt.Errorf("backend: %s", f.planted)
	}
	if f.notFound || runID == "missing" {
		return nil, ErrNotFound
	}
	if f.unauth {
		return nil, ErrUnauthorized
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.steers, nil
}

func (f *consoleFake) ListSteers(ctx context.Context, runID string) ([]SteerRecord, error) {
	if err := f.fail(runID); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.steerHistory, nil
}

func (f *consoleFake) FactoryEvents(ctx context.Context, factory string, from int64) (<-chan FactoryEvent, error) {
	if err := f.fail(factory); err != nil {
		return nil, err
	}
	f.mu.Lock()
	evs := append([]FactoryEvent(nil), f.events...)
	inclusive := f.inclusive
	closeAfter := f.closeAfter
	gate := f.gate
	f.mu.Unlock()
	select {
	case f.started <- from:
	default:
	}
	ch := make(chan FactoryEvent)
	go func() {
		defer close(ch)
		for _, ev := range evs {
			keep := ev.GID > from
			if inclusive {
				keep = ev.GID >= from
			}
			if !keep {
				continue
			}
			if gate != nil {
				select {
				case <-ctx.Done():
					return
				case <-gate:
				}
			}
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
		if closeAfter {
			return
		}
		<-ctx.Done()
	}()
	return ch, nil
}

func (f *consoleFake) Inbox(ctx context.Context, factory string) ([]InboxItem, error) {
	if err := f.fail(factory); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.inbox, nil
}

func (f *consoleFake) Automations(ctx context.Context, factory string) ([]Automation, error) {
	if err := f.fail(factory); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.autos, nil
}

func (f *consoleFake) PauseAutomation(ctx context.Context, factory, name string) error {
	if err := f.fail(factory); err != nil {
		return err
	}
	if name == "missing" {
		return ErrNotFound
	}
	if err := f.blocked(ctx); err != nil {
		return err
	}
	f.paused = append(f.paused, name)
	return nil
}

func (f *consoleFake) ResumeAutomation(ctx context.Context, factory, name string) error {
	if err := f.fail(factory); err != nil {
		return err
	}
	if name == "missing" {
		return ErrNotFound
	}
	if err := f.blocked(ctx); err != nil {
		return err
	}
	f.resumed = append(f.resumed, name)
	return nil
}

func (f *consoleFake) Webhooks(ctx context.Context, factory string) ([]Webhook, error) {
	if err := f.fail(factory); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.hooks, nil
}

func (f *consoleFake) ListWebhookDeliveries(ctx context.Context, factoryID, webhookUID, status string, limit int) ([]Delivery, error) {
	if err := f.fail(factoryID); err != nil {
		return nil, err
	}
	if err := f.fail(webhookUID); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.hookDeliveries, nil
}

func (f *consoleFake) Workers(ctx context.Context) (WorkersView, error) {
	if err := f.fail(""); err != nil {
		return WorkersView{}, err
	}
	if err := f.blocked(ctx); err != nil {
		return WorkersView{}, err
	}
	return f.workers, nil
}

func (f *consoleFake) Secrets(ctx context.Context, factory string) ([]Secret, error) {
	if err := f.fail(factory); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.secrets, nil
}

func (f *consoleFake) Identities(ctx context.Context) ([]Identity, error) {
	if err := f.fail(""); err != nil {
		return nil, err
	}
	if err := f.blocked(ctx); err != nil {
		return nil, err
	}
	return f.identities, nil
}

func (f *consoleFake) Summary(ctx context.Context, factory string, from, to time.Time) (Summary, error) {
	f.summaryCalls++
	f.sumFrom = from
	f.sumTo = to
	if err := f.fail(factory); err != nil {
		return Summary{}, err
	}
	if err := f.blocked(ctx); err != nil {
		return Summary{}, err
	}
	return f.summary, nil
}

func openConsoleStream(t *testing.T, h http.Handler, target string, hdr map[string]string) (*bufio.Reader, func()) {
	t.Helper()
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = pw.Close() }()
		rw := &flushPipe{pw: pw, h: make(http.Header)}
		h.ServeHTTP(rw, req)
	}()
	stop := func() {
		cancel()
		_ = pr.Close()
		<-done
	}
	t.Cleanup(stop)
	return bufio.NewReader(pr), stop
}

type flushPipe struct {
	pw    *io.PipeWriter
	h     http.Header
	wrote bool
}

func (p *flushPipe) Header() http.Header { return p.h }

func (p *flushPipe) WriteHeader(int) { p.wrote = true }

func (p *flushPipe) Write(b []byte) (int, error) {
	p.wrote = true
	return p.pw.Write(b)
}

func (p *flushPipe) Flush() {}

type sseBit struct {
	id, event, data string
}

func readConsoleSSE(t *testing.T, r *bufio.Reader, n int) []sseBit {
	t.Helper()
	var out []sseBit
	deadline := time.Now().Add(2 * time.Second)
	for len(out) < n && time.Now().Before(deadline) {
		parts := readOneSSE(t, r)
		if parts.id == "" && parts.event == "" {
			continue
		}
		out = append(out, parts)
	}
	if len(out) != n {
		t.Fatalf("got %d events, want %d", len(out), n)
	}
	return out
}

func readOneSSE(t *testing.T, r *bufio.Reader) sseBit {
	t.Helper()
	var msg sseBit
	var data []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("sse read: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			msg.data = strings.Join(data, "\n")
			return msg
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "id":
			msg.id = value
		case "event":
			msg.event = value
		case "data":
			data = append(data, value)
		}
	}
}

func splitSSE(body []byte) []sseBit {
	var out []sseBit
	for _, block := range bytes.Split(body, []byte("\n\n")) {
		block = bytes.TrimSpace(block)
		if len(block) == 0 || block[0] == ':' {
			continue
		}
		var msg sseBit
		var data [][]byte
		for _, line := range bytes.Split(block, []byte("\n")) {
			switch {
			case bytes.HasPrefix(line, []byte("id:")):
				msg.id = string(bytes.TrimSpace(line[len("id:"):]))
			case bytes.HasPrefix(line, []byte("event:")):
				msg.event = string(bytes.TrimSpace(line[len("event:"):]))
			case bytes.HasPrefix(line, []byte("data:")):
				data = append(data, bytes.TrimSpace(line[len("data:"):]))
			}
		}
		msg.data = string(bytes.Join(data, []byte("\n")))
		if msg.id != "" || msg.data != "" {
			out = append(out, msg)
		}
	}
	return out
}

var (
	_ Backend        = (*consoleFake)(nil)
	_ ConsoleBackend = (*consoleFake)(nil)
)
