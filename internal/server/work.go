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
	if _, err := s.applyEffects(ctx, item, effects, "", ""); err != nil {
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
		cancel, err := s.applyEffects(ctx, updated, effects, "", "")
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
	if _, err := s.applyEffects(ctx, updated, effects, "", ""); err != nil {
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
		ev = orchestrator.RunFailed{Stage: run.Stage}
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
	cancel, err := s.applyEffects(ctx, updated, effects, run.ID, "")
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
// answer returns api.ErrConflict.
func (s *Server) AnswerInput(ctx context.Context, workItemID, answer, answeredBy string) (store.InputRequest, error) {
	s.orchMu.Lock()
	defer s.orchMu.Unlock()
	item, err := s.store.GetWorkItem(ctx, workItemID)
	if err != nil {
		return store.InputRequest{}, orchErr(err)
	}
	_, pendErr := s.store.PendingInputRequest(ctx, workItemID)
	if pendErr != nil && !errors.Is(pendErr, store.ErrNotFound) {
		return store.InputRequest{}, pendErr
	}
	if pendErr == nil {
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
	}
	stored, err := s.store.AnswerInputRequest(ctx, workItemID, answer, answeredBy)
	if err != nil {
		return store.InputRequest{}, orchErr(err)
	}
	if err := s.resumeStored(ctx, stored); err != nil {
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
// run that caused a StartRun, when there is one. suspendRunID is the run
// whose worker slot AskInput releases. The logical run is not finished.
func (s *Server) applyEffects(ctx context.Context, item store.WorkItem, effects []orchestrator.Effect, parentRunID, suspendRunID string) (bool, error) {
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
			if suspendRunID != "" {
				s.releaseWorkerSlot(suspendRunID)
				s.log.Info("work item awaiting input", "work_item_id", item.ID, "run_id", suspendRunID)
			}
		default:
			return false, fmt.Errorf("server: unhandled orchestrator effect %T", effect)
		}
	}
	return cancel, nil
}

func (s *Server) startStageRun(ctx context.Context, item store.WorkItem, stage, agentName, parentRunID string) (store.Run, error) {
	snap := s.snapshot()
	agentID, bound, err := s.resolveAgent(agentName, item.FactoryID, snap)
	if err != nil {
		return store.Run{}, err
	}
	pool := bound.agent.Pool
	if pool == "" {
		pool = s.cfg.WorkerPool
	}
	prompt := s.stagePrompt(ctx, item, bound)
	model := resolveModelID(snap.tree, bound.agent)
	run, err := s.store.CreateRun(ctx, store.NewRun{
		FactoryID:      item.FactoryID,
		AgentID:        agentID,
		Pool:           pool,
		Priority:       store.PriorityPipeline,
		Prompt:         prompt,
		Title:          item.Title,
		WorkItemID:     item.ID,
		ParentRunID:    parentRunID,
		ConversationID: item.ConversationID,
		Stage:          stage,
		ModelID:        model,
	})
	if err != nil {
		return store.Run{}, err
	}
	s.log.Info("stage run", "work_item_id", item.ID, "stage", stage, "run_id", run.ID, "agent", agentName, "model_id", model)
	s.nudge()
	return run, nil
}

func (s *Server) stagePrompt(ctx context.Context, item store.WorkItem, bound boundAgent) string {
	return handoff.BuildPrompt(bound.agent, handoff.Context{
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
		Source:         item.Source,
		SourceRef:      item.SourceRef,
		Title:          item.Title,
		Body:           item.Body,
		PriorStages:    s.priorStages(ctx, item.ID),
		Env:            s.redactEnv(),
	})
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
	ids, err := s.store.ListSuspendedRunIDs(context.WithoutCancel(s.ctx))
	if err != nil {
		if s.ctx.Err() == nil && s.log != nil {
			s.log.Error("suspended runs", "err", err)
		}
		return out
	}
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func (s *Server) runSuspended(ctx context.Context, runID string) bool {
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

// parkSuspendedRun ends the live execution and removes the queue row.
// executions.state has no suspended value, so the attempt ends as orphaned.
// The run stays running and off the queue: lease expiry has nothing to
// requeue, and a restart does not see a queued run. The work item's
// suspended_* columns are the durable record.
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

func (s *Server) resumeStored(ctx context.Context, req store.InputRequest) error {
	run, err := s.store.GetRun(ctx, req.RunID)
	if err != nil {
		return orchErr(err)
	}
	item, err := s.store.GetWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return orchErr(err)
	}
	def, err := s.pipelineDef()
	if err != nil {
		return err
	}
	agentName := ""
	if def.Agents != nil {
		agentName = def.Agents[item.Stage]
	}
	agentID, _, err := s.resolveAgent(agentName, run.FactoryID, s.snapshot())
	if err != nil {
		return err
	}
	return s.resumeIfParked(ctx, run, agentID, req.Question, req.Answer)
}

// resumeIfParked queues one new execution when the run is parked: still
// running, absent from the queue, and without a live execution. A second
// call finds the run queued or executing and leaves it alone.
func (s *Server) resumeIfParked(ctx context.Context, run store.Run, agentID, question, answer string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Point the parked run at the current agent row. A restarted process
	// applies the definition again and mints new agent ids.
	tag, err := tx.Exec(ctx, `
		UPDATE runs
		SET status = $2, agent_id = $3, prompt = $4, worker_id = NULL, ended_at = NULL, failure_reason = NULL
		WHERE id = $1
		  AND status = $5
		  AND NOT EXISTS (SELECT 1 FROM run_queue q WHERE q.run_id = $1)
		  AND NOT EXISTS (
		    SELECT 1 FROM executions e
		    WHERE e.run_id = $1 AND e.state IN ($6, $7)
		  )`,
		run.ID, store.StatusQueued, agentID, resumePrompt(run.Prompt, question, answer), store.StatusRunning,
		store.ExecAssigned, store.ExecRunning)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())`,
		run.ID, run.FactoryID, run.Pool, int(run.Priority)); err != nil {
		return err
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

func resumePrompt(prompt, question, answer string) string {
	var b strings.Builder
	b.WriteString(prompt)
	if prompt != "" && !strings.HasSuffix(prompt, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("\n# Input\n")
	b.WriteString("Question: ")
	b.WriteString(question)
	b.WriteByte('\n')
	b.WriteString("Answer: ")
	b.WriteString(answer)
	b.WriteByte('\n')
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
