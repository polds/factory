package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/polds/factory/internal/automation"
	"github.com/polds/factory/internal/cron"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/event"
	"github.com/polds/factory/internal/store"
)

func (c cronDB) tick(ctx context.Context, now time.Time) error {
	ticker := &cron.Ticker{
		Store:   c,
		Sink:    c,
		CatchUp: c.s.cfg.ScheduleCatchUp,
	}
	return ticker.Tick(ctx, now)
}

// cronDB adapts the store schedule API to cron.Store and cron.Catalog.
// A missed slot whose last_fired_at is still null cannot use store.MarkFired:
// that method rejects a zero timestamp. The adapter advances next_fire_at only.
type cronDB struct{ s *Server }

func (c cronDB) DueSchedules(ctx context.Context, now time.Time) ([]cron.Schedule, error) {
	rows, err := c.s.store.DueSchedules(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]cron.Schedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCronSchedule(row))
	}
	return out, nil
}

func (c cronDB) MarkFired(ctx context.Context, sch cron.Schedule) error {
	row, err := c.find(ctx, sch.FactoryID, sch.Automation, sch.Name)
	if err != nil {
		return err
	}
	if sch.LastFiredAt.IsZero() {
		tag, err := c.s.db.Exec(ctx, `UPDATE schedules SET next_fire_at = $2 WHERE id = $1`, row.ID, sch.NextFireAt)
		if err != nil {
			return fmt.Errorf("server: schedule next fire: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	}
	return c.s.store.MarkFired(ctx, row.ID, sch.LastFiredAt, sch.NextFireAt)
}

func (c cronDB) Emit(ctx context.Context, ev cron.Event) error {
	if ev.Event != cron.EventCronFired {
		c.s.log.Info("schedule missed",
			"factory", ev.FactoryID,
			"automation", ev.Automation,
			"name", ev.Name,
			"due", ev.DueAt.UTC().Format(time.RFC3339Nano),
		)
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
	if c.s.integ.engine == nil {
		return nil
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

func (c cronDB) ListSchedules(ctx context.Context) ([]cron.Schedule, error) {
	fac := c.s.snapshot().factory
	if fac.ID == "" {
		return nil, nil
	}
	rows, err := c.s.store.ListSchedules(ctx, fac.ID)
	if err != nil {
		return nil, err
	}
	out := make([]cron.Schedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCronSchedule(row))
	}
	return out, nil
}

func (c cronDB) UpsertSchedule(ctx context.Context, sch cron.Schedule) error {
	_, err := c.s.store.UpsertSchedule(ctx, toStoreSchedule(sch))
	return err
}

func (c cronDB) ReplaceSchedule(ctx context.Context, oldName string, sch cron.Schedule) error {
	_, err := c.s.store.ReplaceSchedule(ctx, oldName, toStoreSchedule(sch))
	return err
}

func (c cronDB) DeleteSchedule(ctx context.Context, factoryID, automationName, name string) error {
	return c.s.store.DeleteSchedule(ctx, factoryID, automationName, name)
}

func (c cronDB) find(ctx context.Context, factoryID, automationName, name string) (store.Schedule, error) {
	rows, err := c.s.store.ListSchedules(ctx, factoryID)
	if err != nil {
		return store.Schedule{}, err
	}
	for _, row := range rows {
		if row.Automation == automationName && row.Name == name {
			return row, nil
		}
	}
	return store.Schedule{}, store.ErrNotFound
}

func toCronSchedule(row store.Schedule) cron.Schedule {
	sch := cron.Schedule{
		FactoryID:  row.FactoryID,
		Automation: row.Automation,
		Name:       row.Name,
		Cron:       row.Cron,
		NextFireAt: row.NextFireAt,
		Paused:     row.Paused,
	}
	if row.LastFiredAt != nil {
		sch.LastFiredAt = *row.LastFiredAt
	}
	return sch
}

func toStoreSchedule(sch cron.Schedule) store.Schedule {
	row := store.Schedule{
		FactoryID:  sch.FactoryID,
		Automation: sch.Automation,
		Name:       sch.Name,
		Cron:       sch.Cron,
		NextFireAt: sch.NextFireAt,
		Paused:     sch.Paused,
	}
	if !sch.LastFiredAt.IsZero() {
		fired := sch.LastFiredAt
		row.LastFiredAt = &fired
	}
	return row
}

func (s *Server) syncDefinition(ctx context.Context, tree *definition.Tree) error {
	if tree == nil {
		return nil
	}
	fac := s.snapshot().factory
	s.integ.mu.Lock()
	if s.integ.engine != nil {
		s.integ.engine.FactoryID = fac.ID
		s.integ.engine.Allow = s.allowFor(tree)
	}
	s.integ.mu.Unlock()
	if fac.ID == "" {
		return nil
	}
	if err := cron.Apply(ctx, cronDB{s}, desiredSchedules(tree, fac.ID), s.now()); err != nil {
		return err
	}
	return s.syncWebhooks(ctx, tree, fac.ID)
}

func (s *Server) allowFor(tree *definition.Tree) automation.Allow {
	allow := automation.Allow{}
	if tree != nil {
		for _, integ := range tree.Factory.Integrations {
			if integ.Slack == nil {
				continue
			}
			allow.SlackUserIDs = append([]string(nil), integ.Slack.AllowedUserIDs...)
			break
		}
	}
	if s.integ.githubApp != nil {
		allow.GitHub = s.githubCollaborator
	}
	return allow
}

func desiredSchedules(tree *definition.Tree, factoryID string) []cron.Desired {
	names := make([]string, 0, len(tree.Automations))
	for name := range tree.Automations {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []cron.Desired
	for _, name := range names {
		auto := tree.Automations[name]
		if !auto.Enabled {
			continue
		}
		autoName := auto.Name
		if autoName == "" {
			autoName = name
		}
		for _, trigger := range auto.Triggers {
			if trigger.Schedule == nil || strings.TrimSpace(trigger.Schedule.Cron) == "" {
				continue
			}
			out = append(out, cron.Desired{
				FactoryID:  factoryID,
				Automation: autoName,
				Name:       trigger.Schedule.Name,
				Cron:       trigger.Schedule.Cron,
			})
		}
	}
	return out
}

func (s *Server) syncWebhooks(ctx context.Context, tree *definition.Tree, factoryID string) error {
	names := make([]string, 0, len(tree.Webhooks))
	for name := range tree.Webhooks {
		names = append(names, name)
	}
	sort.Strings(names)
	hooks := make([]definition.Webhook, 0, len(names))
	for _, name := range names {
		hook := tree.Webhooks[name]
		if hook.Name == "" {
			hook.Name = name
		}
		hooks = append(hooks, hook)
	}
	return applyWebhooks(ctx, s, factoryID, hooks)
}

var (
	_ cron.Store   = cronDB{}
	_ cron.Sink    = cronDB{}
	_ cron.Catalog = cronDB{}
)
