package server

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/client"
	"github.com/polds/factory/internal/harness"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

const m9Canary = "CANARY-OLD"

// TestM9NativeToolsE2E boots the real server with Linear absent and drives
// coordinator, lead, and two specialists through the adapter tools.
func TestM9NativeToolsE2E(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
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

	fac, err := env.srv.factory(ctx, "pipe")
	if err != nil {
		t.Fatal(err)
	}
	item, err := env.srv.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID, Source: store.SourceManual, Title: "native tools", Stage: store.StagePlanning,
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.srv.store.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID: fac.ID, ProfileName: "scan", SourceOccurrenceKey: "native:tools:1",
		CanonicalDigest: "digest-tools", DefinitionDigest: "def", State: invocation.StateAccepted,
		Intent: "new", Priority: "interactive", WorkItemID: item.ID, NeutralStage: true,
		PrincipalJSON: json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	})
	if err != nil || !row.Created {
		t.Fatalf("invocation %+v %v", row, err)
	}
	agents, err := env.srv.store.ListAgents(ctx, fac.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("agents %+v %v", agents, err)
	}
	c := &client.Client{BaseURL: env.base, APIKey: env.apiKey}
	req := clientProofRequest()
	req.Slots = 1
	req.AgentID = agents[0].ID
	req.WorkItemID = item.ID
	req.Stage = store.StagePlanning
	opened, err := c.OpenNativeTree(ctx, "pipe", row.Row.ID, req)
	if err != nil || opened.StageOwner != "coordinator" || opened.RootTaskID == "" {
		t.Fatalf("open %+v %v", opened, err)
	}
	root, err := c.GetNativeTask(ctx, "pipe", opened.RootTaskID)
	if err != nil || root.LinearIssue != nil {
		t.Fatalf("root %+v %v", root, err)
	}

	coordTools := harness.NativeTools{API: c, Needles: []string{m9Canary}}
	leadTools := harness.NativeTools{API: c, Needles: []string{m9Canary}}
	specATools := harness.NativeTools{API: c, Needles: []string{m9Canary}}
	specBTools := harness.NativeTools{API: c, Needles: []string{m9Canary}}
	full := []string{
		harness.GrantInboxRead, harness.GrantAckWrite, harness.GrantTaskRead,
		harness.GrantTaskWrite, harness.GrantCommentWrite, harness.GrantRosterDispatch,
	}
	worker := []string{
		harness.GrantInboxRead, harness.GrantAckWrite, harness.GrantTaskRead,
		harness.GrantTaskWrite, harness.GrantCommentWrite,
	}
	coord := m9Token(t, opened.RootTaskID, row.Row.ID, harness.RoleOrchestrator, full)
	coordRun := m9Dispatch(t, coordTools, coord, "native_activate", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID,
	}).Payload
	if coordRun == "" {
		t.Fatal("coordinator run is empty")
	}
	if _, err := coordTools.Dispatch(ctx, coord, "native_delegate", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID,
		"assignee": "specialist-a", "idempotencyKey": "skip-specialist",
	}); err == nil || !strings.Contains(err.Error(), "delegation_denied") {
		t.Fatalf("coordinator delegate specialist: %v", err)
	}
	m9StillOpen(t, env, item.ID)
	if n := m9NodeCount(t, env); n != 1 {
		t.Fatalf("refusal inserted a child: %d nodes", n)
	}

	leadID := m9Dispatch(t, coordTools, coord, "native_delegate", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID,
		"assignee": "lead", "idempotencyKey": "lead",
	}).Payload
	parked := m9Dispatch(t, coordTools, coord, "native_wait", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID, "blockerIds": leadID,
	})
	status, runID, unresolved := m9Joined(t, parked.Payload)
	if status != store.NativeParked || runID != "" || unresolved != 1 {
		t.Fatalf("coordinator park %+v", parked)
	}
	root, err = c.GetNativeTask(ctx, "pipe", opened.RootTaskID)
	if err != nil || !containsID(root.BlockerIDs, leadID) || root.Status != store.NativeParked {
		t.Fatalf("parked root %+v %v", root, err)
	}
	m9StillOpen(t, env, item.ID)

	leadTok := m9Token(t, leadID, row.Row.ID, harness.RoleLead, full)
	leadRun := m9Dispatch(t, leadTools, leadTok, "native_activate", map[string]string{
		"taskId": leadID, "invocationId": row.Row.ID,
	}).Payload
	specA := m9Dispatch(t, leadTools, leadTok, "native_delegate", map[string]string{
		"taskId": leadID, "invocationId": row.Row.ID,
		"assignee": "specialist-a", "idempotencyKey": "spec-a",
	}).Payload
	specB := m9Dispatch(t, leadTools, leadTok, "native_delegate", map[string]string{
		"taskId": leadID, "invocationId": row.Row.ID,
		"assignee": "specialist-b", "idempotencyKey": "spec-b",
	}).Payload
	leadPark := m9Dispatch(t, leadTools, leadTok, "native_wait", map[string]string{
		"taskId": leadID, "invocationId": row.Row.ID, "blockerIds": specA + "," + specB,
	})
	status, runID, unresolved = m9Joined(t, leadPark.Payload)
	if status != store.NativeParked || runID != "" || unresolved != 2 {
		t.Fatalf("lead park %+v", leadPark)
	}

	specATok := m9Token(t, specA, row.Row.ID, harness.RoleSpecialist, worker)
	specBTok := m9Token(t, specB, row.Row.ID, harness.RoleSpecialist, worker)
	specARun := m9Dispatch(t, specATools, specATok, "native_activate", map[string]string{
		"taskId": specA, "invocationId": row.Row.ID,
	}).Payload
	m9Execute(t, env, specARun)
	m9Dispatch(t, specATools, specATok, "native_complete", map[string]string{
		"taskId": specA, "invocationId": row.Row.ID,
	})
	m9StillOpen(t, env, item.ID)
	specBRun := m9Dispatch(t, specBTools, specBTok, "native_activate", map[string]string{
		"taskId": specB, "invocationId": row.Row.ID,
	}).Payload
	m9Execute(t, env, specBRun)
	m9Dispatch(t, specBTools, specBTok, "native_complete", map[string]string{
		"taskId": specB, "invocationId": row.Row.ID,
	})
	m9StillOpen(t, env, item.ID)

	woke := m9Dispatch(t, leadTools, leadTok, "native_wait", map[string]string{
		"taskId": leadID, "invocationId": row.Row.ID,
	})
	status, runID, unresolved = m9Joined(t, woke.Payload)
	if status != store.NativeRunning || runID != leadRun || unresolved != 0 {
		t.Fatalf("lead join wake %+v want run %s", woke, leadRun)
	}
	m9Execute(t, env, leadRun)
	m9Dispatch(t, leadTools, leadTok, "native_complete", map[string]string{
		"taskId": leadID, "invocationId": row.Row.ID,
	})
	m9StillOpen(t, env, item.ID)

	coordJoin := m9Dispatch(t, coordTools, coord, "native_wait", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID,
	})
	status, runID, unresolved = m9Joined(t, coordJoin.Payload)
	if status != store.NativeRunning || runID != coordRun || unresolved != 0 {
		t.Fatalf("coordinator join %+v want run %s", coordJoin, coordRun)
	}

	comment := m9Dispatch(t, coordTools, coord, "native_comment", map[string]string{
		"taskId": opened.RootTaskID, "body": "note " + m9Canary,
	})
	inbox := m9Dispatch(t, coordTools, coord, "native_inbox_read", map[string]string{
		"assignee": "coordinator",
	})
	if strings.Contains(inbox.Payload, ",") || inbox.Payload == "" {
		t.Fatalf("inbox %q", inbox.Payload)
	}
	m9Dispatch(t, coordTools, coord, "native_apply", map[string]string{
		"taskId": opened.RootTaskID, "messageId": comment.Payload,
	})
	acked := m9Dispatch(t, coordTools, coord, "native_ack", map[string]string{
		"taskId": opened.RootTaskID, "deliveryId": inbox.Payload,
	})
	if acked.Payload != inbox.Payload {
		t.Fatalf("ack %q inbox %q", acked.Payload, inbox.Payload)
	}

	m9Execute(t, env, coordRun)
	m9Dispatch(t, coordTools, coord, "native_complete", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID,
	})
	done := m9Item(t, env, item.ID)
	if done.Status != store.WorkItemComplete || done.Stage != store.StagePlanning {
		t.Fatalf("owner completion left %+v", done)
	}

	linked := m9Dispatch(t, coordTools, coord, "native_wait", map[string]string{
		"taskId": opened.RootTaskID, "invocationId": row.Row.ID,
	})
	linkedID := strings.TrimPrefix(linked.Payload, "linked ")
	if linkedID == "" || linkedID == linked.Payload || linkedID == coordRun {
		t.Fatalf("completed wake %+v prior %s", linked, coordRun)
	}
	var priorStatus, evidence, parent string
	if err := env.raw.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, coordRun).Scan(&priorStatus); err != nil {
		t.Fatal(err)
	}
	if err := env.raw.QueryRow(ctx, `SELECT status FROM native_terminal_evidence WHERE run_id = $1`, coordRun).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if err := env.raw.QueryRow(ctx, `SELECT COALESCE(parent_run_id, '') FROM runs WHERE id = $1`, linkedID).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if priorStatus != store.StatusSucceeded || evidence != priorStatus || parent != coordRun {
		t.Fatalf("terminal prior %s evidence %s parent %s", priorStatus, evidence, parent)
	}
	still := m9Item(t, env, item.ID)
	if still.Status != store.WorkItemComplete || still.Stage != store.StagePlanning {
		t.Fatalf("wake moved the work item %+v", still)
	}
	if n := m9NodeCount(t, env); n != 4 {
		t.Fatalf("join synthesized tasks: %d nodes", n)
	}
}

func m9Token(t *testing.T, taskID, invocationID, role string, grants []string) harness.RunToken {
	t.Helper()
	tok, err := harness.IssueRunToken(context.Background(), harness.TokenScope{
		FactoryID:    "pipe",
		TaskID:       taskID,
		InvocationID: invocationID,
		Role:         role,
		Grants:       grants,
		BudgetCents:  50,
	}, harness.Attenuation{})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func m9Execute(t *testing.T, env *env, runID string) {
	t.Helper()
	ctx := context.Background()
	leased, err := env.srv.store.Lease(ctx, "default", time.Minute, 1)
	if err != nil || len(leased) != 1 || leased[0].RunID != runID {
		t.Fatalf("lease %+v %v want %s", leased, err, runID)
	}
	if _, err := env.srv.store.StartExecution(ctx, runID, "m9-worker", tokenHash(runID)); err != nil {
		t.Fatal(err)
	}
}

func m9Dispatch(t *testing.T, tools harness.NativeTools, tok harness.RunToken, name string, args map[string]string) harness.ToolResult {
	t.Helper()
	got, err := tools.Dispatch(context.Background(), tok, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if got.Evidence != harness.EvidenceProduction {
		t.Fatalf("%s evidence %q", name, got.Evidence)
	}
	if strings.Contains(got.Payload, m9Canary) {
		t.Fatalf("%s leaked", name)
	}
	return got
}

func m9Joined(t *testing.T, payload string) (status, runID string, unresolved int) {
	t.Helper()
	rest, ok := strings.CutPrefix(payload, "joined status=")
	if !ok {
		t.Fatalf("join payload %q", payload)
	}
	status, rest, ok = strings.Cut(rest, " run=")
	if !ok {
		t.Fatalf("join payload %q", payload)
	}
	runID, rest, ok = strings.Cut(rest, " unresolved=")
	if !ok {
		t.Fatalf("join payload %q", payload)
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		t.Fatal(err)
	}
	return status, runID, n
}

func m9Item(t *testing.T, env *env, id string) store.WorkItem {
	t.Helper()
	item, err := env.srv.store.GetWorkItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func m9StillOpen(t *testing.T, env *env, id string) {
	t.Helper()
	item := m9Item(t, env, id)
	if item.Status != store.WorkItemOpen || item.Stage != store.StagePlanning {
		t.Fatalf("outer stage moved %+v", item)
	}
}

func m9NodeCount(t *testing.T, env *env) int {
	t.Helper()
	views, err := env.srv.ListNativeTrees(context.Background(), "pipe")
	if err != nil || len(views) != 1 {
		t.Fatalf("trees %+v %v", views, err)
	}
	return len(views[0].Nodes)
}

func containsID(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}
