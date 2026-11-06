package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	nativescan "github.com/polds/factory/deploy/native-scan"
	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/normalize/linear"
	"github.com/polds/factory/internal/outbound"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

// TestM10OfflineAcceptance drives scheduled and pull-request scans through
// the server and harness. It fails when Postgres is unset.
func TestM10OfflineAcceptance(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeReportDefinition})
	if env.srv.cfg.LinearAPIKey != "" || env.srv.cfg.LinearWebhookSecret != "" || env.srv.cfg.LinearAPIBase != "" {
		t.Fatal("linear is configured")
	}
	ctx := context.Background()
	var linearCols int
	if err := env.raw.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'native_tasks'
		  AND column_name = 'linear_issue'`).Scan(&linearCols); err != nil {
		t.Fatal(err)
	}
	if linearCols != 0 {
		t.Fatalf("linear_issue columns %d", linearCols)
	}
	env.srv.PinTarget = func(ctx context.Context, target *invocation.Target) (string, string, error) {
		if target != nil && target.PullRequest > 0 {
			return nativescan.HeadSHA, nativescan.BaseSHA, nil
		}
		return env.srv.pinTargetProduction(ctx, target)
	}

	def, digest, err := nativescan.LoadDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if def.Linear != nil || def.DefaultSHA != nativescan.DefaultSHA {
		t.Fatalf("definition %+v", def)
	}
	sch := m10Schedule(t, env)
	w := dialWorker(t, env, "scan-m10")
	defer w.close()

	slotA := m10Admit(t, env, m10Request(sch.ID, nativescan.SlotA, &invocation.Target{
		Repository: "polds/synthetic",
		Ref:        nativescan.DefaultSHA,
	}))
	m10NativeBrief(t, env, slotA.InvocationID)
	m10Drive(t, env, w, slotA, 0, "findings=0 "+digest)
	if slotA.PinnedHeadSHA != nativescan.DefaultSHA || slotA.PinnedBaseSHA != "" {
		t.Fatalf("slot pin %+v", slotA)
	}

	slotB := m10Admit(t, env, m10Request(sch.ID, nativescan.SlotB, &invocation.Target{
		Repository: "polds/synthetic",
		Ref:        nativescan.DefaultSHA,
	}))
	if slotB.InvocationID == slotA.InvocationID || slotB.State != invocation.StateAccepted || !slotB.CountsAsCompleted {
		t.Fatalf("second slot %+v", slotB)
	}
	m10NativeBrief(t, env, slotB.InvocationID)
	m10Drive(t, env, w, slotB, 0, "findings=0 "+digest)
	if slotB.PinnedHeadSHA != nativescan.DefaultSHA || slotA.DefinitionDigest != slotB.DefinitionDigest {
		t.Fatalf("digests %s %s pin %s", slotA.DefinitionDigest, slotB.DefinitionDigest, slotB.PinnedHeadSHA)
	}

	pr := m10Admit(t, env, invocation.Request{
		SourceOccurrenceKey: "pr-input:1",
		Intent:              "new",
		Priority:            "interactive",
		Profile:             invocation.Profile{Name: "scanner"},
		Target: &invocation.Target{
			Repository:  "polds/synthetic",
			PullRequest: 2213,
		},
	})
	if pr.PinnedHeadSHA != nativescan.HeadSHA || pr.PinnedBaseSHA != nativescan.BaseSHA || pr.DefinitionDigest != slotA.DefinitionDigest {
		t.Fatalf("pr %+v", pr)
	}
	m10NativeBrief(t, env, pr.InvocationID)
	m10Drive(t, env, w, pr, 0, "findings=0 "+digest)

	env.srv.bridge.SetEnabled(true)
	env.srv.bridge.SetSendAuthorized(true)
	env.srv.bridge.SetOutage(true)
	planted := m10Admit(t, env, invocation.Request{
		SourceOccurrenceKey: "planted:1",
		Intent:              "new",
		Priority:            "interactive",
		Profile:             invocation.Profile{Name: "scanner"},
		Target: &invocation.Target{
			Repository: "polds/synthetic",
			Ref:        nativescan.DefaultSHA,
		},
	})
	m10NativeBrief(t, env, planted.InvocationID)
	m10Drive(t, env, w, planted, 1, "synthetic-finding")
	if env.srv.bridge.SendCount() != 0 {
		t.Fatalf("sendCount %d", env.srv.bridge.SendCount())
	}
	var sent int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM bridge_outbox WHERE sent`).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("sent rows %d", sent)
	}
	var queued int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM bridge_outbox WHERE idempotency_key LIKE 'report:%'`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 4 {
		t.Fatalf("queued reports %d", queued)
	}
	var prs int
	if err := env.raw.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE open_pr_url <> ''`).Scan(&prs); err != nil {
		t.Fatal(err)
	}
	if prs != 0 {
		t.Fatalf("pull requests %d", prs)
	}
}

// TestBridgeRestart proves a new server does not send a key the previous
// process already sent. It fails when Postgres is unset.
func TestBridgeRestart(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Fatal("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: writeReportDefinition})
	env.srv.bridge.SetEnabled(true)
	env.srv.bridge.SetSendAuthorized(true)
	sent := env.srv.bridge.Enqueue(outbound.BridgeNotice{IdempotencyKey: "report:restart", Summary: "findings=0"})
	if !sent.Sent || sent.SendCount != 1 || env.srv.bridge.SendCount() != 1 {
		t.Fatalf("first %+v count %d", sent, env.srv.bridge.SendCount())
	}
	env.restart(t)
	env.srv.bridge.SetEnabled(true)
	env.srv.bridge.SetSendAuthorized(true)
	again := env.srv.bridge.Enqueue(outbound.BridgeNotice{IdempotencyKey: "report:restart"})
	if again.Sent || again.Suppressed != "replay" || !again.ReplaySuppressed || env.srv.bridge.SendCount() != 1 {
		t.Fatalf("restart %+v count %d", again, env.srv.bridge.SendCount())
	}
	echo := env.srv.bridge.Enqueue(outbound.BridgeNotice{Binding: sent.Binding})
	if echo.Suppressed != "echo" || echo.Sent || env.srv.bridge.SendCount() != 1 {
		t.Fatalf("echo %+v count %d", echo, env.srv.bridge.SendCount())
	}
}

func m10Schedule(t *testing.T, env *env) store.Schedule {
	t.Helper()
	var factoryID string
	if err := env.raw.QueryRow(context.Background(), `SELECT id FROM factories WHERE name = 'scan'`).Scan(&factoryID); err != nil {
		t.Fatal(err)
	}
	sch, err := env.srv.store.UpsertSchedule(context.Background(), store.Schedule{
		FactoryID:  factoryID,
		Automation: "nightly",
		Name:       "nightly-" + factoryID,
		Cron:       "0 0 * * 1",
		NextFireAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func m10Request(scheduleID, slot string, target *invocation.Target) invocation.Request {
	return invocation.Request{
		SourceOccurrenceKey: invocation.ScheduleOccurrenceKey(scheduleID, slot),
		Intent:              "new",
		Priority:            "batch",
		Profile:             invocation.Profile{Name: "scanner"},
		ScheduleID:          scheduleID,
		DueSlot:             slot,
		OverlapPolicy:       "forbid",
		Target:              target,
	}
}

func m10Admit(t *testing.T, env *env, req invocation.Request) invocation.Receipt {
	t.Helper()
	kind := "api"
	if req.ScheduleID != "" {
		kind = "schedule"
	}
	rec, err := env.srv.AdmitInvocation(context.Background(), "scan", invocation.Principal{
		Kind:  kind,
		Actor: kind,
		Allow: true,
	}, req)
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != invocation.StateAccepted || rec.WorkItemID == "" || !rec.CountsAsCompleted {
		t.Fatalf("receipt %+v", rec)
	}
	return rec
}

func m10NativeBrief(t *testing.T, env *env, invocationID string) {
	t.Helper()
	task, err := env.srv.CreateNativeTask(context.Background(), "scan", api.NativeTaskRequest{
		InvocationID: invocationID,
		Assignee:     "scanner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.LinearIssue != nil || task.TaskRef != task.ID {
		t.Fatalf("task %+v", task)
	}
	brief, err := linear.SyntheticNativeBrief(task.TaskRef)
	if err != nil {
		t.Fatal(err)
	}
	if brief.LinearIssue != nil || brief.RequiresLinear || brief.TaskRef != task.TaskRef || brief.Mode != "native" {
		t.Fatalf("brief %+v", brief)
	}
}

func m10Drive(t *testing.T, env *env, w *wsWorker, rec invocation.Receipt, findings int, body string) {
	t.Helper()
	asg := w.readAssignment(t)
	ws := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "task.txt"), []byte(asg.TaskID), 0o644); err != nil {
		t.Fatal(err)
	}
	summary := body
	if findings == 0 {
		summary = "no findings"
	}
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		fmt.Sprintf(`{"type":"report","ts":"2026-11-06T04:00:00Z","summary":%q,"coverageStatus":"recorded","findingsCount":%d,"report":%q}`, summary, findings, body),
	})
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "scan-m10"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: "done",
	})
	env.waitStatus(t, asg.TaskID, "succeeded")
	if reason := env.runDetail(t, asg.TaskID).FailureReason; reason != "" {
		t.Fatalf("failure_reason %q", reason)
	}
	m10WaitWorkItem(t, env, rec.WorkItemID, store.WorkItemComplete)
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("workspace remains: %v", err)
	}
	if err := env.srv.store.DeleteInvocationWorkspace(context.Background(), rec.InvocationID); err != nil {
		t.Fatal(err)
	}
	m10Result(t, env, rec.InvocationID, findings, body)
}

func m10WaitWorkItem(t *testing.T, env *env, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if err := env.raw.QueryRow(context.Background(), `SELECT status FROM work_items WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("work item %s status %s", id, got)
}

func m10Result(t *testing.T, env *env, invocationID string, findings int, body string) {
	t.Helper()
	status, raw := env.getInvocationResult(t, "scan", invocationID, true)
	if status != http.StatusOK {
		t.Fatalf("result %d %s", status, raw)
	}
	var view struct {
		Summary        string `json:"summary"`
		CoverageStatus string `json:"coverageStatus"`
		FindingsCount  int    `json:"findingsCount"`
		Report         string `json:"report"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if view.CoverageStatus != "recorded" || view.FindingsCount != findings || view.Report != body {
		t.Fatalf("result %+v", view)
	}
	unauth, unauthBody := env.getInvocationResult(t, "scan", invocationID, false)
	if unauth != http.StatusForbidden || len(unauthBody) != 0 {
		t.Fatalf("unauthorized %d %q", unauth, unauthBody)
	}
}
