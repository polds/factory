package webhooks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/ingress"
	"github.com/polds/factory/internal/trigger"
)

func TestNormalizeWebhookReceived(t *testing.T) {
	t.Parallel()
	body := []byte("{\n  \"action\": \"opened\"\n}\n")
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("EST", -5*3600))
	got, err := Normalize(ingress.Delivery{
		Provider:   ingress.ProviderWebhook,
		WebhookUID: "whk_01",
		DeliveryID: "run-9",
		ReceivedAt: when,
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("events %d", len(got))
	}
	ev := got[0]
	if ev.Provider != event.ProviderWebhook || ev.Event != "received" || ev.DeliveryID != "run-9" {
		t.Fatalf("%+v", ev)
	}
	if !ev.OccurredAt.Equal(when.UTC()) {
		t.Fatalf("occurred %s", ev.OccurredAt)
	}
	if ev.Thread.Kind != event.ThreadWebhook || ev.Thread.ExternalRef != "whk_01" {
		t.Fatalf("thread %+v", ev.Thread)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Raw, &payload); err != nil || payload["action"] != "opened" {
		t.Fatalf("raw %s err %v", ev.Raw, err)
	}
	if !json.Valid(ev.Raw) {
		t.Fatalf("raw %s", ev.Raw)
	}
}

func TestNormalizeMatchesWebhookIDTrigger(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"opened","pull_request":{"draft":false}}`)
	got, err := Normalizer{}.Normalize(ingress.Delivery{
		WebhookUID: "whk_01",
		DeliveryID: "d1",
		Body:       body,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := triggerEvent(got[0])
	m, diags := trigger.Compile(definition.Trigger{
		Provider: "webhook",
		Event:    "received",
		Filter: map[string]any{
			"webhook_ids": []any{"whk_01"},
			"payload": map[string]any{
				"action": []any{"opened"},
			},
		},
	})
	if m == nil {
		t.Fatalf("diags %+v", diags)
	}
	if !m.Match(ev) {
		t.Fatal("webhook uid did not match webhook_ids")
	}
	ev.Thread.ExternalRef = "whk_other"
	if m.Match(ev) {
		t.Fatal("other uid matched")
	}
}

func TestNormalizeRejectsUnusableBodies(t *testing.T) {
	t.Parallel()
	cases := []ingress.Delivery{
		{Body: []byte(`{"ok":true}`)},
		{WebhookUID: "whk_01", Truncated: true, Body: []byte(`{"ok":true}`)},
		{WebhookUID: "whk_01", Body: []byte("   ")},
		{WebhookUID: "whk_01", Body: []byte("Hello, World!")},
	}
	for _, d := range cases {
		if _, err := Normalize(d); err == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
}

func triggerEvent(ev event.Event) trigger.Event {
	return trigger.Event{
		Provider:   ev.Provider,
		Event:      ev.Event,
		DeliveryID: ev.DeliveryID,
		OccurredAt: ev.OccurredAt,
		Thread:     trigger.Thread{Kind: ev.Thread.Kind, ExternalRef: ev.Thread.ExternalRef},
		Raw:        ev.Raw,
	}
}

func TestNormalizeErrorTextHasNoSecret(t *testing.T) {
	t.Parallel()
	const secret = "url-token-z9q.ABC_~"
	_, err := Normalize(ingress.Delivery{WebhookUID: "whk_01", Body: []byte(secret)})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err %v", err)
	}
}
