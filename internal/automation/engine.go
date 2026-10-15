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
// Handle is safe for concurrent use. Idempotency of one automation plus
// one delivery lasts for the life of this Engine.
type Engine struct {
	Store Store
	// Tree returns the applied definition. Handle calls it for every event
	// and caches compiled matchers by Digest.
	Tree      func() definition.Tree
	FactoryID string
	Allow     Allow
	Now       func() time.Time

	mu     sync.Mutex
	cache  map[string][]compiled
	claims map[string]struct{}
	owners map[string][]string
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

	e.mu.Lock()
	res, emit, err := e.decide(ctx, ev)
	e.mu.Unlock()
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

// decide runs with e.mu held. emit is the stage change to re-enter, if any.
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
	fresh := e.claimAll(matches, ev.DeliveryID)
	if len(fresh) == 0 {
		return Result{Matched: true, Outcomes: []Outcome{{Kind: KindDuplicate}}}, nil, nil
	}
	item, created, err := e.ensureItem(ctx, tree, ev)
	if err != nil {
		e.unclaim(fresh, ev.DeliveryID)
		return Result{}, nil, err
	}
	if !created {
		if err := e.Store.AppendContext(ctx, item.ID, laterNote(ev)); err != nil {
			e.unclaim(fresh, ev.DeliveryID)
			return Result{}, nil, fmt.Errorf("automation: append context: %w", err)
		}
		out := make([]Outcome, len(fresh))
		for i, auto := range fresh {
			out[i] = Outcome{
				Kind:           KindContinued,
				Automation:     auto.auto.Name,
				WorkItemID:     item.ID,
				ConversationID: item.ConversationID,
			}
		}
		return Result{Matched: true, Outcomes: out}, nil, nil
	}
	return e.startIntake(ctx, tree, ev, item, fresh)
}

func (e *Engine) matching(tree definition.Tree, ev event.Event) []compiled {
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

func (e *Engine) ensureItem(ctx context.Context, tree definition.Tree, ev event.Event) (WorkItem, bool, error) {
	ref, err := e.Store.GetExternalRef(ctx, ev.Provider, ev.Thread.ExternalRef)
	if err == nil {
		item, getErr := e.Store.GetWorkItem(ctx, ref.WorkItemID)
		if getErr != nil {
			return WorkItem{}, false, fmt.Errorf("automation: work item: %w", getErr)
		}
		return item, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return WorkItem{}, false, fmt.Errorf("automation: external ref: %w", err)
	}
	def, err := pipelineDef(tree)
	if err != nil {
		return WorkItem{}, false, err
	}
	next, effects, err := orchestrator.Step(def, orchestrator.State{}, orchestrator.Created{}, orchestrator.M3Predicates())
	if err != nil {
		return WorkItem{}, false, err
	}
	stage, status := next.Stage, next.Status
	if stage == "" {
		stage = firstStage(def, effects)
	}
	item, err := e.Store.CreateWorkItem(ctx, NewWorkItem{
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
		return WorkItem{}, false, fmt.Errorf("automation: create work item: %w", err)
	}
	bound, created, err := e.Store.BindExternalRef(ctx, ev.Provider, ev.Thread.ExternalRef, item.ID, item.ConversationID)
	if err != nil {
		return WorkItem{}, false, fmt.Errorf("automation: bind external ref: %w", err)
	}
	if created {
		return item, true, nil
	}
	winner, err := e.Store.GetWorkItem(ctx, bound.WorkItemID)
	if err != nil {
		return WorkItem{}, false, fmt.Errorf("automation: work item: %w", err)
	}
	return winner, false, nil
}

func (e *Engine) startIntake(ctx context.Context, tree definition.Tree, ev event.Event, item WorkItem, fresh []compiled) (Result, *event.Event, error) {
	def, err := pipelineDef(tree)
	if err != nil {
		return Result{}, nil, err
	}
	_, effects, err := orchestrator.Step(def, orchestrator.State{}, orchestrator.Created{}, orchestrator.M3Predicates())
	if err != nil {
		return Result{}, nil, err
	}
	if len(effects) != 1 {
		return Result{}, nil, errors.New("automation: orchestrator returned no effect")
	}
	names := autoNames(fresh)
	e.remember(item.ID, names)
	out := Result{Matched: true}
	switch eff := effects[0].(type) {
	case orchestrator.StartRun:
		for _, auto := range fresh {
			run, err := e.enqueue(ctx, tree, ev, item, auto, eff.Stage)
			if err != nil {
				return Result{}, nil, err
			}
			out.Outcomes = append(out.Outcomes, outcomeFrom(auto.auto, run, KindCreated))
		}
	case orchestrator.RequestApproval:
		for _, auto := range fresh {
			out.Outcomes = append(out.Outcomes, Outcome{
				Kind:           KindCreated,
				Automation:     auto.auto.Name,
				WorkItemID:     item.ID,
				ConversationID: item.ConversationID,
				Stage:          item.Stage,
			})
		}
	default:
		return Result{}, nil, fmt.Errorf("automation: unhandled orchestrator effect %T", effects[0])
	}
	emitted, err := e.stageEvent(ev, item, chain(ev, names))
	if err != nil {
		return Result{}, nil, err
	}
	return out, &emitted, nil
}

func (e *Engine) enqueue(ctx context.Context, tree definition.Tree, ev event.Event, item WorkItem, auto compiled, stage string) (Run, error) {
	agent, model, pool := runIdentity(tree, auto.auto, stage)
	promptAuto := auto.auto
	promptAuto.Agent = agent
	if promptAuto.Model == "" {
		promptAuto.Model = model
	}
	run, err := e.Store.CreateRun(ctx, NewRun{
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
	})
	if err != nil {
		return Run{}, fmt.Errorf("automation: create run: %w", err)
	}
	return run, nil
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

func (e *Engine) claimAll(matches []compiled, deliveryID string) []compiled {
	var fresh []compiled
	for _, auto := range matches {
		if e.claim(claimKey(auto.auto.Name, deliveryID)) {
			fresh = append(fresh, auto)
		}
	}
	return fresh
}

func (e *Engine) unclaim(matches []compiled, deliveryID string) {
	for _, auto := range matches {
		delete(e.claims, claimKey(auto.auto.Name, deliveryID))
	}
}

func (e *Engine) claim(key string) bool {
	if e.claims == nil {
		e.claims = map[string]struct{}{}
	}
	if _, ok := e.claims[key]; ok {
		return false
	}
	e.claims[key] = struct{}{}
	return true
}

func (e *Engine) remember(workItem string, names []string) {
	if e.owners == nil {
		e.owners = map[string][]string{}
	}
	cur := e.owners[workItem]
	for _, name := range names {
		if !contains(cur, name) {
			cur = append(cur, name)
		}
	}
	e.owners[workItem] = cur
}

func (e *Engine) ownerNames(workItem string) []string {
	return append([]string(nil), e.owners[workItem]...)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func claimKey(name, deliveryID string) string {
	return name + "\x00" + deliveryID
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

func autoNames(fresh []compiled) []string {
	names := make([]string, len(fresh))
	for i, auto := range fresh {
		names[i] = auto.auto.Name
	}
	return names
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
