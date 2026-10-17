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
	"sync"
	"testing"
	"time"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/defsync"
	"github.com/polds/factory/internal/failure"
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

// TestReviewerRetryMatchesClassify checks the retry bit against
// failure.Classify for every Cases row whose inputs are columns of runs:
// status, failure_reason, and exit_code. Rows that need HarnessStatus or
// executions.failure_details_json are skipped; those values are not on the
// run row advanceAfterTerminal already loaded.
func TestReviewerRetryMatchesClassify(t *testing.T) {
	seen := 0
	for _, rule := range failure.Cases() {
		in := rule.Input
		if in.HarnessStatus != "" || len(in.Details) > 0 {
			continue
		}
		seen++
		got := reviewerInfra(in.RunStatus, in.FailureReason, in.ExitCode)
		want := failure.Classify(in).Class == failure.ClassInfrastructure
		if got != want {
			t.Errorf("%s: retry %v, infrastructure %v", rule.Name, got, want)
		}
	}
	if seen == 0 {
		t.Fatal("no Cases rows were checked")
	}
	code := 10
	if reviewerInfra(failure.StatusFailed, failure.ReasonContainerExit, &code) {
		t.Fatal("exit 10 with container_exit retried")
	}
}

func TestE2ESupersededSuspensionDoesNotResume(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task, asg := reachSuspendedBuilding(t, env, w, "Superseded suspension", "Which staging env?")
	w.close()
	var attempts int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	env.stop(t)
	env.markRunFailed(t, asg.TaskID, "superseded_duplicate_building_run", 1)
	var suspended string
	if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, task.ID).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended != asg.TaskID {
		t.Fatalf("suspended run %s", suspended)
	}

	// Hold the scheduler so the startup sweep cannot clear the pointer
	// before AnswerInput and park do.
	gate := make(chan struct{})
	env.srv.cfg.ScheduleGate = gate
	env.start(t)
	if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, task.ID).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended != asg.TaskID {
		t.Fatalf("startup cleared the pointer before the answer path: %s", suspended)
	}
	if err := env.srv.parkSuspendedRun(context.Background(), asg.TaskID); err != nil {
		t.Fatal(err)
	}
	if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, task.ID).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended != "" {
		t.Fatalf("park left suspended_run_id %s", suspended)
	}
	if st := env.status(t, asg.TaskID); st != "failed" {
		t.Fatalf("status %s", st)
	}
	if n := env.queueRows(t, asg.TaskID); n != 0 {
		t.Fatalf("queue rows after park %d", n)
	}

	if _, err := env.raw.Exec(context.Background(), `
		UPDATE work_items
		SET suspended_run_id = $2, suspended_at = clock_timestamp()
		WHERE id = $1`, task.ID, asg.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.AnswerInput(context.Background(), task.ID, "staging-2", "peter"); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("answer err %v", err)
	}
	if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, task.ID).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended != "" {
		t.Fatalf("answer left suspended_run_id %s", suspended)
	}
	if st := env.status(t, asg.TaskID); st != "failed" {
		t.Fatalf("status %s", st)
	}
	if n := env.queueRows(t, asg.TaskID); n != 0 {
		t.Fatalf("queue rows after answer %d", n)
	}
	var gotAttempts int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&gotAttempts); err != nil {
		t.Fatal(err)
	}
	if gotAttempts != attempts {
		t.Fatalf("executions %d, want %d", gotAttempts, attempts)
	}
	var answered *time.Time
	if err := env.raw.QueryRow(context.Background(), `SELECT answered_at FROM input_requests WHERE work_item_id = $1`, task.ID).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	if answered != nil {
		t.Fatal("answer was stored for a dead run")
	}
	still := env.getTask(t, task.ID)
	if still.Status != "awaiting_input" || still.Stage != "building" {
		t.Fatalf("task %+v", still)
	}
	if _, err := env.srv.AnswerInput(context.Background(), task.ID, "staging-2", "again"); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("second answer err %v", err)
	}
	if n := env.queueRows(t, asg.TaskID); n != 0 {
		t.Fatalf("queue rows after second answer %d", n)
	}
}

func TestE2EStaleSuspensionSweepIdempotent(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	liveTask, liveRun := reachSuspendedBuilding(t, env, w, "Live suspension", "Which live env?")
	staleTask, staleRun := reachSuspendedBuilding(t, env, w, "Stale suspension", "Which stale env?")
	w.close()
	env.stop(t)
	env.markRunFailed(t, staleRun.TaskID, "superseded_duplicate_building_run", 1)

	gate := make(chan struct{})
	env.srv.cfg.ScheduleGate = gate
	env.start(t)
	if err := env.srv.sweepStaleSuspensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSuspension(t, env, liveTask.ID, liveRun.TaskID)
	assertSuspension(t, env, staleTask.ID, "")
	if err := env.srv.sweepStaleSuspensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSuspension(t, env, liveTask.ID, liveRun.TaskID)
	assertSuspension(t, env, staleTask.ID, "")

	if _, err := env.raw.Exec(context.Background(), `
		UPDATE work_items
		SET suspended_run_id = $2, suspended_at = clock_timestamp()
		WHERE id = $1`, staleTask.ID, staleRun.TaskID); err != nil {
		t.Fatal(err)
	}
	close(gate)
	deadline := time.Now().Add(8 * time.Second)
	var suspended string
	for {
		if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, staleTask.ID).Scan(&suspended); err != nil {
			t.Fatal(err)
		}
		if suspended == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("startup sweep left suspended_run_id %s", suspended)
		}
		time.Sleep(20 * time.Millisecond)
	}
	assertSuspension(t, env, liveTask.ID, liveRun.TaskID)
	if err := env.srv.sweepStaleSuspensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSuspension(t, env, liveTask.ID, liveRun.TaskID)
	assertSuspension(t, env, staleTask.ID, "")
}

func TestE2EReviewerInfraRetryAcrossRestart(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task, review := reachReviewing(t, env, w)
	failAssignment(t, w, review, "model_unavailable", 21)
	env.waitRunCount(t, task.ID, "reviewing", 2)
	var retryID, retryStatus string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT id, status FROM runs
		WHERE work_item_id = $1 AND stage = 'reviewing' AND id <> $2`, task.ID, review.TaskID).Scan(&retryID, &retryStatus); err != nil {
		t.Fatal(err)
	}
	if retryID == "" || (retryStatus != "queued" && retryStatus != "assigned" && retryStatus != "running") {
		t.Fatalf("retry run %s status %s", retryID, retryStatus)
	}
	if got := env.getTask(t, task.ID); got.RevisionCount != 0 || got.Stage != "reviewing" || got.Status != "open" {
		t.Fatalf("after retry %+v", got)
	}
	// Drop the worker before restart so an assignment lease cannot hide the
	// queued retry from the next process. The run row is what survives.
	w.close()
	env.waitStatus(t, retryID, "queued")
	env.restart(t)
	env.assertRunCountStable(t, task.ID, "reviewing", 2)

	w = dialWorker(t, env, "local-1")
	again := w.readAssignment(t)
	if again.TaskID != retryID || again.EnvVars["FACTORY_STAGE"] != "reviewing" {
		t.Fatalf("assignment after restart %s stage %s", again.TaskID, again.EnvVars["FACTORY_STAGE"])
	}
	failAssignment(t, w, again, "container_exit", 137)
	build := w.readAssignment(t)
	if build.EnvVars["FACTORY_STAGE"] != "building" || build.TaskID == again.TaskID {
		t.Fatalf("revision assignment stage %s id %s", build.EnvVars["FACTORY_STAGE"], build.TaskID)
	}
	if got := env.waitRevision(t, task.ID, 1); got.Stage != "building" || got.Status != "open" {
		t.Fatalf("after second infra failure %+v", got)
	}
	if env.countStage(t, task.ID, "reviewing") != 2 || env.countStage(t, task.ID, "building") != 2 {
		t.Fatalf("reviewing %d building %d", env.countStage(t, task.ID, "reviewing"), env.countStage(t, task.ID, "building"))
	}
	w.close()
	env.restart(t)
	env.assertRunCountStable(t, task.ID, "reviewing", 2)
	env.assertRunCountStable(t, task.ID, "building", 2)
}

func TestE2EReviewerInfraRetrySurvivesCrash(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task, review := reachReviewing(t, env, w)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: review.TaskID, WorkerID: "local-1"})
	env.waitStatus(t, review.TaskID, "running")
	w.close()
	env.stop(t)
	env.markRunFailed(t, review.TaskID, "environment_setup_failed", 30)
	if env.countStage(t, task.ID, "reviewing") != 1 {
		t.Fatalf("reviewing runs %d before restart", env.countStage(t, task.ID, "reviewing"))
	}
	env.start(t)
	env.waitRunCount(t, task.ID, "reviewing", 2)
	var parent, stage, status string
	if err := env.raw.QueryRow(context.Background(), `
		SELECT COALESCE(parent_run_id, ''), stage, status
		FROM runs
		WHERE work_item_id = $1 AND id <> $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, task.ID, review.TaskID).Scan(&parent, &stage, &status); err != nil {
		t.Fatal(err)
	}
	if parent != review.TaskID || stage != "reviewing" || status != "queued" {
		t.Fatalf("recovered run parent %s stage %s status %s", parent, stage, status)
	}
	if got := env.getTask(t, task.ID); got.RevisionCount != 0 || got.Stage != "reviewing" {
		t.Fatalf("task %+v", got)
	}
	// The recovered retry is in flight. A second startup must not start another.
	env.restart(t)
	env.assertRunCountStable(t, task.ID, "reviewing", 2)
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

// TestE2EAskInputRequeueGap restores the queue row CreateInputRequest leaves
// behind before parkSuspendedRun deletes it. Restart plus ExpireLeases used
// to requeue that run, and AnswerInput then returned success without a new
// execution.
func TestE2EAskInputRequeueGap(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	const question = "Which staging env?"
	const answer = "staging-2"
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task := env.createTask(t, "Gap before park", "which env?")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"` + question + `"}`,
	})
	waiting := env.waitTask(t, task.ID, "awaiting_input", "triage")
	env.assertSuspended(t, task.ID, asg.TaskID, question)
	w.close()
	env.stop(t)
	env.restorePrePark(t, task.ID, asg.TaskID)
	env.start(t)
	if _, err := env.srv.store.ExpireLeases(context.Background(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("executions before answer %d, want 1", before)
	}

	req, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "peter")
	if err != nil {
		t.Fatal(err)
	}
	if req.Answer != answer || req.RunID != asg.TaskID {
		t.Fatalf("answer %+v", req)
	}
	resumed := env.getTask(t, task.ID)
	if resumed.Status != "open" || resumed.ConversationID != waiting.ConversationID {
		t.Fatalf("resumed %+v", resumed)
	}

	w2 := dialWorker(t, env, "local-2")
	defer w2.close()
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
	again, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "someone")
	if err != nil || again.ID != req.ID {
		t.Fatalf("replay %+v %v", again, err)
	}
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("replay executions %d, want 2", attempts)
	}
	if _, err := env.srv.AnswerInput(context.Background(), task.ID, "a different answer", "peter"); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("conflict %v", err)
	}
}

// TestE2EAskInputPromptRereadOnClaim commits the resumed question and answer
// after the scheduler has snapshotted the queued run and before claim leases
// the queue row. The assignment carries the prompt committed at claim time.
//
// The synthetic lease is seeded only after the claim hook is installed and a
// worker is connected. The scheduler ticks as soon as the server starts, and
// a queue row that already exists is expired and parked before assignOne.
func TestE2EAskInputPromptRereadOnClaim(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	const question = "Which staging env?"
	const answer = "staging-2"
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task := env.createTask(t, "Prompt reread on claim", "which env?")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"` + question + `"}`,
	})
	waiting := env.waitTask(t, task.ID, "awaiting_input", "triage")
	env.assertSuspended(t, task.ID, asg.TaskID, question)
	w.close()
	env.stop(t)
	// Hold the restarted scheduler until the hook and the worker are ready.
	// The first tick expires the synthetic lease and parks the run, and that
	// deletes the queue row when assignOne has no worker to call.
	gate := make(chan struct{})
	env.srv.cfg.ScheduleGate = gate
	env.start(t)

	var once sync.Once
	var hookMu sync.Mutex
	var hookErr error
	hooked := false
	env.srv.mu.Lock()
	env.srv.beforeClaim = func(runID string) {
		if runID != asg.TaskID {
			return
		}
		once.Do(func() {
			_, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "peter")
			hookMu.Lock()
			hookErr = err
			hooked = true
			hookMu.Unlock()
		})
	}
	env.srv.mu.Unlock()

	w2 := dialWorker(t, env, "local-2")
	defer w2.close()
	env.waitConnected(t, "local-2")
	env.restorePrePark(t, task.ID, asg.TaskID)
	// time.Now, not an hour ahead. The gate is still closed, but a fresh
	// claim lease must not be eligible if a tick already passed it.
	if _, err := env.srv.store.ExpireLeases(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	close(gate)

	next := w2.readAssignment(t)
	hookMu.Lock()
	ran, err := hooked, hookErr
	hookMu.Unlock()
	if !ran {
		t.Fatal("claim hook did not run")
	}
	if err != nil {
		t.Fatal(err)
	}
	if next.TaskID != asg.TaskID {
		t.Fatalf("resume run %s, want %s", next.TaskID, asg.TaskID)
	}
	prompt := next.EnvVars["FACTORY_TASK_PROMPT"]
	if !strings.Contains(prompt, question) || !strings.Contains(prompt, answer) {
		t.Fatal("resumed prompt is missing the question and answer")
	}
	if next.Task == nil || !strings.Contains(next.Task.Definition.Prompt, question) || !strings.Contains(next.Task.Definition.Prompt, answer) {
		t.Fatal("assignment task prompt is missing the question and answer")
	}
	if next.EnvVars["FACTORY_CONVERSATION_ID"] != waiting.ConversationID {
		t.Fatalf("conversation %s", next.EnvVars["FACTORY_CONVERSATION_ID"])
	}
	var attempts int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("executions %d, want 2", attempts)
	}
}

// TestE2EAskInputStaleParkLeavesResumeQueued snapshots the suspended run id,
// answers, then parks that id the way a leader tick does after waiting out
// the answer. The resume stays queued and starts one execution.
func TestE2EAskInputStaleParkLeavesResumeQueued(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	const question = "Which staging env?"
	const answer = "staging-2"
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task := env.createTask(t, "Stale park after answer", "which env?")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"` + question + `"}`,
	})
	waiting := env.waitTask(t, task.ID, "awaiting_input", "triage")
	env.assertSuspended(t, task.ID, asg.TaskID, question)
	var stale string
	if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, task.ID).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != asg.TaskID {
		t.Fatalf("suspended run %s", stale)
	}
	w.close()

	if _, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "peter"); err != nil {
		t.Fatal(err)
	}
	env.srv.reconcileSuspended(context.Background(), []string{stale})
	if err := env.srv.parkSuspendedRun(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := env.raw.QueryRow(context.Background(), `SELECT status FROM runs WHERE id = $1`, asg.TaskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("status %s, want queued", status)
	}
	if n := env.queueRows(t, asg.TaskID); n != 1 {
		t.Fatalf("queue rows %d, want 1", n)
	}
	var attempts int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("executions before claim %d, want 1", attempts)
	}
	if _, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "someone"); err != nil {
		t.Fatal(err)
	}
	if n := env.queueRows(t, asg.TaskID); n != 1 {
		t.Fatalf("queue rows after replay %d, want 1", n)
	}

	w2 := dialWorker(t, env, "local-2")
	defer w2.close()
	next := w2.readAssignment(t)
	if next.TaskID != asg.TaskID {
		t.Fatalf("resume run %s, want %s", next.TaskID, asg.TaskID)
	}
	prompt := next.EnvVars["FACTORY_TASK_PROMPT"]
	if !strings.Contains(prompt, question) || !strings.Contains(prompt, answer) {
		t.Fatal("resumed prompt is missing the question and answer")
	}
	if next.EnvVars["FACTORY_CONVERSATION_ID"] != waiting.ConversationID {
		t.Fatalf("conversation %s", next.EnvVars["FACTORY_CONVERSATION_ID"])
	}
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("executions %d, want 2", attempts)
	}
	if _, err := env.srv.AnswerInput(context.Background(), task.ID, answer, "again"); err != nil {
		t.Fatal(err)
	}
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("executions after replay %d, want 2", attempts)
	}
}

func TestE2EAskInputMissingStageAgentLeavesPending(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task := env.createTask(t, "No stage agent", "which env?")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"Which staging env?"}`,
	})
	env.waitTask(t, task.ID, "awaiting_input", "triage")
	env.assertSuspended(t, task.ID, asg.TaskID, "Which staging env?")
	w.close()

	env.srv.mu.Lock()
	pipeline := env.srv.loaded.tree.Factory.Pipeline
	agents := make(map[string]string, len(pipeline.Agents))
	for stage, name := range pipeline.Agents {
		if stage == "triage" {
			continue
		}
		agents[stage] = name
	}
	pipeline.Agents = agents
	var name, id string
	for n, agentID := range env.srv.loaded.byName {
		name, id = n, agentID
		break
	}
	if id == "" {
		env.srv.mu.Unlock()
		t.Fatal("no agent")
	}
	bound := env.srv.loaded.byID[id]
	env.srv.loaded.byName = map[string]string{name: id}
	env.srv.loaded.byID = map[string]boundAgent{id: bound}
	env.srv.mu.Unlock()

	if _, err := env.srv.AnswerInput(context.Background(), task.ID, "staging-2", "peter"); err == nil {
		t.Fatal("missing stage agent was accepted")
	}
	var answered *time.Time
	if err := env.raw.QueryRow(context.Background(), `SELECT answered_at FROM input_requests WHERE work_item_id = $1`, task.ID).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	if answered != nil {
		t.Fatal("answer was stored")
	}
	still := env.getTask(t, task.ID)
	if still.Status != "awaiting_input" {
		t.Fatalf("status %s", still.Status)
	}
	if n := env.queueRows(t, asg.TaskID); n != 0 {
		t.Fatalf("queue rows %d", n)
	}
	var attempts int
	if err := env.raw.QueryRow(context.Background(), `SELECT count(*) FROM executions WHERE run_id = $1`, asg.TaskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("executions %d, want 1", attempts)
	}
}

func TestE2EAskInputCancelSettlesWithoutGrace(t *testing.T) {
	if os.Getenv("FACTORY_TEST_DATABASE_URL") == "" {
		t.Skip("FACTORY_TEST_DATABASE_URL unset")
	}
	env := boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w := dialWorker(t, env, "local-1")
	task := env.createTask(t, "Cancel while parked", "which env?")
	asg := w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"Which staging env?"}`,
	})
	env.waitTask(t, task.ID, "awaiting_input", "triage")
	env.assertSuspended(t, task.ID, asg.TaskID, "Which staging env?")
	w.close()

	code, raw := env.authed(t, http.MethodPost, "/api/v1/factory/pipe/tasks/"+task.ID+"/cancel", "")
	if code != http.StatusOK {
		t.Fatalf("cancel %d %s", code, raw)
	}
	env.waitStatus(t, asg.TaskID, "cancelled")
	cancelled := env.getTask(t, task.ID)
	if cancelled.Status != "cancelled" {
		t.Fatalf("task %+v", cancelled)
	}
	if n := env.queueRows(t, asg.TaskID); n != 0 {
		t.Fatalf("queue rows %d", n)
	}
	env.requireOneTerminal(t, asg.TaskID, "cancelled")
	env.stop(t)

	env = boot(t, bootOpts{definition: func(t *testing.T) string {
		return writePipeline(t, 3)
	}})
	w = dialWorker(t, env, "local-1")
	task = env.createTask(t, "Cancel flag then restart", "which env?")
	asg = w.readAssignment(t)
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	postEvents(t, asg.EnvVars["FACTORY_EVENTS_URL"], asg.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"Which staging env?"}`,
	})
	env.waitTask(t, task.ID, "awaiting_input", "triage")
	env.assertSuspended(t, task.ID, asg.TaskID, "Which staging env?")
	w.close()
	env.stop(t)
	if _, err := env.raw.Exec(context.Background(), `UPDATE runs SET cancel_requested = true WHERE id = $1`, asg.TaskID); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := env.raw.QueryRow(context.Background(), `SELECT status FROM runs WHERE id = $1`, asg.TaskID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st == "cancelled" {
		t.Fatal("cancel settled while the server was stopped")
	}
	env.start(t)
	env.waitStatus(t, asg.TaskID, "cancelled")
	env.requireOneTerminal(t, asg.TaskID, "cancelled")
	if n := env.queueRows(t, asg.TaskID); n != 0 {
		t.Fatalf("queue rows %d", n)
	}
}

func (e *env) restorePrePark(t *testing.T, workItemID, runID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.raw.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the run before the queue insert so a live scheduler tick cannot
	// observe a partial seed and park it away.
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at, lease_until)
		SELECT id, factory_id, pool, priority, clock_timestamp(), '1970-01-02T00:00:00Z'
		FROM runs WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE executions
		SET state = 'running', ended_at = NULL
		WHERE id = (
			SELECT execution_id FROM input_requests
			WHERE work_item_id = $1 AND answered_at IS NULL
		)`, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("restored executions %d", tag.RowsAffected())
	}
	tag, err = tx.Exec(ctx, `
		UPDATE runs
		SET status = 'running', worker_id = NULL, ended_at = NULL
		WHERE id = $1`, runID)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("restored runs %d", tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.queueRows(t, runID); n != 1 {
		t.Fatalf("pre-park queue rows %d", n)
	}
}

func (e *env) waitConnected(t *testing.T, workerID string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, info := range e.srv.gw.Connected() {
			if info.WorkerID == workerID {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker %s did not connect", workerID)
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
	e.stop(t)
	e.start(t)
}

func (e *env) stop(t *testing.T) {
	t.Helper()
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
}

func (e *env) start(t *testing.T) {
	t.Helper()
	cfg := e.srv.cfg
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

func reachSuspendedBuilding(t *testing.T, env *env, w *wsWorker, title, question string) (taskJSON, protocol.TaskAssignment) {
	t.Helper()
	task := env.createTask(t, title, "which env?")
	finishStage(t, w, "triage-done")
	finishStage(t, w, "planning-done")
	env.waitTask(t, task.ID, "awaiting_approval", "planning")
	var gateID string
	for _, approval := range env.listApprovals(t) {
		if approval.WorkItemID == task.ID && approval.Decision == "" {
			gateID = approval.ID
		}
	}
	if gateID == "" {
		t.Fatal("no pending gate")
	}
	env.decide(t, gateID, "approve", http.StatusOK)
	building := w.readAssignment(t)
	if building.EnvVars["FACTORY_STAGE"] != "building" || building.EnvVars["FACTORY_WORK_ITEM_ID"] != task.ID {
		t.Fatalf("building env %+v", building.EnvVars)
	}
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: building.TaskID, WorkerID: "local-1"})
	postEvents(t, building.EnvVars["FACTORY_EVENTS_URL"], building.EnvVars["FACTORY_RUN_TOKEN"], 0, []string{
		`{"type":"question","gate":"clarification","text":"` + question + `"}`,
	})
	env.waitTask(t, task.ID, "awaiting_input", "building")
	env.assertSuspended(t, task.ID, building.TaskID, question)
	return task, building
}

func reachReviewing(t *testing.T, env *env, w *wsWorker) (taskJSON, protocol.TaskAssignment) {
	t.Helper()
	task := env.createTask(t, "Review the change", "scope")
	finishStage(t, w, "triage-done")
	finishStage(t, w, "planning-done")
	env.waitTask(t, task.ID, "awaiting_approval", "planning")
	var gateID string
	for _, approval := range env.listApprovals(t) {
		if approval.WorkItemID == task.ID && approval.Decision == "" {
			gateID = approval.ID
		}
	}
	if gateID == "" {
		t.Fatal("no pending gate")
	}
	env.decide(t, gateID, "approve", http.StatusOK)
	building := w.readAssignment(t)
	if building.EnvVars["FACTORY_STAGE"] != "building" {
		t.Fatalf("stage %s", building.EnvVars["FACTORY_STAGE"])
	}
	completeAssignment(t, w, building, "building-done")
	review := w.readAssignment(t)
	if review.EnvVars["FACTORY_STAGE"] != "reviewing" || review.EnvVars["FACTORY_WORK_ITEM_ID"] != task.ID {
		t.Fatalf("review env %+v", review.EnvVars)
	}
	return task, review
}

func failAssignment(t *testing.T, w *wsWorker, asg protocol.TaskAssignment, reason string, exitCode int) {
	t.Helper()
	w.send(t, protocol.TypeTaskClaimed, protocol.TaskClaimed{TaskID: asg.TaskID, WorkerID: "local-1"})
	w.send(t, protocol.TypeTaskFailed, protocol.TaskFailed{
		TaskID: asg.TaskID, ExecutionID: asg.ExecutionID, Message: reason,
		FailureReason: reason, ExitCode: exitCode,
	})
}

func (e *env) markRunFailed(t *testing.T, runID, reason string, exitCode int) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.raw.Exec(ctx, `
		UPDATE runs
		SET status = 'failed', failure_reason = $2, exit_code = $3,
		    ended_at = clock_timestamp(), worker_id = NULL
		WHERE id = $1`, runID, reason, exitCode); err != nil {
		t.Fatal(err)
	}
	if _, err := e.raw.Exec(ctx, `
		UPDATE executions
		SET state = 'failed', failure_reason = $2, exit_code = $3,
		    ended_at = clock_timestamp()
		WHERE run_id = $1 AND state IN ('assigned', 'running')`, runID, reason, exitCode); err != nil {
		t.Fatal(err)
	}
	if _, err := e.raw.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		t.Fatal(err)
	}
}

func assertSuspension(t *testing.T, env *env, workItemID, runID string) {
	t.Helper()
	var suspended string
	if err := env.raw.QueryRow(context.Background(), `SELECT suspended_run_id FROM work_items WHERE id = $1`, workItemID).Scan(&suspended); err != nil {
		t.Fatal(err)
	}
	if suspended != runID {
		t.Fatalf("suspended run %s, want %s", suspended, runID)
	}
}

func (e *env) waitRunCount(t *testing.T, workItemID, stage string, n int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		last = e.countStage(t, workItemID, stage)
		if last == n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s runs %d, want %d", stage, last, n)
}

func (e *env) assertRunCountStable(t *testing.T, workItemID, stage string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := e.countStage(t, workItemID, stage); got != n {
			t.Fatalf("%s runs %d, want %d", stage, got, n)
		}
		time.Sleep(50 * time.Millisecond)
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
