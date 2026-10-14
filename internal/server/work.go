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
	if err := s.openGates(ctx, item.ID, effects); err != nil {
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
		if err := s.openGates(ctx, current.ID, effects); err != nil {
			return err
		}
		updated, err := s.commitState(ctx, current, next)
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
	if err := s.openGates(ctx, item.ID, effects); err != nil {
		s.orchMu.Unlock()
		return api.Approval{}, err
	}
	updated, err := s.commitState(ctx, item, next)
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
	if err := s.openGates(ctx, item.ID, effects); err != nil {
		s.orchMu.Unlock()
		s.log.Error("approval", "work_item_id", item.ID, "err", err)
		return
	}
	updated, err := s.commitState(ctx, item, next)
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

// onQuestion suspends the work item and releases the worker slot. The run
// stays assigned so the logical execution is not retried or finished.
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
	next, effects, err := step(def, state, orchestrator.AskInput{})
	if err != nil {
		s.log.Info("question ignored", "work_item_id", item.ID, "err", err)
		return
	}
	if err := s.openGates(ctx, item.ID, effects); err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	updated, err := s.commitState(ctx, item, next)
	if err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
		return
	}
	if _, err := s.applyEffects(ctx, updated, effects, "", run.ID); err != nil {
		s.log.Error("question", "work_item_id", item.ID, "err", err)
	}
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

// openGates inserts a pending approval before the work item status becomes
// awaiting_approval. A reader then sees pending_gate with that status.
func (s *Server) openGates(ctx context.Context, workItemID string, effects []orchestrator.Effect) error {
	for _, effect := range effects {
		req, ok := effect.(orchestrator.RequestApproval)
		if !ok {
			continue
		}
		if _, err := s.store.RequestApproval(ctx, workItemID, req.Gate); err != nil {
			return err
		}
	}
	return nil
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
	defer s.mu.Unlock()
	out := make(map[string]struct{}, len(s.suspended))
	for id := range s.suspended {
		out[id] = struct{}{}
	}
	return out
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
	case errors.Is(err, store.ErrAlreadyDecided), errors.Is(err, orchestrator.ErrTerminal):
		return api.ErrConflict
	default:
		return err
	}
}

var (
	errNoPipeline = errors.New("server: factory has no pipeline")
	errCancelRuns = errors.New("server: cancel work item runs")
)
