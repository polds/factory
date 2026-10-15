package slack

import (
	"encoding/json"
	"time"
)

// Delivery is the M4 ingress contract (internal/ingress.Delivery).
// Body is the verified raw payload. A failed verify never reaches Normalize.
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

// Event is the docs/07 normalized event (internal/event.Event).
type Event struct {
	Provider   string          `json:"provider"`
	Event      string          `json:"event"`
	DeliveryID string          `json:"delivery_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Actor      Actor           `json:"actor"`
	Subject    Subject         `json:"subject"`
	Thread     Thread          `json:"thread"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

// Actor is who caused the event. FactoryUserID is set by a later resolver.
type Actor struct {
	ProviderID    string  `json:"provider_id,omitempty"`
	Handle        string  `json:"handle,omitempty"`
	FactoryUserID *string `json:"factory_user_id,omitempty"`
}

// Subject is what the event is about.
type Subject struct {
	Kind       string   `json:"kind,omitempty"`
	ID         string   `json:"id,omitempty"`
	Number     int      `json:"number,omitempty"`
	Title      string   `json:"title,omitempty"`
	URL        string   `json:"url,omitempty"`
	Repo       Repo     `json:"repo,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Branch     *string  `json:"branch,omitempty"`
	BaseBranch *string  `json:"base_branch,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Text       string   `json:"text,omitempty"`
}

// Repo is a code forge repository. Slack events leave it empty.
type Repo struct {
	CodeForge string `json:"code_forge,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Thread ties a conversation to one external ref.
type Thread struct {
	Kind        string `json:"kind,omitempty"`
	ExternalRef string `json:"external_ref,omitempty"`
}
