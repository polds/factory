package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RunFilter selects GET /api/v1/agent/runs. Empty fields are omitted.
type RunFilter struct {
	Agent        string
	Status       string
	Worker       string
	Model        string
	FailureClass string
	Source       string
	ParentRunID  string
	WorkItemID   string
}

// Failure is the agent-vs-infrastructure classification on a run.
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

// RunDetail is GET /api/v1/agent/runs/{id} with the console fields.
type RunDetail struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	FactoryID     string     `json:"factory_id,omitempty"`
	Agent         string     `json:"agent,omitempty"`
	WorkItemID    string     `json:"work_item_id,omitempty"`
	ParentRunID   string     `json:"parent_run_id,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	Failure       Failure    `json:"failure"`
	QueueWaitMS   int64      `json:"queue_wait_ms"`
	ExecMS        int64      `json:"exec_ms"`
	Steer         SteerState `json:"steer"`
	PRURL         string     `json:"pr_url,omitempty"`
}

// Phase is one timeline row.
type Phase struct {
	Phase       string    `json:"phase"`
	At          time.Time `json:"at"`
	DurationMS  int64     `json:"duration_ms"`
	ExecutionID string    `json:"execution_id"`
}

// Usage is the harness token and cost report.
type Usage struct {
	Total   UsageTotal   `json:"total"`
	ByModel []ModelUsage `json:"by_model"`
	Calls   []UsageCall  `json:"calls"`
}

// UsageTotal is the summed token line.
type UsageTotal struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostCents    int64 `json:"cost_cents"`
}

// ModelUsage is one model's totals.
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

// Steer is one queued steer.
type Steer struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// InboxItem is one inbox row.
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

// Automation is one automation and its runtime pause.
type Automation struct {
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	RuntimePaused bool       `json:"runtime_paused"`
	Triggers      []string   `json:"triggers"`
	LastFiredAt   *time.Time `json:"last_fired_at,omitempty"`
	MatchCount    int        `json:"match_count"`
	RunsStarted   int        `json:"runs_started"`
}

// Webhook is a registration. It has no token or signing secret.
type Webhook struct {
	UID             string     `json:"uid"`
	Name            string     `json:"name"`
	IngressPath     string     `json:"ingress_path"`
	AuthMode        string     `json:"auth_mode"`
	SignatureScheme string     `json:"signature_scheme"`
	Enabled         bool       `json:"enabled"`
	LastDeliveryAt  *time.Time `json:"last_delivery_at,omitempty"`
}

// WorkersView is the connected workers report.
type WorkersView struct {
	Workers []Worker   `json:"workers"`
	Pools   []PoolStat `json:"pools"`
}

// Worker is one connected worker.
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

// PoolStat is one pool's queue line.
type PoolStat struct {
	Pool            string `json:"pool"`
	QueueDepth      int    `json:"queue_depth"`
	P50TimeToAssign int64  `json:"p50_time_to_assign_ms"`
	Rejected1h      int    `json:"rejected_1h"`
}

// Secret is a name and whether it is present. It has no value field.
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

// Summary is the operational dashboard.
type Summary struct {
	RunsByStatus        map[string]int `json:"runs_by_status"`
	RunsByFailureClass  map[string]int `json:"runs_by_failure_class"`
	QueueWaitP50MS      int64          `json:"queue_wait_p50_ms"`
	ActiveRuns          int            `json:"active_runs"`
	WaitingOnHuman      int            `json:"waiting_on_human"`
	PRsOpened           int            `json:"prs_opened"`
	DeliveriesUnmatched int            `json:"deliveries_unmatched"`
}

// Definition is GET /factory/{uid}/definition.
type Definition struct {
	Source        string           `json:"source"`
	GitSHA        string           `json:"git_sha"`
	ProductionRef string           `json:"production_ref"`
	Editable      bool             `json:"editable"`
	Files         []DefinitionFile `json:"files"`
}

// DefinitionFile is one definition file.
// Content is the source text. definition show prints it. This type has no
// value field, so a JSON "value" on the response is not printed.
type DefinitionFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type followupBody struct {
	Text string `json:"text"`
}

// RunRow is one GET /api/v1/agent/runs element from a console backend.
// Older rows omit the console fields. Queue wait and exec decode as zero
// when the field is absent. Cost is nil when unknown, including a missing
// field, and zero when the backend sends 0. Steer and pr_url are not here.
type RunRow struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	FactoryID      string `json:"factory_id,omitempty"`
	Agent          string `json:"agent,omitempty"`
	WorkItemID     string `json:"work_item_id,omitempty"`
	ParentRunID    string `json:"parent_run_id,omitempty"`
	FailureReason  string `json:"failure_reason,omitempty"`
	ModelID        string `json:"model_id,omitempty"`
	Stage          string `json:"stage,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	FailureClass   string `json:"failure_class,omitempty"`
	FailureSummary string `json:"failure_summary,omitempty"`
	QueueWaitMS    int64  `json:"queue_wait_ms"`
	ExecMS         int64  `json:"exec_ms"`
	CostCents      *int64 `json:"cost_cents,omitempty"`
}

// ListRuns lists runs with the console filters.
func (c *Client) ListRuns(ctx context.Context, f RunFilter) ([]RunRow, error) {
	q := url.Values{}
	setQuery(q, "agent", f.Agent)
	setQuery(q, "status", f.Status)
	setQuery(q, "worker", f.Worker)
	setQuery(q, "model", f.Model)
	setQuery(q, "failure_class", f.FailureClass)
	setQuery(q, "source", f.Source)
	setQuery(q, "parent_run_id", f.ParentRunID)
	setQuery(q, "work_item_id", f.WorkItemID)
	var runs []RunRow
	err := c.doJSONQuery(ctx, http.MethodGet, "/api/v1/agent/runs", q, nil, &runs, http.StatusOK)
	return runs, err
}

// RunDetail fetches one run, including failure class, summary, and evidence.
func (c *Client) RunDetail(ctx context.Context, id string) (RunDetail, error) {
	if err := requireID(id, "run id"); err != nil {
		return RunDetail{}, err
	}
	var detail RunDetail
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/runs/"+url.PathEscape(id), nil, &detail, http.StatusOK)
	return detail, err
}

// RunTimeline fetches the run's phases.
func (c *Client) RunTimeline(ctx context.Context, id string) ([]Phase, error) {
	if err := requireID(id, "run id"); err != nil {
		return nil, err
	}
	var phases []Phase
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/runs/"+url.PathEscape(id)+"/timeline", nil, &phases, http.StatusOK)
	return phases, err
}

// RunUsage fetches harness token and cost totals.
func (c *Client) RunUsage(ctx context.Context, id string) (Usage, error) {
	if err := requireID(id, "run id"); err != nil {
		return Usage{}, err
	}
	var usage Usage
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/runs/"+url.PathEscape(id)+"/harness-usage", nil, &usage, http.StatusOK)
	return usage, err
}

// Interrupt queues an interrupt and returns the steer id.
func (c *Client) Interrupt(ctx context.Context, id string) (string, error) {
	if err := requireID(id, "run id"); err != nil {
		return "", err
	}
	var ack SteerAck
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/runs/"+url.PathEscape(id)+"/interrupt", struct{}{}, &ack, http.StatusAccepted)
	return ack.SteerID, err
}

// Followup queues a follow-up and returns the steer id.
func (c *Client) Followup(ctx context.Context, id, text string) (string, error) {
	if err := requireID(id, "run id"); err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("text is required")
	}
	var ack SteerAck
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/runs/"+url.PathEscape(id)+"/followups", followupBody{Text: text}, &ack, http.StatusAccepted)
	return ack.SteerID, err
}

// PollSteers long-polls GET …/steer with the run token, not the factory API key.
func (c *Client) PollSteers(ctx context.Context, runID, runToken, after string) ([]Steer, error) {
	if err := requireID(runID, "run id"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(runToken) == "" {
		return nil, errors.New("run token is required")
	}
	q := url.Values{}
	setQuery(q, "after", after)
	var steers []Steer
	path := "/api/v1/agent/runs/" + url.PathEscape(runID) + "/steer"
	err := c.doBearer(ctx, runToken, http.MethodGet, path, q, nil, &steers, http.StatusOK)
	return steers, err
}

// Inbox lists the factory inbox, oldest first.
func (c *Client) Inbox(ctx context.Context, factory string) ([]InboxItem, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	var items []InboxItem
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "inbox"), nil, &items, http.StatusOK)
	return items, err
}

// Automations lists automations and their runtime pause.
func (c *Client) Automations(ctx context.Context, factory string) ([]Automation, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	var rows []Automation
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "automations"), nil, &rows, http.StatusOK)
	return rows, err
}

// PauseAutomation sets the runtime pause override.
func (c *Client) PauseAutomation(ctx context.Context, factory, name string) error {
	return c.automationState(ctx, factory, name, "pause")
}

// ResumeAutomation clears the runtime pause override.
func (c *Client) ResumeAutomation(ctx context.Context, factory, name string) error {
	return c.automationState(ctx, factory, name, "resume")
}

func (c *Client) automationState(ctx context.Context, factory, name, op string) error {
	if err := requireFactory(factory); err != nil {
		return err
	}
	if err := requireID(name, "automation name"); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, factoryPath(factory, "automations", name)+"/"+op, nil, nil, http.StatusNoContent)
}

// Webhooks lists registrations. Tokens and signing secrets are not returned.
func (c *Client) Webhooks(ctx context.Context, factory string) ([]Webhook, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	var rows []Webhook
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "webhooks"), nil, &rows, http.StatusOK)
	return rows, err
}

// Workers lists connected workers and pool stats.
func (c *Client) Workers(ctx context.Context) (WorkersView, error) {
	var view WorkersView
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/connected-self-hosted-workers", nil, &view, http.StatusOK)
	return view, err
}

// Secrets lists secret names. Values are not part of the response.
func (c *Client) Secrets(ctx context.Context, factory string) ([]Secret, error) {
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	var rows []Secret
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "secrets"), nil, &rows, http.StatusOK)
	return rows, err
}

// Identities lists credential names. Values are not part of the response.
func (c *Client) Identities(ctx context.Context) ([]Identity, error) {
	var rows []Identity
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/identities", nil, &rows, http.StatusOK)
	return rows, err
}

// Summary fetches the operational dashboard. Zero times omit the query.
func (c *Client) Summary(ctx context.Context, factory string, from, to time.Time) (Summary, error) {
	if err := requireFactory(factory); err != nil {
		return Summary{}, err
	}
	q := url.Values{}
	if !from.IsZero() {
		q.Set("from", from.UTC().Format(time.RFC3339))
	}
	if !to.IsZero() {
		q.Set("to", to.UTC().Format(time.RFC3339))
	}
	var sum Summary
	err := c.doJSONQuery(ctx, http.MethodGet, factoryPath(factory, "summary"), q, nil, &sum, http.StatusOK)
	return sum, err
}

// Definition fetches the factory definition view.
func (c *Client) Definition(ctx context.Context, factory string) (Definition, error) {
	if err := requireFactory(factory); err != nil {
		return Definition{}, err
	}
	var view Definition
	err := c.doJSON(ctx, http.MethodGet, factoryPath(factory, "definition"), nil, &view, http.StatusOK)
	return view, err
}

// ReadFactoryEvents reads one factory SSE response until the server closes it.
// lastEventID is sent as Last-Event-ID and wins over from on the server.
func (c *Client) ReadFactoryEvents(ctx context.Context, factory string, from int64, lastEventID string) ([]Event, error) {
	if err := c.requireKey(); err != nil {
		return nil, err
	}
	if err := requireFactory(factory); err != nil {
		return nil, err
	}
	q := url.Values{}
	if from > 0 {
		q.Set("from_seq", strconv.FormatInt(from, 10))
	}
	endpoint, err := c.endpoint(factoryPath(factory, "events"), q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, readErr
		}
		return nil, problemError(resp.StatusCode, raw)
	}
	return ParseSSE(resp.Body)
}

func setQuery(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}
