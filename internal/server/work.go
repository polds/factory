package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
	"github.com/polds/factory/internal/handoff"
	"github.com/polds/factory/internal/orchestrator"
	"github.com/polds/factory/internal/store"
)

// step calls the stage machine with M3Predicates so M3 never skips a stage.
func step(def orchestrator.Def, state orchestrator.State, ev orchestrator.Event) (orchestrator.State, []orchestrator.Effect, error) {
	return orchestrator.Step(def, state, ev, orchestrator.M3Predicates())
}

func (s *Server) CreateTask(ctx context.Context, factoryID string, req api.TaskRequest) (api.Task, error) {
	s.orchMu.Lock()
	defer s.orchMu.Unlock()
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return api.Task{}, err
	}
	def, err := s.pipelineDef()
	if err != nil {
		return api.Task{}, err
	}
	next, effects, err := step(def, orchestrator.State{}, orchestrator.Created{})
	if err != nil {
		return api.Task{}, err
	}
	item, err := s.store.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: fac.ID,
		Source:    req.Source,
		SourceRef: req.SourceRef,
		Title:     req.Title,
		Body:      req.Body,
		Stage:     next.Stage,
		Status:    next.Status,
		CreatedBy: req.CreatedBy,
	})
	if err != nil {
		return api.Task{}, err
	}
	if next.RevisionCount != item.RevisionCount {
		item, err = s.store.SetWorkItemRevision(ctx, item.ID, next.RevisionCount)
		if err != nil {
			return api.Task{}, err
		}
	}
	item, err = s.commitStep(ctx, item, next, effects)
	if err != nil {
		return api.Task{}, err
	}
	if _, err := s.applyEffects(ctx, item, effects, ""); err != nil {
		return api.Task{}, err
	}
	return s.taskView(ctx, item)
}

func (s *Server) GetTask(ctx context.Context, factoryID, id string) (api.Task, error) {
	_, item, err := s.workItemInFactory(ctx, factoryID, id)
	if err != nil {
		return api.Task{}, err
	}
	return s.taskView(ctx, item)
}

func (s *Server) ListTasks(ctx context.Context, factoryID string, f api.TaskFilter) ([]api.Task, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListWorkItems(ctx, store.WorkItemFilter{
		FactoryID: fac.ID,
		Stage:     f.Stage,
		Status:    f.Status,
		Source:    f.Source,
		Limit:     200,
	})
	if err != nil {
		return nil, err
	}
	out := make([]api.Task, 0, len(rows))
	for _, row := range rows {
		task, err := s.taskView(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *Server) CancelTask(ctx context.Context, factoryID, id string) (api.Task, error) {
	var item store.WorkItem
	err := s.withWorkItem(ctx, factoryID, id, func(current store.WorkItem) error {
		if current.Status == store.WorkItemComplete || current.Status == store.WorkItemCancelled {
			return api.ErrConflict
		}
		def, err := s.pipelineDef()
		if err != nil {
			return err
		}
		state, err := s.loadState(ctx, current)
		if err != nil {
			return err
		}
		next, effects, err := step(def, state, orchestrator.Cancel{})
		if err != nil {
			return orchErr(err)
		}
		updated, err := s.commitStep(ctx, current, next, effects)
		if err != nil {
			return err
		}
		cancel, err := s.applyEffects(ctx, updated, effects, "")
		if err != nil {
			return err
		}
		item = updated
		if cancel {
			// Drop the lock before cancelling runs. finish advances the
			// work item and takes orchMu again.
			return errCancelRuns
		}
		return nil
	})
	if errors.Is(err, errCancelRuns) {
		s.cancelWorkItemRuns(ctx, item.ID)
		err = nil
	}
	if err != nil {
		return api.Task{}, err
	}
	return s.taskView(ctx, item)
}

func (s *Server) ListApprovals(ctx context.Context, factoryID string, pendingOnly bool) ([]api.Approval, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListApprovals(ctx, fac.ID, pendingOnly)
	if err != nil {
		return nil, err
	}
	out := make([]api.Approval, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAPIApproval(row))
	}
	return out, nil
}

func (s *Server) DecideApproval(ctx context.Context, id, decision, decidedBy, note string) (api.Approval, error) {
	s.orchMu.Lock()
	approval, err := s.store.DecideApproval(ctx, id, decision, decidedBy, note)
	if err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, orchErr(err)
	}
	item, err := s.store.GetWorkItem(ctx, approval.WorkItemID)
	if err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, orchErr(err)
	}
	advance := item.Status == store.WorkItemAwaitingApproval
	if advance {
		pending, pendErr := s.pendingGate(ctx, item.ID)
		if pendErr != nil {
			s.orchMu.Unlock()
			return api.Approval{}, pendErr
		}
		// The row we just decided is no longer pending. Another open gate
		// means this decision was not the one blocking the work item.
		if pending != "" && pending != approval.Gate {
			advance = false
		}
	}
	if !advance {
		s.orchMu.Unlock()
		return toAPIApproval(approval), nil
	}
	def, err := s.pipelineDef()
	if err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, err
	}
	state, err := s.loadState(ctx, item)
	if err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, err
	}
	state.PendingGate = approval.Gate
	var ev orchestrator.Event
	switch decision {
	case store.DecisionApprove:
		ev = orchestrator.Approved{Gate: approval.Gate}
	case store.DecisionReject:
		ev = orchestrator.Rejected{Gate: approval.Gate}
	default:
		s.orchMu.Unlock()
		return api.Approval{}, fmt.Errorf("server: decision %q", decision)
	}
	next, effects, err := step(def, state, ev)
	if err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, orchErr(err)
	}
	updated, err := s.commitStep(ctx, item, next, effects)
	if err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, err
	}
	if _, err := s.applyEffects(ctx, updated, effects, ""); err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, err
	}
	s.orchMu.Unlock()
	return toAPIApproval(approval), nil
}

// advanceAfterTerminal steps the work item that owns a run which just
// reached a terminal status. Infra requeue does not call this.
func (s *Server) advanceAfterTerminal(ctx context.Context, runID string) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run.WorkItemID == "" || run.Stage == "" {
		return
	}
	var ev orchestrator.Event
	switch run.Status {
	case store.StatusSucceeded:
		ev = orchestrator.RunSucceeded{Stage: run.Stage}
	case store.StatusFailed:
		ev = orchestrator.RunFailed{
			Stage: run.Stage,
			Infra: reviewerInfra(run.Status, run.FailureReason, run.ExitCode),
		}
	default:
		return
	}
	s.orchMu.Lock()
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil {
		s.orchMu.Unlock()
		s.log.Error("work item", "run_id", runID, "err", err)
		return
	}
	if item.Status != store.WorkItemOpen || item.Stage != run.Stage {
		s.orchMu.Unlock()
		return
	}
	// A reviewing infrastructure failure restarts that stage once. The
	// budget and the new run commit in one transaction. A used budget falls
	// through and revises, which is today's reviewing-failure behaviour.
	if failed, ok := ev.(orchestrator.RunFailed); ok && failed.Infra && run.Stage == store.StageReviewing {
		handled, retryErr := s.retryReviewerOnce(ctx, item, run)
		if retryErr != nil {
			s.orchMu.Unlock()
			s.log.Error("reviewer retry", "work_item_id", item.ID, "run_id", runID, "err", retryErr)
			return
		}
		if handled {
			s.orchMu.Unlock()
			return
		}
	}
	def, err := s.pipelineDef()
	if err != nil {
		s.orchMu.Unlock()
		s.log.Error("pipeline", "work_item_id", item.ID, "err", err)
		return
	}
	state, err := s.loadState(ctx, item)
	if err != nil {
		s.orchMu.Unlock()
		s.log.Error("work item state", "work_item_id", item.ID, "err", err)
		return
	}
	// retryReviewerOnce returns unhandled only when the one retry is already
	// recorded, so Step must revise instead of starting another review.
	if failed, ok := ev.(orchestrator.RunFailed); ok && failed.Infra && run.Stage == store.StageReviewing {
		state.ReviewerRetryUsed = true
	}
	next, effects, err := step(def, state, ev)
	if err != nil {
		s.orchMu.Unlock()
		s.log.Error("orchestrator", "work_item_id", item.ID, "run_id", runID, "err", err)
		return
	}
	updated, err := s.commitStep(ctx, item, next, effects)
	if err != nil {
		s.orchMu.Unlock()
		s.log.Error("work item stage", "work_item_id", item.ID, "err", err)
		return
	}
	cancel, err := s.applyEffects(ctx, updated, effects, run.ID)
	s.orchMu.Unlock()
	if err != nil {
		s.log.Error("orchestrator effect", "work_item_id", item.ID, "run_id", runID, "err", err)
		return
	}
	if cancel {
		s.cancelWorkItemRuns(ctx, item.ID)
	}
}

// onQuestion suspends the work item and releases the worker slot. The input
// request and awaiting_input status commit together. The execution ends and
// the run leaves the queue, so it is not failed and not requeued.
func (s *Server) onQuestion(ctx context.Context, runID string) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run.WorkItemID == "" {
		return
	}
	s.orchMu.Lock()
	defer s.orchMu.Unlock()
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil {
		s.log.Error("question", "run_id", runID, "err", err)
		return
	}
	if item.SuspendedRunID != "" {
		cleared, clearErr := s.clearStaleSuspension(ctx, item.SuspendedRunID)
		if clearErr != nil {
			s.log.Error("question", "work_item_id", item.ID, "err", clearErr)
			return
		}
		if cleared {
			item, err = s.store.GetWorkItem(ctx, run.WorkItemID)
			if err != nil {
				s.log.Error("question", "run_id", runID, "err", err)
				return
			}
		}
	}
	if item.Status == store.WorkItemAwaitingInput && item.SuspendedRunID == run.ID {
		if err := s.parkSuspendedRun(ctx, run.ID); err != nil {
			s.log.Error("question", "work_item_id", item.ID, "err", err)
			return
		}
		s.releaseWorkerSlot(run.ID)
		return
	}
	if item.Status != store.WorkItemOpen {
		return
	}
	def, err := s.pipelineDef()
	if err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	state, err := s.loadState(ctx, item)
	if err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	next, _, err := step(def, state, orchestrator.AskInput{})
	if err != nil {
		s.log.Info("question ignored", "work_item_id", item.ID, "err", err)
		return
	}
	if next.Status != orchestrator.StatusAwaitingInput {
		s.log.Error("question", "work_item_id", item.ID, "status", next.Status)
		return
	}
	question, err := s.latestQuestion(ctx, run.ID)
	if err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	if question == "" {
		s.log.Info("question ignored", "work_item_id", item.ID, "err", "empty question")
		return
	}
	execID, err := s.liveExecution(ctx, run.ID)
	if err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	if execID == "" {
		s.log.Error("question", "work_item_id", item.ID, "err", "no live execution")
		return
	}
	if _, err := s.store.CreateInputRequest(ctx, item.ID, run.ID, execID, question); err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	if err := s.parkSuspendedRun(ctx, run.ID); err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	s.releaseWorkerSlot(run.ID)
	s.log.Info("work item awaiting input", "work_item_id", item.ID, "run_id", run.ID)
}

// AnswerInput records an answer and resumes the parked run. The same answer
// returns the stored row and does not start another execution. A different
// answer returns api.ErrConflict. The stage agent is resolved before the
// answer is stored, so a missing agent leaves the request pending.
func (s *Server) AnswerInput(ctx context.Context, workItemID, answer, answeredBy string) (store.InputRequest, error) {
	s.orchMu.Lock()
	defer s.orchMu.Unlock()
	item, err := s.store.GetWorkItem(ctx, workItemID)
	if err != nil {
		return store.InputRequest{}, orchErr(err)
	}
	if item.SuspendedRunID != "" {
		if _, err := s.clearStaleSuspension(ctx, item.SuspendedRunID); err != nil {
			return store.InputRequest{}, err
		}
	}
	pending, pendErr := s.store.PendingInputRequest(ctx, workItemID)
	if pendErr != nil && !errors.Is(pendErr, store.ErrNotFound) {
		return store.InputRequest{}, pendErr
	}
	agentID := ""
	if pendErr == nil {
		// A question whose run already finished is not a pending suspension.
		// Record nothing and do not resume that run. Callers then take the
		// same path as a work item that has no input request.
		dead, deadErr := s.runIsTerminal(ctx, pending.RunID)
		if deadErr != nil {
			return store.InputRequest{}, deadErr
		}
		if dead {
			if _, err := s.clearStaleSuspension(ctx, pending.RunID); err != nil {
				return store.InputRequest{}, err
			}
			return store.InputRequest{}, api.ErrNotFound
		}
		agentID, err = s.stageAgentID(item, item.FactoryID)
		if err != nil {
			return store.InputRequest{}, err
		}
		def, err := s.pipelineDef()
		if err != nil {
			return store.InputRequest{}, err
		}
		state, err := s.loadState(ctx, item)
		if err != nil {
			return store.InputRequest{}, err
		}
		if _, _, err := step(def, state, orchestrator.ResumeInput{}); err != nil {
			return store.InputRequest{}, orchErr(err)
		}
		// ExpireLeases can requeue a run that still has a queue row from the
		// gap before park. Put it back before the answer is stored.
		if err := s.parkSuspendedRun(ctx, pending.RunID); err != nil {
			return store.InputRequest{}, err
		}
	}
	stored, err := s.store.AnswerInputRequest(ctx, workItemID, answer, answeredBy)
	if err != nil {
		return store.InputRequest{}, orchErr(err)
	}
	if err := s.resumeStored(ctx, stored, agentID); err != nil {
		return store.InputRequest{}, err
	}
	return stored, nil
}

func (s *Server) withWorkItem(ctx context.Context, factoryID, id string, fn func(store.WorkItem) error) error {
	s.orchMu.Lock()
	defer s.orchMu.Unlock()
	_, item, err := s.workItemInFactory(ctx, factoryID, id)
	if err != nil {
		return err
	}
	return fn(item)
}

func (s *Server) workItemInFactory(ctx context.Context, factoryID, id string) (store.Factory, store.WorkItem, error) {
	fac, err := s.factory(ctx, factoryID)
	if err != nil {
		return store.Factory{}, store.WorkItem{}, err
	}
	item, err := s.store.GetWorkItem(ctx, id)
	if err != nil {
		return store.Factory{}, store.WorkItem{}, orchErr(err)
	}
	if item.FactoryID != fac.ID {
		return store.Factory{}, store.WorkItem{}, api.ErrNotFound
	}
	return fac, item, nil
}

func (s *Server) pipelineDef() (orchestrator.Def, error) {
	snap := s.snapshot()
	if snap.tree == nil || snap.tree.Factory.Pipeline == nil {
		return orchestrator.Def{}, errNoPipeline
	}
	p := snap.tree.Factory.Pipeline
	approvals := make(map[string]orchestrator.ApprovalGate, len(p.Approvals))
	for name, gate := range p.Approvals {
		approvals[name] = orchestrator.ApprovalGate{Required: gate.Required, Approvers: gate.Approvers}
	}
	return orchestrator.Def{
		Stages:        append([]string(nil), p.Stages...),
		Agents:        copyMap(p.Agents),
		Skip:          copyMap(p.Skip),
		Approvals:     approvals,
		RevisionLimit: p.RevisionLimit,
	}, nil
}

func (s *Server) loadState(ctx context.Context, item store.WorkItem) (orchestrator.State, error) {
	gate, err := s.pendingGate(ctx, item.ID)
	if err != nil {
		return orchestrator.State{}, err
	}
	def, err := s.pipelineDef()
	if err != nil {
		return orchestrator.State{}, err
	}
	agent := ""
	if def.Agents != nil {
		agent = def.Agents[item.Stage]
	}
	return orchestrator.State{
		Stage:          item.Stage,
		Status:         item.Status,
		RevisionCount:  item.RevisionCount,
		PendingGate:    gate,
		LastStageAgent: agent,
	}, nil
}

// commitStep persists the orchestrator state. A RequestApproval effect is
// inserted in the same transaction as the work-item update, so a crash cannot
// leave a pending row without awaiting_approval, and a reader of that status
// sees pending_gate.
func (s *Server) commitStep(ctx context.Context, item store.WorkItem, next orchestrator.State, effects []orchestrator.Effect) (store.WorkItem, error) {
	opened := false
	for _, effect := range effects {
		req, ok := effect.(orchestrator.RequestApproval)
		if !ok {
			continue
		}
		_, updated, err := s.store.OpenApprovalGate(ctx, item.ID, req.Gate, next.Stage, next.Status, next.RevisionCount)
		if err != nil {
			return store.WorkItem{}, err
		}
		item = updated
		opened = true
	}
	if opened {
		return item, nil
	}
	return s.commitState(ctx, item, next)
}

func (s *Server) commitState(ctx context.Context, item store.WorkItem, next orchestrator.State) (store.WorkItem, error) {
	updated, err := s.store.UpdateWorkItemStage(ctx, item.ID, next.Stage, next.Status)
	if err != nil {
		return store.WorkItem{}, err
	}
	if next.RevisionCount != updated.RevisionCount {
		updated, err = s.store.SetWorkItemRevision(ctx, item.ID, next.RevisionCount)
		if err != nil {
			return store.WorkItem{}, err
		}
	}
	return updated, nil
}

// applyEffects performs the single effect Step returned. parentRunID is the
// run that caused a StartRun, when there is one.
func (s *Server) applyEffects(ctx context.Context, item store.WorkItem, effects []orchestrator.Effect, parentRunID string) (bool, error) {
	cancel := false
	for _, effect := range effects {
		switch eff := effect.(type) {
		case orchestrator.StartRun:
			if parentRunID == "" {
				parentRunID = s.latestRunID(ctx, item.ID)
			}
			if _, err := s.startStageRun(ctx, item, eff.Stage, eff.Agent, parentRunID); err != nil {
				return false, err
			}
		case orchestrator.RequestApproval:
			if _, err := s.store.RequestApproval(ctx, item.ID, eff.Gate); err != nil {
				return false, err
			}
			s.log.Info("approval requested", "work_item_id", item.ID, "gate", eff.Gate)
		case orchestrator.MarkComplete:
			s.log.Info("work item complete", "work_item_id", item.ID)
		case orchestrator.MarkCancelled:
			cancel = true
			s.log.Info("work item cancelled", "work_item_id", item.ID)
		case orchestrator.Escalate:
			s.log.Info("work item escalated", "work_item_id", item.ID, "stage", item.Stage, "revision_count", item.RevisionCount)
		case orchestrator.Noop:
			continue
		default:
			return false, fmt.Errorf("server: unhandled orchestrator effect %T", effect)
		}
	}
	return cancel, nil
}

func (s *Server) prepareStageRun(ctx context.Context, item store.WorkItem, stage, agentName, parentRunID string) (store.NewRun, error) {
	snap := s.snapshot()
	agentID, bound, err := s.resolveAgent(agentName, item.FactoryID, snap)
	if err != nil {
		return store.NewRun{}, err
	}
	pool := bound.agent.Pool
	if pool == "" {
		pool = s.cfg.WorkerPool
	}
	return store.NewRun{
		FactoryID:      item.FactoryID,
		AgentID:        agentID,
		Pool:           pool,
		Priority:       store.PriorityPipeline,
		Prompt:         s.stagePrompt(ctx, item, stage, bound),
		Title:          item.Title,
		WorkItemID:     item.ID,
		ParentRunID:    parentRunID,
		ConversationID: item.ConversationID,
		Stage:          stage,
		ModelID:        resolveModelID(snap.tree, bound.agent),
	}, nil
}

func (s *Server) startStageRun(ctx context.Context, item store.WorkItem, stage, agentName, parentRunID string) (store.Run, error) {
	newRun, err := s.prepareStageRun(ctx, item, stage, agentName, parentRunID)
	if err != nil {
		return store.Run{}, err
	}
	if stage == store.StageBuilding {
		run, created, err := s.store.BeginBuildingRun(ctx, newRun)
		if err != nil {
			return store.Run{}, err
		}
		if !created {
			s.log.Info("building run already in flight", "work_item_id", item.ID, "run_id", run.ID)
			return run, nil
		}
		s.log.Info("stage run", "work_item_id", item.ID, "stage", stage, "run_id", run.ID, "agent", agentName, "model_id", newRun.ModelID)
		s.nudge()
		return run, nil
	}
	run, err := s.store.CreateRun(ctx, newRun)
	if err != nil {
		return store.Run{}, err
	}
	s.log.Info("stage run", "work_item_id", item.ID, "stage", stage, "run_id", run.ID, "agent", agentName, "model_id", newRun.ModelID)
	s.nudge()
	return run, nil
}

const noPullRequest = "Do not open a pull request."

// withNoPullRequest puts the trusted sentence in the agent instructions,
// which BuildPrompt writes before the work-item body and its untrusted fence.
func withNoPullRequest(body string) string {
	if strings.Contains(body, noPullRequest) {
		return body
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + noPullRequest + "\n"
}

func (s *Server) stagePrompt(ctx context.Context, item store.WorkItem, stage string, bound boundAgent) string {
	agent := bound.agent
	if stage != store.StageBuilding {
		agent.Body = withNoPullRequest(agent.Body)
	}
	prompt := handoff.BuildPrompt(agent, handoff.Context{
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
		Source:         item.Source,
		SourceRef:      item.SourceRef,
		Title:          item.Title,
		Body:           item.Body,
		PriorStages:    s.priorStages(ctx, item.ID),
		Env:            s.redactEnv(),
	})
	if stage != store.StageBuilding {
		return prompt
	}
	if s.store == nil || item.ID == "" {
		return prompt
	}
	url, ok, err := (&store.Automation{Store: s.store}).OpenPullRequest(ctx, item.ID)
	if err != nil {
		s.log.Error("pull request", "work_item_id", item.ID, "err", err)
		return prompt
	}
	if !ok || url == "" {
		return prompt
	}
	return "Pull request: " + url + "\nPush to this pull request. Do not open another pull request.\n\n" + prompt
}

func (s *Server) redactEnv() map[string]string {
	env := make(map[string]string, len(s.cfg.Secrets)+1)
	for name, value := range s.cfg.Secrets {
		env[name] = value
	}
	if s.cfg.InferenceAPIKey != "" {
		env["OPENAI_API_KEY"] = s.cfg.InferenceAPIKey
	}
	return env
}

func (s *Server) priorStages(ctx context.Context, workItemID string) []handoff.StageOutput {
	runs, err := s.runsForWorkItem(ctx, workItemID)
	if err != nil {
		s.log.Error("prior stages", "work_item_id", workItemID, "err", err)
		return nil
	}
	snap := s.snapshot()
	out := make([]handoff.StageOutput, 0, len(runs))
	for _, run := range runs {
		if !isTerminalStatus(run.Status) {
			continue
		}
		agent := ""
		if bound, ok := snap.byID[run.AgentID]; ok {
			agent = bound.name
		}
		out = append(out, handoff.StageOutput{
			Stage:       run.Stage,
			Agent:       agent,
			RunID:       run.ID,
			SummaryText: s.terminalSummary(ctx, run.ID),
		})
	}
	return out
}

func (s *Server) terminalSummary(ctx context.Context, runID string) string {
	var summary string
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(payload_json->>'summary', '')
		FROM run_events
		WHERE run_id = $1 AND type = 'run.terminal'
		ORDER BY seq DESC
		LIMIT 1`, runID).Scan(&summary)
	if err != nil {
		return ""
	}
	return summary
}

func (s *Server) runsForWorkItem(ctx context.Context, workItemID string) ([]store.Run, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id FROM runs
		WHERE work_item_id = $1
		ORDER BY created_at ASC, id ASC`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]store.Run, 0, len(ids))
	for _, id := range ids {
		run, err := s.store.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *Server) latestRunID(ctx context.Context, workItemID string) string {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM runs
		WHERE work_item_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, workItemID).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

func (s *Server) pendingGate(ctx context.Context, workItemID string) (string, error) {
	var gate string
	err := s.db.QueryRow(ctx, `
		SELECT gate FROM approvals
		WHERE work_item_id = $1 AND decision = ''
		ORDER BY requested_at ASC, id ASC
		LIMIT 1`, workItemID).Scan(&gate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return gate, nil
}

func (s *Server) taskView(ctx context.Context, item store.WorkItem) (api.Task, error) {
	gate := ""
	if item.Status == store.WorkItemAwaitingApproval {
		var err error
		gate, err = s.pendingGate(ctx, item.ID)
		if err != nil {
			return api.Task{}, err
		}
	}
	return toAPITask(item, gate), nil
}

func (s *Server) cancelWorkItemRuns(ctx context.Context, workItemID string) {
	runs, err := s.runsForWorkItem(ctx, workItemID)
	if err != nil {
		s.log.Error("cancel work item runs", "work_item_id", workItemID, "err", err)
		return
	}
	for _, run := range runs {
		if isTerminalStatus(run.Status) {
			continue
		}
		if _, err := s.CancelRun(ctx, run.ID); err != nil && !errors.Is(err, api.ErrConflict) {
			s.log.Error("cancel stage run", "work_item_id", workItemID, "run_id", run.ID, "err", err)
			continue
		}
		if err := s.settleParkedCancel(ctx, run.ID); err != nil {
			s.log.Error("settle parked cancel", "work_item_id", workItemID, "run_id", run.ID, "err", err)
		}
	}
}

// releaseWorkerSlot drops the run from scheduler capacity. The execution
// stays tracked, so the lease keeps renewing and the run is not requeued.
func (s *Server) releaseWorkerSlot(runID string) {
	s.mu.Lock()
	s.suspended[runID] = struct{}{}
	s.mu.Unlock()
	s.nudge()
}

func (s *Server) clearSuspended(runID string) {
	s.mu.Lock()
	delete(s.suspended, runID)
	s.mu.Unlock()
}

func (s *Server) suspendedSnapshot() map[string]struct{} {
	s.mu.Lock()
	out := make(map[string]struct{}, len(s.suspended))
	for id := range s.suspended {
		out[id] = struct{}{}
	}
	s.mu.Unlock()
	if s.store == nil {
		return out
	}
	// The leader's first tick is process startup. Clearing here, and again
	// on later ticks, drops suspensions whose runs migration 0005 already
	// marked terminal. A second pass matches no rows.
	sweepCtx := context.WithoutCancel(s.ctx)
	if err := s.sweepStaleSuspensions(sweepCtx); err != nil && s.ctx.Err() == nil && s.log != nil {
		s.log.Error("stale suspensions", "err", err)
	}
	if err := s.recoverReviewerRetries(sweepCtx); err != nil && s.ctx.Err() == nil && s.log != nil {
		s.log.Error("reviewer retry", "err", err)
	}
	ids, err := s.store.ListSuspendedRunIDs(sweepCtx)
	if err != nil {
		if s.ctx.Err() == nil && s.log != nil {
			s.log.Error("suspended runs", "err", err)
		}
		return out
	}
	for _, id := range ids {
		out[id] = struct{}{}
	}
	s.reconcileSuspended(context.WithoutCancel(s.ctx), ids)
	return out
}

// reconcileSuspended parks a run that still has a queue row after the input
// request committed, and finishes a parked run whose cancel was only flagged.
// ExpireLeases does not read suspended_run_id, so a leftover row would
// otherwise be requeued. The leader calls this after ExpireLeases, and again
// on startup when the loop ticks.
func (s *Server) reconcileSuspended(ctx context.Context, ids []string) {
	for _, id := range ids {
		if err := s.parkSuspendedRun(ctx, id); err != nil {
			if ctx.Err() == nil && s.log != nil {
				s.log.Error("repark suspended run", "run_id", id, "err", err)
			}
			continue
		}
		if err := s.settleParkedCancel(ctx, id); err != nil && ctx.Err() == nil && s.log != nil {
			s.log.Error("settle parked cancel", "run_id", id, "err", err)
		}
	}
}

// settleParkedCancel makes a cancel-requested parked run terminal in the
// database. A parked run has no worker, so waiting for the in-memory grace
// timer would lose the cancel across a restart.
func (s *Server) settleParkedCancel(ctx context.Context, runID string) error {
	if !s.runSuspended(ctx, runID) {
		return nil
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if isTerminalStatus(run.Status) || !run.CancelRequested {
		return nil
	}
	live, err := s.liveExecution(ctx, runID)
	if err != nil {
		return err
	}
	if live != "" {
		return nil
	}
	s.settleCancel(ctx, runID)
	s.clearSuspended(runID)
	return nil
}

func (s *Server) runSuspended(ctx context.Context, runID string) bool {
	if cleared, err := s.clearStaleSuspension(ctx, runID); err != nil {
		if s.log != nil && ctx.Err() == nil {
			s.log.Error("stale suspension", "run_id", runID, "err", err)
		}
	} else if cleared {
		return false
	}
	s.mu.Lock()
	_, ok := s.suspended[runID]
	s.mu.Unlock()
	if ok {
		return true
	}
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM work_items WHERE suspended_run_id = $1`, runID).Scan(&n)
	return err == nil && n > 0
}

// clearStaleSuspensionSQL drops a suspended_run_id that names a terminal run.
// The predicate is the current value, so a concurrent answer or a newer
// suspension of a different run is left alone. Repeating it changes nothing.
const clearStaleSuspensionSQL = `
UPDATE work_items
SET suspended_run_id = '',
    suspended_execution_id = '',
    suspended_at = NULL
WHERE suspended_run_id = $1
  AND EXISTS (
    SELECT 1 FROM runs
    WHERE id = $1
      AND status IN ($2, $3, $4)
  )`

func clearStaleSuspensionTx(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	if runID == "" {
		return false, nil
	}
	tag, err := tx.Exec(ctx, clearStaleSuspensionSQL,
		runID, store.StatusSucceeded, store.StatusFailed, store.StatusCancelled)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// clearStaleSuspension treats a suspended_run_id whose run is terminal as no
// suspension. The update is one transaction guarded on that id.
func (s *Server) clearStaleSuspension(ctx context.Context, runID string) (bool, error) {
	if runID == "" || s.db == nil {
		return false, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cleared, err := clearStaleSuspensionTx(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if cleared {
		s.clearSuspended(runID)
	}
	return cleared, nil
}

// sweepStaleSuspensions clears every suspended_run_id that names a terminal
// run. Each update is guarded on the id read in this transaction. A second
// call matches no rows.
func (s *Server) sweepStaleSuspensions(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT w.suspended_run_id
		FROM work_items w
		JOIN runs r ON r.id = w.suspended_run_id
		WHERE w.suspended_run_id <> ''
		  AND r.status IN ($1, $2, $3)
		FOR UPDATE OF w`,
		store.StatusSucceeded, store.StatusFailed, store.StatusCancelled)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	cleared := make([]string, 0, len(ids))
	for _, id := range ids {
		ok, err := clearStaleSuspensionTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if ok {
			cleared = append(cleared, id)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range cleared {
		s.clearSuspended(id)
	}
	return nil
}

// reviewerInfra reports whether this run is an infrastructure failure.
// Class comes from failure.Classify. The runs row carries status,
// failure_reason, and exit_code. Harness status and
// executions.failure_details_json are not columns of runs, so they are
// not passed.
func reviewerInfra(status, reason string, exit *int) bool {
	return failure.Classify(failure.Input{
		RunStatus:     status,
		FailureReason: reason,
		ExitCode:      exit,
	}).Class == failure.ClassInfrastructure
}

// priorReviewerInfraFails counts infrastructure-failed reviewing runs for the
// work item since the latest building run, excluding currentRunID.
//
// The count is derived from committed run rows, not from a column and not
// from process memory. A restart reads the same rows, so a retry that
// already happened is visible and is not started again. The caller runs this
// in the same transaction that inserts the retry, under a lock on the work
// item, so two processes cannot both observe an unused budget and both insert.
func (s *Server) priorReviewerInfraFails(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, workItemID, currentRunID string) (int, error) {
	rows, err := q.Query(ctx, `
		SELECT r.status, COALESCE(r.failure_reason, ''), r.exit_code
		FROM runs r
		WHERE r.work_item_id = $1
		  AND r.stage = $2
		  AND r.status = $3
		  AND r.id <> $4
		  AND NOT EXISTS (
		    SELECT 1 FROM runs b
		    WHERE b.work_item_id = r.work_item_id
		      AND b.stage = $5
		      AND (b.created_at, b.id) > (r.created_at, r.id)
		  )`,
		workItemID, store.StageReviewing, store.StatusFailed, currentRunID, store.StageBuilding)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var status, reason string
		var exit *int
		if err := rows.Scan(&status, &reason, &exit); err != nil {
			return 0, err
		}
		if reviewerInfra(status, reason, exit) {
			n++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// retryReviewerOnce starts one more reviewing run when this infrastructure
// failure is the first since the latest building run. handled is true when
// the retry was started or is already in flight. handled is false when the
// budget is used; the caller then revises.
func (s *Server) retryReviewerOnce(ctx context.Context, item store.WorkItem, failed store.Run) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var stage, status string
	if err := tx.QueryRow(ctx, `
		SELECT stage, status FROM work_items WHERE id = $1 FOR UPDATE`, item.ID).Scan(&stage, &status); err != nil {
		return false, err
	}
	if stage != store.StageReviewing || status != store.WorkItemOpen {
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	var inflight bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM runs
			WHERE work_item_id = $1
			  AND stage = $2
			  AND status IN ($3, $4, $5)
		)`, item.ID, store.StageReviewing, store.StatusQueued, store.StatusAssigned, store.StatusRunning).Scan(&inflight); err != nil {
		return false, err
	}
	if inflight {
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	// Count before building a prompt. A used budget, including one noticed
	// on a later startup tick, inserts nothing.
	prior, err := s.priorReviewerInfraFails(ctx, tx, item.ID, failed.ID)
	if err != nil {
		return false, err
	}
	if prior > 0 {
		return false, nil
	}

	def, err := s.pipelineDef()
	if err != nil {
		return false, err
	}
	state, err := s.loadState(ctx, item)
	if err != nil {
		return false, err
	}
	state.ReviewerRetryUsed = false
	next, effects, err := step(def, state, orchestrator.RunFailed{Stage: store.StageReviewing, Infra: true})
	if err != nil {
		return false, err
	}
	if len(effects) != 1 {
		return false, fmt.Errorf("server: reviewer retry effects %d", len(effects))
	}
	start, ok := effects[0].(orchestrator.StartRun)
	if !ok || start.Stage != orchestrator.StageReviewing {
		return false, fmt.Errorf("server: reviewer retry effect %T", effects[0])
	}
	if next.Stage != orchestrator.StageReviewing || next.RevisionCount != item.RevisionCount {
		return false, fmt.Errorf("server: reviewer retry state stage %s revision %d", next.Stage, next.RevisionCount)
	}
	newRun, err := s.prepareStageRun(ctx, item, start.Stage, start.Agent, failed.ID)
	if err != nil {
		return false, err
	}
	id, err := store.NewID(store.PrefixRun)
	if err != nil {
		return false, err
	}
	if err := insertQueuedRun(ctx, tx, id, newRun); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	s.log.Info("stage run", "work_item_id", item.ID, "stage", start.Stage, "run_id", id, "agent", start.Agent, "model_id", newRun.ModelID)
	s.nudge()
	return true, nil
}

// recoverReviewerRetries starts the one reviewer retry when the failure
// committed and the process stopped before the retry run did. A retry that
// is already queued is left alone. A used budget is not revised here; that
// decision stays on advanceAfterTerminal for the failure that used it.
func (s *Server) recoverReviewerRetries(ctx context.Context) error {
	if s.db == nil || s.store == nil {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.status, COALESCE(r.failure_reason, ''), r.exit_code
		FROM work_items w
		JOIN LATERAL (
			SELECT id, status, failure_reason, exit_code
			FROM runs
			WHERE work_item_id = w.id AND stage = $1
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) r ON true
		WHERE w.status = $2 AND w.stage = $1 AND r.status = $3`,
		store.StageReviewing, store.WorkItemOpen, store.StatusFailed)
	if err != nil {
		return err
	}
	defer rows.Close()
	type failedReview struct {
		id     string
		status string
		reason string
		exit   *int
	}
	var pending []failedReview
	for rows.Next() {
		var row failedReview
		if err := rows.Scan(&row.id, &row.status, &row.reason, &row.exit); err != nil {
			return err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, row := range pending {
		if !reviewerInfra(row.status, row.reason, row.exit) {
			continue
		}
		run, err := s.store.GetRun(ctx, row.id)
		if err != nil {
			return err
		}
		item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
		if err != nil {
			return err
		}
		if _, err := s.retryReviewerOnce(ctx, item, run); err != nil {
			return err
		}
	}
	return nil
}

func insertQueuedRun(ctx context.Context, tx pgx.Tx, id string, r store.NewRun) error {
	var agentID any
	if r.AgentID != "" {
		agentID = r.AgentID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (
		    id, factory_id, agent_id, status, priority, pool, prompt, title, repo,
		    work_item_id, parent_run_id, conversation_id, stage, model_id)
		VALUES (
		    $1, $2, $3, $4, $5, $6, $7, $8, $9,
		    NULLIF($10, ''), NULLIF($11, ''), $12, $13, $14)`,
		id, r.FactoryID, agentID, store.StatusQueued, int(r.Priority), r.Pool, r.Prompt, r.Title, r.Repo,
		r.WorkItemID, r.ParentRunID, r.ConversationID, r.Stage, r.ModelID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())`,
		id, r.FactoryID, r.Pool, int(r.Priority)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('run_queue', $1)`, id); err != nil {
		return err
	}
	return nil
}

func (s *Server) runIsTerminal(ctx context.Context, runID string) (bool, error) {
	if runID == "" {
		return false, nil
	}
	run, err := s.store.GetRun(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return isTerminalStatus(run.Status), nil
}

func (s *Server) latestQuestion(ctx context.Context, runID string) (string, error) {
	var text string
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(payload_json->>'text', '')
		FROM run_events
		WHERE run_id = $1 AND type = 'question'
		ORDER BY seq DESC
		LIMIT 1`, runID).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// runStillSuspended locks the work item that still names this run, if any.
// A concurrent answer clears suspended_run_id under the same row lock, so
// the caller observes the committed value and does not park a resumed run.
func (s *Server) runStillSuspended(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM work_items
		WHERE suspended_run_id = $1
		FOR UPDATE`, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	held := false
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		held = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return held, nil
}

// parkSuspendedRun ends the live execution and removes the queue row.
// executions.state has no suspended value, so the attempt ends as orphaned.
// The run stays running and off the queue: lease expiry has nothing to
// requeue, and a restart does not see a queued run. The work item's
// suspended_* columns are the durable record.
//
// The leader loads suspended run ids, then waits on leaseMu. AnswerInput
// can clear suspended_run_id and queue the resume in that gap. Parking is
// conditional on the work item still naming this run, in this transaction,
// so that stale id does not delete the resume's queue row.
func (s *Server) parkSuspendedRun(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var queued string
	err = tx.QueryRow(ctx, `SELECT run_id FROM run_queue WHERE run_id = $1 FOR UPDATE`, runID).Scan(&queued)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1 FOR UPDATE`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
		if _, err := clearStaleSuspensionTx(ctx, tx, runID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.clearSuspended(runID)
		return nil
	}
	still, err := s.runStillSuspended(ctx, tx, runID)
	if err != nil {
		return err
	}
	if !still {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE executions
		SET state = $2, ended_at = COALESCE(ended_at, clock_timestamp()), failure_reason = NULL
		WHERE run_id = $1 AND state IN ($3, $4)`,
		runID, store.ExecOrphaned, store.ExecAssigned, store.ExecRunning); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, worker_id = NULL, ended_at = NULL, failure_reason = NULL
		WHERE id = $1 AND status IN ($3, $4, $2)`,
		runID, store.StatusRunning, store.StatusQueued, store.StatusAssigned); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// stageAgentID resolves the pipeline agent for the work item's stage.
// An empty stage mapping is an error. It does not call resolveAgent with an
// empty name, which would bind the factory's only agent.
func (s *Server) stageAgentID(item store.WorkItem, factoryID string) (string, error) {
	def, err := s.pipelineDef()
	if err != nil {
		return "", err
	}
	agentName := ""
	if def.Agents != nil {
		agentName = strings.TrimSpace(def.Agents[item.Stage])
	}
	if agentName == "" {
		return "", fmt.Errorf("server: stage %q has no agent", item.Stage)
	}
	agentID, _, err := s.resolveAgent(agentName, factoryID, s.snapshot())
	if err != nil {
		return "", err
	}
	return agentID, nil
}

func (s *Server) resumeStored(ctx context.Context, req store.InputRequest, agentID string) error {
	run, err := s.store.GetRun(ctx, req.RunID)
	if err != nil {
		return orchErr(err)
	}
	if agentID == "" {
		item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
		if err != nil {
			return orchErr(err)
		}
		agentID, err = s.stageAgentID(item, run.FactoryID)
		if err != nil {
			return err
		}
	}
	return s.resumeIfParked(ctx, run, agentID, req.Question, req.Answer)
}

// resumeIfParked queues one new execution for this answer. A run that lease
// expiry already put back on the queue is updated in place so the answer
// still yields one execution. A second call finds that execution and leaves
// it alone. A pre-answer lease is cleared; a lease taken after the answer
// was written into the prompt is left in place.
func (s *Server) resumeIfParked(ctx context.Context, run store.Run, agentID, question, answer string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var queued string
	err = tx.QueryRow(ctx, `SELECT run_id FROM run_queue WHERE run_id = $1 FOR UPDATE`, run.ID).Scan(&queued)
	hasQueue := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		hasQueue = false
		err = nil
	}
	if err != nil {
		return err
	}
	var status, prompt, factoryID, pool string
	var priority int
	err = tx.QueryRow(ctx, `
		SELECT status, prompt, factory_id, pool, priority
		FROM runs WHERE id = $1 FOR UPDATE`, run.ID).Scan(&status, &prompt, &factoryID, &pool, &priority)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled:
		if _, err := clearStaleSuspensionTx(ctx, tx, run.ID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.clearSuspended(run.ID)
		return nil
	}
	var live bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM executions
			WHERE run_id = $1 AND state IN ($2, $3)
		)`, run.ID, store.ExecAssigned, store.ExecRunning).Scan(&live); err != nil {
		return err
	}
	section := inputSection(question, answer)
	if strings.Contains(prompt, section) && (hasQueue || live) {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		s.clearSuspended(run.ID)
		return nil
	}
	nextPrompt := prompt
	if !strings.Contains(prompt, section) {
		if _, err := tx.Exec(ctx, `
			UPDATE executions
			SET state = $2, ended_at = COALESCE(ended_at, clock_timestamp()), failure_reason = NULL
			WHERE run_id = $1 AND state IN ($3, $4)`,
			run.ID, store.ExecOrphaned, store.ExecAssigned, store.ExecRunning); err != nil {
			return err
		}
		nextPrompt = resumePrompt(prompt, question, answer)
		if hasQueue {
			if _, err := tx.Exec(ctx, `UPDATE run_queue SET lease_until = NULL WHERE run_id = $1`, run.ID); err != nil {
				return err
			}
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, agent_id = $3, prompt = $4, worker_id = NULL, ended_at = NULL, failure_reason = NULL
		WHERE id = $1 AND status IN ($2, $5, $6)`,
		run.ID, store.StatusQueued, agentID, nextPrompt, store.StatusAssigned, store.StatusRunning)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("server: resume run %s from %s", run.ID, status)
	}
	if !hasQueue {
		if _, err := tx.Exec(ctx, `
			INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at)
			VALUES ($1, $2, $3, $4, clock_timestamp())`,
			run.ID, factoryID, pool, priority); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('run_queue', $1)`, run.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.clearSuspended(run.ID)
	s.nudge()
	return nil
}

func inputSection(question, answer string) string {
	return "\n# Input\nQuestion: " + question + "\nAnswer: " + answer + "\n"
}

func resumePrompt(prompt, question, answer string) string {
	section := inputSection(question, answer)
	if strings.Contains(prompt, section) {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	if prompt != "" && !strings.HasSuffix(prompt, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(section)
	return b.String()
}

func withoutSuspended(runs []store.Run, held map[string]struct{}) []store.Run {
	if len(held) == 0 {
		return runs
	}
	out := make([]store.Run, 0, len(runs))
	for _, run := range runs {
		if _, ok := held[run.ID]; ok {
			continue
		}
		out = append(out, run)
	}
	return out
}

// resolveModelID picks the concrete model placed on the run.
// A prompt router uses its default. A complexity router uses routing.medium.
func resolveModelID(tree *definition.Tree, agent definition.Agent) string {
	model := strings.TrimSpace(agent.Model)
	if model == "" && agent.Harness != nil {
		model = strings.TrimSpace(agent.Harness.Model)
	}
	if tree == nil || model == "" {
		return model
	}
	router, ok := tree.Routers[model]
	if !ok {
		return model
	}
	switch router.Type {
	case "prompt":
		return router.Default
	case "complexity":
		if medium := strings.TrimSpace(router.Complexity["medium"]); medium != "" {
			return medium
		}
		return router.Default
	default:
		return router.Default
	}
}

func sawQuestion(lines [][]byte) bool {
	for _, line := range lines {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "question" {
			return true
		}
	}
	return false
}

func toAPITask(item store.WorkItem, gate string) api.Task {
	return api.Task{
		ID:             item.ID,
		FactoryID:      item.FactoryID,
		Source:         item.Source,
		SourceRef:      item.SourceRef,
		Title:          item.Title,
		Body:           item.Body,
		Stage:          item.Stage,
		Status:         item.Status,
		CreatedBy:      item.CreatedBy,
		ConversationID: item.ConversationID,
		RevisionCount:  item.RevisionCount,
		PendingGate:    gate,
		CreatedAt:      item.CreatedAt,
	}
}

func toAPIApproval(row store.Approval) api.Approval {
	return api.Approval{
		ID:          row.ID,
		WorkItemID:  row.WorkItemID,
		Gate:        row.Gate,
		RequestedAt: row.RequestedAt,
		DecidedAt:   row.DecidedAt,
		DecidedBy:   row.DecidedBy,
		Decision:    row.Decision,
		Note:        row.Note,
	}
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func orchErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return api.ErrNotFound
	case errors.Is(err, store.ErrAlreadyDecided), errors.Is(err, store.ErrConflict), errors.Is(err, orchestrator.ErrTerminal):
		return api.ErrConflict
	default:
		return err
	}
}

var (
	errNoPipeline = errors.New("server: factory has no pipeline")
	errCancelRuns = errors.New("server: cancel work item runs")
)
