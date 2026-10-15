package github

import (
	"encoding/json"
	"time"
)

// Delivery is the subset of internal/ingress.Delivery this normalizer reads.
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

// Event is one canonical event (docs/07 §Normalized events).
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

// Actor is who caused the delivery. FactoryUserID is unset until a later
// stage resolves the provider identity.
type Actor struct {
	ProviderID    string  `json:"provider_id"`
	Handle        string  `json:"handle"`
	FactoryUserID *string `json:"factory_user_id"`
}

// Subject is what the event is about.
type Subject struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id,omitempty"`
	Number     int      `json:"number,omitempty"`
	Title      string   `json:"title,omitempty"`
	URL        string   `json:"url,omitempty"`
	Repo       *Repo    `json:"repo,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Branch     *string  `json:"branch"`
	BaseBranch *string  `json:"base_branch"`
	Paths      []string `json:"paths,omitempty"`
	Text       string   `json:"text,omitempty"`
}

// Repo is the code forge repository the subject lives in.
type Repo struct {
	CodeForge string `json:"code_forge"`
	Owner     string `json:"owner"`
	Name      string `json:"name"`
}

// Thread is the conversation key. ExternalRef is owner/repo#N for an issue
// or pull request.
type Thread struct {
	Kind        string `json:"kind"`
	ExternalRef string `json:"external_ref"`
}
