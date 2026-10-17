package api

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// DefinitionTreeApplier applies an uploaded definition tree. An empty apply
// body still uses Backend.ApplyDefinition. A backend that does not implement
// this answers 501 when the body is non-empty.
type DefinitionTreeApplier interface {
	ApplyDefinitionTree(ctx context.Context, factoryID string, tree io.Reader) (DefinitionRef, []Diagnostic, error)
}

// consoleUnwiredDetail is the 501 problem detail when Backend does not
// implement ConsoleBackend. T47 wires the implementation.
const consoleUnwiredDetail = "console backend not wired"

// ErrSteerUnavailable is the conflict interrupt and followup return when
// the run cannot be steered. Reason is copied verbatim into the problem
// reason field. T47 uses these reasons:
//
//   - "worker/harness does not support steering"
//   - "run finished; idle window ended at …"
//   - "run has not started"
//
// Compare it with errors.As. It is not a sentinel: the reason varies.
type ErrSteerUnavailable struct {
	Reason string
}

func (e ErrSteerUnavailable) Error() string {
	if e.Reason == "" {
		return "steer unavailable"
	}
	return e.Reason
}

// ConsoleBackend is the M5 surface the console routes call.
// Handlers type-assert Backend to ConsoleBackend. A Backend that does not
// implement it answers 501 on those routes, so internal/server keeps
// compiling until T47 adds the methods.
//
// Secret and Identity have no value field. Credential fields on these
// types use hidden and json:"-", so encoding a value does not emit them.
type ConsoleBackend interface {
	Definition(ctx context.Context, factory string) (DefinitionView, error)
	RunDetail(ctx context.Context, id string) (RunDetail, error)
	// ListRunRows is the console list. Backend.ListRuns stays the unwired
	// body. T47 applies the filter, including the console fields ListRuns
	// still ignores. Steer and pr_url stay on RunDetail.
	ListRunRows(ctx context.Context, filter RunFilter) ([]RunRow, error)
	RunTimeline(ctx context.Context, id string) ([]Phase, error)
	RunUsage(ctx context.Context, id string) (Usage, error)
	Interrupt(ctx context.Context, id, actor string) (steerID string, err error)
	Followup(ctx context.Context, id, actor, text string) (steerID string, err error)
	// NextSteers long-polls. runToken is the run bearer, not the factory API
	// key. after is exclusive; empty starts at the beginning. wait is at most
	// 25s. Return when ctx ends.
	NextSteers(ctx context.Context, runToken, runID, after string, wait time.Duration) ([]Steer, error)
	// FactoryEvents opens the factory log at gid > from, then live rows, in
	// gid order. Close the channel when the stream ends. Honor ctx.
	FactoryEvents(ctx context.Context, factory string, from int64) (<-chan FactoryEvent, error)
	Inbox(ctx context.Context, factory string) ([]InboxItem, error)
	Automations(ctx context.Context, factory string) ([]Automation, error)
	PauseAutomation(ctx context.Context, factory, name string) error
	ResumeAutomation(ctx context.Context, factory, name string) error
	Webhooks(ctx context.Context, factory string) ([]Webhook, error)
	Workers(ctx context.Context) (WorkersView, error)
	Secrets(ctx context.Context, factory string) ([]Secret, error)
	Identities(ctx context.Context) ([]Identity, error)
	Summary(ctx context.Context, factory string, from, to time.Time) (Summary, error)
}

// SteerHistory lists steers for a run. API-key auth, not the run token.
// A backend that does not implement it answers 501.
type SteerHistory interface {
	ListSteers(ctx context.Context, runID string) ([]SteerRecord, error)
}

// WebhookDeliveryBackend lists one webhook's deliveries.
// A backend that does not implement it answers 404, matching DeliveryBackend.
type WebhookDeliveryBackend interface {
	ListWebhookDeliveries(ctx context.Context, factoryID, webhookUID, status string, limit int) ([]Delivery, error)
}

const (
	// DefinitionGit is a git-backed factory. The editor is read-only.
	DefinitionGit = "git"
	// DefinitionLocal is a local factory. The editor can apply changes.
	DefinitionLocal = "local"

	// PhaseQueued is the wait before a worker takes the run.
	PhaseQueued = "queued"
	// PhaseAssigned is the handoff onto a worker.
	PhaseAssigned = "assigned"
	// PhaseRunning is execution.
	PhaseRunning = "running"
	// PhaseIdle is the post-completion steer window.
	PhaseIdle = "idle"
	// PhaseTerminal is the run's final phase.
	PhaseTerminal = "terminal"

	// SteerKindInterrupt cancels the in-flight turn.
	SteerKindInterrupt = "interrupt"
	// SteerKindFollowup appends a user message.
	SteerKindFollowup = "followup"

	// FailureClassAgent is a failure the agent reported.
	FailureClassAgent = "agent"
	// FailureClassInfrastructure is a failure of the platform around the agent.
	FailureClassInfrastructure = "infrastructure"
	// FailureClassConfiguration is a bad definition or environment.
	FailureClassConfiguration = "configuration"
	// FailureClassCancelled is an operator or supersede cancel.
	FailureClassCancelled = "cancelled"

	// InboxKindApproval is a gate waiting for a decision.
	InboxKindApproval = "approval"
	// InboxKindQuestion is an agent question waiting for an answer.
	InboxKindQuestion = "question"
	// InboxKindFailedRun is a failed run that is still the latest on its work item.
	InboxKindFailedRun = "failed_run"

	// InboxActionDecide is the approval action.
	InboxActionDecide = "decide"
	// InboxActionAnswer is the question action.
	InboxActionAnswer = "answer"
	// InboxActionOpen is the failed-run action.
	InboxActionOpen = "open"
)

// DefinitionView is GET /factory/{uid}/definition.
// HeldValue is an environment value and HeldSign is a signing key.
// Both stay off the wire. The response echoes file source.
type DefinitionView struct {
	Source        string           `json:"source"`
	GitSHA        string           `json:"git_sha"`
	ProductionRef string           `json:"production_ref"`
	Editable      bool             `json:"editable"`
	ForgeURL      string           `json:"forge_url,omitempty"`
	Files         []DefinitionFile `json:"files"`
	HeldValue     hidden           `json:"-"`
	HeldSign      hidden           `json:"-"`
}

// DefinitionFile is one file in a definition view.
// Content is the definition source. The editor shows it verbatim, so this
// route does not redact it. Credentials are not stored in Content; they
// stay on DefinitionView.HeldValue and HeldSign, which are not serialized.
type DefinitionFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Failure is the agent-vs-infrastructure classification on a run.
// T47 maps it from the classifier. This package does not import that package.
type Failure struct {
	Class     string   `json:"class"`
	Code      string   `json:"code"`
	Summary   string   `json:"summary"`
	Retryable bool     `json:"retryable"`
	Evidence  []string `json:"evidence"`
}

// SteerState says whether interrupt and follow-up are available.
type SteerState struct {
	Enabled   bool       `json:"enabled"`
	Reason    string     `json:"reason"`
	IdleUntil *time.Time `json:"idle_until,omitempty"`
}

// RunDetail is GET /agent/runs/{id} when ConsoleBackend is wired.
// It adds the console fields on top of Run. Without ConsoleBackend the
// route keeps returning Run.
type RunDetail struct {
	Run
	Failure            Failure           `json:"failure"`
	QueueWaitMS        int64             `json:"queue_wait_ms"`
	ExecMS             int64             `json:"exec_ms"`
	Steer              SteerState        `json:"steer"`
	PRURL              string            `json:"pr_url,omitempty"`
	ExitCode           *int              `json:"exit_code,omitempty"`
	CostCents          *int64            `json:"cost_cents,omitempty"`
	KubernetesSnapshot map[string]string `json:"kubernetes_snapshot,omitempty"`
}

// RunRow is one GET /agent/runs element when ConsoleBackend is wired.
// The unwired list still encodes []Run, so these fields are absent there.
// Queue wait and exec are always present, including zero. Failure class,
// failure summary, and cost are omitted when unknown. The summary is the
// detail contract's failure.summary; the list does not carry steer or pr_url.
type RunRow struct {
	Run
	FailureClass   string `json:"failure_class,omitempty"`
	FailureCode    string `json:"failure_code,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`
	QueueWaitMS    int64  `json:"queue_wait_ms"`
	ExecMS         int64  `json:"exec_ms"`
	CostCents      *int64 `json:"cost_cents,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
}

// SteerRecord is one row of GET /agent/runs/{id}/steers.
// Text is present even when empty. DeliveredAt is omitted until the harness acks.
type SteerRecord struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Text        string     `json:"text"`
	Actor       string     `json:"actor"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// Phase is one row of GET /agent/runs/{id}/timeline.
type Phase struct {
	Phase       string    `json:"phase"`
	At          time.Time `json:"at"`
	DurationMS  int64     `json:"duration_ms"`
	ExecutionID string    `json:"execution_id"`
}

// Usage is GET /agent/runs/{id}/harness-usage.
type Usage struct {
	Total   UsageTotal   `json:"total"`
	ByModel []ModelUsage `json:"by_model"`
	Calls   []UsageCall  `json:"calls"`
}

// UsageTotal is the summed token and cost line.
type UsageTotal struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostCents    int64 `json:"cost_cents"`
}

// ModelUsage is the same totals for one model.
type ModelUsage struct {
	ModelID      string `json:"model_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostCents    int64  `json:"cost_cents"`
}

// UsageCall is one model call.
type UsageCall struct {
	Seq          int64  `json:"seq"`
	Turn         int    `json:"turn"`
	ModelID      string `json:"model_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostCents    int64  `json:"cost_cents"`
}

// SteerAck is the 202 body for interrupt and followup.
type SteerAck struct {
	SteerID string `json:"steer_id"`
}

// Steer is one queued steer the harness receives.
type Steer struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// FactoryEvent is one row of the factory-wide log.
// GID is run_events.gid and becomes the SSE id. Data is the per-run object;
// the writer adds run_id and gid.
type FactoryEvent struct {
	GID   int64
	RunID string
	Type  string
	Data  json.RawMessage
	// Held is the run bearer stored beside the payload. The SSE frame copies
	// Data and does not copy Held. Data's text is transcript content and is
	// echoed; it is not this credential.
	Held hidden `json:"-"`
}

// InboxItem is one oldest-first inbox row.
type InboxItem struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	WorkItemID string    `json:"work_item_id"`
	RunID      string    `json:"run_id"`
	CreatedAt  time.Time `json:"created_at"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	Action     string    `json:"action"`
}

// Automation is one automation plus its runtime override.
type Automation struct {
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	RuntimePaused bool       `json:"runtime_paused"`
	Triggers      []string   `json:"triggers"`
	LastFiredAt   *time.Time `json:"last_fired_at,omitempty"`
	MatchCount    int        `json:"match_count"`
	RunsStarted   int        `json:"runs_started"`
}

// Webhook is a registration without its token or signing secret.
type Webhook struct {
	UID             string     `json:"uid"`
	Name            string     `json:"name"`
	IngressPath     string     `json:"ingress_path"`
	AuthMode        string     `json:"auth_mode"`
	SignatureScheme string     `json:"signature_scheme"`
	Enabled         bool       `json:"enabled"`
	LastDeliveryAt  *time.Time `json:"last_delivery_at,omitempty"`
	// HeldSign is the webhook signing key. The response does not include it.
	HeldSign hidden `json:"-"`
}

// WorkersView is GET /agent/connected-self-hosted-workers.
type WorkersView struct {
	Workers []Worker   `json:"workers"`
	Pools   []PoolStat `json:"pools"`
}

// Worker is one connected worker. It has no credentials.
type Worker struct {
	ID             string `json:"id"`
	Pool           string `json:"pool"`
	Platform       string `json:"platform"`
	Backend        string `json:"backend"`
	Version        string `json:"version"`
	InFlight       int    `json:"in_flight"`
	Capacity       int    `json:"capacity"`
	HeartbeatAgeMS int64  `json:"heartbeat_age_ms"`
	Reconnects     int    `json:"reconnects"`
	Rejected1h     int    `json:"rejected_1h"`
}

// PoolStat is one pool's queue and rejection line.
type PoolStat struct {
	Pool            string `json:"pool"`
	QueueDepth      int    `json:"queue_depth"`
	P50TimeToAssign int64  `json:"p50_time_to_assign_ms"`
	Rejected1h      int    `json:"rejected_1h"`
}

// Secret is a name, where it is used, and whether it is present.
// It has no value field. Callers cannot read secret material from this type.
type Secret struct {
	Name          string      `json:"name"`
	Provider      string      `json:"provider"`
	Present       bool        `json:"present"`
	UsedBy        []SecretUse `json:"used_by"`
	LastUsedRunID string      `json:"last_used_run_id,omitempty"`
}

// SecretUse is one consumer of a secret name.
type SecretUse struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Identity is a read-only credential name. It has no value field.
type Identity struct {
	Subject    string     `json:"subject"`
	Kind       string     `json:"kind"`
	Scope      string     `json:"scope"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Summary is the operational dashboard for one window.
// from and to are query parameters, not response fields.
type Summary struct {
	RunsByStatus        map[string]int `json:"runs_by_status"`
	RunsByFailureClass  map[string]int `json:"runs_by_failure_class"`
	QueueWaitP50MS      int64          `json:"queue_wait_p50_ms"`
	ActiveRuns          int            `json:"active_runs"`
	WaitingOnHuman      int            `json:"waiting_on_human"`
	PRsOpened           int            `json:"prs_opened"`
	DeliveriesUnmatched int            `json:"deliveries_unmatched"`
}
