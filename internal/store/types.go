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
	// ErrAlreadyDecided is returned when DecideApproval is asked to change a
	// decision that was already recorded. Repeating the same decision is not an error.
	ErrAlreadyDecided = errors.New("store: approval already decided")
	// ErrConflict is returned when an input answer would replace a different
	// recorded answer, or a second pending question is opened.
	ErrConflict = errors.New("store: conflict")
	// ErrImmutable is returned when a webhook registration would change
	// auth_mode or signature_scheme. Delete the registration and create it again.
	ErrImmutable = errors.New("store: immutable")
)

const (
	WorkItemOpen             = "open"
	WorkItemAwaitingApproval = "awaiting_approval"
	WorkItemAwaitingInput    = "awaiting_input"
	WorkItemComplete         = "complete"
	WorkItemCancelled        = "cancelled"

	StageTriage       = "triage"
	StagePlanning     = "planning"
	StageBuilding     = "building"
	StageReviewing    = "reviewing"
	StageHumanHandoff = "human_handoff"

	DecisionApprove = "approve"
	DecisionReject  = "reject"

	SourceIssue    = "issue"
	SourceManual   = "manual"
	SourceAPI      = "api"
	SourceWebhook  = "webhook"
	SourceChat     = "chat"
	SourceSchedule = "schedule"
	SourceMCP      = "mcp"
	// SourceFactory is a stage event re-entered by the automation engine.
	SourceFactory = "factory"

	ProviderGitHub   = "github"
	ProviderSlack    = "slack"
	ProviderLinear   = "linear"
	ProviderWebhook  = "webhook"
	ProviderSchedule = "schedule"

	DeliveryReceived  = "received"
	DeliveryMatched   = "matched"
	DeliveryUnmatched = "unmatched"
	DeliveryIgnored   = "ignored"
	DeliveryFailed    = "failed"

	WebhookAuthToken     = "token"
	WebhookAuthURLToken  = "url_token"
	WebhookAuthSignature = "signature"

	// DeliveryBodyCap is the maximum raw body stored for one delivery.
	DeliveryBodyCap = 256 << 10

	SteerInterrupt = "interrupt"
	SteerFollowup  = "followup"

	InboxApproval = "approval"
	InboxQuestion = "question"
	InboxFailed   = "failed_run"

	InboxActionApprove = "approve"
	InboxActionAnswer  = "answer"
	InboxActionOpen    = "open"

	PhaseQueued   = "queued"
	PhaseAssigned = "assigned"
	PhaseRunning  = "running"
	PhaseTerminal = "terminal"
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
	// Linkage is empty when the run was started on its own, without a work item.
	WorkItemID     string
	ParentRunID    string
	ConversationID string
	Stage          string
	ModelID        string
	// FailureClass and FailureCode are empty until SetRunClass or the
	// migration backfill stores them. AssignedAt is the first
	// StartExecution and stays put when the run is requeued.
	FailureClass string
	FailureCode  string
	AssignedAt   *time.Time
	// Source is the linked work item's source. It is not a runs column.
	// List queries fill it from the join. Single-run reads fill it with
	// one extra query when WorkItemID is set.
	Source string
	// Purpose is work, scorer, benchmark, or improvement. It is set on
	// insert and cannot change. Reads return the stored value.
	Purpose string
}

// NewRun is the input to CreateRun. The store enqueues it in the same transaction.
type NewRun struct {
	FactoryID      string
	AgentID        string
	Pool           string
	Priority       Priority
	Prompt         string
	Title          string
	Repo           string
	WorkItemID     string
	ParentRunID    string
	ConversationID string
	Stage          string
	ModelID        string
	// Purpose empty means work. Any other value must be a known purpose.
	Purpose string
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
	Automations      []DefinedAutomation
}

// DefinedAutomation is one automation declared by an applied definition.
// Runtime pause is not a field: it lives in automation_overrides and
// survives a later apply.
type DefinedAutomation struct {
	Name          string
	Enabled       bool
	TriggersJSON  json.RawMessage
	Prompt        string
	OverridesJSON json.RawMessage
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

// WorkItem is one request kept from intake through handoff.
// Secret values are not fields of WorkItem.
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
	RevisionCount  int
	CreatedAt      time.Time
	// Suspension is set while a question is unanswered. Empty means the work
	// item is not suspended.
	SuspendedRunID       string
	SuspendedExecutionID string
	SuspendedAt          *time.Time
}

// NewWorkItem is the input to CreateWorkItem.
// Empty Stage defaults to triage, empty Status to open, and empty
// ConversationID is assigned a conv_ id.
type NewWorkItem struct {
	FactoryID      string
	Source         string
	SourceRef      string
	Title          string
	Body           string
	Stage          string
	Status         string
	CreatedBy      string
	ConversationID string
	RevisionCount  int
}

// WorkItemFilter narrows ListWorkItems. Zero values are unconstrained.
// Limit <= 0 uses 100.
type WorkItemFilter struct {
	FactoryID string
	Stage     string
	Status    string
	Source    string
	Limit     int
}

// Approval is one gate request on a work item. Decision is empty until decided.
type Approval struct {
	ID          string
	WorkItemID  string
	Gate        string
	RequestedAt time.Time
	DecidedAt   *time.Time
	DecidedBy   string
	Decision    string
	Note        string
}

// Delivery is one persisted provider payload. Headers are redacted and Body
// is capped at DeliveryBodyCap. Secret values are not fields of Delivery.
type Delivery struct {
	ID                 string
	FactoryID          string
	Provider           string
	WebhookUID         string
	DeliveryID         string
	ReceivedAt         time.Time
	Headers            map[string]string
	Body               []byte
	Truncated          bool
	Status             string
	MatchedAutomations []string
	ExpiresAt          time.Time
	// Normalized is the dispatcher event list with the raw key removed.
	// Nil means the column is NULL. [] means normalization produced no events.
	Normalized []byte
}

// WebhookRegistration is the live ingress config for one custom webhook.
// SecretRef is a credential name. URLTokenHash is a SHA-256 digest.
// Neither field holds a secret value.
type WebhookRegistration struct {
	UID              string
	FactoryID        string
	Name             string
	AuthMode         string
	SignatureScheme  string
	SecretRef        string
	URLTokenHash     []byte
	DeliveryIDHeader string
	Enabled          bool
}

// Schedule is one cron trigger. Name may be empty for the single unnamed
// schedule of an automation.
type Schedule struct {
	ID          string
	FactoryID   string
	Automation  string
	Name        string
	Cron        string
	LastFiredAt *time.Time
	NextFireAt  time.Time
	Paused      bool
}

// ExternalRef binds a provider thread to one work item.
type ExternalRef struct {
	Provider       string
	ExternalRef    string
	WorkItemID     string
	ConversationID string
}

// OutboundThread is the one root-and-status thread for a work item and provider.
type OutboundThread struct {
	WorkItemID   string
	Provider     string
	Channel      string
	RootRef      string
	StatusMsgRef string
}

// InputRequest is one question asked of a person. AnsweredAt is nil until answered.
type InputRequest struct {
	ID          string
	WorkItemID  string
	RunID       string
	ExecutionID string
	Question    string
	Answer      string
	AnsweredBy  string
	CreatedAt   time.Time
	AnsweredAt  *time.Time
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

// WorkerInfo is one worker plus the console's heartbeat and rejection fields.
// HeartbeatAgeMS is -1 when last_heartbeat_at is null.
type WorkerInfo struct {
	ID              string
	Pool            string
	Platform        string
	Backend         string
	Version         string
	LabelsJSON      json.RawMessage
	LastHeartbeatAt *time.Time
	ConnectedAt     *time.Time
	MaxConcurrent   int
	InFlight        int
	Reconnects      int
	Status          string
	HeartbeatAgeMS  int64
	Rejected1h      int
}

// PoolStat is queue depth, assignment latency, and rejections for one pool.
// P50TimeToAssignMS is nil when the last hour has no assignment.
type PoolStat struct {
	Pool              string
	QueueDepth        int
	P50TimeToAssignMS *int64
	Rejected1h        int
}

// Steer is one interrupt or follow-up waiting for the harness.
type Steer struct {
	ID          string
	RunID       string
	Kind        string
	Text        string
	CreatedAt   time.Time
	DeliveredAt *time.Time
	Actor       string
}

// Phase is one timeline segment. DurationMS is the time until the next
// phase, and 0 while the phase is still open.
type Phase struct {
	Phase       string
	At          time.Time
	DurationMS  int64
	ExecutionID string
}

// UsageCall is one usage event.
type UsageCall struct {
	Seq          int64
	Turn         int
	ModelID      string
	InputTokens  int64
	OutputTokens int64
	CostCents    int64
}

// ModelUsage is the usage events for one model.
type ModelUsage struct {
	ModelID      string
	InputTokens  int64
	OutputTokens int64
	CostCents    int64
	Calls        int
}

// UsageTotal is the sum of a run's usage events.
type UsageTotal struct {
	InputTokens  int64
	OutputTokens int64
	CostCents    int64
}

// RunUsage is harness usage for one run.
type RunUsage struct {
	Total   UsageTotal
	ByModel []ModelUsage
	Calls   []UsageCall
}

// FactoryEvent is a run event plus both identifiers.
// FactorySeq is the exclusive cursor: it is assigned after the
// inserting transaction commits, so commit order is seq order.
// GID is the identity value assigned at insert and is not a cursor.
type FactoryEvent struct {
	FactorySeq int64
	GID        int64
	Event
}

// InboxItem is one thing waiting on a person.
type InboxItem struct {
	Kind       string
	ID         string
	WorkItemID string
	RunID      string
	CreatedAt  time.Time
	Title      string
	Detail     string
	Action     string
}

// AutomationStat is one automation on the factory's current definition.
type AutomationStat struct {
	Name          string
	Enabled       bool
	RuntimePaused bool
	Triggers      json.RawMessage
	LastFiredAt   *time.Time
	MatchCount    int
	RunsStarted   int
}

// FactorySummary is the operational dashboard for [from, to).
// ActiveRuns and WaitingOnHuman are point-in-time, not windowed.
// PRsOpened counts work items created in the window that have an open
// PR URL. The schema has no timestamp for when the URL was stored.
type FactorySummary struct {
	RunsByStatus        map[string]int
	RunsByFailureClass  map[string]int
	QueueWaitP50MS      *int64
	ActiveRuns          int
	WaitingOnHuman      int
	PRsOpened           int
	DeliveriesUnmatched int
}

// ScorerResult is one scorer's label for a run. The id is assigned on the
// first upsert and kept when an on-demand score replaces the row.
type ScorerResult struct {
	ID                string
	FactoryID         string
	RunID             string
	ScorerName        string
	JudgeRunID        string
	Label             string
	Score             float64
	Passed            bool
	Reasoning         string
	Status            string
	CreatedAt         time.Time
	ReviewedAt        *time.Time
	ReviewedByBatchID string
}

// UnreviewedFailure is a failing ok result that no batch has reviewed.
// Agent is the scored run's agent name.
type UnreviewedFailure struct {
	ResultID  string
	RunID     string
	Agent     string
	Scorer    string
	Label     string
	Reasoning string
	At        time.Time
}

// NewImprovementBatch opens one queued batch for an agent.
// FailureIDs are scorer result ids in that factory.
type NewImprovementBatch struct {
	FactoryID  string
	Agent      string
	FailureIDs []string
}

// ImprovementFailure is one scored run linked to a batch.
type ImprovementFailure struct {
	RunID    string
	ResultID string
	Scorer   string
	Label    string
}

// ImprovementBatch is one self-improvement batch and its failures.
type ImprovementBatch struct {
	ID               string
	FactoryID        string
	Agent            string
	Status           string
	Failures         []ImprovementFailure
	PRURL            string
	PRNumber         int
	DefinitionRepo   string
	RejectionNote    string
	ImprovementRunID string
	CreatedAt        time.Time
	ClosedAt         *time.Time
}

// BenchmarkRun is one :run of a suite.
type BenchmarkRun struct {
	ID          string
	FactoryID   string
	Suite       string
	RequestedBy string
	Repetitions int
	CreatedAt   time.Time
}

// BenchmarkTrial is one configuration, task, and repetition.
type BenchmarkTrial struct {
	ID                  string
	BenchmarkRunID      string
	Configuration       string
	Task                string
	Repetition          int
	RunID               string
	Outcome             string
	CostCents           int64
	DurationMS          int64
	CorrectnessResultID string
	// DefinitionSHA is the 40-hex definition commit resolved for this trial's
	// configuration. Empty when the row predates the column.
	DefinitionSHA string
}

// TrialCell is one configuration and task.
// N counts pass and fail. Errored and pending stay outside N.
// The medians use the pass and fail rows only.
type TrialCell struct {
	Configuration    string
	Task             string
	N                int
	Pass             int
	Errored          int
	MedianCostCents  int64
	MedianDurationMS int64
}

// PRFact is one pull request. A nil timestamp, a nil bool, or a nil
// ChangedLines means this event does not mention the field. Upsert fills
// a blank and does not clear a stored value. A true human-push or
// improvement flag stays true.
type PRFact struct {
	FactoryID     string
	Repo          string
	Number        int
	WorkItemID    string
	OpenedAt      *time.Time
	MergedAt      *time.Time
	ClosedAt      *time.Time
	FirstReviewAt *time.Time
	HumanPushSeen *bool
	IsImprovement *bool
	ChangedLines  *int
}
