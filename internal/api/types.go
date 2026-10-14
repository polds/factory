package api

import (
	"context"
	"errors"
	"io"
	"time"
)

// Sentinel errors a Backend returns. Handlers map them to problem+json and
// never copy Backend error text into the response.
var (
	// ErrNotFound means the factory, agent, run, work item, or approval does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the call conflicts with current state: CancelRun on a
	// terminal run, CancelTask on a terminal work item, or DecideApproval with
	// a different decision than the one already recorded. Repeating the same
	// decision is not a conflict; DecideApproval is idempotent for that case.
	ErrConflict = errors.New("conflict")
	// ErrUnauthorized means IngestEvents rejected the run bearer.
	ErrUnauthorized = errors.New("unauthorized")
)

// EventIndexHeader is the ingest batch's first event index.
const EventIndexHeader = "X-Factory-Event-Index"

// Backend is the control-plane surface the REST handlers call.
// Implementations live outside this package; tests use an in-package fake.
//
// Work items and approvals are a separate WorkBackend. Schema documents are
// a separate SchemaCatalog. M2 backends keep compiling without either.
type Backend interface {
	ListFactories(ctx context.Context) ([]Factory, error)
	GetFactory(ctx context.Context, id string) (Factory, error)
	ValidateDefinition(ctx context.Context, factoryID string, tarball io.Reader) ([]Diagnostic, error)
	ApplyDefinition(ctx context.Context, factoryID string) (DefinitionRef, []Diagnostic, error)
	ListAgents(ctx context.Context, factoryID string) ([]Agent, error)
	StartRun(ctx context.Context, req RunRequest) (Run, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, f RunFilter) ([]Run, error)
	CancelRun(ctx context.Context, id string) (Run, error)
	IngestEvents(ctx context.Context, runID, bearer string, startIdx int, lines [][]byte) error
}

// WorkBackend is the M3 extension of Backend for work items and approvals.
// Handlers type-assert Backend to WorkBackend. A Backend that does not
// implement it answers 404 on those routes.
//
// CreateTask queues orchestrator effects for the new work item. The first
// effect is EffectStartRun for the triage stage. CreateTask does not start
// a bare run; POST /agent/run remains the only route that calls StartRun.
//
// DecideApproval is idempotent when the recorded decision matches. A
// different decision returns ErrConflict. decidedBy is the caller, never a
// value taken from the request body.
type WorkBackend interface {
	CreateTask(ctx context.Context, factoryID string, req TaskRequest) (Task, error)
	GetTask(ctx context.Context, factoryID, id string) (Task, error)
	ListTasks(ctx context.Context, factoryID string, f TaskFilter) ([]Task, error)
	CancelTask(ctx context.Context, factoryID, id string) (Task, error)
	ListApprovals(ctx context.Context, factoryID string, pendingOnly bool) ([]Approval, error)
	DecideApproval(ctx context.Context, id, decision, decidedBy, note string) (Approval, error)
}

// Effect is one orchestrator action queued by WorkBackend.CreateTask.
// The REST handler does not execute effects.
type Effect struct {
	Op    string
	Stage string
	Agent string
	Gate  string
}

const (
	// EffectStartRun is the CreateTask effect that enters triage.
	// The orchestrator resolves the stage agent; Agent is empty here.
	EffectStartRun = "start_run"

	// APIKeyPrincipal is the actor recorded for a factory API key.
	// The key value itself is never stored on the work item or approval.
	APIKeyPrincipal = "api-key"

	// TaskSourceIssue is intake that stands in for a labeled issue.
	TaskSourceIssue = "issue"
	// TaskSourceManual is intake created by an operator.
	TaskSourceManual = "manual"
	// TaskSourceAPI is intake created through this REST API.
	// An omitted source on create becomes TaskSourceAPI.
	TaskSourceAPI = "api"

	// TaskStageTriage is the first orchestrated stage. Intake is a source, not a stage.
	TaskStageTriage = "triage"
	// TaskStagePlanning is the spec stage.
	TaskStagePlanning = "planning"
	// TaskStageBuilding is the implement stage.
	TaskStageBuilding = "building"
	// TaskStageReviewing is the review stage.
	TaskStageReviewing = "reviewing"
	// TaskStageHumanHandoff is the stage after review, while a pull request is open.
	TaskStageHumanHandoff = "human_handoff"

	// TaskStatusOpen is a work item the orchestrator may advance.
	TaskStatusOpen = "open"
	// TaskStatusAwaitingApproval means a gate is blocking the next stage.
	TaskStatusAwaitingApproval = "awaiting_approval"
	// TaskStatusAwaitingInput means a human answer is blocking the work item.
	TaskStatusAwaitingInput = "awaiting_input"
	// TaskStatusComplete is a terminal success status.
	TaskStatusComplete = "complete"
	// TaskStatusCancelled is a terminal cancelled status.
	TaskStatusCancelled = "cancelled"

	// DecisionApprove accepts a gate.
	DecisionApprove = "approve"
	// DecisionReject rejects a gate.
	DecisionReject = "reject"
)

// Task is a work item. It carries no secrets and no run token.
type Task struct {
	ID             string `json:"id"`
	FactoryID      string `json:"factory_id"`
	Source         string `json:"source"`
	SourceRef      string `json:"source_ref,omitempty"`
	Title          string `json:"title"`
	Body           string `json:"body,omitempty"`
	Stage          string `json:"stage"`
	Status         string `json:"status"`
	CreatedBy      string `json:"created_by,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	RevisionCount  int    `json:"revision_count"`
	// PendingGate is the gate blocking this work item. factoryctl task watch
	// prints it. Empty when no gate is pending.
	PendingGate string    `json:"pending_gate,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// TaskRequest is the POST /factory/{uid}/tasks body.
// CreatedBy is set by the handler from the caller, not from JSON.
type TaskRequest struct {
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Source    string `json:"source,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
	CreatedBy string `json:"-"`
}

// TaskFilter selects work items. Empty fields match everything.
type TaskFilter struct {
	Stage  string
	Status string
	Source string
}

// Approval is one gate decision for a work item.
type Approval struct {
	ID          string     `json:"id"`
	WorkItemID  string     `json:"work_item_id"`
	Gate        string     `json:"gate"`
	RequestedAt time.Time  `json:"requested_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	DecidedBy   string     `json:"decided_by,omitempty"`
	Decision    string     `json:"decision,omitempty"`
	Note        string     `json:"note,omitempty"`
}

// Factory is a factory record returned by the API.
type Factory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Alias string `json:"alias,omitempty"`
}

// Agent is an agent record returned by the API.
type Agent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FactoryID string `json:"factory_id"`
}

// Diagnostic is one definition-loader finding.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// DefinitionRef identifies an applied definition.
type DefinitionRef struct {
	ID string `json:"id"`
}

// Run is a run record. It carries no execution material.
type Run struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	FactoryID string `json:"factory_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Title     string `json:"title,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Repo      string `json:"repo,omitempty"`
	// WorkItemID links a stage run to its work item. factoryctl task watch
	// matches child transcripts on this field. Empty for a bare run.
	WorkItemID string `json:"work_item_id,omitempty"`
	// ParentRunID, ConversationID, Stage, and ModelID are the stage-run
	// columns already stored on runs. Empty for a bare run.
	ParentRunID    string `json:"parent_run_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Stage          string `json:"stage,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	FailureReason  string `json:"failure_reason,omitempty"`
}

// RunRequest is the POST /agent/run body.
type RunRequest struct {
	Factory  string `json:"factory"`
	Agent    string `json:"agent,omitempty"`
	Prompt   string `json:"prompt"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority,omitempty"`
	Repo     string `json:"repo,omitempty"`
}

// RunFilter selects runs for the list routes.
type RunFilter struct {
	FactoryID string
	Agent     string
	Status    string
	Worker    string
	Model     string
}

// ValidateResponse is the definition:validate body.
type ValidateResponse struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// ApplyResponse is the definition:apply body.
type ApplyResponse struct {
	ID          string       `json:"id"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// Health is the GET /healthz body.
type Health struct {
	Status string `json:"status"`
}

// Problem is an RFC 9457 error body.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}
