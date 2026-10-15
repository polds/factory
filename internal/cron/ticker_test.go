package cron

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRestartAfterThreeMissedHourlyFiresOnce(t *testing.T) {
	// Stored next fire is 10:00. The process comes back at 12:30, so the
	// 10:00, 11:00, and 12:00 slots were all missed. 12:00 is inside the
	// default 1h window, so the restart fires that slot once and does not
	// replay the older two.
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	now := start.Add(2*time.Hour + 30*time.Minute)
	store := newMemSched(Schedule{
		FactoryID:  "fac",
		Automation: "audit",
		Name:       "hourly",
		Cron:       "@hourly",
		NextFireAt: start,
	})
	sink := &captureSink{}
	tk := &Ticker{Store: store, Sink: sink}
	if err := tk.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%d %+v", len(got), got)
	}
	ev := got[0]
	if ev.Provider != Provider || ev.Event != EventCronFired || ev.Priority != PriorityBatch {
		t.Fatalf("event %+v", ev)
	}
	if ev.FactoryID != "fac" || ev.Automation != "audit" || ev.Name != "hourly" {
		t.Fatalf("identity %+v", ev)
	}
	if ev.ExternalRef != "schedule:fac/audit/hourly" {
		t.Fatalf("ref %s", ev.ExternalRef)
	}
	due := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if !ev.DueAt.Equal(due) || !ev.OccurredAt.Equal(now) {
		t.Fatalf("due=%s at=%s", ev.DueAt, ev.OccurredAt)
	}
	saved := store.must("hourly")
	next := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	if !saved.NextFireAt.Equal(next) {
		t.Fatalf("next=%s", saved.NextFireAt)
	}
	if !saved.LastFiredAt.Equal(now) {
		t.Fatalf("last=%s", saved.LastFiredAt)
	}
	if err := tk.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.snapshot()); n != 1 {
		t.Fatalf("second tick emitted %d", n)
	}
}

func TestCatchUpOutsideWindowRecordsMiss(t *testing.T) {
	// Daily slot at 00:00. At 02:00 the latest slot is two hours old, outside
	// the 1h window. One schedule_missed, no cron_fired, next advances.
	due := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	store := newMemSched(Schedule{
		FactoryID:   "fac",
		Automation:  "audit",
		Name:        "nightly",
		Cron:        "@daily",
		NextFireAt:  due,
		LastFiredAt: due.Add(-24 * time.Hour),
	})
	sink := &captureSink{}
	tk := &Ticker{Store: store, Sink: sink}
	if err := tk.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("events=%d %+v", len(got), got)
	}
	if got[0].Event != EventScheduleMissed || got[0].Priority != "" {
		t.Fatalf("event %+v", got[0])
	}
	latest := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if !got[0].DueAt.Equal(latest) {
		t.Fatalf("due=%s", got[0].DueAt)
	}
	saved := store.must("nightly")
	wantNext := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	if !saved.NextFireAt.Equal(wantNext) {
		t.Fatalf("next=%s", saved.NextFireAt)
	}
	if !saved.LastFiredAt.Equal(due.Add(-24 * time.Hour)) {
		t.Fatalf("last fired changed to %s", saved.LastFiredAt)
	}
}

func TestCatchUpBoundary(t *testing.T) {
	due := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	store := newMemSched(Schedule{
		FactoryID:  "fac",
		Automation: "audit",
		Name:       "nightly",
		Cron:       "@daily",
		NextFireAt: due,
	})
	sink := &captureSink{}
	tk := &Ticker{Store: store, Sink: sink}
	now := due.Add(DefaultCatchUp)
	if err := tk.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got := sink.snapshot(); len(got) != 1 || got[0].Event != EventCronFired {
		t.Fatalf("exact window %+v", got)
	}

	store.setNext("nightly", due)
	sink.reset()
	now = due.Add(DefaultCatchUp + time.Nanosecond)
	if err := tk.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	got := sink.snapshot()
	if len(got) != 1 || got[0].Event != EventScheduleMissed {
		t.Fatalf("past window %+v", got)
	}
}

func TestTickSkipsPausedAndFuture(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	store := newMemSched(
		Schedule{
			FactoryID: "fac", Automation: "a", Name: "paused", Cron: "@hourly",
			NextFireAt: now.Add(-time.Minute), Paused: true,
		},
		Schedule{
			FactoryID: "fac", Automation: "a", Name: "future", Cron: "@hourly",
			NextFireAt: now.Add(time.Hour),
		},
	)
	sink := &captureSink{}
	tk := &Ticker{Store: store, Sink: sink}
	if err := tk.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if n := len(sink.snapshot()); n != 0 {
		t.Fatalf("events %d", n)
	}
}

func TestTickRejectsImpossibleCron(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	store := newMemSched(Schedule{
		FactoryID: "fac", Automation: "a", Name: "bad", Cron: "0 0 30 2 *",
		NextFireAt: now.Add(-time.Minute),
	})
	sink := &captureSink{}
	tk := &Ticker{Store: store, Sink: sink}
	if err := tk.Tick(context.Background(), now); err == nil {
		t.Fatal("expected error")
	}
	if n := len(sink.snapshot()); n != 0 {
		t.Fatalf("emitted %d", n)
	}
}

func TestReconcileCronChangeUpdatesInPlace(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	last := now.Add(-48 * time.Hour)
	existing := []Schedule{{
		FactoryID: "fac", Automation: "audit", Name: "weekly",
		Cron: "0 6 * * 1", LastFiredAt: last, NextFireAt: now.Add(time.Hour), Paused: true,
	}}
	changes, err := Reconcile(existing, []Desired{{
		FactoryID: "fac", Automation: "audit", Name: "weekly", Cron: "0 7 * * 1",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeUpsert {
		t.Fatalf("%+v", changes)
	}
	got := changes[0].Schedule
	if got.Name != "weekly" || got.Cron != "0 7 * * 1" || !got.Paused || !got.LastFiredAt.Equal(last) {
		t.Fatalf("row %+v", got)
	}
	wantNext := time.Date(2026, 10, 19, 7, 0, 0, 0, time.UTC)
	if !got.NextFireAt.Equal(wantNext) {
		t.Fatalf("next=%s", got.NextFireAt)
	}
}

func TestReconcileNameChangeReplaces(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	existing := []Schedule{{
		FactoryID: "fac", Automation: "audit", Name: "weekly",
		Cron: "0 6 * * 1", LastFiredAt: now.Add(-time.Hour), NextFireAt: now.Add(time.Hour), Paused: true,
	}}
	changes, err := Reconcile(existing, []Desired{{
		FactoryID: "fac", Automation: "audit", Name: "nightly", Cron: "0 6 * * 1",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeReplace || changes[0].OldName != "weekly" {
		t.Fatalf("%+v", changes)
	}
	got := changes[0].Schedule
	if got.Name != "nightly" || got.Paused || !got.LastFiredAt.IsZero() {
		t.Fatalf("row %+v", got)
	}
	wantNext := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	if !got.NextFireAt.Equal(wantNext) {
		t.Fatalf("next=%s", got.NextFireAt)
	}
}

func TestReconcileUnchangedAndDelete(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	next := now.Add(3 * time.Hour)
	existing := []Schedule{
		{FactoryID: "fac", Automation: "audit", Name: "keep", Cron: "@daily", NextFireAt: next},
		{FactoryID: "fac", Automation: "audit", Name: "drop", Cron: "@hourly", NextFireAt: next},
	}
	changes, err := Reconcile(existing, []Desired{{
		FactoryID: "fac", Automation: "audit", Name: "keep", Cron: "@daily",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeDelete || changes[0].Schedule.Name != "drop" {
		t.Fatalf("%+v", changes)
	}
}

func TestReconcileAmbiguousRenameInsertsAndDeletes(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	existing := []Schedule{
		{FactoryID: "fac", Automation: "audit", Name: "a", Cron: "@daily", NextFireAt: now},
		{FactoryID: "fac", Automation: "audit", Name: "b", Cron: "@daily", NextFireAt: now},
	}
	changes, err := Reconcile(existing, []Desired{
		{FactoryID: "fac", Automation: "audit", Name: "c", Cron: "@daily"},
		{FactoryID: "fac", Automation: "audit", Name: "d", Cron: "@hourly"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	var replaces, deletes, upserts int
	for _, c := range changes {
		switch c.Kind {
		case ChangeReplace:
			replaces++
		case ChangeDelete:
			deletes++
		case ChangeUpsert:
			upserts++
		default:
			t.Fatalf("kind %d", c.Kind)
		}
	}
	if replaces != 0 || deletes != 2 || upserts != 2 {
		t.Fatalf("replace=%d delete=%d upsert=%d", replaces, deletes, upserts)
	}
}

func TestApplyRejectsBadCronWithoutWrites(t *testing.T) {
	cat := &memCatalog{}
	err := Apply(context.Background(), cat, []Desired{{
		FactoryID: "fac", Automation: "audit", Name: "weekly", Cron: "nope",
	}}, time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected error")
	}
	if cat.writes != 0 {
		t.Fatalf("writes %d", cat.writes)
	}
}

func TestApplyNameAndCron(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cat := &memCatalog{rows: []Schedule{{
		FactoryID: "fac", Automation: "audit", Name: "weekly",
		Cron: "0 6 * * 1", NextFireAt: now.Add(time.Hour), LastFiredAt: now.Add(-time.Hour),
	}}}
	err := Apply(context.Background(), cat, []Desired{{
		FactoryID: "fac", Automation: "audit", Name: "weekly", Cron: "0 7 * * 1",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if cat.upserts != 1 || cat.replaces != 0 {
		t.Fatalf("upserts=%d replaces=%d", cat.upserts, cat.replaces)
	}
	got := cat.rows[0]
	if got.Cron != "0 7 * * 1" || got.Name != "weekly" {
		t.Fatalf("%+v", got)
	}

	err = Apply(context.Background(), cat, []Desired{{
		FactoryID: "fac", Automation: "audit", Name: "nightly", Cron: "@daily",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if cat.replaces != 1 {
		t.Fatalf("replaces=%d", cat.replaces)
	}
	if len(cat.rows) != 1 || cat.rows[0].Name != "nightly" {
		t.Fatalf("%+v", cat.rows)
	}
}

type captureSink struct {
	mu     sync.Mutex
	events []Event
}

func (c *captureSink) Emit(_ context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func (c *captureSink) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

func (c *captureSink) reset() {
	c.mu.Lock()
	c.events = nil
	c.mu.Unlock()
}

type memSched struct {
	mu   sync.Mutex
	rows []Schedule
}

func newMemSched(rows ...Schedule) *memSched {
	cp := make([]Schedule, len(rows))
	copy(cp, rows)
	return &memSched{rows: cp}
}

func (m *memSched) DueSchedules(_ context.Context, now time.Time) ([]Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Schedule
	for _, row := range m.rows {
		if row.Paused || row.NextFireAt.IsZero() || row.NextFireAt.After(now) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (m *memSched) MarkFired(_ context.Context, s Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].FactoryID == s.FactoryID && m.rows[i].Automation == s.Automation && m.rows[i].Name == s.Name {
			m.rows[i] = s
			return nil
		}
	}
	return errMissing
}

func (m *memSched) must(name string) Schedule {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.rows {
		if row.Name == name {
			return row
		}
	}
	panic("missing " + name)
}

func (m *memSched) setNext(name string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].Name == name {
			m.rows[i].NextFireAt = at
			m.rows[i].LastFiredAt = time.Time{}
		}
	}
}

type memCatalog struct {
	mu       sync.Mutex
	rows     []Schedule
	upserts  int
	replaces int
	deletes  int
	writes   int
}

func (m *memCatalog) ListSchedules(context.Context) ([]Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Schedule, len(m.rows))
	copy(out, m.rows)
	return out, nil
}

func (m *memCatalog) UpsertSchedule(_ context.Context, s Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	m.upserts++
	for i := range m.rows {
		if m.rows[i].FactoryID == s.FactoryID && m.rows[i].Automation == s.Automation && m.rows[i].Name == s.Name {
			m.rows[i] = s
			return nil
		}
	}
	m.rows = append(m.rows, s)
	return nil
}

func (m *memCatalog) ReplaceSchedule(_ context.Context, oldName string, s Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	m.replaces++
	kept := make([]Schedule, 0, len(m.rows))
	for _, row := range m.rows {
		if row.FactoryID == s.FactoryID && row.Automation == s.Automation && row.Name == oldName {
			continue
		}
		kept = append(kept, row)
	}
	m.rows = append(kept, s)
	return nil
}

func (m *memCatalog) DeleteSchedule(_ context.Context, factoryID, automation, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	m.deletes++
	kept := make([]Schedule, 0, len(m.rows))
	for _, row := range m.rows {
		if row.FactoryID == factoryID && row.Automation == automation && row.Name == name {
			continue
		}
		kept = append(kept, row)
	}
	m.rows = kept
	return nil
}

var errMissing = errString("cron: schedule not found")

type errString string

func (e errString) Error() string { return string(e) }
