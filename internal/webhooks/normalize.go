package webhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/ingress"
)

// Normalizer maps a verified custom-webhook delivery to canonical events.
// Handler does not call it.
type Normalizer struct{}

var _ ingress.Normalizer = Normalizer{}

// Normalize returns one webhook/received event.
// raw is the JSON body. thread.external_ref is the webhook uid.
func (Normalizer) Normalize(d ingress.Delivery) ([]event.Event, error) {
	return Normalize(d)
}

// Normalize returns one webhook/received event.
// raw is the JSON body. thread.external_ref is the webhook uid.
func Normalize(d ingress.Delivery) ([]event.Event, error) {
	if d.WebhookUID == "" {
		return nil, errors.New("webhooks: webhook uid is required")
	}
	if d.Truncated {
		return nil, errors.New("webhooks: delivery body truncated")
	}
	body := bytes.TrimSpace(d.Body)
	if len(body) == 0 {
		return nil, errors.New("webhooks: body is not JSON")
	}
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("webhooks: body is not JSON: %w", err)
	}
	occurred := d.ReceivedAt
	if !occurred.IsZero() {
		occurred = occurred.UTC()
	}
	return []event.Event{{
		Provider:   event.ProviderWebhook,
		Event:      "received",
		DeliveryID: d.DeliveryID,
		OccurredAt: occurred,
		Thread: event.Thread{
			Kind:        event.ThreadWebhook,
			ExternalRef: d.WebhookUID,
		},
		Raw: raw,
	}}, nil
}
