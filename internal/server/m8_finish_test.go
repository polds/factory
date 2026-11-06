package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

func TestM8ForbidOverlapConcurrentFirstSlots(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	sch, factoryID := env.overlapSchedule(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	type got struct {
		rec invocation.Receipt
		err error
	}
	out := make([]got, 2)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			slot := time.Date(2026, 11, 6, 1, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
			out[i].rec, out[i].err = env.srv.AdmitInvocation(context.Background(), "pipe", invocation.Principal{
				Kind:  "schedule",
				Actor: "schedule",
				Allow: true,
			}, env.overlapRequest(sch.ID, slot))
		}(i)
	}
	close(start)
	wg.Wait()
	completed := 0
	skipped := 0
	for i, item := range out {
		if item.err != nil {
			t.Fatalf("slot %d: %v", i, item.err)
		}
		if item.rec.CountsAsCompleted {
			completed++
		}
		if item.rec.State == invocation.StateSkippedOverlap {
			skipped++
			if item.rec.CountsAsCompleted || item.rec.WorkItemID != "" {
				t.Fatalf("skipped receipt %+v", item.rec)
			}
		}
	}
	if completed != 1 || skipped != 1 {
		t.Fatalf("completed %d skipped %d", completed, skipped)
	}
	var scans int
	prefix := "schedule:" + sch.ID + ":slot:"
	if err := env.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM invocations
		WHERE factory_id = $1 AND source_occurrence_key LIKE $2 || '%'
		  AND counts_as_completed_scan`, factoryID, prefix).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("completed scans %d", scans)
	}
}

func TestM8ForbidOverlapSequentialAndReplay(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	sch, factoryID := env.overlapSchedule(t)
	slotA := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	first := env.admitSlot(t, sch.ID, slotA)
	if !first.CountsAsCompleted || first.WorkItemID == "" {
		t.Fatalf("first %+v", first)
	}
	replay := env.admitSlot(t, sch.ID, slotA)
	if replay.InvocationID != first.InvocationID || !replay.CountsAsCompleted {
		t.Fatalf("replay %+v first %+v", replay, first)
	}
	var rows int
	if err := env.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM invocations WHERE source_occurrence_key = $1`,
		invocation.ScheduleOccurrenceKey(sch.ID, slotA)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("replay rows %d", rows)
	}
	busy, err := env.srv.scheduleOverlapBusy(context.Background(), factoryID, sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !busy {
		t.Fatal("open work item should be busy")
	}
	slotB := time.Date(2026, 11, 6, 2, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	second := env.admitSlot(t, sch.ID, slotB)
	if second.State != invocation.StateSkippedOverlap || second.CountsAsCompleted || second.WorkItemID != "" {
		t.Fatalf("second %+v", second)
	}
	if _, err := env.raw.Exec(context.Background(), `UPDATE work_items SET status = $2 WHERE id = $1`, first.WorkItemID, store.WorkItemComplete); err != nil {
		t.Fatal(err)
	}
	busy, err = env.srv.scheduleOverlapBusy(context.Background(), factoryID, sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if busy {
		t.Fatal("completed work item should leave the next slot free")
	}
	slotC := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	third := env.admitSlot(t, sch.ID, slotC)
	if !third.CountsAsCompleted || third.InvocationID == first.InvocationID {
		t.Fatalf("third %+v", third)
	}
	var scans int
	prefix := "schedule:" + sch.ID + ":slot:"
	if err := env.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM invocations
		WHERE factory_id = $1 AND source_occurrence_key LIKE $2 || '%'
		  AND counts_as_completed_scan`, factoryID, prefix).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatalf("completed scans %d", scans)
	}
}

func (e *env) overlapSchedule(t *testing.T) (store.Schedule, string) {
	t.Helper()
	var factoryID string
	if err := e.raw.QueryRow(context.Background(), `SELECT id FROM factories WHERE name = 'pipe'`).Scan(&factoryID); err != nil {
		t.Fatal(err)
	}
	sch, err := e.srv.store.UpsertSchedule(context.Background(), store.Schedule{
		FactoryID:  factoryID,
		Automation: "nightly",
		Name:       "nightly-" + factoryID,
		Cron:       "0 * * * *",
		NextFireAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sch, factoryID
}

func (e *env) overlapRequest(scheduleID, slot string) invocation.Request {
	return invocation.Request{
		SourceOccurrenceKey: invocation.ScheduleOccurrenceKey(scheduleID, slot),
		Intent:              "new",
		Priority:            "batch",
		Profile:             invocation.Profile{Name: "triage"},
		ScheduleID:          scheduleID,
		DueSlot:             slot,
		OverlapPolicy:       "forbid",
	}
}

func (e *env) admitSlot(t *testing.T, scheduleID, slot string) invocation.Receipt {
	t.Helper()
	rec, err := e.srv.AdmitInvocation(context.Background(), "pipe", invocation.Principal{
		Kind:  "schedule",
		Actor: "schedule",
		Allow: true,
	}, e.overlapRequest(scheduleID, slot))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestM8ReportFinishZeroFindingsAndMissing(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeReportDefinition})
	w := dialWorker(t, env, "scan-1")
	defer w.close()

	missing := env.postJSON(t, "/api/v1/factory/scan/invocations", map[string]any{
		"sourceOccurrenceKey": "scan:missing:1",
		"intent":              "new",
		"priority":            "interactive",
		"profile":             map[string]any{"name": "scanner"},
	}, http.StatusCreated)
	missingID, _ := missing["invocationId"].(string)
	missAsg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: missAsg.TaskID, WorkerID: "scan-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: missAsg.TaskID, ExecutionID: missAsg.ExecutionID, Message: "done",
	})
	env.waitStatus(t, missAsg.TaskID, "failed")
	if reason := env.runDetail(t, missAsg.TaskID).FailureReason; reason != "report_missing" {
		t.Fatalf("missing report failure_reason %q", reason)
	}
	var stored int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM invocation_results WHERE invocation_id = $1`, missingID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("missing report stored %d", stored)
	}

	okBody := env.postJSON(t, "/api/v1/factory/scan/invocations", map[string]any{
		"sourceOccurrenceKey": "scan:zero:1",
		"intent":              "new",
		"priority":            "interactive",
		"profile":             map[string]any{"name": "scanner"},
	}, http.StatusCreated)
	invocationID, _ := okBody["invocationId"].(string)
	asg := w.readAssignment(t)
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"report","ts":"2026-11-06T04:00:00Z","summary":"no findings","coverageStatus":"full","findingsCount":0,"report":"coverage full"}`,
	})
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "scan-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: "done",
	})
	env.waitStatus(t, asg.TaskID, "succeeded")
	if reason := env.runDetail(t, asg.TaskID).FailureReason; reason != "" {
		t.Fatalf("zero findings failure_reason %q", reason)
	}
	env.assertStoredReport(t, invocationID)
	status, body := env.getInvocationResult(t, "scan", invocationID, true)
	if status != http.StatusOK {
		t.Fatalf("get result %d %s", status, body)
	}
	var view struct {
		Summary        string `json:"summary"`
		CoverageStatus string `json:"coverageStatus"`
		FindingsCount  int    `json:"findingsCount"`
		Report         string `json:"report"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.CoverageStatus != "full" || view.FindingsCount != 0 || view.Report != "coverage full" || view.Summary != "no findings" {
		t.Fatalf("result %+v", view)
	}
	unauth, unauthBody := env.getInvocationResult(t, "scan", invocationID, false)
	if unauth != http.StatusForbidden || len(unauthBody) != 0 {
		t.Fatalf("unauthorized %d %q", unauth, unauthBody)
	}

	env.restart(t)
	env.assertStoredReport(t, invocationID)
	status, body = env.getInvocationResult(t, "scan", invocationID, true)
	if status != http.StatusOK {
		t.Fatalf("after restart %d %s", status, body)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.CoverageStatus != "full" || view.FindingsCount != 0 || view.Report != "coverage full" {
		t.Fatalf("after restart %+v", view)
	}
}

func TestM8ParseFinishReportCoverage(t *testing.T) {
	rep, ok := parseFinishReport([]byte(`{"coverageStatus":"full","findingsCount":0,"summary":"clean","report":"body"}`))
	if !ok || rep.FindingsCount != 0 || rep.CoverageStatus != "full" || rep.Body != "body" {
		t.Fatalf("parsed %+v %v", rep, ok)
	}
	if _, ok := parseFinishReport([]byte("done")); ok {
		t.Fatal("plain summary is not a report")
	}
	if _, ok := parseFinishReport([]byte(`{"summary":"clean","findingsCount":0}`)); ok {
		t.Fatal("zero findings without coverage is not a report")
	}
}

func (e *env) assertStoredReport(t *testing.T, invocationID string) {
	t.Helper()
	var coverage, body string
	var findings int
	err := e.raw.QueryRow(context.Background(), `
		SELECT coverage_status, findings_count, convert_from(report_body, 'UTF8')
		FROM invocation_results WHERE invocation_id = $1`, invocationID).Scan(&coverage, &findings, &body)
	if err != nil {
		t.Fatal(err)
	}
	if coverage != "full" || findings != 0 || body != "coverage full" {
		t.Fatalf("stored coverage %q findings %d body %q", coverage, findings, body)
	}
}

func (e *env) getInvocationResult(t *testing.T, factory, invocationID string, auth bool) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.base+"/api/v1/factory/"+factory+"/invocations/"+invocationID+"/result", nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body
}

func writeReportDefinition(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "factory.yaml"), `
schemaVersion: v1alpha1
name: scan
agentDefaults:
  model: auto
  runner: local
  pool: standard
pipeline:
  stages: [triage]
  agents:
    triage: scanner
`)
	writeAgent(t, root, "scanner", "REVIEW", "model: test-model\nrequires:\n  - report")
	return root
}
