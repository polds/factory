package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

func skipNoDB(t *testing.T) {
	t.Helper()
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
}

type wall struct {
	mu sync.Mutex
	t  time.Time
}

func (w *wall) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.t
}

func (w *wall) advance(d time.Duration) {
	w.mu.Lock()
	w.t = w.t.Add(d)
	w.mu.Unlock()
}

func (e *env) factoryAgent(t *testing.T) (factoryID, agentID string) {
	t.Helper()
	err := e.raw.QueryRow(context.Background(), `
		SELECT f.id, a.id
		FROM factories f
		JOIN agents a ON a.factory_id = f.id
		ORDER BY a.name
		LIMIT 1`).Scan(&factoryID, &agentID)
	if err != nil {
		t.Fatal(err)
	}
	return factoryID, agentID
}

func (e *env) runDetail(t *testing.T, id string) api.RunDetail {
	t.Helper()
	code, raw := e.authed(t, http.MethodGet, "/api/v1/agent/runs/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("detail %d %s", code, raw)
	}
	var detail api.RunDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

func (e *env) listRows(t *testing.T, query string) []api.RunRow {
	t.Helper()
	code, raw := e.authed(t, http.MethodGet, "/api/v1/agent/runs?"+query, "")
	if code != http.StatusOK {
		t.Fatalf("list %d %s", code, raw)
	}
	var rows []api.RunRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestM5ConsoleMount(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "mount")
	_ = w.readAssignment(t)

	checks := []struct {
		method, path, want string
	}{
		{http.MethodGet, "/healthz", `"status":"ok"`},
		{http.MethodGet, "/metrics", "factory_"},
		{http.MethodGet, "/api/v1/factory", "m2"},
		{http.MethodGet, "/api/v1/agent/runs/" + runID + "/events", "text/event-stream"},
	}
	for _, check := range checks {
		req, err := http.NewRequest(check.method, env.base+check.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+env.apiKey)
		req.Header.Set("Accept", "text/event-stream")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req = req.WithContext(ctx)
		res, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil && !strings.Contains(err.Error(), "context") {
			t.Fatalf("%s %s: %v", check.method, check.path, err)
		}
		if res == nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		got := res.Header.Get("Content-Type") + " " + string(body)
		if !strings.Contains(got, check.want) {
			t.Fatalf("%s %s: %s", check.path, res.Status, got)
		}
		if strings.Contains(string(body), consoleIndexMarker()) && check.path != "/runs/x" {
			t.Fatalf("%s served the console page", check.path)
		}
	}
	ingress, err := http.Post(env.base+"/api/v1/ingress/github", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ingressBody, _ := io.ReadAll(ingress.Body)
	_ = ingress.Body.Close()
	if strings.Contains(string(ingressBody), consoleIndexMarker()) {
		t.Fatal("ingress served the console page")
	}

	page, err := http.Get(env.base + "/runs/x")
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), consoleIndexMarker()) {
		t.Fatalf("console page %d %s", page.StatusCode, pageBody)
	}

	off := boot(t, bootOpts{configure: func(cfg *Config) { cfg.Console = "off" }})
	offPage, err := http.Get(off.base + "/runs/x")
	if err != nil {
		t.Fatal(err)
	}
	offBody, _ := io.ReadAll(offPage.Body)
	_ = offPage.Body.Close()
	if strings.Contains(string(offBody), "Console not built") || strings.Contains(string(offBody), `<div id="root">`) {
		t.Fatalf("console off still served a page: %s", offBody)
	}
	health, err := http.Get(off.base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	healthBody, _ := io.ReadAll(health.Body)
	_ = health.Body.Close()
	if !strings.Contains(string(healthBody), `"status":"ok"`) {
		t.Fatalf("healthz %s", healthBody)
	}
}

func TestM5RunFiltersPastLimit(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	ctx := context.Background()
	factoryID, agentID := env.factoryAgent(t)
	parent, err := store.NewID(store.PrefixRun)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO runs (id, factory_id, agent_id, status, priority, pool, prompt, created_at)
		VALUES ($1, $2, $3, 'succeeded', 2, 'standard', 'parent', clock_timestamp())`,
		parent, factoryID, agentID); err != nil {
		t.Fatal(err)
	}
	workID, err := store.NewID(store.PrefixWorkItem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(ctx, `
		INSERT INTO work_items (id, factory_id, source, title, stage, status)
		VALUES ($1, $2, 'manual', 'needle', 'triage', 'open')`, workID, factoryID); err != nil {
		t.Fatal(err)
	}
	const n = 250
	ids := make([]string, n)
	for i := range ids {
		ids[i], err = store.NewID(store.PrefixRun)
		if err != nil {
			t.Fatal(err)
		}
	}
	needle := ids[0]
	for i, id := range ids {
		worker, model, class := "", "", ""
		var work any
		var parentID any
		if i == 0 {
			worker, model, class = "needle-worker", "needle-model", "agent"
			work, parentID = workID, parent
		}
		if _, err := env.raw.Exec(ctx, `
			INSERT INTO runs (
			    id, factory_id, agent_id, status, priority, pool, prompt, worker_id,
			    model_id, failure_class, work_item_id, parent_run_id, created_at)
			VALUES (
			    $1, $2, $3, 'queued', 2, 'standard', 'row', NULLIF($4, ''),
			    $5, NULLIF($6, ''), $7, $8, clock_timestamp() - make_interval(secs => $9))`,
			id, factoryID, agentID, worker, model, class, work, parentID, n-i); err != nil {
			t.Fatal(err)
		}
	}
	rows := env.listRows(t, "worker=needle-worker")
	if len(rows) != 1 || rows[0].ID != needle {
		t.Fatalf("worker filter %+v", idsOf(rows))
	}
	if rows[0].QueueWaitMS <= 0 || rows[0].ExecMS != 0 || rows[0].CostCents != nil {
		t.Fatalf("timing %+v cost %v", rows[0], rows[0].CostCents)
	}
	for _, query := range []string{
		"model=needle-model",
		"failure_class=agent",
		"source=manual",
		"parent_run_id=" + parent,
		"work_item_id=" + workID,
		"agent=implementer&status=queued&worker=needle-worker",
	} {
		got := env.listRows(t, query)
		if len(got) != 1 || got[0].ID != needle {
			t.Fatalf("query %s -> %v", query, idsOf(got))
		}
	}
	plain := env.listRows(t, "")
	if len(plain) != 200 {
		t.Fatalf("unfiltered page %d", len(plain))
	}
	for _, row := range plain {
		if row.ID == needle {
			t.Fatal("unfiltered page included the oldest row")
		}
	}
}

func idsOf(rows []api.RunRow) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out
}

func TestM5ListSummaryMatchesDetail(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "summarize")
	asg := w.readAssignment(t)
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"usage","model_id":"test-model","input_tokens":3,"output_tokens":2,"cost_cents":4}`,
	})
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: runID, ExecutionID: asg.ExecutionID, Message: "died",
		FailureReason: "container_exit", ExitCode: 1,
	})
	env.waitStatus(t, runID, "failed")
	detail := env.runDetail(t, runID)
	rows := env.listRows(t, "status=failed")
	var row api.RunRow
	for _, item := range rows {
		if item.ID == runID {
			row = item
		}
	}
	if row.ID == "" {
		t.Fatal("list missed the run")
	}
	if row.FailureSummary != detail.Failure.Summary || row.FailureClass != detail.Failure.Class {
		t.Fatalf("list %s %q detail %s %q", row.FailureClass, row.FailureSummary, detail.Failure.Class, detail.Failure.Summary)
	}
	if row.CostCents == nil || *row.CostCents != 4 {
		t.Fatalf("cost %v", row.CostCents)
	}
	if row.ExecMS < 0 || row.QueueWaitMS < 0 {
		t.Fatalf("timing queue %d exec %d", row.QueueWaitMS, row.ExecMS)
	}
}

func TestM5OOMEvidence(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "oom")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: runID, ExecutionID: asg.ExecutionID, Message: "oom",
		FailureReason: "container_exit",
		FailureDetails: &protocol.FailureDetails{
			Kubernetes: &protocol.KubernetesFailureDetails{
				SchemaVersion: 1,
				Container: &protocol.KubernetesContainerFailureDetails{
					TerminationReason: "OOMKilled",
				},
			},
		},
	})
	env.waitStatus(t, runID, "failed")
	detail := env.runDetail(t, runID)
	if detail.Failure.Class != "infrastructure" || detail.Failure.Code != "oom_killed" {
		t.Fatalf("class %s/%s", detail.Failure.Class, detail.Failure.Code)
	}
	blob, _ := json.Marshal(detail.Failure.Evidence)
	if !strings.Contains(string(blob), "OOMKilled") {
		t.Fatalf("evidence %s", blob)
	}
	var raw []byte
	if err := env.raw.QueryRow(context.Background(), `
		SELECT failure_details_json FROM executions WHERE id = $1`, asg.ExecutionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "OOMKilled") {
		t.Fatalf("stored details %s", raw)
	}
}

func TestM5LateCancelKeepsExit(t *testing.T) {
	skipNoDB(t)
	t.Run("plain", func(t *testing.T) {
		env := boot(t, bootOpts{})
		runID := env.startRun(t, "cancel queued")
		env.cancel(t, runID)
		env.waitStatus(t, runID, "cancelled")
		detail := env.runDetail(t, runID)
		if detail.Failure.Class != "cancelled" || detail.Failure.Code != "cancelled" {
			t.Fatalf("class %s/%s", detail.Failure.Class, detail.Failure.Code)
		}
	})
	for _, tc := range []struct {
		name        string
		exit        int
		class, code string
	}{
		{name: "signal", exit: 137, class: "infrastructure", code: "container_killed"},
		{name: "agent", exit: 10, class: "agent", code: "agent_reported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := boot(t, bootOpts{cancelGrace: 150 * time.Millisecond})
			w := dialWorker(t, env, "local-1")
			defer w.close()
			runID := env.startRun(t, "cancel "+tc.name)
			asg := w.readAssignment(t)
			env.cancel(t, runID)
			env.waitStatus(t, runID, "cancelled")
			w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
				TaskID: runID, ExecutionID: asg.ExecutionID, Message: "late",
				FailureReason: "container_exit", ExitCode: tc.exit,
			})
			deadline := time.Now().Add(5 * time.Second)
			var got *int
			var detail api.RunDetail
			for time.Now().Before(deadline) {
				if err := env.raw.QueryRow(context.Background(), `SELECT exit_code FROM runs WHERE id = $1`, runID).Scan(&got); err != nil {
					t.Fatal(err)
				}
				// recordLateExit stores the exit, then classifies. The
				// exit can be visible while class is still the cancel value.
				if got != nil && *got == tc.exit {
					detail = env.runDetail(t, runID)
					if detail.Failure.Class == tc.class && detail.Failure.Code == tc.code {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
			}
			if got == nil || *got != tc.exit {
				t.Fatalf("exit %v", got)
			}
			if env.status(t, runID) != "cancelled" {
				t.Fatal("status changed")
			}
			if detail.Failure.Class != tc.class || detail.Failure.Code != tc.code {
				t.Fatalf("class %s/%s", detail.Failure.Class, detail.Failure.Code)
			}
		})
	}
}

func TestM5ClassSweepOnRestart(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	runID := env.startRun(t, "sweep")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	env.waitStatus(t, runID, "running")
	w.close()
	env.stop(t)
	env.markRunFailed(t, runID, "environment_setup_failed", 30)
	var class *string
	if err := env.raw.QueryRow(context.Background(), `SELECT failure_class FROM runs WHERE id = $1`, runID).Scan(&class); err != nil {
		t.Fatal(err)
	}
	if class != nil {
		t.Fatal("markRunFailed wrote a class")
	}
	env.start(t)
	deadline := time.Now().Add(5 * time.Second)
	var got, code string
	for time.Now().Before(deadline) {
		if err := env.raw.QueryRow(context.Background(), `
			SELECT COALESCE(failure_class, ''), COALESCE(failure_code, '') FROM runs WHERE id = $1`, runID).Scan(&got, &code); err != nil {
			t.Fatal(err)
		}
		if got != "" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if got != "infrastructure" || code != "environment_setup_failed" {
		t.Fatalf("class %s/%s", got, code)
	}
}

func TestM5ReclassifyAfterHarness(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "reclass")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: runID, ExecutionID: asg.ExecutionID, Message: "exit",
		FailureReason: "container_exit", ExitCode: 1,
	})
	env.waitStatus(t, runID, "failed")
	first := env.runDetail(t, runID)
	if first.Failure.Class != "infrastructure" || first.Failure.Code != "container_exit" {
		t.Fatalf("before %s/%s", first.Failure.Class, first.Failure.Code)
	}
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"terminal","status":"failed_by_agent","summary":"tests failed"}`,
	})
	deadline := time.Now().Add(5 * time.Second)
	var detail api.RunDetail
	for time.Now().Before(deadline) {
		detail = env.runDetail(t, runID)
		if detail.Failure.Code == "agent_reported" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if detail.Failure.Class != "agent" || detail.Failure.Code != "agent_reported" {
		t.Fatalf("after %s/%s", detail.Failure.Class, detail.Failure.Code)
	}
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 1, []string{
		`{"type":"terminal","status":"failed_by_agent","summary":"tests failed"}`,
	})
	again := env.runDetail(t, runID)
	if again.Failure.Class != "agent" || again.Failure.Code != "agent_reported" {
		t.Fatalf("repeat %s/%s", again.Failure.Class, again.Failure.Code)
	}
}

func TestM5SteerRules(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	code, raw := env.authed(t, http.MethodPost, "/api/v1/agent/runs/run_missing/interrupt", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing %d %s", code, raw)
	}
	queued := env.startRun(t, "not started")
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+queued+"/interrupt", "")
	assertSteerReason(t, code, raw, "run has not started")

	w := dialWorker(t, env, "local-1")
	defer w.close()
	asg := w.readAssignment(t)
	if asg.TaskID != queued {
		t.Fatalf("assignment %s", asg.TaskID)
	}
	running := queued
	env.waitStatus(t, running, "running")
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+running+"/interrupt", "")
	assertSteerReason(t, code, raw, "worker/harness does not support steering")
	detail := env.runDetail(t, running)
	if detail.Steer.Enabled || detail.Steer.Reason != "worker/harness does not support steering" {
		t.Fatalf("steer %+v", detail.Steer)
	}

	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"run.capabilities","steer":true}`,
	})
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+running+"/interrupt", "")
	if code != http.StatusAccepted {
		t.Fatalf("interrupt %d %s", code, raw)
	}
	var ack api.SteerAck
	if err := json.Unmarshal(raw, &ack); err != nil || !strings.HasPrefix(ack.SteerID, store.PrefixSteer) {
		t.Fatalf("ack %s %v", raw, err)
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+running+"/followups", `{"text":""}`)
	if code != http.StatusBadRequest {
		t.Fatalf("empty followup %d %s", code, raw)
	}

	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: running, ExecutionID: asg.ExecutionID, Message: "done",
	})
	env.waitStatus(t, running, "succeeded")
	code, raw = env.authed(t, http.MethodPost, "/api/v1/agent/runs/"+running+"/interrupt", "")
	var ended time.Time
	if err := env.raw.QueryRow(context.Background(), `SELECT ended_at FROM runs WHERE id = $1`, running).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	assertSteerReason(t, code, raw, "run finished; idle window ended at "+ended.UTC().Format(time.RFC3339))

	steers, err := env.srv.NextSteers(context.Background(), asg.EnvVars["FACTORY_RUN_TOKEN"], running, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(steers) != 0 {
		t.Fatalf("terminal poll claimed %+v", steers)
	}
	var claimed int
	if err := env.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM steer_requests WHERE run_id = $1 AND claim_expires_at IS NOT NULL`, running).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("terminal poll claimed %d rows", claimed)
	}
	if _, err := env.srv.NextSteers(context.Background(), "nope", running, "", 0); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("bad token %v", err)
	}
}

func assertSteerReason(t *testing.T, code int, raw []byte, reason string) {
	t.Helper()
	if code != http.StatusConflict {
		t.Fatalf("status %d %s", code, raw)
	}
	var problem api.Problem
	if err := json.Unmarshal(raw, &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Reason != reason || problem.Detail != reason {
		t.Fatalf("reason %q detail %q want %q", problem.Reason, problem.Detail, reason)
	}
}

func TestM5SteerClaimAckAndLease(t *testing.T) {
	skipNoDB(t)
	clock := &wall{t: time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)}
	env := boot(t, bootOpts{now: clock.now})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "steer claim")
	asg := w.readAssignment(t)
	env.waitStatus(t, runID, "running")
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"run.capabilities","steer":true}`,
	})
	token := asg.EnvVars["FACTORY_RUN_TOKEN"]
	ctx := context.Background()

	const n = 8
	ready := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]int{}
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			<-ready
			batch, err := env.srv.NextSteers(ctx, token, runID, "", 0)
			if err != nil {
				t.Errorf("poll %v", err)
				return
			}
			mu.Lock()
			for _, st := range batch {
				got[st.ID]++
			}
			mu.Unlock()
		}()
	}
	time.Sleep(50 * time.Millisecond)
	want := make([]string, 0, n)
	for range n {
		id, err := env.srv.Interrupt(ctx, runID, api.APIKeyPrincipal)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
	}
	close(ready)
	wg.Wait()
	if len(got) != n {
		t.Fatalf("unique %d map %v", len(got), got)
	}
	for _, id := range want {
		if got[id] != 1 {
			t.Fatalf("steer %s count %d", id, got[id])
		}
	}
	last := want[0]
	for _, id := range want[1:] {
		if id > last {
			last = id
		}
	}
	if _, err := env.srv.NextSteers(ctx, token, runID, last, 0); err != nil {
		t.Fatal(err)
	}

	follow, err := env.srv.Followup(ctx, runID, api.APIKeyPrincipal, "keep going")
	if err != nil {
		t.Fatal(err)
	}
	first, err := env.srv.NextSteers(ctx, token, runID, "", 0)
	if err != nil || len(first) != 1 || first[0].ID != follow {
		t.Fatalf("claim %+v %v", first, err)
	}
	clock.advance(store.SteerLease + time.Second)
	again, err := env.srv.NextSteers(ctx, token, runID, "", 0)
	if err != nil || len(again) != 1 || again[0].ID != follow {
		t.Fatalf("redeliver %+v %v", again, err)
	}
	acked, err := env.srv.NextSteers(ctx, token, runID, follow, 0)
	if err != nil || len(acked) != 0 {
		t.Fatalf("ack poll %+v %v", acked, err)
	}
	var delivered *time.Time
	if err := env.raw.QueryRow(ctx, `SELECT delivered_at FROM steer_requests WHERE id = $1`, follow).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered == nil {
		t.Fatal("delivered_at is null")
	}
	clock.advance(store.SteerLease + time.Second)
	afterAck, err := env.srv.NextSteers(ctx, token, runID, "", 0)
	if err != nil || len(afterAck) != 0 {
		t.Fatalf("acked steer returned %+v %v", afterAck, err)
	}
}

func TestM5IdleUntil(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		t.Helper()
		root := writeDefinition(t)
		mustWrite(t, root+"/agents/implementer/agent.md", `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
maxTurns: 7
idleTimeoutMinutes: 15
---

Do the task.
`)
		return root
	}})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "idle")
	asg := w.readAssignment(t)
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes == nil || *asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes != 15 {
		t.Fatalf("snapshot idle %+v", asg.Task)
	}
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"run.capabilities","steer":true}`,
		`{"type":"terminal","status":"succeeded","summary":"done","ts":"2026-04-02T12:00:00Z"}`,
	})
	deadline := time.Now().Add(5 * time.Second)
	var detail api.RunDetail
	for time.Now().Before(deadline) {
		detail = env.runDetail(t, runID)
		if detail.Steer.IdleUntil != nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if detail.Steer.IdleUntil == nil || !detail.Steer.Enabled {
		t.Fatalf("steer %+v", detail.Steer)
	}
	want := time.Date(2026, 4, 2, 12, 15, 0, 0, time.UTC)
	if !detail.Steer.IdleUntil.Equal(want) {
		t.Fatalf("idle_until %s", detail.Steer.IdleUntil)
	}
}

func TestM5FactorySSEResume(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	resumeFactorySSE(t, env, w)
}

func resumeFactorySSE(t *testing.T, env *env, w *wsWorker) {
	t.Helper()
	runA := env.startRun(t, "sse-a")
	asgA := w.readAssignment(t)
	runB := env.startRun(t, "sse-b")
	asgB := w.readAssignment(t)
	if asgA.TaskID != runA || asgB.TaskID != runB {
		t.Fatalf("assignments %s %s", asgA.TaskID, asgB.TaskID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	frames := env.factoryFrames(t, ctx, "m2", "")
	defer cancel()

	schemaDSN := env.schemaDSN(t)
	connA, err := pgx.Connect(context.Background(), schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	connB, err := pgx.Connect(context.Background(), schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connA.Close(context.Background())
		_ = connB.Close(context.Background())
	})
	txA, err := connA.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txA.Rollback(context.Background()) })
	if _, err := txA.Exec(context.Background(), `
		INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
		VALUES ($1, 1, $2, 1, clock_timestamp(), 'message.delta', '{"type":"message.delta","text":"late-a"}'::jsonb)`,
		runA, asgA.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, err := connB.Exec(context.Background(), `
		INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
		VALUES ($1, 1, $2, 1, clock_timestamp(), 'message.delta', '{"type":"message.delta","text":"early-b"}'::jsonb)`,
		runB, asgB.ExecutionID); err != nil {
		t.Fatal(err)
	}
	early := waitFrame(t, frames, "early-b")
	if strings.Contains(early.data, "late-a") {
		t.Fatal("uncommitted event was delivered")
	}
	if err := txA.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := waitFrame(t, frames, "late-a")
	if late.id <= early.id {
		t.Fatalf("factory seq late %d early %d", late.id, early.id)
	}
	cancel()

	more, stop := context.WithCancel(context.Background())
	defer stop()
	resumed := env.factoryFrames(t, more, "m2", fmt.Sprintf("%d", late.id))
	postEvents(t, asgA.EnvVars["FACTORY_EVENTS_URL"], asgA.EnvVars["FACTORY_RUN_TOKEN"], 2, []string{
		`{"type":"message.delta","text":"after-resume"}`,
	})
	got := waitFrame(t, resumed, "after-resume")
	if got.id <= late.id {
		t.Fatalf("resumed id %d cursor %d", got.id, late.id)
	}
	select {
	case extra := <-resumed:
		if strings.Contains(extra.data, "early-b") || strings.Contains(extra.data, "late-a") {
			t.Fatalf("duplicate %s", extra.data)
		}
	case <-time.After(300 * time.Millisecond):
	}
}

type sseFrame struct {
	id   int64
	data string
}

func (e *env) schemaDSN(t *testing.T) string {
	t.Helper()
	var schema string
	if err := e.raw.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn, err := withSearchPath(os.Getenv("FACTORY_TEST_DATABASE_URL"), schema, false)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func (e *env) factoryFrames(t *testing.T, ctx context.Context, factory, lastID string) <-chan sseFrame {
	t.Helper()
	out := make(chan sseFrame, 16)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/api/v1/factory/"+factory+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Accept", "text/event-stream")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	go func() {
		defer close(out)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer func() { _ = res.Body.Close() }()
		sc := bufio.NewScanner(res.Body)
		var id int64
		var data strings.Builder
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				if _, scanErr := fmt.Sscanf(line, "id: %d", &id); scanErr != nil {
					id = 0
				}
			case strings.HasPrefix(line, "data: "):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(line, "data: "))
			case line == "" && data.Len() > 0:
				select {
				case <-ctx.Done():
					return
				case out <- sseFrame{id: id, data: data.String()}:
				}
				data.Reset()
			}
		}
	}()
	return out
}

func waitFrame(t *testing.T, frames <-chan sseFrame, needle string) sseFrame {
	t.Helper()
	deadline := time.After(4 * time.Second)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatalf("stream closed before %s", needle)
			}
			if strings.Contains(frame.data, needle) {
				return frame
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", needle)
			return sseFrame{}
		}
	}
}

func TestM5TimelineIdle(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "timeline")
	asg := w.readAssignment(t)
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"terminal","status":"succeeded","summary":"done","ts":"2020-01-01T00:00:00Z"}`,
	})
	time.Sleep(20 * time.Millisecond)
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: runID, ExecutionID: asg.ExecutionID, Message: "done",
	})
	env.waitStatus(t, runID, "succeeded")
	code, raw := env.authed(t, http.MethodGet, "/api/v1/agent/runs/"+runID+"/timeline", "")
	if code != http.StatusOK {
		t.Fatalf("timeline %d %s", code, raw)
	}
	var phases []api.Phase
	if err := json.Unmarshal(raw, &phases); err != nil {
		t.Fatal(err)
	}
	var idle *api.Phase
	for i := range phases {
		if phases[i].Phase == api.PhaseIdle {
			idle = &phases[i]
		}
	}
	if idle == nil {
		t.Fatalf("no idle phase in %+v", phases)
	}
	if !idle.At.Equal(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) || idle.DurationMS <= 0 {
		t.Fatalf("idle %+v", idle)
	}
}

func TestM5WorkerFacts(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	plain := dialWorkerHeader(t, env, "plain-1", map[string]string{"User-Agent": ""}, "")
	defer plain.close()
	rich := dialWorkerHeader(t, env, "rich-1", map[string]string{"User-Agent": "factory-worker/9"}, "k8s")
	defer rich.close()
	env.waitConnected(t, "plain-1")
	env.waitConnected(t, "rich-1")
	deadline := time.Now().Add(3 * time.Second)
	var view api.WorkersView
	var plainInfo, richInfo api.Worker
	for time.Now().Before(deadline) {
		view = env.workers(t)
		if !workerPresent(view, "rich-1") || !workerPresent(view, "plain-1") {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		plainInfo = workerByID(t, view, "plain-1")
		richInfo = workerByID(t, view, "rich-1")
		// UpsertWorker lists the row before MarkWorkerConnected stores
		// version and backend. Empty is valid when the worker sent neither.
		if plainInfo.Version == "" && plainInfo.Backend == "" &&
			richInfo.Version == "factory-worker/9" && richInfo.Backend == "k8s" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !workerPresent(view, "rich-1") || !workerPresent(view, "plain-1") {
		t.Fatalf("worker rich-1 missing in %+v logs %s", view.Workers, env.logs.String())
	}
	if plainInfo.Version != "" || plainInfo.Backend != "" {
		t.Fatalf("invented facts %+v", plainInfo)
	}
	if richInfo.Version != "factory-worker/9" || richInfo.Backend != "k8s" {
		t.Fatalf("facts %+v", richInfo)
	}
	if richInfo.Capacity != 2 {
		t.Fatalf("capacity %d", richInfo.Capacity)
	}
	plain.close()
	runID := env.startRun(t, "in flight")
	asg := rich.readAssignment(t)
	if asg.TaskID != runID {
		t.Fatalf("assignment %s", asg.TaskID)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view = env.workers(t)
		if workerByID(t, view, "rich-1").InFlight >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if workerByID(t, view, "rich-1").InFlight < 1 {
		t.Fatalf("in flight %+v", workerByID(t, view, "rich-1"))
	}
	rich.send(t, protocol.TypeTaskRejected, protocol.TaskRejected{TaskID: runID, Reason: "busy"})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view = env.workers(t)
		if workerByID(t, view, "rich-1").Rejected1h >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("rejected %+v", workerByID(t, view, "rich-1"))
}

func (e *env) workers(t *testing.T) api.WorkersView {
	t.Helper()
	code, raw := e.authed(t, http.MethodGet, "/api/v1/agent/connected-self-hosted-workers", "")
	if code != http.StatusOK {
		t.Fatalf("workers %d %s", code, raw)
	}
	var view api.WorkersView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func workerPresent(view api.WorkersView, id string) bool {
	for _, worker := range view.Workers {
		if worker.ID == id {
			return true
		}
	}
	return false
}

func workerByID(t *testing.T, view api.WorkersView, id string) api.Worker {
	t.Helper()
	for _, worker := range view.Workers {
		if worker.ID == id {
			return worker
		}
	}
	t.Fatalf("worker %s missing in %+v", id, view.Workers)
	return api.Worker{}
}

func dialWorkerHeader(t *testing.T, env *env, workerID string, header map[string]string, backend string) *wsWorker {
	t.Helper()
	rawURL := "ws" + strings.TrimPrefix(env.base, "http") + "/api/v1/selfhosted/worker/ws?worker_id=" + workerID
	if backend != "" {
		rawURL += "&backend=" + backend
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+env.workerKey)
	for key, value := range header {
		hdr[key] = []string{value}
	}
	conn, resp, err := websocket.DefaultDialer.Dial(rawURL, hdr)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			t.Fatalf("dial %v %s", err, body)
		}
		t.Fatal(err)
	}
	return &wsWorker{t: t, conn: conn}
}

func TestM5PauseSurvivesReapply(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{
		definition: writeM4Definition,
		configure: func(cfg *Config) {
			cfg.GitHubWebhookSecret = e2eWebhook
		},
	})
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m4/automations/labeled-intake/pause", "")
	if code != http.StatusNoContent {
		t.Fatalf("pause %d %s", code, raw)
	}
	before := automationNamed(t, env, "labeled-intake")
	postGitHub(t, env.base+"/api/v1/ingress/github", e2eWebhook, "issues", "delivery-paused", labeledIssue())
	deadline := time.Now().Add(8 * time.Second)
	var after api.Automation
	for time.Now().Before(deadline) {
		after = automationNamed(t, env, "labeled-intake")
		if after.MatchCount == before.MatchCount+1 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if after.MatchCount != before.MatchCount+1 || after.RunsStarted != before.RunsStarted {
		t.Fatalf("before %+v after %+v", before, after)
	}
	if !strings.Contains(env.logs.String(), "reason=paused") {
		t.Fatalf("log missing reason=paused: %s", env.logs.String())
	}
	var started int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM runs`).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("paused automation started %d runs", started)
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/factory/m4/definition:apply", "")
	if code != http.StatusOK {
		t.Fatalf("reapply %d %s", code, raw)
	}
	if !automationNamed(t, env, "labeled-intake").RuntimePaused {
		t.Fatal("reapply cleared the pause")
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/factory/m4/automations/labeled-intake/resume", "")
	if code != http.StatusNoContent {
		t.Fatalf("resume %d %s", code, raw)
	}
	if automationNamed(t, env, "labeled-intake").RuntimePaused {
		t.Fatal("resume left the automation paused")
	}
	w := dialWorker(t, env, "local-1")
	defer w.close()
	postGitHub(t, env.base+"/api/v1/ingress/github", e2eWebhook, "issues", "delivery-resumed", labeledIssueNumber(413))
	if _, ok := w.readAssignmentWithin(t, 8*time.Second); !ok {
		t.Fatal("resume did not start a run")
	}
}

func labeledIssueNumber(number int) []byte {
	return mustJSON(map[string]any{
		"action": "labeled",
		"label":  map[string]any{"name": "factory-ready"},
		"issue": map[string]any{
			"id": number + 4000, "number": number, "title": "Add refunds", "body": "Please refund.",
			"html_url":   fmt.Sprintf("https://github.com/acme/payments-service/issues/%d", number),
			"updated_at": "2026-01-02T03:05:06Z",
			"labels":     []any{map[string]any{"name": "bug"}, map[string]any{"name": "factory-ready"}},
		},
		"repository": ghRepo(),
		"sender":     map[string]any{"login": "octocat", "id": 1, "type": "User"},
	})
}

func automationNamed(t *testing.T, env *env, name string) api.Automation {
	t.Helper()
	code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/m4/automations", "")
	if code != http.StatusOK {
		t.Fatalf("automations %d %s", code, raw)
	}
	var rows []api.Automation
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("automation %s missing in %s", name, raw)
	return api.Automation{}
}

func TestM5ApplyTree(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	marker := "uploaded-agent-body"
	body := treeBody(t, marker)
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/definition:apply", body)
	if code != http.StatusOK {
		t.Fatalf("apply %d %s", code, raw)
	}
	var applied struct {
		ID          string           `json:"id"`
		Diagnostics []api.Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &applied); err != nil || applied.ID == "" || len(applied.Diagnostics) != 0 {
		t.Fatalf("apply body %s", raw)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m2/definition", "")
	if code != http.StatusOK {
		t.Fatalf("definition %d %s", code, raw)
	}
	var view api.DefinitionView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.Source != api.DefinitionLocal || !view.Editable || view.GitSHA != "" {
		t.Fatalf("view %+v", view)
	}
	if !strings.Contains(string(raw), marker) {
		t.Fatal("uploaded file was not listed")
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/factory/m2/definition:apply", `{"files":[{"path":"../secret","content":"nope"}]}`)
	if code != http.StatusOK {
		t.Fatalf("bad apply %d %s", code, raw)
	}
	if !strings.Contains(string(raw), "path must be a relative file") {
		t.Fatalf("diagnostics %s", raw)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m2/definition", "")
	if code != http.StatusOK || !strings.Contains(string(raw), marker) {
		t.Fatalf("bad path applied: %s", raw)
	}
	code, raw = env.authed(t, http.MethodPost, "/api/v1/factory/m2/definition:apply", "")
	if code != http.StatusOK {
		t.Fatalf("empty apply %d %s", code, raw)
	}
}

func TestM5FinishClassAgrees(t *testing.T) {
	skipNoDB(t)
	t.Run("agent", func(t *testing.T) {
		env := boot(t, bootOpts{definition: func(t *testing.T) string { return writePipeline(t, 3) }})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		task, review := reachReviewing(t, env, w)
		postEvents(t, review.EnvVars["FACTORY_EVENTS_URL"], review.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
			`{"type":"terminal","status":"failed_by_agent","summary":"tests failed"}`,
		})
		failAssignment(t, w, review, "container_exit", 1)
		env.waitStatus(t, review.TaskID, "failed")
		assertFinishClass(t, env, review.TaskID, "agent", "agent_reported", false)
		env.assertRunCountStable(t, task.ID, "reviewing", 1)
	})
	t.Run("infra", func(t *testing.T) {
		env := boot(t, bootOpts{definition: func(t *testing.T) string { return writePipeline(t, 3) }})
		w := dialWorker(t, env, "local-1")
		defer w.close()
		task, review := reachReviewing(t, env, w)
		failAssignment(t, w, review, "container_exit", 137)
		env.waitStatus(t, review.TaskID, "failed")
		assertFinishClass(t, env, review.TaskID, "infrastructure", "container_killed", true)
		env.waitRunCount(t, task.ID, "reviewing", 2)
	})
}

func assertFinishClass(t *testing.T, env *env, runID, class, code string, infra bool) {
	t.Helper()
	var storedClass, storedCode string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT COALESCE(failure_class, ''), COALESCE(failure_code, '')
		FROM runs WHERE id = $1`, runID).Scan(&storedClass, &storedCode); err != nil {
		t.Fatal(err)
	}
	if storedClass != class || storedCode != code {
		t.Fatalf("stored %s/%s", storedClass, storedCode)
	}
	detail := env.runDetail(t, runID)
	if detail.Failure.Class != class || detail.Failure.Code != code {
		t.Fatalf("detail %s/%s", detail.Failure.Class, detail.Failure.Code)
	}
	listed := ""
	for _, row := range env.listRows(t, "status=failed") {
		if row.ID == runID {
			listed = row.FailureClass
		}
	}
	if listed != class {
		t.Fatalf("list %q", listed)
	}
	in, err := env.srv.classInput(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewerInfra(in) != infra {
		t.Fatalf("reviewerInfra %v harness %q", reviewerInfra(in), in.HarnessStatus)
	}
}

func TestM5ApplyTreeSurvivesRestart(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	marker := "survives-restart-body"
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/definition:apply", treeBody(t, marker))
	if code != http.StatusOK {
		t.Fatalf("apply %d %s", code, raw)
	}
	disk, err := os.ReadFile(filepath.Join(env.srv.cfg.DefinitionPath, "agents", "implementer", "agent.md"))
	if err != nil || !strings.Contains(string(disk), marker) {
		t.Fatalf("disk %q %v", disk, err)
	}
	env.restart(t)
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m2/definition", "")
	if code != http.StatusOK || !strings.Contains(string(raw), marker) {
		t.Fatalf("after restart %d %s", code, raw)
	}
}

func TestM5ApplySyncErrorKeepsDir(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	marker := "sync-failed-body"
	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/m2/definition:apply", treeBodyBadCron(t, marker))
	if code == http.StatusOK {
		t.Fatalf("sync error was success %s", raw)
	}
	code, raw = env.authed(t, http.MethodGet, "/api/v1/factory/m2/definition", "")
	if code != http.StatusOK || !strings.Contains(string(raw), marker) {
		t.Fatalf("listing lost the uploaded tree %d %s", code, raw)
	}
	disk, err := os.ReadFile(filepath.Join(env.srv.cfg.DefinitionPath, "agents", "implementer", "agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), marker) {
		t.Fatal("failed sync replaced DefinitionPath")
	}
}

func TestM5AutomationIdleAndOverrides(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		t.Helper()
		root := writeDefinition(t)
		mustWrite(t, root+"/agents/implementer/agent.md", `
---
description: Implements the requested change.
agentType: IMPLEMENT
model: test-model
secrets:
  - NPM_TOKEN
maxTurns: 7
idleTimeoutMinutes: 15
---

Do the task.
`)
		mustWrite(t, root+"/automations/nightly/automation.md", `
---
enabled: true
agent: implementer
model: override-model
runner: local
pool: batch
secrets:
  - GH_TOKEN
idleTimeoutMinutes: 3
---

Nightly.
`)
		return root
	}})
	var rawOverrides string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT overrides_json::text FROM automations WHERE name = 'nightly'`).Scan(&rawOverrides); err != nil {
		t.Fatal(err)
	}
	var overrides struct {
		Model              string   `json:"model"`
		Runner             string   `json:"runner"`
		Pool               string   `json:"pool"`
		Secrets            []string `json:"secrets"`
		IdleTimeoutMinutes int      `json:"idleTimeoutMinutes"`
	}
	if err := json.Unmarshal([]byte(rawOverrides), &overrides); err != nil {
		t.Fatal(err)
	}
	if overrides.Model != "override-model" || overrides.Runner != "local" || overrides.Pool != "batch" || overrides.IdleTimeoutMinutes != 3 || len(overrides.Secrets) != 1 || overrides.Secrets[0] != "GH_TOKEN" {
		t.Fatalf("overrides %+v", overrides)
	}
	if strings.Contains(rawOverrides, plantedGH) || strings.Contains(rawOverrides, plantedNPM) {
		t.Fatalf("overrides contain a secret value %s", rawOverrides)
	}
	runID := env.startRun(t, "auto idle")
	workID, err := store.NewID(store.PrefixWorkItem)
	if err != nil {
		t.Fatal(err)
	}
	factoryID, _ := env.factoryAgent(t)
	if _, err := env.raw.Exec(context.Background(), `
		INSERT INTO work_items (id, factory_id, source, title, stage, status)
		VALUES ($1, $2, 'manual', 'nightly', 'triage', 'open')`, workID, factoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.raw.Exec(context.Background(), `
		INSERT INTO intake_slots (work_item_id, automation, delivery_id, run_id)
		VALUES ($1, 'nightly', 'slot-1', $2)`, workID, runID); err != nil {
		t.Fatal(err)
	}
	w := dialWorker(t, env, "local-1")
	defer w.close()
	asg := w.readAssignment(t)
	if asg.TaskID != runID || asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes == nil || *asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes != 3 {
		idle := any(nil)
		if asg.Task != nil && asg.Task.AgentConfigSnapshot != nil {
			idle = asg.Task.AgentConfigSnapshot.IdleTimeoutMinutes
		}
		t.Fatalf("run %s assignment %s idle %v", runID, asg.TaskID, idle)
	}
}

func treeBody(t *testing.T, marker string) string {
	t.Helper()
	files := []api.DefinitionFile{
		{Path: "factory.yaml", Content: "schemaVersion: v1alpha1\nname: m2\nsecrets:\n  - GH_TOKEN\nagentDefaults:\n  model: auto\n  runner: local\n  pool: standard\n"},
		{Path: "runners/local.yaml", Content: "description: Local docker runner\nplatform:\n  os: linux\n  arch: x86_64\n  linux:\n    dockerImage: factory-m1-task:dev\n"},
		{Path: "agents/implementer/agent.md", Content: "---\ndescription: Implements the requested change.\nagentType: IMPLEMENT\nmodel: test-model\nsecrets:\n  - NPM_TOKEN\nmaxTurns: 7\n---\n\n" + marker + "\n"},
	}
	raw, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func treeBodyBadCron(t *testing.T, marker string) string {
	t.Helper()
	files := []api.DefinitionFile{
		{Path: "factory.yaml", Content: "schemaVersion: v1alpha1\nname: m2\nsecrets:\n  - GH_TOKEN\nagentDefaults:\n  model: auto\n  runner: local\n  pool: standard\n"},
		{Path: "runners/local.yaml", Content: "description: Local docker runner\nplatform:\n  os: linux\n  arch: x86_64\n  linux:\n    dockerImage: factory-m1-task:dev\n"},
		{Path: "agents/implementer/agent.md", Content: "---\ndescription: Implements the requested change.\nagentType: IMPLEMENT\nmodel: test-model\nsecrets:\n  - NPM_TOKEN\nmaxTurns: 7\n---\n\n" + marker + "\n"},
		{Path: "automations/bad/automation.md", Content: "---\nenabled: true\nagent: implementer\ntriggers:\n  - provider: schedule\n    event: cron_fired\n    schedule:\n      name: bad\n      cron: not-a-cron\n---\n\n" + marker + "\n"},
	}
	raw, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestM5DefinitionGitSource(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{
		gitURL: "https://example.invalid/acme/factory.git",
		gitRef: "main",
		configure: func(cfg *Config) {
			cfg.DefinitionGitProductionRef = "main"
		},
	})
	code, raw := env.authed(t, http.MethodGet, "/api/v1/factory/m2/definition", "")
	if code != http.StatusOK {
		t.Fatalf("definition %d %s", code, raw)
	}
	var view api.DefinitionView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.Source != api.DefinitionGit || view.Editable || view.ProductionRef != "main" {
		t.Fatalf("view %+v", view)
	}
}

func TestM5RunURL(t *testing.T) {
	skipNoDB(t)
	var body bytes.Buffer
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body.Write(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"ts":"1.2"}`)
	}))
	t.Cleanup(slack.Close)
	env := boot(t, bootOpts{
		definition: func(t *testing.T) string { return writePipeline(t, 3) },
		configure: func(cfg *Config) {
			cfg.ExternalURL = "https://factory.example/"
			cfg.SlackBotToken = "xoxb-test"
			cfg.SlackAPIBase = slack.URL
		},
	})
	task := env.createTask(t, "Notify", "body")
	if env.srv.integ.notifier == nil {
		t.Fatal("notifier")
	}
	if err := env.srv.integ.notifier.Notify(context.Background(), outbound.Notice{
		Kind:       outbound.KindRunStarted,
		WorkItemID: task.ID,
		RunID:      "run_1",
		Stage:      "triage",
		Title:      "Notify",
		Channel:    "C0",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), "https://factory.example/runs/run_1|Open run") {
		t.Fatalf("card %s", body.String())
	}
}

func TestM5SecretWalk(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	w := dialWorker(t, env, "local-1")
	defer w.close()
	runID := env.startRun(t, "secrets")
	asg := w.readAssignment(t)
	token := asg.EnvVars["FACTORY_RUN_TOKEN"]
	paths := []string{
		"/api/v1/factory/m2/definition",
		"/api/v1/agent/runs",
		"/api/v1/agent/runs/" + runID,
		"/api/v1/agent/runs/" + runID + "/timeline",
		"/api/v1/agent/runs/" + runID + "/harness-usage",
		"/api/v1/factory/m2/inbox",
		"/api/v1/factory/m2/automations",
		"/api/v1/factory/m2/webhooks",
		"/api/v1/agent/connected-self-hosted-workers",
		"/api/v1/factory/m2/secrets",
		"/api/v1/agent/identities",
		"/api/v1/factory/m2/summary?from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z",
	}
	for _, path := range paths {
		code, raw := env.authed(t, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("%s %d %s", path, code, raw)
		}
		forbidPlanted(t, path, string(raw), token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frames := env.factoryFrames(t, ctx, "m2", "")
	select {
	case frame := <-frames:
		forbidPlanted(t, "sse", frame.data, token)
	case <-ctx.Done():
	}
}

func forbidPlanted(t *testing.T, where, body, token string) {
	t.Helper()
	for _, secret := range []string{plantedGH, plantedNPM, plantedOpenAI, token} {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatalf("%s contains a planted secret", where)
		}
	}
}

func TestM5ReviewerSecondInfraRestartsToBuilding(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task, review := reachReviewing(t, env, w)
	failAssignment(t, w, review, "environment_setup_failed", 30)
	env.waitRunCount(t, task.ID, "reviewing", 2)
	var retry string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT id FROM runs
		WHERE work_item_id = $1 AND id <> $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, task.ID, review.TaskID).Scan(&retry); err != nil {
		t.Fatal(err)
	}
	w.close()
	env.stop(t)
	env.markRunFailed(t, retry, "environment_setup_failed", 30)
	env.start(t)
	deadline := time.Now().Add(8 * time.Second)
	var stage string
	for time.Now().Before(deadline) {
		stage = env.getTask(t, task.ID).Stage
		if stage == "building" {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("stage %s", stage)
}

func TestM5BackfillParity(t *testing.T) {
	skipNoDB(t)
	env := boot(t, bootOpts{})
	assertBackfillParity(t, env)
}

func assertBackfillParity(t *testing.T, env *env) {
	t.Helper()
	ctx := context.Background()
	factoryID, agentID := env.factoryAgent(t)
	rules := failure.Cases()
	if len(rules) == 0 {
		t.Fatal("no failure cases")
	}
	for _, rule := range rules {
		status, execState, reason := storedShape(rule.Input)
		runID, err := store.NewID(store.PrefixRun)
		if err != nil {
			t.Fatal(err)
		}
		execID, err := store.NewID(store.PrefixExec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.raw.Exec(ctx, `
			INSERT INTO runs (
			    id, factory_id, agent_id, status, priority, pool, prompt,
			    failure_reason, exit_code)
			VALUES ($1, $2, $3, $4, 2, 'standard', $5, NULLIF($6, ''), $7)`,
			runID, factoryID, agentID, status, rule.Name, reason, rule.Input.ExitCode); err != nil {
			t.Fatalf("%s run: %v", rule.Name, err)
		}
		var details any
		if len(rule.Input.Details) > 0 {
			details = []byte(rule.Input.Details)
		}
		if _, err := env.raw.Exec(ctx, `
			INSERT INTO executions (
			    id, run_id, attempt, worker_id, state, failure_reason, exit_code,
			    failure_details_json, run_token_sha256)
			VALUES ($1, $2, 1, 'w', $3, NULLIF($4, ''), $5, $6::jsonb, $7)`,
			execID, runID, execState, reason, rule.Input.ExitCode, details, []byte("token")); err != nil {
			t.Fatalf("%s exec: %v", rule.Name, err)
		}
		seq := 1
		if rule.Input.HarnessStatus != "" {
			payload, err := json.Marshal(map[string]string{"type": "terminal", "status": rule.Input.HarnessStatus})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.raw.Exec(ctx, `
				INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
				VALUES ($1, 1, $2, 1, clock_timestamp(), 'terminal', $3::jsonb)`,
				runID, execID, payload); err != nil {
				t.Fatalf("%s event: %v", rule.Name, err)
			}
			seq = 2
		}
		if isTerminalStatus(status) {
			// commitFinish writes run.terminal after the harness event, with
			// status set to the run status. A higher seq must not shadow the
			// harness terminal row.
			payload, err := json.Marshal(map[string]string{"type": "run.terminal", "status": status})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.raw.Exec(ctx, `
				INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
				VALUES ($1, $2, $3, $4, clock_timestamp(), 'run.terminal', $5::jsonb)`,
				runID, seq, execID, serverEventBase, payload); err != nil {
				t.Fatalf("%s run.terminal: %v", rule.Name, err)
			}
		}
		if isTerminalStatus(status) {
			if err := env.srv.persistClass(ctx, runID); err != nil {
				t.Fatalf("%s persist: %v", rule.Name, err)
			}
		}
		var storedClass, storedCode, caseClass, caseCode string
		if err := env.raw.QueryRow(ctx, `
			SELECT
			    COALESCE(r.failure_class, ''),
			    COALESCE(r.failure_code, ''),
			    split_part(coalesce(classified.class_code, ''), '/', 1),
			    split_part(coalesce(classified.class_code, ''), '/', 2)
			FROM runs r
			JOIN LATERAL (
			    SELECT `+store.FailureBackfillCASE+` AS class_code
			    FROM (
			        SELECT
			            r.status AS run_status,
			            COALESCE(r.failure_reason, '') AS failure_reason,
			            r.exit_code AS exit_code,
			            COALESCE((
			                SELECT e.payload_json->>'status'
			                FROM run_events e
			                WHERE e.run_id = r.id AND e.type = 'terminal'
			                ORDER BY e.seq DESC
			                LIMIT 1
			            ), '') AS harness_status,
			            COALESCE((
			                SELECT x.state FROM executions x
			                WHERE x.run_id = r.id
			                ORDER BY x.attempt DESC
			                LIMIT 1
			            ), '') AS exec_state,
			            (
			                SELECT x.failure_details_json FROM executions x
			                WHERE x.run_id = r.id
			                ORDER BY x.attempt DESC
			                LIMIT 1
			            ) AS details
			    ) AS shaped
			) AS classified ON true
			WHERE r.id = $1`, runID).Scan(&storedClass, &storedCode, &caseClass, &caseCode); err != nil {
			t.Fatalf("%s case: %v", rule.Name, err)
		}
		in, err := env.srv.classInput(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		want := failure.Classify(in)
		if storedClass != string(want.Class) || storedCode != want.Code {
			t.Fatalf("%s stored %s/%s classify %s/%s", rule.Name, storedClass, storedCode, want.Class, want.Code)
		}
		if caseClass != storedClass || caseCode != storedCode {
			t.Fatalf("%s case %s/%s stored %s/%s", rule.Name, caseClass, caseCode, storedClass, storedCode)
		}
	}
}

func storedShape(in failure.Input) (status, execState, reason string) {
	status = in.RunStatus
	reason = in.FailureReason
	execState = "failed"
	switch status {
	case store.StatusQueued, store.StatusAssigned, store.StatusRunning, store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
	case store.ExecOrphaned:
		status = store.StatusFailed
		execState = store.ExecOrphaned
	case "":
		if reason != "" || in.ExitCode != nil || in.HarnessStatus != "" || len(in.Details) > 0 {
			status = store.StatusFailed
		} else {
			status = store.StatusSucceeded
		}
	default:
		if reason == "" {
			reason = status
		}
		status = store.StatusFailed
	}
	switch {
	case execState == store.ExecOrphaned:
	case status == store.StatusSucceeded:
		execState = store.ExecCompleted
	case status == store.StatusCancelled:
		execState = store.ExecCancelled
	case status == store.StatusQueued || status == store.StatusAssigned || status == store.StatusRunning:
		execState = store.ExecRunning
	default:
		execState = store.ExecFailed
	}
	return status, execState, reason
}
