package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/defsync"
	"github.com/polds/factory/internal/protocol"
	"github.com/polds/factory/internal/store"
)

func TestApprovalGateHiddenUntilStatusCommits(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	task := env.createTask(t, "Ship the gate", "body")
	observed := make(chan struct {
		status  string
		pending int
	}, 1)
	env.srv.store.SetOpenGateHookForTest(func(ctx context.Context) error {
		var status string
		var pending int
		if err := env.raw.QueryRow(ctx, `SELECT status FROM work_items WHERE id = $1`, task.ID).Scan(&status); err != nil {
			return err
		}
		if err := env.raw.QueryRow(ctx, `
			SELECT count(*) FROM approvals
			WHERE work_item_id = $1 AND decision = ''`, task.ID).Scan(&pending); err != nil {
			return err
		}
		select {
		case observed <- struct {
			status  string
			pending int
		}{status, pending}:
		default:
		}
		return nil
	})
	t.Cleanup(func() { env.srv.store.SetOpenGateHookForTest(nil) })

	finishStage(t, w, "triage-done")
	finishStage(t, w, "planning-done")
	blocked := env.waitTask(t, task.ID, "awaiting_approval", "planning")
	if blocked.PendingGate != "spec" {
		t.Fatalf("pending_gate %q", blocked.PendingGate)
	}
	select {
	case obs := <-observed:
		if obs.status != store.WorkItemOpen || obs.pending != 0 {
			t.Fatalf("gate visible before commit: status %q pending %d", obs.status, obs.pending)
		}
	default:
		t.Fatal("approval row and awaiting_approval were not written in one transaction")
	}
}

func TestE2EWorkItemApprovalPipeline(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	assertSchemaUnauthenticated(t, env)

	w := dialWorker(t, env, "local-1")
	defer w.close()

	task := env.createTask(t, "Ship the gate", "body carries "+plantedGH)
	if task.ID == "" || task.ConversationID == "" || task.Stage != "triage" || task.Status != "open" {
		t.Fatalf("created %+v", task)
	}
	if task.PendingGate != "" {
		t.Fatalf("pending gate %q before a run finished", task.PendingGate)
	}

	triage := finishStage(t, w, "triage-done")
	if triage.EnvVars["FACTORY_STAGE"] != "triage" || triage.EnvVars["FACTORY_WORK_ITEM_ID"] != task.ID {
		t.Fatalf("triage env %+v", triage.EnvVars)
	}
	if triage.EnvVars["FACTORY_CONVERSATION_ID"] != task.ConversationID {
		t.Fatalf("conversation %s", triage.EnvVars["FACTORY_CONVERSATION_ID"])
	}
	if modelOf(triage) != "triage-model" {
		t.Fatalf("router resolved to %q", modelOf(triage))
	}
	if strings.Contains(triage.EnvVars["FACTORY_TASK_PROMPT"], plantedGH) {
		t.Fatal("handoff prompt contains a secret value")
	}

	planning := finishStage(t, w, "planning-done")
	if planning.EnvVars["FACTORY_STAGE"] != "planning" {
		t.Fatalf("stage %s", planning.EnvVars["FACTORY_STAGE"])
	}
	blocked := env.waitTask(t, task.ID, "awaiting_approval", "planning")
	if blocked.PendingGate != "spec" {
		t.Fatalf("pending_gate %q", blocked.PendingGate)
	}
	if n := env.countStage(t, task.ID, "building"); n != 0 {
		t.Fatalf("building runs while the gate is open: %d", n)
	}
	approvals := env.listApprovals(t)
	if len(approvals) != 1 || approvals[0].Gate != "spec" || approvals[0].WorkItemID != task.ID || approvals[0].Decision != "" {
		t.Fatalf("approvals %+v", approvals)
	}
	env.assertRunLinkage(t, task)

	first := env.decide(t, approvals[0].ID, "approve", http.StatusOK)
	if first.Decision != "approve" || first.ID != approvals[0].ID {
		t.Fatalf("decide %+v", first)
	}
	repeat := env.decide(t, approvals[0].ID, "approve", http.StatusOK)
	if repeat.ID != first.ID || repeat.Decision != "approve" {
		t.Fatalf("repeat decide %+v", repeat)
	}
	if code, _ := env.decideRaw(t, approvals[0].ID, "reject"); code != http.StatusConflict {
		t.Fatalf("different decision status %d", code)
	}

	building := w.readAssignment(t)
	if building.EnvVars["FACTORY_STAGE"] != "building" || building.EnvVars["FACTORY_WORK_ITEM_ID"] != task.ID {
		t.Fatalf("building env %+v", building.EnvVars)
	}
	prompt := building.EnvVars["FACTORY_TASK_PROMPT"]
	if !strings.Contains(prompt, "triage-done") || !strings.Contains(prompt, "planning-done") {
		t.Fatal("building prompt is missing prior stage output")
	}
	if strings.Contains(prompt, plantedGH) || strings.Contains(prompt, plantedOpenAI) {
		t.Fatal("building prompt contains a secret value")
	}
	link := env.runLink(t, building.TaskID)
	planLink := env.runLink(t, planning.TaskID)
	if link.Parent != planning.TaskID || link.WorkItem != task.ID || link.Model != "building-model" {
		t.Fatalf("building link %+v planning %s", link, planning.TaskID)
	}
	if link.Conversation == "" || link.Conversation != planLink.Conversation || link.Conversation != task.ConversationID {
		t.Fatalf("conversation building %s planning %s task %s", link.Conversation, planLink.Conversation, task.ConversationID)
	}
	if planLink.Parent != triage.TaskID {
		t.Fatalf("planning parent %s, triage %s", planLink.Parent, triage.TaskID)
	}

	completeAssignment(t, w, building, "building-done")
	review := finishStage(t, w, "review-done")
	if review.EnvVars["FACTORY_STAGE"] != "reviewing" || modelOf(review) != "reviewing-model" {
		t.Fatalf("review stage %s model %s", review.EnvVars["FACTORY_STAGE"], modelOf(review))
	}
	done := env.waitTask(t, task.ID, "complete", "human_handoff")
	if done.PendingGate != "" {
		t.Fatalf("pending gate on a complete item %q", done.PendingGate)
	}
	env.forbidSecrets(t, triage.EnvVars["FACTORY_RUN_TOKEN"])
	env.forbidSecrets(t, building.EnvVars["FACTORY_RUN_TOKEN"])
}

func TestE2EApprovalRejectSkipsBuilding(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	task := env.createTask(t, "Reject the spec", "no secret here")
	finishStage(t, w, "triage-done")
	finishStage(t, w, "planning-done")
	blocked := env.waitTask(t, task.ID, "awaiting_approval", "planning")
	if blocked.PendingGate != "spec" {
		t.Fatalf("pending_gate %q", blocked.PendingGate)
	}
	approvals := env.listApprovals(t)
	if len(approvals) != 1 {
		t.Fatalf("approvals %+v", approvals)
	}
	decided := env.decide(t, approvals[0].ID, "reject", http.StatusOK)
	if decided.Decision != "reject" {
		t.Fatalf("decision %+v", decided)
	}
	next := w.readAssignment(t)
	if next.EnvVars["FACTORY_STAGE"] != "planning" {
		t.Fatalf("reject resumed stage %s", next.EnvVars["FACTORY_STAGE"])
	}
	if env.countStage(t, task.ID, "building") != 0 {
		t.Fatal("reject started a building run")
	}
	resumed := env.getTask(t, task.ID)
	if resumed.Status != "open" || resumed.Stage != "planning" || resumed.PendingGate != "" {
		t.Fatalf("after reject %+v", resumed)
	}
}

func TestE2ERevisionLimitEscalates(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 1)
	}})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	task := env.createTask(t, "Revise once", "scope")
	finishStage(t, w, "triage-done")
	finishStage(t, w, "planning-done")
	env.waitTask(t, task.ID, "awaiting_approval", "planning")
	approvals := env.listApprovals(t)
	env.decide(t, approvals[0].ID, "approve", http.StatusOK)

	first := w.readAssignment(t)
	if first.EnvVars["FACTORY_STAGE"] != "building" {
		t.Fatalf("stage %s", first.EnvVars["FACTORY_STAGE"])
	}
	completeAssignment(t, w, first, "build-1")
	failStage(t, w, "needs changes")

	second := w.readAssignment(t)
	if second.EnvVars["FACTORY_STAGE"] != "building" || second.TaskID == first.TaskID {
		t.Fatalf("revision run %+v", second.TaskID)
	}
	if !strings.Contains(second.EnvVars["FACTORY_TASK_PROMPT"], "needs changes") {
		t.Fatal("revision prompt is missing the review summary")
	}
	completeAssignment(t, w, second, "build-2")
	failStage(t, w, "still wrong")

	// The second review is already open/reviewing at revision 1. Escalation
	// is the moment the count passes the limit and no further build starts.
	escalated := env.waitRevision(t, task.ID, 2)
	if escalated.Status != "open" || escalated.Stage != "reviewing" || escalated.PendingGate != "" {
		t.Fatalf("escalated %+v", escalated)
	}
	if n := env.countStage(t, task.ID, "building"); n != 2 {
		t.Fatalf("building runs %d, want 2", n)
	}
}

func TestE2EWorkItemAskInputReleasesSlot(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{
		definition: func(t *testing.T) string {
			return writePipeline(t, 3)
		},
		maxConcurrent: 1,
	})
	w := dialWorker(t, env, "local-1")
	defer w.close()

	task := env.createTask(t, "Ask a question", "which env?")
	asg := w.readAssignment(t)
	if asg.EnvVars["FACTORY_STAGE"] != "triage" {
		t.Fatalf("stage %s", asg.EnvVars["FACTORY_STAGE"])
	}
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"Which staging env?"}`,
	})
	waiting := env.waitTask(t, task.ID, "awaiting_input", "triage")
	if waiting.PendingGate != "" {
		t.Fatalf("question opened a gate %q", waiting.PendingGate)
	}
	if st := env.status(t, asg.TaskID); st == "succeeded" || st == "failed" || st == "cancelled" {
		t.Fatalf("logical run status %s", st)
	}

	other := env.startFactoryRun(t, "pipe", "implementer", "unrelated")
	next := w.readAssignment(t)
	if next.TaskID != other {
		t.Fatalf("slot stayed held: assigned %s, want %s", next.TaskID, other)
	}
	env.forbidSecrets(t, asg.EnvVars["FACTORY_RUN_TOKEN"])
}

func TestE2EAskInputSurvivesRestart(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	const question = "Which staging env?"
	const answer = "staging-2"
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")

	task := env.createTask(t, "Ask across restart", "which env?")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"` + question + `"}`,
	})
	waiting := env.waitTask(t, task.ID, "awaiting_input", "triage")
	if waiting.ConversationID == "" {
		t.Fatal("work item has no conversation")
	}
	env.assertSuspended(t, task.ID, asg.TaskID, question)

	w.close()
	env.waitNotRequeued(t, asg.TaskID)
	if _, err := env.srv.store.ExpireLeases(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	env.waitNotRequeued(t, asg.TaskID)

	env.restart(t)
	// The restarted loop expires leases on its own. ExpireLeases is the same
	// call, on the pool, so the test does not share the leader connection.
	if _, err := env.srv.store.ExpireLeases(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	still := env.getTask(t, task.ID)
	if still.Status != "awaiting_input" || still.ConversationID != waiting.ConversationID {
		t.Fatalf("after restart %+v", still)
	}
	env.assertSuspended(t, task.ID, asg.TaskID, question)
	env.waitNotRequeued(t, asg.TaskID)

	w2 := dialWorker(t, env, "local-2")
	defer w2.close()
	req, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "peter")
	if err != nil {
		t.Fatal(err)
	}
	again, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "someone")
	if err != nil || again.ID != req.ID || again.Answer != answer {
		t.Fatalf("replay %+v %v", again, err)
	}
	if _, err := env.srv.AnswerInput(context.Background(), task.ID, "a different answer", "peter"); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("conflict %v", err)
	}
	resumed := env.getTask(t, task.ID)
	if resumed.Status != "open" || resumed.ConversationID != waiting.ConversationID {
		t.Fatalf("resumed %+v", resumed)
	}
	next := w2.readAssignment(t)
	if next.TaskID != asg.TaskID {
		t.Fatalf("resume run %s, want %s", next.TaskID, asg.TaskID)
	}
	if next.EnvVars["FACTORY_CONVERSATION_ID"] != waiting.ConversationID {
		t.Fatalf("conversation %s", next.EnvVars["FACTORY_CONVERSATION_ID"])
	}
	prompt := next.EnvVars["FACTORY_TASK_PROMPT"]
	if !strings.Contains(prompt, question) || !strings.Contains(prompt, answer) {
		t.Fatal("resumed prompt is missing the question and answer")
	}
	var attempts int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("executions %d, want 2", attempts)
	}
}

func (e *env) assertSuspended(t *testing.T, workItemID, runID, question string) {
	t.Helper()
	var gotQuestion, execState, runStatus string
	var ended *time.Time
	err := e.raw.QueryRow(context.Background(), `
		SELECT r.question, ex.state, ex.ended_at, run.status
		FROM input_requests r
		JOIN runs run ON run.id = r.run_id
		JOIN executions ex ON ex.id = r.execution_id
		WHERE r.work_item_id = $1 AND r.answered_at IS NULL`, workItemID).Scan(&gotQuestion, &execState, &ended, &runStatus)
	if err != nil {
		t.Fatalf("pending input: %v", err)
	}
	if gotQuestion != question {
		t.Fatalf("question %q", gotQuestion)
	}
	if execState == "assigned" || execState == "running" || execState == "failed" || ended == nil {
		t.Fatalf("execution state %s ended %v", execState, ended != nil)
	}
	if runStatus == "queued" || runStatus == "succeeded" || runStatus == "failed" || runStatus == "cancelled" {
		t.Fatalf("run status %s", runStatus)
	}
	var suspended string
	if err := e.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, workItemID).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended != runID {
		t.Fatalf("suspended run %s", suspended)
	}
	if n := e.queueRows(t, runID); n != 0 {
		t.Fatalf("queue rows %d", n)
	}
}

func (e *env) waitNotRequeued(t *testing.T, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := e.status(t, runID)
		if st == "queued" || st == "succeeded" || st == "failed" || st == "cancelled" {
			t.Fatalf("run requeued or finished: %s", st)
		}
		if e.queueRows(t, runID) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s still queued", runID)
}

func (e *env) restart(t *testing.T) {
	t.Helper()
	cfg := e.srv.cfg
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
	srv, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	srv.cfg.TaskServerURL = ts.URL
	e.srv = srv
	e.base = ts.URL
}

func TestDefinitionSyncKeepsLastGood(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	good := &dirCloner{
		sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		write: func(dest string) error {
			return writeNamedFactory(dest, "m2", true)
		},
	}
	env := boot(t, bootOpts{
		gitURL:      "https://example.invalid/factory.git",
		gitRef:      "main",
		gitInterval: time.Hour,
		gitCloner:   good,
	})
	env.waitAgent(t, "extra")

	bad := &dirCloner{sha: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", write: func(dest string) error {
		return os.WriteFile(filepath.Join(dest, "factory.yaml"), []byte("name: m2\nnotAKey: true\n"), 0o644)
	}}
	sha, changed, diags, err := defsync.Sync(context.Background(), defsync.Source{
		URL: "https://example.invalid/factory.git",
		Ref: "main",
	}, env.srv.cfg.DefinitionPath, bad)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("invalid tree was published")
	}
	if len(diags) == 0 {
		t.Fatal("invalid tree produced no diagnostics")
	}
	env.srv.onDefinitionSync(sha, changed, diags, nil)
	env.waitAgent(t, "extra")
	env.waitAgent(t, "implementer")
}

type taskJSON struct {
	ID             string `json:"id"`
	Stage          string `json:"stage"`
	Status         string `json:"status"`
	ConversationID string `json:"conversation_id"`
	PendingGate    string `json:"pending_gate"`
	RevisionCount  int    `json:"revision_count"`
}

type approvalJSON struct {
	ID         string `json:"id"`
	WorkItemID string `json:"work_item_id"`
	Gate       string `json:"gate"`
	Decision   string `json:"decision"`
}

type runLink struct {
	Parent       string
	WorkItem     string
	Model        string
	Conversation string
	Prompt       string
	Stage        string
}

type dirCloner struct {
	sha   string
	write func(dest string) error
}

func (c *dirCloner) Clone(_ context.Context, _, _, dest string) (string, error) {
	if c.write == nil {
		return c.sha, nil
	}
	if err := c.write(dest); err != nil {
		return "", err
	}
	return c.sha, nil
}

func writePipeline(t *testing.T, revisionLimit int) string {
	t.Helper()
	root := t.TempDir()
	if err := writeNamedFactory(root, "pipe", false); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "factory.yaml"), `
schemaVersion: v1alpha1
name: pipe
secrets:
  - GH_TOKEN
agentDefaults:
  model: auto
  runner: local
  pool: standard
pipeline:
  stages: [triage, planning, building, reviewing]
  agents:
    triage: triage
    planning: spec
    building: implementer
    reviewing: reviewer
  skip:
    triage: whenScopeClear
  approvals:
    spec: { required: true, approvers: "role:editor" }
  revisionLimit: `+itoa(revisionLimit)+`
`)
	mustWrite(t, filepath.Join(root, "routers", "triage-route.yaml"), `
type: prompt
default: triage-model
routing:
  - description: anything else
    model: unused-model
`)
	writeAgent(t, root, "triage", "TRIAGE", "model: triage-route")
	writeAgent(t, root, "spec", "SPEC", "model: planning-model")
	writeAgent(t, root, "implementer", "IMPLEMENT", "model: building-model")
	writeAgent(t, root, "reviewer", "REVIEW", "model: reviewing-model")
	return root
}

func writeNamedFactory(root, name string, extra bool) error {
	if err := os.MkdirAll(filepath.Join(root, "runners"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "factory.yaml"), []byte(strings.TrimSpace(`
schemaVersion: v1alpha1
name: `+name+`
agentDefaults:
  model: auto
  runner: local
  pool: standard
`)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "runners", "local.yaml"), []byte(strings.TrimSpace(`
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`)+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "agents", "implementer"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "implementer", "agent.md"), []byte(agentMD("implementer", "IMPLEMENT", "model: test-model")), 0o644); err != nil {
		return err
	}
	if !extra {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(root, "agents", "extra"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "agents", "extra", "agent.md"), []byte(agentMD("extra", "IMPLEMENT", "model: extra-model")), 0o644)
}

func writeAgent(t *testing.T, root, name, kind, model string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, "agents", name, "agent.md"), agentMD(name, kind, model))
	mustWrite(t, filepath.Join(root, "runners", "local.yaml"), `
description: Local docker runner
platform:
  os: linux
  arch: x86_64
  linux:
    dockerImage: factory-m1-task:dev
`)
}

func agentMD(name, kind, extra string) string {
	return strings.TrimSpace(`
---
description: ` + name + ` does one stage.
agentType: ` + kind + `
` + extra + `
---

Do the stage.
` + "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (e *env) createTask(t *testing.T, title, body string) taskJSON {
	t.Helper()
	payload := `{"title":` + jsonString(title) + `,"body":` + jsonString(body) + `,"source":"issue","source_ref":"acme/payments#1"}`
	code, raw := e.authed(t, http.MethodPost, "/api/v1/factory/pipe/tasks", payload)
	if code != http.StatusCreated {
		t.Fatalf("create task %d %s", code, raw)
	}
	var task taskJSON
	if err := json.Unmarshal(raw, &task); err != nil || task.ID == "" {
		t.Fatalf("task %s", raw)
	}
	return task
}

func (e *env) getTask(t *testing.T, id string) taskJSON {
	t.Helper()
	code, raw := e.authed(t, http.MethodGet, "/api/v1/factory/pipe/tasks/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("get task %d %s", code, raw)
	}
	var task taskJSON
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func (e *env) waitRevision(t *testing.T, id string, count int) taskJSON {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last taskJSON
	for time.Now().Before(deadline) {
		last = e.getTask(t, id)
		if last.RevisionCount == count {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("revision %d, want %d (status %s stage %s)", last.RevisionCount, count, last.Status, last.Stage)
	return last
}

func (e *env) waitTask(t *testing.T, id, status, stage string) taskJSON {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last taskJSON
	for time.Now().Before(deadline) {
		last = e.getTask(t, id)
		if last.Status == status && (stage == "" || last.Stage == stage) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task status %s stage %s, want %s %s", last.Status, last.Stage, status, stage)
	return last
}

func (e *env) listApprovals(t *testing.T) []approvalJSON {
	t.Helper()
	code, raw := e.authed(t, http.MethodGet, "/api/v1/factory/pipe/approvals", "")
	if code != http.StatusOK {
		t.Fatalf("approvals %d %s", code, raw)
	}
	var rows []approvalJSON
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func (e *env) decide(t *testing.T, id, decision string, want int) approvalJSON {
	t.Helper()
	code, raw := e.decideRaw(t, id, decision)
	if code != want {
		t.Fatalf("decide %d %s", code, raw)
	}
	var row approvalJSON
	if want == http.StatusOK {
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
	}
	return row
}

func (e *env) decideRaw(t *testing.T, id, decision string) (int, []byte) {
	t.Helper()
	return e.authed(t, http.MethodPost, "/api/v1/approvals/"+id+":decide", `{"decision":"`+decision+`"}`)
}

func (e *env) authed(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.base+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw
}

func (e *env) assertRunLinkage(t *testing.T, task taskJSON) {
	t.Helper()
	code, raw := e.authed(t, http.MethodGet, "/api/v1/factory/pipe/runs", "")
	if code != http.StatusOK {
		t.Fatalf("factory runs %d %s", code, raw)
	}
	var runs []runAPI
	if err := json.Unmarshal(raw, &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("factory runs empty")
	}
	for _, run := range runs {
		link := e.runLink(t, run.ID)
		if run.WorkItemID != task.ID || run.WorkItemID != link.WorkItem {
			t.Fatalf("run %s work_item_id %q", run.ID, run.WorkItemID)
		}
		if run.ParentRunID != link.Parent || run.ConversationID != link.Conversation || run.Stage != link.Stage || run.ModelID != link.Model {
			t.Fatalf("run %s api parent=%q conversation=%q stage=%q model=%q db parent=%q conversation=%q stage=%q model=%q",
				run.ID, run.ParentRunID, run.ConversationID, run.Stage, run.ModelID,
				link.Parent, link.Conversation, link.Stage, link.Model)
		}
		if run.Stage == "" || run.ModelID == "" || run.ConversationID == "" {
			t.Fatalf("run %s missing stage linkage %+v", run.ID, run)
		}
		one, oneRaw := e.authed(t, http.MethodGet, "/api/v1/agent/runs/"+run.ID, "")
		if one != http.StatusOK {
			t.Fatalf("run object %d %s", one, oneRaw)
		}
		var got runAPI
		if err := json.Unmarshal(oneRaw, &got); err != nil {
			t.Fatal(err)
		}
		if got != run {
			t.Fatalf("get %+v list %+v", got, run)
		}
	}
}

type runAPI struct {
	ID             string `json:"id"`
	WorkItemID     string `json:"work_item_id"`
	ParentRunID    string `json:"parent_run_id"`
	ConversationID string `json:"conversation_id"`
	Stage          string `json:"stage"`
	ModelID        string `json:"model_id"`
}

func (e *env) countStage(t *testing.T, workItemID, stage string) int {
	t.Helper()
	var n int
	err := e.raw.QueryRow(context.Background(), `
		SELECT count(*) FROM runs WHERE work_item_id = $1 AND stage = $2`, workItemID, stage).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) runLink(t *testing.T, runID string) runLink {
	t.Helper()
	var link runLink
	err := e.raw.QueryRow(context.Background(), `
		SELECT COALESCE(parent_run_id, ''), COALESCE(work_item_id, ''), COALESCE(model_id, ''),
		       COALESCE(conversation_id, ''), prompt, COALESCE(stage, '')
		FROM runs WHERE id = $1`, runID).Scan(
		&link.Parent, &link.WorkItem, &link.Model, &link.Conversation, &link.Prompt, &link.Stage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(link.Prompt, plantedGH) || strings.Contains(link.Prompt, plantedOpenAI) {
		t.Fatal("stored prompt contains a secret value")
	}
	return link
}

func (e *env) startFactoryRun(t *testing.T, factory, agent, prompt string) string {
	t.Helper()
	body := `{"factory":` + jsonString(factory) + `,"agent":` + jsonString(agent) + `,"prompt":` + jsonString(prompt) + `}`
	code, raw := e.authed(t, http.MethodPost, "/api/v1/agent/run", body)
	if code != http.StatusCreated {
		t.Fatalf("start %d %s", code, raw)
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &run); err != nil || run.ID == "" {
		t.Fatalf("start body %s", raw)
	}
	return run.ID
}

func (e *env) waitAgent(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		code, raw := e.authed(t, http.MethodGet, "/api/v1/factory/m2/agents", "")
		if code == http.StatusOK && strings.Contains(string(raw), `"name":"`+name+`"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("agent %s was not applied", name)
}

func assertSchemaUnauthenticated(t *testing.T, env *env) {
	t.Helper()
	res, err := http.Get(env.base + "/api/v1/factory-files/schemas/v1alpha1/factory.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("schema %d %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), "pipeline") {
		t.Fatal("schema document missing pipeline")
	}
	index, err := http.Get(env.base + "/api/v1/factory-files/schemas")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Body.Close() }()
	if index.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(index.Body)
		t.Fatalf("schema index %d %s", index.StatusCode, raw)
	}
}

func finishStage(t *testing.T, w *wsWorker, message string) protocol.TaskAssignment {
	t.Helper()
	asg := w.readAssignment(t)
	completeAssignment(t, w, asg, message)
	return asg
}

func completeAssignment(t *testing.T, w *wsWorker, asg protocol.TaskAssignment, message string) {
	t.Helper()
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: message,
	})
}

func failStage(t *testing.T, w *wsWorker, message string) protocol.TaskAssignment {
	t.Helper()
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: message,
		FailureReason: "failed_by_agent", ExitCode: 10,
	})
	return asg
}

func modelOf(asg protocol.TaskAssignment) string {
	if asg.Task == nil || asg.Task.AgentConfigSnapshot == nil || asg.Task.AgentConfigSnapshot.ModelID == nil {
		return ""
	}
	return *asg.Task.AgentConfigSnapshot.ModelID
}
