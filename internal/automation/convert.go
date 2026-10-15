package automation

import (
	"encoding/json"

	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/trigger"
)

// toTrigger copies a canonical event into the matcher input.
// internal/trigger still carries its own event shape; this is the only
// conversion, not a third copy of the schema.
func toTrigger(ev event.Event) trigger.Event {
	return trigger.Event{
		Provider:   ev.Provider,
		Event:      ev.Event,
		DeliveryID: ev.DeliveryID,
		OccurredAt: ev.OccurredAt,
		Actor: trigger.Actor{
			ProviderID:    ev.Actor.ProviderID,
			Handle:        ev.Actor.Handle,
			FactoryUserID: ev.Actor.FactoryUserID,
		},
		Subject: trigger.Subject{
			Kind:       ev.Subject.Kind,
			ID:         ev.Subject.ID,
			Number:     ev.Subject.Number,
			Title:      ev.Subject.Title,
			URL:        ev.Subject.URL,
			Repo:       trigger.Repo{CodeForge: ev.Subject.Repo.CodeForge, Owner: ev.Subject.Repo.Owner, Name: ev.Subject.Repo.Name},
			Labels:     copyStrings(ev.Subject.Labels),
			Branch:     deref(ev.Subject.Branch),
			BaseBranch: deref(ev.Subject.BaseBranch),
			Paths:      copyStrings(ev.Subject.Paths),
			Text:       ev.Subject.Text,
		},
		Thread: trigger.Thread{Kind: ev.Thread.Kind, ExternalRef: ev.Thread.ExternalRef},
		Raw:    copyRaw(ev.Raw),
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func copyRaw(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage(nil), in...)
}
