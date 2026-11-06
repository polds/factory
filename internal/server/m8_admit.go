package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/cron"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

const (
	occurrenceLegacy = "legacy_continue"
	occurrenceFresh  = "fresh"
)

func normalizeOccurrenceMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" || mode == occurrenceLegacy {
		return occurrenceLegacy
	}
	return mode
}

func (s *Server) dispatchAutomationEvent(ctx context.Context, ev event.Event) (automation.Result, error) {
	eng := s.integ.engine
	if eng == nil {
		return automation.Result{}, errors.New("server: automation engine is not configured")
	}
	preview, err := eng.Preview(ctx, ev)
	if err != nil {
		return automation.Result{}, err
	}
	if preview.Continuation {
		return eng.Handle(ctx, ev)
	}
	if len(preview.Matches) == 0 {
		return eng.Handle(ctx, ev)
	}
	auto := preview.FirstAutomation()
	if auto.Name == "" {
		return eng.Handle(ctx, ev)
	}
	if normalizeOccurrenceMode(auto.OccurrenceMode) != occurrenceFresh {
		return eng.Handle(ctx, ev)
	}
	if err := s.admitIngressInvocation(ctx, ev, auto); err != nil {
		return automation.Result{}, err
	}
	return automation.Result{
		Matched: true,
		Outcomes: []automation.Outcome{{
			Kind:       "invocation",
			Automation: auto.Name,
		}},
	}, nil
}

func (s *Server) admitIngressInvocation(ctx context.Context, ev event.Event, auto definition.Automation) error {
	fac := s.snapshot().factory
	if fac.Name == "" {
		return errors.New("server: factory is not applied")
	}
	key := ingressOccurrenceKey(ev)
	req := invocation.Request{
		SourceOccurrenceKey: key,
		Intent:              "new",
		Priority:            "interactive",
		Profile:             invocation.Profile{Name: profileForAutomation(auto)},
		Inputs:              map[string]any{"instruction": eventFence(ev)},
	}
	if ev.Thread.ExternalRef != "" {
		req.Correlation = &invocation.Correlation{ThreadRef: ev.Thread.ExternalRef}
	}
	principal := ingressPrincipal(ev)
	_, err := s.AdmitInvocation(ctx, fac.Name, principal, req)
	return err
}

func ingressOccurrenceKey(ev event.Event) string {
	delivery := strings.TrimSpace(ev.DeliveryID)
	if delivery == "" {
		delivery = "unknown"
	}
	return fmt.Sprintf("ingress:%s:%s", ev.Provider, delivery)
}

func ingressPrincipal(ev event.Event) invocation.Principal {
	kind := strings.TrimSpace(ev.Provider)
	actor := strings.TrimSpace(ev.Actor.Handle)
	if actor == "" {
		actor = strings.TrimSpace(ev.Actor.ProviderID)
	}
	return invocation.Principal{Kind: kind, Actor: actor, Allow: actor != ""}
}

func profileForAutomation(auto definition.Automation) string {
	if name := strings.TrimSpace(auto.Agent); name != "" {
		return name
	}
	return auto.Name
}

func (c cronDB) emitSchedule(ctx context.Context, ev cron.Event) error {
	if ev.Event != cron.EventCronFired {
		c.s.log.Info("schedule missed",
			"factory", ev.FactoryID,
			"automation", ev.Automation,
			"name", ev.Name,
			"due", ev.DueAt.UTC().Format(time.RFC3339Nano),
		)
		return nil
	}
	row, err := c.find(ctx, ev.FactoryID, ev.Automation, ev.Name)
	if err != nil {
		return err
	}
	mode := scheduleOccurrenceMode(c.s.snapshot().tree, ev.Automation, ev.Name)
	if normalizeOccurrenceMode(mode) == occurrenceFresh {
		return c.emitFreshSchedule(ctx, ev, row)
	}
	return c.emitLegacySchedule(ctx, ev)
}

func scheduleOccurrenceMode(tree *definition.Tree, automationName, scheduleName string) string {
	if tree == nil {
		return occurrenceLegacy
	}
	auto, ok := tree.Automations[automationName]
	if !ok {
		return occurrenceLegacy
	}
	if normalizeOccurrenceMode(auto.OccurrenceMode) == occurrenceFresh {
		return occurrenceFresh
	}
	for _, trig := range auto.Triggers {
		if trig.Schedule == nil {
			continue
		}
		if scheduleName != "" && trig.Schedule.Name != scheduleName {
			continue
		}
		if mode := normalizeOccurrenceMode(trig.Schedule.OccurrenceMode); mode != occurrenceLegacy {
			return mode
		}
	}
	return occurrenceLegacy
}

func scheduleOverlapPolicy(tree *definition.Tree, automationName, scheduleName string) string {
	if tree == nil {
		return ""
	}
	auto, ok := tree.Automations[automationName]
	if !ok {
		return ""
	}
	for _, trig := range auto.Triggers {
		if trig.Schedule == nil {
			continue
		}
		if scheduleName != "" && trig.Schedule.Name != scheduleName {
			continue
		}
		return strings.TrimSpace(trig.Schedule.OverlapPolicy)
	}
	return ""
}

func (c cronDB) emitFreshSchedule(ctx context.Context, ev cron.Event, row store.Schedule) error {
	fac := c.s.snapshot().factory
	if fac.Name == "" {
		return errors.New("server: factory is not applied")
	}
	auto, ok := c.s.snapshot().tree.Automations[ev.Automation]
	if !ok {
		return fmt.Errorf("server: unknown automation %q", ev.Automation)
	}
	dueSlot := ev.DueAt.UTC().Format(time.RFC3339Nano)
	key := invocation.ScheduleOccurrenceKey(row.ID, dueSlot)
	req := invocation.Request{
		SourceOccurrenceKey: key,
		Intent:              "new",
		Priority:            "batch",
		Profile:             invocation.Profile{Name: profileForAutomation(auto)},
		ScheduleID:          row.ID,
		DueSlot:             dueSlot,
		OverlapPolicy:       scheduleOverlapPolicy(c.s.snapshot().tree, ev.Automation, ev.Name),
	}
	_, err := c.s.AdmitInvocation(ctx, fac.Name, invocation.Principal{
		Kind:  "schedule",
		Actor: "schedule",
		Allow: true,
	}, req)
	return err
}

func (c cronDB) emitLegacySchedule(ctx context.Context, ev cron.Event) error {
	if c.s.integ.engine == nil {
		return nil
	}
	occurred := ev.OccurredAt
	if occurred.IsZero() {
		occurred = c.s.now()
	}
	canonical := event.Event{
		Provider:   event.ProviderSchedule,
		Event:      cron.EventCronFired,
		DeliveryID: ev.ExternalRef + ":" + ev.DueAt.UTC().Format(time.RFC3339Nano),
		OccurredAt: occurred.UTC(),
		Actor:      event.Actor{ProviderID: "schedule", Handle: "schedule"},
		Subject:    event.Subject{Kind: "schedule", Title: ev.Automation},
		Thread:     event.Thread{Kind: event.ThreadSchedule, ExternalRef: ev.ExternalRef},
	}
	c.s.integ.mu.Lock()
	res, err := c.s.integ.engine.Handle(ctx, canonical)
	c.s.integ.mu.Unlock()
	if err != nil {
		return err
	}
	c.s.recordOutcomes(ctx, res)
	c.s.notePaused(ctx, res)
	return nil
}

// eventFence copies automation.eventFence for ingress bodies without importing unexported helpers.
func eventFence(ev event.Event) string {
	if strings.TrimSpace(ev.Subject.Text) != "" {
		return ev.Subject.Text
	}
	return ev.Subject.Title
}
