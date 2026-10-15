package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/orchestrator"
	"github.com/polds/factory/internal/trigger"
)

const maxEventDepth = 8

// Engine matches events to automations and starts or continues work.
// Handle is safe for concurrent use. Idempotency lives in Store, so a
// second engine and a re-claimed delivery observe the same rows.
type Engine struct {
	Store Store
	// Answer resumes a parked run. T30 sets an adapter around
	// (*server.Server).AnswerInput. See ResumingAnswerer. A nil Answer
	// is a configuration error on an eligible reply; Handle does not
	// fall through to the store method.
	Answer    ResumingAnswerer
	Tree      func() definition.Tree
	FactoryID string
	Allow     Allow
	Now       func() time.Time

	// mu guards cache only. Store calls run outside it.
	mu    sync.Mutex
	cache map[string][]compiled
}

type compiled struct {
	auto     definition.Automation
	matchers []trigger.Matcher
}

// Handle applies ev. See the package comment for the match and
// continuation rules.
func (e *Engine) Handle(ctx context.Context, ev event.Event) (Result, error) {
	return e.handle(ctx, ev, 0)
}

func (e *Engine) handle(ctx context.Context, ev event.Event, depth int) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if depth > maxEventDepth {
		return Result{}, errors.New("automation: stage event depth exceeded")
	}
	if e == nil || e.Store == nil || e.Tree == nil || e.FactoryID == "" {
		return Result{}, errors.New("automation: engine is not configured")
	}
	if ev.DeliveryID == "" {
		return Result{}, errors.New("automation: delivery id is required")
	}
	if strings.TrimSpace(ev.Thread.ExternalRef) == "" {
		return Result{}, errors.New("automation: external ref is required")
	}

	res, emit, err := e.decide(ctx, ev)
	if err != nil || emit == nil {
		return res, err
	}
	child, err := e.handle(ctx, *emit, depth+1)
	if err != nil {
		return Result{}, err
	}
	res.Emitted = append([]event.Event{*emit}, child.Emitted...)
	res.Outcomes = append(res.Outcomes, child.Outcomes...)
	return res, nil
}

// decide does not hold e.mu. Store methods run here.
func (e *Engine) decide(ctx context.Context, ev event.Event) (Result, *event.Event, error) {
	tree := e.Tree()
	if shape := replyShape(tree, ev); shape != replyNone {
		ref, err := e.Store.GetExternalRef(ctx, ev.Provider, ev.Thread.ExternalRef)
		switch {
		case err == nil:
			return e.continueReply(ctx, tree, ev, ref, shape)
		case errors.Is(err, ErrNotFound):
		default:
			return Result{}, nil, fmt.Errorf("automation: external ref: %w", err)
		}
	}

	matches := e.matching(tree, ev)
	if len(matches) == 0 {
		return Result{}, nil, nil
	}
	return e.intake(ctx, tree, ev, matches)
}

func (e *Engine) matching(tree definition.Tree, ev event.Event) []compiled {
	// The GitHub normalizer appends [bot] when sender.type is Bot.
	// That includes labels the App applies. Those actors do not open work.
	if e.bot(ev) {
		return nil
	}
	caused := causation(ev)
	tev := toTrigger(ev)
	var out []compiled
	for _, auto := range e.compiled(tree) {
		if !auto.auto.Enabled || contains(caused, auto.auto.Name) {
			continue
		}
		if matchAny(auto.matchers, tev) {
			out = append(out, auto)
		}
	}
	return out
}

func (e *Engine) compiled(tree definition.Tree) []compiled {
	e.mu.Lock()
	defer e.mu.Unlock()
	if hit, ok := e.cache[tree.Digest]; ok {
		return hit
	}
	names := make([]string, 0, len(tree.Automations))
	for name := range tree.Automations {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]compiled, 0, len(names))
	for _, name := range names {
		auto := tree.Automations[name]
		matchers := make([]trigger.Matcher, 0, len(auto.Triggers))
		for _, trig := range auto.Triggers {
			m, _ := trigger.Compile(trig)
			if m == nil {
				continue
			}
			matchers = append(matchers, m)
		}
		out = append(out, compiled{auto: auto, matchers: matchers})
	}
	if e.cache == nil {
		e.cache = map[string][]compiled{}
	}
	e.cache[tree.Digest] = out
	return out
}

func (e *Engine) intake(ctx context.Context, tree definition.Tree, ev event.Event, matches []compiled) (Result, *event.Event, error) {
	def, err := pipelineDef(tree)
	if err != nil {
		return Result{}, nil, err
	}
	next, effects, err := orchestrator.Step(def, orchestrator.State{}, orchestrator.Created{}, orchestrator.M3Predicates())
	if err != nil {
		return Result{}, nil, err
	}
	if len(effects) != 1 {
		return Result{}, nil, errors.New("automation: orchestrator returned no effect")
	}
	stage, status := next.Stage, next.Status
	if stage == "" {
		stage = firstStage(def, effects)
	}
	item, _, err := e.Store.OpenWorkItem(ctx, ev.Provider, ev.Thread.ExternalRef, NewWorkItem{
		FactoryID: e.FactoryID,
		Source:    ev.Provider,
		SourceRef: ev.Thread.ExternalRef,
		Title:     titleOf(ev),
		Body:      eventFence(ev),
		Stage:     stage,
		Status:    status,
		CreatedBy: actorName(ev),
	})
	if err != nil {
		return Result{}, nil, fmt.Errorf("automation: open work item: %w", err)
	}
	slots, err := intakeSlots(tree, ev, item, matches, effects[0])
	if err != nil {
		return Result{}, nil, err
	}
	got, err := e.Store.EnsureIntake(ctx, item.ID, ev.DeliveryID, slots)
	if err != nil {
		return Result{}, nil, fmt.Errorf("automation: intake: %w", err)
	}
	if got.Continuation {
		res, noteErr := e.noteContinuation(ctx, ev, got.Item, matches)
		return res, nil, noteErr
	}
	return e.intakeResult(ctx, ev, got, matches)
}

func intakeSlots(tree definition.Tree, ev event.Event, item WorkItem, matches []compiled, effect orchestrator.Effect) ([]IntakeSlot, error) {
	slots := make([]IntakeSlot, len(matches))
	switch eff := effect.(type) {
	case orchestrator.StartRun:
		for i, auto := range matches {
			run := intakeRun(tree, ev, item, auto, eff.Stage)
			slots[i] = IntakeSlot{Automation: auto.auto.Name, Run: &run}
		}
	case orchestrator.RequestApproval:
		for i, auto := range matches {
			slots[i] = IntakeSlot{Automation: auto.auto.Name}
		}
	default:
		return nil, fmt.Errorf("automation: unhandled orchestrator effect %T", effect)
	}
	return slots, nil
}

func intakeRun(tree definition.Tree, ev event.Event, item WorkItem, auto compiled, stage string) NewRun {
	agent, model, pool := runIdentity(tree, auto.auto, stage)
	promptAuto := auto.auto
	promptAuto.Agent = agent
	if promptAuto.Model == "" {
		promptAuto.Model = model
	}
	return NewRun{
		FactoryID:      item.FactoryID,
		Agent:          agent,
		Pool:           pool,
		Priority:       priorityFor(ev),
		Prompt:         intakePrompt(promptAuto, item),
		Title:          item.Title,
		WorkItemID:     item.ID,
		ConversationID: item.ConversationID,
		Stage:          stage,
		ModelID:        model,
	}
}

func (e *Engine) intakeResult(ctx context.Context, ev event.Event, got IntakeResult, matches []compiled) (Result, *event.Event, error) {
	if len(got.Slots) != len(matches) {
		return Result{}, nil, errors.New("automation: intake result does not match automations")
	}
	inserted := false
	out := make([]Outcome, 0, len(matches))
	for i, slot := range got.Slots {
		if !slot.Inserted {
			continue
		}
		inserted = true
		auto := matches[i].auto
		if slot.Run.ID == "" {
			out = append(out, Outcome{
				Kind:           KindCreated,
				Automation:     auto.Name,
				WorkItemID:     got.Item.ID,
				ConversationID: got.Item.ConversationID,
				Stage:          got.Item.Stage,
			})
			continue
		}
		out = append(out, outcomeFrom(auto, slot.Run, KindCreated))
	}
	if !inserted {
		return Result{Matched: true, Outcomes: []Outcome{{
			Kind:           KindDuplicate,
			WorkItemID:     got.Item.ID,
			ConversationID: got.Item.ConversationID,
		}}}, nil, nil
	}
	owners, err := e.Store.Owners(ctx, got.Item.ID)
	if err != nil {
		return Result{}, nil, fmt.Errorf("automation: owners: %w", err)
	}
	emitted, err := e.stageEvent(ev, got.Item, chain(ev, owners))
	if err != nil {
		return Result{}, nil, err
	}
	return Result{Matched: true, Outcomes: out}, &emitted, nil
}

func (e *Engine) noteContinuation(ctx context.Context, ev event.Event, item WorkItem, matches []compiled) (Result, error) {
	applied, err := e.Store.AppendContext(ctx, item.ID, ev.DeliveryID, laterNote(ev))
	if err != nil {
		return Result{}, fmt.Errorf("automation: append context: %w", err)
	}
	if !applied {
		return Result{Matched: true, Outcomes: []Outcome{{
			Kind:           KindDuplicate,
			WorkItemID:     item.ID,
			ConversationID: item.ConversationID,
		}}}, nil
	}
	out := make([]Outcome, len(matches))
	for i, auto := range matches {
		out[i] = Outcome{
			Kind:           KindContinued,
			Automation:     auto.auto.Name,
			WorkItemID:     item.ID,
			ConversationID: item.ConversationID,
		}
	}
	return Result{Matched: true, Outcomes: out}, nil
}

func (e *Engine) stageEvent(cause event.Event, item WorkItem, caused []string) (event.Event, error) {
	raw, err := json.Marshal(struct {
		Stage      string   `json:"stage"`
		Status     string   `json:"status"`
		WorkItemID string   `json:"work_item_id"`
		CausedBy   []string `json:"caused_by"`
	}{item.Stage, item.Status, item.ID, caused})
	if err != nil {
		return event.Event{}, fmt.Errorf("automation: stage event: %w", err)
	}
	return event.Event{
		Provider:   "factory",
		Event:      "work_item_stage_changed",
		DeliveryID: "factory:" + item.ID + ":" + item.Stage + ":" + item.Status,
		OccurredAt: e.now(),
		Actor:      event.Actor{ProviderID: "factory", Handle: "factory"},
		Subject: event.Subject{
			Kind:  item.Stage,
			ID:    item.ID,
			Title: item.Title,
			Repo:  cause.Subject.Repo,
		},
		Thread: event.Thread{Kind: "factory", ExternalRef: item.ID},
		Raw:    raw,
	}, nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func pipelineDef(tree definition.Tree) (orchestrator.Def, error) {
	if tree.Factory.Pipeline == nil {
		return orchestrator.Def{}, errors.New("automation: factory has no pipeline")
	}
	p := tree.Factory.Pipeline
	approvals := make(map[string]orchestrator.ApprovalGate, len(p.Approvals))
	for name, gate := range p.Approvals {
		approvals[name] = orchestrator.ApprovalGate{Required: gate.Required, Approvers: gate.Approvers}
	}
	return orchestrator.Def{
		Stages:        p.Stages,
		Agents:        p.Agents,
		Skip:          p.Skip,
		Approvals:     approvals,
		RevisionLimit: p.RevisionLimit,
	}, nil
}

func firstStage(def orchestrator.Def, effects []orchestrator.Effect) string {
	if len(effects) == 1 {
		if run, ok := effects[0].(orchestrator.StartRun); ok {
			return run.Stage
		}
	}
	if len(def.Stages) > 0 {
		return def.Stages[0]
	}
	return ""
}

func runIdentity(tree definition.Tree, auto definition.Automation, stage string) (agent, model, pool string) {
	agent = auto.Agent
	if agent == "" && tree.Factory.Pipeline != nil {
		agent = tree.Factory.Pipeline.Agents[stage]
	}
	model = auto.Model
	pool = auto.Pool
	if ag, ok := tree.Agents[agent]; ok {
		if model == "" {
			model = ag.Model
		}
		if pool == "" {
			pool = ag.Pool
		}
	}
	if pool == "" {
		pool = "standard"
	}
	return agent, model, pool
}

func stageIdentity(tree definition.Tree, stage string) (agent, model, pool string) {
	if tree.Factory.Pipeline != nil {
		agent = tree.Factory.Pipeline.Agents[stage]
	}
	if ag, ok := tree.Agents[agent]; ok {
		model = ag.Model
		pool = ag.Pool
	}
	if pool == "" {
		pool = "standard"
	}
	return agent, model, pool
}

func priorityFor(ev event.Event) int {
	if ev.Provider == event.ProviderSchedule || ev.Event == "cron_fired" {
		return PriorityBatch
	}
	return PriorityPipeline
}

func titleOf(ev event.Event) string {
	if strings.TrimSpace(ev.Subject.Title) != "" {
		return ev.Subject.Title
	}
	return ev.Provider + " " + ev.Event + " " + ev.Thread.ExternalRef
}

func actorName(ev event.Event) string {
	if ev.Actor.Handle != "" {
		return ev.Actor.Handle
	}
	return ev.Actor.ProviderID
}

func outcomeFrom(auto definition.Automation, run Run, kind string) Outcome {
	return Outcome{
		Kind:           kind,
		Automation:     auto.Name,
		WorkItemID:     run.WorkItemID,
		ConversationID: run.ConversationID,
		RunID:          run.ID,
		Stage:          run.Stage,
		Agent:          run.Agent,
		ModelID:        run.ModelID,
		Pool:           run.Pool,
		Runner:         auto.Runner,
		Secrets:        append([]string(nil), auto.Secrets...),
		RequiredRepos:  append([]definition.Repository(nil), auto.RequiredRepos...),
	}
}

func matchAny(matchers []trigger.Matcher, ev trigger.Event) bool {
	for _, m := range matchers {
		if m != nil && m.Match(ev) {
			return true
		}
	}
	return false
}

func causation(ev event.Event) []string {
	if ev.Provider != "factory" || ev.Event != "work_item_stage_changed" {
		return nil
	}
	var body struct {
		CausedBy []string `json:"caused_by"`
	}
	if err := json.Unmarshal(ev.Raw, &body); err != nil {
		return nil
	}
	return body.CausedBy
}

func chain(ev event.Event, extra []string) []string {
	return dedupe(append(append([]string{}, causation(ev)...), extra...))
}

func dedupe(in []string) []string {
	var out []string
	for _, s := range in {
		if s == "" || contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
