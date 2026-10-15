package trigger

import (
	"encoding/json"
	"time"
)

// Event is the normalized event from the M4 contract (docs/07).
// internal/event belongs to T18 and is not on main yet, so this package
// vendors the shape. Rebase onto that package when it lands; the field
// set is the contract, not a second schema.
type Event struct {
	Provider   string
	Event      string
	DeliveryID string
	OccurredAt time.Time
	Actor      Actor
	Subject    Subject
	Thread     Thread
	Raw        json.RawMessage
}

// Actor is who caused the event.
type Actor struct {
	ProviderID    string
	Handle        string
	FactoryUserID *string
}

// Repo is a code-forge repository.
type Repo struct {
	CodeForge string
	Owner     string
	Name      string
}

// Subject is what the event is about.
type Subject struct {
	Kind       string
	ID         string
	Number     int
	Title      string
	URL        string
	Repo       Repo
	Labels     []string
	Branch     string
	BaseBranch string
	Paths      []string
	Text       string
}

// Thread is the conversation the event belongs to.
type Thread struct {
	Kind        string
	ExternalRef string
}
