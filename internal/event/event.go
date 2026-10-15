package event

import (
	"encoding/json"
	"time"
)

// Provider names on Event.Provider.
const (
	ProviderGitHub   = "github"
	ProviderSlack    = "slack"
	ProviderLinear   = "linear"
	ProviderWebhook  = "webhook"
	ProviderSchedule = "schedule"
)

// Thread kinds on Thread.Kind. external_ref is the continuity key.
const (
	ThreadGitHubIssue = "github_issue"
	ThreadSlack       = "slack_thread"
	ThreadLinearIssue = "linear_issue"
	ThreadWebhook     = "webhook"
	ThreadSchedule    = "schedule"
)

// Event is one normalized provider event.
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

// Actor is who caused the event. FactoryUserID is nil until the identity
// is linked.
type Actor struct {
	ProviderID    string  `json:"provider_id"`
	Handle        string  `json:"handle"`
	FactoryUserID *string `json:"factory_user_id"`
}

// Repo is the code forge repository the subject lives in.
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

// Thread ties the event to an external conversation.
type Thread struct {
	Kind        string `json:"kind"`
	ExternalRef string `json:"external_ref"`
}
