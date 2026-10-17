package orchestrator

import (
	"errors"
	"fmt"
	"strings"
)

// Work-item statuses. Intake is a source, not a stage.
const (
	StatusOpen             = "open"
	StatusAwaitingApproval = "awaiting_approval"
	StatusAwaitingInput    = "awaiting_input"
	StatusComplete         = "complete"
	StatusCancelled        = "cancelled"
)

// Run outcomes recorded on State.LastRunOutcome. Empty means a run is in flight.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
)

// Stage names from docs/02. human_handoff is the terminal stage after the
// last run; it is an agent stage only when listed in Def.Stages.
const (
	StageTriage       = "triage"
	StagePlanning     = "planning"
	StageBuilding     = "building"
	StageReviewing    = "reviewing"
	StageHumanHandoff = "human_handoff"
)

// Gate names from pipeline.approvals (docs/03).
const (
	GateSpec    = "spec"
	GateHandoff = "handoff"
	GateMerge   = "merge"
)

// Skip predicate names. M3 registers both and never makes them true.
const (
	PredWhenScopeClear     = "whenScopeClear"
	PredWhenIntakeHasScope = "whenIntakeHasScope"
)

var (
	ErrTerminal         = errors.New("orchestrator: work item is terminal")
	ErrStatus           = errors.New("orchestrator: event is not valid in the current status")
	ErrWrongGate        = errors.New("orchestrator: approval does not match the pending gate")
	ErrNoPending        = errors.New("orchestrator: no approval is pending")
	ErrStaleStage       = errors.New("orchestrator: event stage does not match the current stage")
	ErrNotAwaitingInput = errors.New("orchestrator: work item is not awaiting input")
	ErrAlreadyStarted   = errors.New("orchestrator: work item has already started")
	ErrUnknownEvent     = errors.New("orchestrator: unknown event")
	ErrUnknownPredicate = errors.New("orchestrator: unknown skip predicate")
	ErrNoStage          = errors.New("orchestrator: no runnable stage")
	ErrNoAgent          = errors.New("orchestrator: stage has no agent")
)

// ApprovalGate is one pipeline.approvals entry. Approvers is recorded for
// the caller; Step does not check identity.
type ApprovalGate struct {
	Required  bool
	Approvers string
}

// Def is the pipeline Step evaluates.
type Def struct {
	Stages        []string
	Agents        map[string]string
	Skip          map[string]string
	Approvals     map[string]ApprovalGate
	RevisionLimit int
}

// State is one work item. PendingGate is non-empty only while status is
// awaiting_approval. LastRunOutcome is succeeded, failed, or cancelled when
// no run is in flight, and empty while a run started by Step is in flight.
//
// ReviewerRetryUsed is the one-shot reviewer retry budget for this call.
// The caller sets it from committed run rows (infrastructure-failed
// reviewing runs since the latest building run). Step does not store it.
// A restarted process passes the same value because the count is still in
// those rows. Step also sets it on the state it returns after consuming
// the budget, so a pure chain of calls retries once.
type State struct {
	Stage             string
	Status            string
	RevisionCount     int
	PendingGate       string
	LastStageAgent    string
	LastRunOutcome    string
	ReviewerRetryUsed bool
}

// Event is one input to Step.
//
// Concrete values are Created, RunSucceeded, RunFailed, Approved, Rejected,
// Cancel, AskInput, and ResumeInput. Pass them by value.
type Event any

// Effect is one side effect for the caller to apply. Step does not apply it.
//
// Concrete values are StartRun, RequestApproval, MarkComplete, MarkCancelled,
// Escalate, and Noop. A successful Step returns exactly one effect.
type Effect any

// Created opens a work item and starts the first runnable stage.
type Created struct{}

// RunSucceeded is a stage run that finished successfully.
type RunSucceeded struct {
	Stage string
}

// RunFailed is a stage run that finished unsuccessfully. Reviewing failure
// is a revision, unless Infra is set and the reviewer retry budget is still
// unused. Any other failure escalates and does not retry.
type RunFailed struct {
	Stage string
	// Infra is true when the run failed for an infrastructure reason.
	// Agent and configuration failures leave it false.
	Infra bool
}

// Approved decides the pending gate.
type Approved struct {
	Gate string
}

// Rejected decides the pending gate. The work item returns to planning.
type Rejected struct {
	Gate string
}

// Cancel stops the work item from any non-terminal status.
type Cancel struct{}

// AskInput parks an open work item. The caller persists the question, ends
// the execution, and does not requeue the run.
type AskInput struct{}

// ResumeInput returns an awaiting_input work item to open. The caller
// starts a new execution on the same conversation with the answer.
type ResumeInput struct{}

// StartRun asks the caller to launch the stage agent.
type StartRun struct {
	Stage string
	Agent string
}

// RequestApproval asks the caller to record a pending gate.
type RequestApproval struct {
	Gate string
}

// MarkComplete asks the caller to finish the work item.
type MarkComplete struct{}

// MarkCancelled asks the caller to cancel the work item.
type MarkCancelled struct{}

// Escalate asks the caller to surface the work item. No run is started.
type Escalate struct{}

// Noop is an effect with nothing for the caller to launch.
type Noop struct{}

// Predicates evaluates skip names. A nil function is false, which never skips.
type Predicates struct {
	WhenScopeClear     func() bool
	WhenIntakeHasScope func() bool
}

// M3Predicates is the D2 pair: both predicates stay false, so M3 never skips.
func M3Predicates() Predicates {
	return Predicates{
		WhenScopeClear:     func() bool { return false },
		WhenIntakeHasScope: func() bool { return false },
	}
}

type eventKind int

const (
	kindCreated eventKind = iota
	kindRunSucceeded
	kindRunFailed
	kindApproved
	kindRejected
	kindCancel
	kindAskInput
	kindResumeInput
)

func classify(ev Event) (eventKind, error) {
	switch ev.(type) {
	case Created:
		return kindCreated, nil
	case RunSucceeded:
		return kindRunSucceeded, nil
	case RunFailed:
		return kindRunFailed, nil
	case Approved:
		return kindApproved, nil
	case Rejected:
		return kindRejected, nil
	case Cancel:
		return kindCancel, nil
	case AskInput:
		return kindAskInput, nil
	case ResumeInput:
		return kindResumeInput, nil
	default:
		return 0, fmt.Errorf("%w: %T", ErrUnknownEvent, ev)
	}
}

// Step is the stage machine. It does not mutate def or the caller's state.
// On error it returns that state and a nil effect slice.
func Step(def Def, s State, ev Event, preds Predicates) (State, []Effect, error) {
	kind, err := classify(ev)
	if err != nil {
		return s, nil, err
	}
	switch kind {
	case kindCreated:
		return stepCreated(def, s, preds)
	case kindRunSucceeded:
		return stepRunSucceeded(def, s, ev.(RunSucceeded), preds)
	case kindRunFailed:
		return stepRunFailed(def, s, ev.(RunFailed))
	case kindApproved:
		return stepApproved(def, s, ev.(Approved))
	case kindRejected:
		return stepRejected(def, s, ev.(Rejected))
	case kindCancel:
		return stepCancel(s)
	case kindAskInput:
		return stepAsk(s)
	case kindResumeInput:
		return stepResume(s)
	default:
		panic(fmt.Sprintf("orchestrator: unhandled event kind %d", neverKind(kind)))
	}
}

// neverKind keeps the eventKind switch exhaustive: a new kind fails here
// until Step handles it.
func neverKind(kind eventKind) eventKind { return kind }

func stepCreated(def Def, s State, preds Predicates) (State, []Effect, error) {
	if terminal(s) {
		return s, nil, ErrTerminal
	}
	if s.Status != "" || s.Stage != "" {
		return s, nil, ErrAlreadyStarted
	}
	if len(def.Stages) == 0 {
		return s, nil, ErrNoStage
	}
	nxt, err := firstRunnable(def, 0, preds)
	if err != nil {
		return s, nil, err
	}
	if nxt == "" {
		return s, nil, ErrNoStage
	}
	return enter(def, s, nxt)
}

func stepRunSucceeded(def Def, s State, e RunSucceeded, preds Predicates) (State, []Effect, error) {
	if err := ensureOpen(s); err != nil {
		return s, nil, err
	}
	if e.Stage == "" || e.Stage != s.Stage {
		return s, nil, ErrStaleStage
	}
	nxt, err := nextRunnable(def, s.Stage, preds)
	if err != nil {
		return s, nil, err
	}
	out := succeeded(def, s)
	if nxt == "" {
		return finishPipeline(def, out)
	}
	entered, eff, err := enter(def, out, nxt)
	if err != nil {
		return s, nil, err
	}
	return entered, eff, nil
}

func stepRunFailed(def Def, s State, e RunFailed) (State, []Effect, error) {
	if err := ensureOpen(s); err != nil {
		return s, nil, err
	}
	if e.Stage == "" || e.Stage != s.Stage {
		return s, nil, ErrStaleStage
	}
	if s.Stage == StageReviewing {
		if e.Infra && !s.ReviewerRetryUsed {
			started, eff, err := startRun(def, s, StageReviewing)
			if err != nil {
				return s, nil, err
			}
			started.ReviewerRetryUsed = true
			return started, eff, nil
		}
		started, eff, err := stepRevision(def, s)
		if err != nil {
			return s, nil, err
		}
		// A new building run opens a new reviewer-retry window.
		started.ReviewerRetryUsed = false
		return started, eff, nil
	}
	out, eff := escalate(def, s, s.RevisionCount)
	return out, eff, nil
}

func stepRevision(def Def, s State) (State, []Effect, error) {
	count := s.RevisionCount + 1
	if count > def.RevisionLimit {
		out, eff := escalate(def, s, count)
		return out, eff, nil
	}
	if indexOf(def.Stages, StageBuilding) < 0 {
		return s, nil, fmt.Errorf("%w: %q", ErrNoStage, StageBuilding)
	}
	out := s
	out.RevisionCount = count
	started, eff, err := startRun(def, out, StageBuilding)
	if err != nil {
		return s, nil, err
	}
	return started, eff, nil
}

func stepApproved(def Def, s State, e Approved) (State, []Effect, error) {
	if err := ensurePending(s, e.Gate); err != nil {
		return s, nil, err
	}
	switch s.PendingGate {
	case GateSpec:
		return startRun(def, s, StageBuilding)
	case GateHandoff:
		if indexOf(def.Stages, StageHumanHandoff) >= 0 {
			return startRun(def, s, StageHumanHandoff)
		}
		out := s
		out.Stage = StageHumanHandoff
		out.Status = StatusComplete
		out.PendingGate = ""
		return out, []Effect{MarkComplete{}}, nil
	default:
		return s, nil, fmt.Errorf("%w: %q", ErrWrongGate, s.PendingGate)
	}
}

func stepRejected(def Def, s State, e Rejected) (State, []Effect, error) {
	if err := ensurePending(s, e.Gate); err != nil {
		return s, nil, err
	}
	if indexOf(def.Stages, StagePlanning) < 0 {
		return s, nil, fmt.Errorf("%w: %q", ErrNoStage, StagePlanning)
	}
	return startRun(def, s, StagePlanning)
}

func stepCancel(s State) (State, []Effect, error) {
	if terminal(s) {
		return s, nil, ErrTerminal
	}
	switch s.Status {
	case StatusOpen, StatusAwaitingApproval, StatusAwaitingInput:
	default:
		return s, nil, ErrStatus
	}
	s.Status = StatusCancelled
	s.PendingGate = ""
	s.LastRunOutcome = OutcomeCancelled
	return s, []Effect{MarkCancelled{}}, nil
}

func stepAsk(s State) (State, []Effect, error) {
	if err := ensureOpen(s); err != nil {
		return s, nil, err
	}
	s.Status = StatusAwaitingInput
	return s, []Effect{Noop{}}, nil
}

func stepResume(s State) (State, []Effect, error) {
	if terminal(s) {
		return s, nil, ErrTerminal
	}
	if s.Status != StatusAwaitingInput {
		return s, nil, ErrNotAwaitingInput
	}
	s.Status = StatusOpen
	return s, []Effect{Noop{}}, nil
}

// enter starts stage, or parks on the gate that guards it.
// The spec gate is not re-checked on the reviewing → building revision path;
// callers of this function are the forward paths only.
func enter(def Def, s State, stage string) (State, []Effect, error) {
	if agentName(def, stage) == "" {
		return s, nil, fmt.Errorf("%w: %q", ErrNoAgent, stage)
	}
	if g := gateBefore(stage); gateRequired(def, g) {
		return awaitGate(s, g)
	}
	return startRun(def, s, stage)
}

func finishPipeline(def Def, s State) (State, []Effect, error) {
	if s.Stage == StageHumanHandoff {
		s.Status = StatusComplete
		s.PendingGate = ""
		return s, []Effect{MarkComplete{}}, nil
	}
	if gateRequired(def, GateHandoff) {
		return awaitGate(s, GateHandoff)
	}
	s.Stage = StageHumanHandoff
	s.Status = StatusComplete
	s.PendingGate = ""
	return s, []Effect{MarkComplete{}}, nil
}

func startRun(def Def, s State, stage string) (State, []Effect, error) {
	agent := agentName(def, stage)
	if agent == "" {
		return s, nil, fmt.Errorf("%w: %q", ErrNoAgent, stage)
	}
	s.Stage = stage
	s.Status = StatusOpen
	s.PendingGate = ""
	s.LastStageAgent = agent
	s.LastRunOutcome = ""
	return s, []Effect{StartRun{Stage: stage, Agent: agent}}, nil
}

func awaitGate(s State, gate string) (State, []Effect, error) {
	s.Status = StatusAwaitingApproval
	s.PendingGate = gate
	return s, []Effect{RequestApproval{Gate: gate}}, nil
}

func escalate(def Def, s State, revisionCount int) (State, []Effect) {
	s.Status = StatusOpen
	s.RevisionCount = revisionCount
	s.LastRunOutcome = OutcomeFailed
	if agent := agentName(def, s.Stage); agent != "" {
		s.LastStageAgent = agent
	}
	return s, []Effect{Escalate{}}
}

func succeeded(def Def, s State) State {
	s.LastRunOutcome = OutcomeSucceeded
	if agent := agentName(def, s.Stage); agent != "" {
		s.LastStageAgent = agent
	}
	return s
}

func firstRunnable(def Def, start int, preds Predicates) (string, error) {
	if start < 0 || start > len(def.Stages) {
		return "", nil
	}
	for _, stage := range def.Stages[start:] {
		skip, err := stageSkipped(def, stage, preds)
		if err != nil {
			return "", err
		}
		if !skip {
			return stage, nil
		}
	}
	return "", nil
}

func nextRunnable(def Def, current string, preds Predicates) (string, error) {
	i := indexOf(def.Stages, current)
	if i < 0 {
		return "", fmt.Errorf("%w: %q", ErrNoStage, current)
	}
	return firstRunnable(def, i+1, preds)
}

func stageSkipped(def Def, stage string, preds Predicates) (bool, error) {
	if len(def.Skip) == 0 {
		return false, nil
	}
	name := def.Skip[stage]
	if name == "" {
		return false, nil
	}
	var fn func() bool
	switch name {
	case PredWhenScopeClear:
		fn = preds.WhenScopeClear
	case PredWhenIntakeHasScope:
		fn = preds.WhenIntakeHasScope
	default:
		return false, fmt.Errorf("%w: %q", ErrUnknownPredicate, name)
	}
	if fn == nil {
		return false, nil
	}
	return fn(), nil
}

func gateBefore(stage string) string {
	switch stage {
	case StageBuilding:
		return GateSpec
	case StageHumanHandoff:
		return GateHandoff
	default:
		return ""
	}
}

func gateRequired(def Def, gate string) bool {
	if gate == "" || len(def.Approvals) == 0 {
		return false
	}
	g, ok := def.Approvals[gate]
	return ok && g.Required
}

func agentName(def Def, stage string) string {
	if len(def.Agents) == 0 {
		return ""
	}
	return strings.TrimSpace(def.Agents[stage])
}

func indexOf(stages []string, stage string) int {
	for i, name := range stages {
		if name == stage {
			return i
		}
	}
	return -1
}

func terminal(s State) bool {
	return s.Status == StatusComplete || s.Status == StatusCancelled
}

func ensureOpen(s State) error {
	if terminal(s) {
		return ErrTerminal
	}
	if s.Status != StatusOpen {
		return ErrStatus
	}
	return nil
}

func ensurePending(s State, gate string) error {
	if terminal(s) {
		return ErrTerminal
	}
	if s.Status != StatusAwaitingApproval || s.PendingGate == "" {
		return ErrNoPending
	}
	if gate != s.PendingGate {
		return ErrWrongGate
	}
	return nil
}
