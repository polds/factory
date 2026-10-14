package api

import (
	"context"
	"errors"
	"io"
)

// Sentinel errors a Backend returns. Handlers map them to problem+json and
// never copy Backend error text into the response.
var (
	// ErrNotFound means the factory, agent, or run does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means CancelRun was called on a terminal run.
	ErrConflict = errors.New("conflict")
	// ErrUnauthorized means IngestEvents rejected the run bearer.
	ErrUnauthorized = errors.New("unauthorized")
)

// EventIndexHeader is the ingest batch's first event index.
const EventIndexHeader = "X-Factory-Event-Index"

// Backend is the control-plane surface the REST handlers call.
// Implementations live outside this package; tests use an in-package fake.
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
	ID            string `json:"id"`
	Status        string `json:"status"`
	FactoryID     string `json:"factory_id,omitempty"`
	Agent         string `json:"agent,omitempty"`
	Title         string `json:"title,omitempty"`
	Priority      string `json:"priority,omitempty"`
	Repo          string `json:"repo,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
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
