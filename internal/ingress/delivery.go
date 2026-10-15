package ingress

import (
	"context"
	"net/http"
	"time"

	"github.com/polds/factory/internal/event"
)

// Provider names accepted by Handler. They match event.Provider*.
const (
	ProviderGitHub  = event.ProviderGitHub
	ProviderSlack   = event.ProviderSlack
	ProviderLinear  = event.ProviderLinear
	ProviderWebhook = event.ProviderWebhook
)

// Delivery statuses. Dispatcher writes every status except received.
const (
	StatusReceived  = "received"
	StatusMatched   = "matched"
	StatusUnmatched = "unmatched"
	StatusIgnored   = "ignored"
	StatusFailed    = "failed"
)

// MaxBody is the read cap. A larger request is rejected with 413 and is
// neither verified nor persisted.
const MaxBody = 1 << 20

// MaxStoredBody is the persistence cap. A verified body above this size is
// stored truncated.
const MaxStoredBody = 256 << 10

// PrefixDelivery is the id prefix. The body is a 26-character ULID.
const PrefixDelivery = "dlv_"

// Delivery is one verified provider payload ready to persist.
type Delivery struct {
	ID         string // dlv_<ulid>
	Provider   string // github|slack|linear|webhook|schedule
	WebhookUID string // custom webhooks only
	DeliveryID string // provider delivery id; dedup key with Provider and WebhookUID
	ReceivedAt time.Time
	Headers    map[string]string // redacted allowlist
	Body       []byte            // at most MaxStoredBody
	Truncated  bool
}

// Verifier checks a provider signature over the raw body.
// Implementations live in internal/ingress/sig.
type Verifier interface {
	Verify(h http.Header, body []byte, now time.Time) error
}

// Normalizer maps a delivery onto canonical events.
// Zero events means the delivery is ignorable.
// The request handler does not call a normalizer.
type Normalizer interface {
	Normalize(d Delivery) ([]event.Event, error)
}

// Sink persists a delivery. Enqueue is idempotent on
// (Provider, WebhookUID, DeliveryID): a duplicate returns dup=true and a nil
// error. A non-nil error means the delivery was not recorded.
type Sink interface {
	Enqueue(ctx context.Context, d Delivery) (dup bool, err error)
}
