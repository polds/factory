package automation

import (
	"context"
	"errors"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/event"
)

// Scheduler priorities. The values match internal/store.
const (
	PriorityBatch    = 1
	PriorityPipeline = 2
)

// Outcome kinds.
const (
	KindCreated   = "created"
	KindDuplicate = "duplicate"
	KindContinued = "continued"
	KindFollowUp  = "follow_up"
	KindAnswered  = "answered"
	KindIgnored   = "ignored"
)

var (
	// ErrNotFound is returned by Store when a row does not exist.
	ErrNotFound = errors.New("automation: not found")
	// ErrConflict is returned when a pending question is answered twice
	// with different text.
	ErrConflict = errors.New("automation: conflict")
)

// WorkItem is the subset of a factory work item Handle reads and writes.
type WorkItem struct {
	ID             string
	FactoryID      string
	Source         string
	SourceRef      string
	Title          string
	Body           string
	Stage          string
	Status         string
	CreatedBy      string
	ConversationID string
}

// NewWorkItem is the input to Store.CreateWorkItem. An empty conversation
// id is assigned by the store.
type NewWorkItem struct {
	FactoryID string
	Source    string
	SourceRef string
	Title     string
	Body      string
	Stage     string
	Status    string
	CreatedBy string
}

// Ref binds a provider thread to one work item.
type Ref struct {
	Provider       string
	ExternalRef    string
	WorkItemID     string
	ConversationID string
}

// Input is one question asked of a person.
type Input struct {
	ID         string
	WorkItemID string
	Question   string
	Answer     string
	AnsweredBy string
}

// Run is one enqueued stage run.
type Run struct {
	ID             string
	FactoryID      string
	Agent          string
	Pool           string
	Priority       int
	Prompt         string
	Title          string
	WorkItemID     string
	ParentRunID    string
	ConversationID string
	Stage          string
	ModelID        string
}

// NewRun is the input to Store.CreateRun.
type NewRun struct {
	FactoryID      string
	Agent          string
	Pool           string
	Priority       int
	Prompt         string
	Title          string
	WorkItemID     string
	ParentRunID    string
	ConversationID string
	Stage          string
	ModelID        string
}

// Store is the persistence Handle needs. T30 adapts internal/store.
// Implementations must not call Engine.Handle.
type Store interface {
	CreateWorkItem(ctx context.Context, w NewWorkItem) (WorkItem, error)
	GetWorkItem(ctx context.Context, id string) (WorkItem, error)
	UpdateWorkItemStage(ctx context.Context, id, stage, status string) (WorkItem, error)
	BindExternalRef(ctx context.Context, provider, externalRef, workItemID, conversationID string) (Ref, bool, error)
	GetExternalRef(ctx context.Context, provider, externalRef string) (Ref, error)
	PendingInput(ctx context.Context, workItemID string) (Input, error)
	AnswerInput(ctx context.Context, workItemID, answer, answeredBy string) (Input, error)
	CreateRun(ctx context.Context, r NewRun) (Run, error)
	LatestRun(ctx context.Context, workItemID string) (Run, error)
	AppendContext(ctx context.Context, workItemID, text string) error
	Context(ctx context.Context, workItemID string) (string, error)
	OpenPullRequest(ctx context.Context, workItemID string) (string, bool, error)
}

// Allow decides who may continue work (D12).
//
// SlackUserIDs empty accepts any non-bot Slack user. A non-empty list is
// the allowlist. GitHub nil accepts any non-bot login; otherwise it reports
// whether handle may continue work in owner/name. BotUserIDs are provider
// user ids to ignore, along with handles containing [bot] and Slack bot_id.
type Allow struct {
	SlackUserIDs []string
	BotUserIDs   []string
	GitHub       func(ctx context.Context, owner, name, handle string) (bool, error)
}

// Outcome is one effect of Handle.
type Outcome struct {
	Kind           string
	Automation     string
	WorkItemID     string
	ConversationID string
	RunID          string
	Stage          string
	Agent          string
	ModelID        string
	Pool           string
	Runner         string
	Secrets        []string
	RequiredRepos  []definition.Repository
}

// Result is what Handle did. Matched is false when no automation matched
// and the event was not a reply on a bound thread. Emitted lists stage
// changes, a cause before the events it produced.
type Result struct {
	Matched  bool
	Outcomes []Outcome
	Emitted  []event.Event
}
