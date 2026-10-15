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
	// ErrNotFound is returned when a row does not exist.
	// ResumingAnswerer returns it when the work item has no input request.
	// Adapters return this error, not store.ErrNotFound.
	ErrNotFound = errors.New("automation: not found")
	// ErrConflict is returned when a question is answered twice with
	// different text. Adapters return this error, not store.ErrConflict.
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

// NewWorkItem is the input to Store.OpenWorkItem. An empty conversation
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
// RunID is the parked run (store.InputRequest.RunID), not the input-request id.
type Input struct {
	ID         string
	WorkItemID string
	RunID      string
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

// NewRun is the input to an enqueued stage run.
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

// IntakeSlot is one automation's first stage for a delivery.
// Run is nil when the orchestrator effect is an approval gate.
// The slot is still recorded so a redelivery is a duplicate and so
// Owners can name the automation. WorkItemID and ConversationID may be
// empty; EnsureIntake sets them from the bound work item.
type IntakeSlot struct {
	Automation string
	Run        *NewRun
}

// IntakeSlotResult is one slot after EnsureIntake.
// Inserted is false when this delivery already recorded the automation.
type IntakeSlotResult struct {
	Automation string
	Run        Run
	Inserted   bool
}

// IntakeResult is the durable outcome of EnsureIntake.
// Continuation is true when the work item was already opened by a
// different delivery. No slot was inserted. The caller appends context.
type IntakeResult struct {
	Item         WorkItem
	Continuation bool
	Slots        []IntakeSlotResult
}

// Store is the persistence Handle needs. T30 adapts internal/store.
// Implementations must not call Engine.Handle.
//
// Idempotency is durable. Deliveries are re-claimed after a crash or a
// batch rollback, and more than one engine may run. Process-local claims
// are not enough.
//
// T30 additions, composed in transactions (do not call CreateWorkItem and
// then BindExternalRef as two commits):
//
//	CREATE TABLE intake_slots (
//	    work_item_id text NOT NULL REFERENCES work_items (id),
//	    automation   text NOT NULL,
//	    delivery_id  text NOT NULL,
//	    run_id       text NOT NULL DEFAULT '',
//	    PRIMARY KEY (work_item_id, automation)
//	);
//
//	CREATE TABLE delivery_effects (
//	    delivery_id  text PRIMARY KEY,
//	    work_item_id text NOT NULL REFERENCES work_items (id),
//	    kind         text NOT NULL CHECK (kind IN ('follow_up', 'context')),
//	    run_id       text NOT NULL DEFAULT ''
//	);
//
// OpenWorkItem inserts the work item and the external ref in one
// transaction. On (provider, external_ref) conflict the new work item is
// rolled back and the existing row is returned with created=false.
//
// EnsureIntake inserts each missing slot for this delivery. The run, when
// the slot has one, is inserted in that same transaction. A slot already
// stored for a different delivery returns Continuation and inserts nothing.
// A slot already stored for this delivery returns Inserted=false. Owners
// reads intake_slots.automation for the work item.
//
// FollowUp inserts the run, moves the work item to stage/status, and
// inserts delivery_effects in one transaction. A conflicting delivery_id
// returns the existing run and created=false.
//
// AppendContext appends text once per delivery_id and updates
// work_items.body. server.stagePrompt passes that column as Body into
// handoff.BuildPrompt. A private log the orchestrator does not read drops
// the continuation for later pipeline stages. applied is false when this
// delivery was already stored. Context returns the notes appended after
// intake (the same bytes appended to body), apart from the original
// fenced event.
type Store interface {
	OpenWorkItem(ctx context.Context, provider, externalRef string, w NewWorkItem) (item WorkItem, created bool, err error)
	EnsureIntake(ctx context.Context, workItemID, deliveryID string, slots []IntakeSlot) (IntakeResult, error)
	Owners(ctx context.Context, workItemID string) ([]string, error)
	GetWorkItem(ctx context.Context, id string) (WorkItem, error)
	GetExternalRef(ctx context.Context, provider, externalRef string) (Ref, error)
	FollowUp(ctx context.Context, deliveryID string, run NewRun, stage, status string) (runOut Run, created bool, err error)
	AppendContext(ctx context.Context, workItemID, deliveryID, text string) (applied bool, err error)
	Context(ctx context.Context, workItemID string) (string, error)
	LatestRun(ctx context.Context, workItemID string) (Run, error)
	OpenPullRequest(ctx context.Context, workItemID string) (url string, ok bool, err error)
}

// ResumingAnswerer resumes a parked run.
//
// Answer is (*server.Server).AnswerInput from T27 (PR #65). It is not
// (*store.Store).AnswerInputRequest. The store method writes the answer,
// clears suspension, and sets an awaiting_input work item back to open.
// It does not insert run_queue and it does not notify. Wiring it here
// leaves the run off the queue with no live execution.
//
// Server.AnswerInput records the answer and resumes the parked run: one
// new execution of the same run, the same conversation_id, with the
// question and answer appended to the prompt. The same answer is
// idempotent and does not start another execution. The call still reaches
// resumeStored when the row is already answered, and resumeIfParked queues
// the run when it is parked (running, absent from the queue, no live
// execution). A different answer returns ErrConflict.
//
// ErrNotFound means this work item has no input request. Handle then
// follows up or appends context. A retry whose row is already stored does
// not return ErrNotFound, so it resumes instead of following up.
//
// Input.RunID is store.InputRequest.RunID, the parked run, not the
// input-request id. T30's adapter must call Server.AnswerInput. A direct
// use of Store.AnswerInputRequest does not implement this interface.
type ResumingAnswerer interface {
	Answer(ctx context.Context, workItemID, answer, answeredBy string) (Input, error)
}

// Allow decides who may continue work (D12).
//
// T30 must supply this. An empty SlackUserIDs list admits no Slack user.
// A nil GitHub func admits no GitHub commenter. Both are fail-closed.
// definition.SlackIntegration has no allowedUserIds field yet
// (only AutoRespondToThreadReplies). T30 adds
// AllowedUserIDs []string `json:"allowedUserIds,omitempty"` and copies it
// here. A missing list must not be read as "allow every non-bot".
//
// SlackUserIDs are the Slack user ids permitted to continue a thread.
// GitHub reports whether handle may continue work in owner/name, which is
// a collaborator check. BotUserIDs are provider user ids to ignore, along
// with handles containing [bot] and Slack bot_id or subtype bot_message.
// Bot actors are ignored on intake and on replies.
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
