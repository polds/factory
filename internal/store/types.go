package store

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	StatusQueued    = "queued"
	StatusAssigned  = "assigned"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	ExecAssigned  = "assigned"
	ExecRunning   = "running"
	ExecCompleted = "completed"
	ExecFailed    = "failed"
	ExecOrphaned  = "orphaned"
	ExecCancelled = "cancelled"
)

// Priority is the run_queue order key. Higher values lease first.
type Priority int

const (
	PriorityBatch       Priority = 1
	PriorityPipeline    Priority = 2
	PriorityInteractive Priority = 3
)

var (
	ErrNotFound        = errors.New("store: not found")
	ErrAlreadyTerminal = errors.New("store: already terminal")
)

// Run is one logical task. Tokens and env values are never fields of Run.
type Run struct {
	ID              string
	FactoryID       string
	AgentID         string
	Status          string
	Pool            string
	Priority        Priority
	Prompt          string
	Title           string
	Repo            string
	WorkerID        string
	FailureReason   string
	ExitCode        *int
	CancelRequested bool
	CreatedAt       time.Time
	StartedAt       *time.Time
	EndedAt         *time.Time
}

// NewRun is the input to CreateRun. The store enqueues it in the same transaction.
type NewRun struct {
	FactoryID string
	AgentID   string
	Pool      string
	Priority  Priority
	Prompt    string
	Title     string
	Repo      string
}

// RunFilter narrows ListRuns. Zero values are unconstrained. Limit <= 0 uses 100.
type RunFilter struct {
	FactoryID string
	AgentID   string
	Status    string
	Limit     int
}

// QueuedRun is a run_queue row leased to a scheduler.
type QueuedRun struct {
	RunID      string
	FactoryID  string
	Pool       string
	Priority   Priority
	EnqueuedAt time.Time
	LeaseUntil time.Time
	Attempts   int
}

// Execution is one concrete launch of a run. RunTokenSHA256 is the only token material stored.
type Execution struct {
	ID             string
	RunID          string
	Attempt        int
	WorkerID       string
	State          string
	FailureReason  string
	ExitCode       *int
	RunTokenSHA256 []byte
}

// Terminal is the first terminal outcome of an execution. A repeat is a no-op.
type Terminal struct {
	State          string
	FailureReason  string
	ExitCode       *int
	FailureDetails json.RawMessage
}

// OrphanedExecution is an execution marked orphaned by ExpireLeases.
type OrphanedExecution struct {
	ExecutionID string
	RunID       string
	WorkerID    string
}

// Event is one append-only run_events row. EventsFrom returns rows with seq > fromSeq.
type Event struct {
	RunID       string
	Seq         int64
	ExecutionID string
	Idx         int
	TS          time.Time
	Type        string
	Payload     json.RawMessage
}

// Factory is the current applied definition pointer for a named factory.
type Factory struct {
	ID                  string
	Name                string
	Alias               string
	SchemaVersion       string
	DefinitionSource    string
	ProductionRef       string
	CurrentDefinitionID string
}

// Agent is an agent row from the factory's current definition.
type Agent struct {
	ID           string
	FactoryID    string
	DefinitionID string
	Name         string
	AgentType    string
	Runner       string
	SpecJSON     json.RawMessage
}

// AppliedDefinition is a validated tree written in one transaction.
// An agent whose Runner is absent from Runners fails the runners foreign key
// and leaves the previous definition in place.
type AppliedDefinition struct {
	SchemaVersion    string
	Alias            string
	DefinitionSource string
	GitSHA           string
	SpecJSON         json.RawMessage
	Agents           []DefinedAgent
	Runners          []DefinedRunner
}

// DefinedAgent is one agent in an applied definition.
type DefinedAgent struct {
	Name      string
	AgentType string
	Runner    string
	SpecJSON  json.RawMessage
}

// DefinedRunner is one runner in an applied definition.
type DefinedRunner struct {
	Name              string
	PlatformJSON      json.RawMessage
	InstanceShapeJSON json.RawMessage
	SetupCommands     json.RawMessage
}

// Worker is a connected worker row. Upsert replaces mutable fields.
type Worker struct {
	ID              string
	Pool            string
	Platform        string
	LabelsJSON      json.RawMessage
	LastHeartbeatAt time.Time
	MaxConcurrent   int
	Status          string
}
