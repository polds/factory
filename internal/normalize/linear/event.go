package linear

import (
	"encoding/json"
	"time"
)

// Provider is the normalized provider name for every event this package emits.
const Provider = "linear"

// Normalized event names. They match docs/07.
const (
	EventIssueCreated        = "issue_created"
	EventIssueAssigned       = "issue_assigned"
	EventIssueLabeled        = "issue_labeled"
	EventIssueStateChanged   = "issue_state_changed"
	EventCommentCreated      = "comment_created"
	EventAgentSessionCreated = "agent_session_created"
)

// ThreadKind is thread.kind for every Linear event.
const ThreadKind = "linear_issue"

// Delivery mirrors internal/ingress.Delivery until T18 merges.
// A failed signature check never produces a Delivery.
type Delivery struct {
	ID         string
	Provider   string
	WebhookUID string
	DeliveryID string
	ReceivedAt time.Time
	Headers    map[string]string
	Body       []byte
	Truncated  bool
}

// Event mirrors internal/event.Event until T18 merges.
// JSON names follow docs/07.
type Event struct {
	Provider   string          `json:"provider"`
	Event      string          `json:"event"`
	DeliveryID string          `json:"delivery_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Actor      Actor           `json:"actor"`
	Subject    Subject         `json:"subject"`
	Thread     Thread          `json:"thread"`
	Raw        json.RawMessage `json:"raw"`
}

// Actor is who triggered the provider event.
// FactoryUserID is nil until a later stage links a factory user.
type Actor struct {
	ProviderID    string  `json:"provider_id"`
	Handle        string  `json:"handle"`
	FactoryUserID *string `json:"factory_user_id"`
}

// Repo is a code forge repository. Linear events leave it empty.
type Repo struct {
	CodeForge string `json:"code_forge"`
	Owner     string `json:"owner"`
	Name      string `json:"name"`
}

// Subject is what the event is about.
type Subject struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	Repo       Repo     `json:"repo"`
	Labels     []string `json:"labels"`
	Branch     *string  `json:"branch"`
	BaseBranch *string  `json:"base_branch"`
	Paths      []string `json:"paths,omitempty"`
	Text       string   `json:"text,omitempty"`
}

// Thread ties a provider conversation to one work item.
type Thread struct {
	Kind        string `json:"kind"`
	ExternalRef string `json:"external_ref"`
}

// ExternalRef is the idempotency key for a Linear issue.
func ExternalRef(issueID string) string {
	return "linear:" + issueID
}
