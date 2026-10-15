package cron

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	// DefaultCatchUp is the D8 window. A schedule whose latest due slot is
	// older than this does not fire.
	DefaultCatchUp = time.Hour

	// Provider is the normalized provider for schedule events.
	Provider = "schedule"
	// EventCronFired is the trigger event. It is batch priority.
	EventCronFired = "cron_fired"
	// EventScheduleMissed records a skipped window. It is not a trigger.
	EventScheduleMissed = "schedule_missed"
	// PriorityBatch is the scheduler class for scheduled work (docs/02).
	PriorityBatch = "batch"
)

// Schedule is one automation schedule row. T30 maps it onto the T17 store.
// Identity is (FactoryID, Automation, Name). Name may be empty for the one
// unnamed schedule an automation is allowed to have.
type Schedule struct {
	FactoryID   string
	Automation  string
	Name        string
	Cron        string
	LastFiredAt time.Time
	NextFireAt  time.Time
	Paused      bool
}

// ExternalRef is the schedule thread id T30 copies onto event.Thread.
func (s Schedule) ExternalRef() string {
	return Provider + ":" + s.FactoryID + "/" + s.Automation + "/" + s.Name
}

// Event is one ticker result. T30 maps cron_fired onto internal/event.Event
// and enqueues the run at Priority. schedule_missed is a record of a skipped
// window; Priority is empty so it is not enqueued as work.
type Event struct {
	Provider    string
	Event       string
	Priority    string
	FactoryID   string
	Automation  string
	Name        string
	ExternalRef string
	OccurredAt  time.Time
	DueAt       time.Time
}

// Sink receives ticker events. Emit runs before the row is advanced, so a
// crash in between can emit the same slot twice. It cannot emit one event
// per missed interval.
type Sink interface {
	Emit(ctx context.Context, e Event) error
}

// Store is the due-schedule half of the T17 schedule API. MarkFired persists
// LastFiredAt and NextFireAt as given. A miss leaves LastFiredAt unchanged
// and still advances NextFireAt, so a stale row is not retried on the next tick.
type Store interface {
	DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error)
	MarkFired(ctx context.Context, s Schedule) error
}

// Catalog is the definition-apply half of the T17 schedule API.
// UpsertSchedule writes a cron change in place. ReplaceSchedule deletes
// oldName and inserts the new row. DeleteSchedule drops a schedule that
// left the definition.
type Catalog interface {
	ListSchedules(ctx context.Context) ([]Schedule, error)
	UpsertSchedule(ctx context.Context, s Schedule) error
	ReplaceSchedule(ctx context.Context, oldName string, s Schedule) error
	DeleteSchedule(ctx context.Context, factoryID, automation, name string) error
}

// Desired is a schedule declared by an automation definition.
type Desired struct {
	FactoryID  string
	Automation string
	Name       string
	Cron       string
}

// ChangeKind is one reconciliation result.
type ChangeKind int

const (
	// ChangeUpsert inserts a schedule or updates its cron in place.
	ChangeUpsert ChangeKind = iota + 1
	// ChangeReplace deletes OldName and inserts Schedule. A single renamed
	// schedule uses this. The new row does not keep the old fire history.
	ChangeReplace
	// ChangeDelete removes Schedule. LastFiredAt and NextFireAt are zero.
	ChangeDelete
)

// Change is one row the catalog should write.
type Change struct {
	Kind     ChangeKind
	OldName  string
	Schedule Schedule
}

// Ticker evaluates due schedules. Now is not read from the wall clock; Tick
// takes the time so tests can jump it. CatchUp of zero uses DefaultCatchUp.
type Ticker struct {
	Store   Store
	Sink    Sink
	CatchUp time.Duration
}

// Tick fires or skips each due schedule at most once.
//
// A schedule is due when NextFireAt is at or before now. The slot that is
// judged against the catch-up window is the latest activation at or before
// now, not the oldest stored NextFireAt. Three missed hourly boundaries
// therefore become one cron_fired when the newest of them is still inside
// the window. NextFireAt becomes Next(now) either way.
func (t *Ticker) Tick(ctx context.Context, now time.Time) error {
	if t == nil || t.Store == nil || t.Sink == nil {
		return errors.New("cron: ticker store and sink are required")
	}
	if now.IsZero() {
		return errors.New("cron: tick time is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now = now.UTC()
	due, err := t.Store.DueSchedules(ctx, now)
	if err != nil {
		return err
	}
	window := t.CatchUp
	if window <= 0 {
		window = DefaultCatchUp
	}
	var errs []error
	for _, s := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Paused || s.NextFireAt.IsZero() || s.NextFireAt.After(now) {
			continue
		}
		if err := t.fireOne(ctx, s, now, window); err != nil {
			var soft exprError
			if errors.As(err, &soft) {
				errs = append(errs, err)
				continue
			}
			return err
		}
	}
	return errors.Join(errs...)
}

// exprError is a bad cron on one row. Tick keeps going so one definition
// cannot stall every other schedule. Store and sink errors are not soft.
type exprError struct{ err error }

func (e exprError) Error() string { return e.err.Error() }

func (e exprError) Unwrap() error { return e.err }

func (t *Ticker) fireOne(ctx context.Context, s Schedule, now time.Time, window time.Duration) error {
	expr, err := Parse(s.Cron)
	if err != nil {
		return exprError{fmt.Errorf("cron: schedule %s/%s/%s: %w", s.FactoryID, s.Automation, s.Name, err)}
	}
	slot, ok := expr.latest(s.NextFireAt.UTC(), now)
	if !ok {
		return exprError{fmt.Errorf("cron: schedule %s/%s/%s: no due slot", s.FactoryID, s.Automation, s.Name)}
	}
	next := expr.Next(now)
	if next.IsZero() {
		return exprError{fmt.Errorf("cron: schedule %s/%s/%s: no next fire", s.FactoryID, s.Automation, s.Name)}
	}
	ev := Event{
		Provider:    Provider,
		FactoryID:   s.FactoryID,
		Automation:  s.Automation,
		Name:        s.Name,
		ExternalRef: s.ExternalRef(),
		OccurredAt:  now,
		DueAt:       slot,
	}
	if !slot.Before(now.Add(-window)) {
		ev.Event = EventCronFired
		ev.Priority = PriorityBatch
		s.LastFiredAt = now
	} else {
		ev.Event = EventScheduleMissed
	}
	s.NextFireAt = next
	if err := t.Sink.Emit(ctx, ev); err != nil {
		return err
	}
	return t.Store.MarkFired(ctx, s)
}

// latest is the latest activation at or before limit, anchored at from.
// from itself counts. ok is false when from is zero, from is after limit,
// or the search cannot finish.
func (e Expr) latest(from, limit time.Time) (time.Time, bool) {
	if from.IsZero() || from.After(limit) {
		return time.Time{}, false
	}
	from = from.UTC()
	limit = limit.UTC()
	if e.every > 0 {
		return e.latestEvery(from, limit)
	}
	return e.latestCalendar(from, limit)
}

func (e Expr) latestEvery(from, limit time.Time) (time.Time, bool) {
	if !limit.After(from) {
		return from, true
	}
	// Whole-second delays stay on a fixed grid, so truncation in Next does
	// not shorten the step. Anything else walks Next from the anchor.
	if e.every%time.Second == 0 && from.Equal(from.Truncate(time.Second)) {
		n := limit.Sub(from) / e.every
		latest := from.Add(n * e.every)
		if latest.After(limit) {
			latest = from.Add((n - 1) * e.every)
		}
		if latest.Before(from) {
			return from, true
		}
		return latest, true
	}
	latest := from
	for i := 0; i < 1_000_000; i++ {
		nxt := e.Next(latest)
		if nxt.IsZero() || nxt.After(limit) {
			return latest, true
		}
		latest = nxt
	}
	return time.Time{}, false
}

func (e Expr) latestCalendar(from, limit time.Time) (time.Time, bool) {
	best := from
	lo := from
	hi := limit
	for i := 0; i < 80 && lo.Before(hi); i++ {
		span := hi.Sub(lo)
		if span <= time.Nanosecond {
			break
		}
		mid := lo.Add(span / 2)
		nxt := e.Next(mid)
		if nxt.IsZero() || nxt.After(limit) {
			hi = mid
			continue
		}
		if nxt.After(best) {
			best = nxt
			lo = nxt
			continue
		}
		if mid.After(lo) {
			lo = mid
			continue
		}
		break
	}
	for i := 0; i < 64; i++ {
		nxt := e.Next(best)
		if nxt.IsZero() || nxt.After(limit) {
			return best, true
		}
		best = nxt
	}
	return time.Time{}, false
}

// Reconcile diffs stored rows against the definition.
// A cron change on the same name updates that row in place and keeps
// LastFiredAt and Paused. A single removed name paired with a single added
// name in the same automation is a replace. Any other add or drop is an
// insert or a delete. An unchanged row with a next fire time is left alone.
func Reconcile(existing []Schedule, desired []Desired, now time.Time) ([]Change, error) {
	if now.IsZero() {
		return nil, errors.New("cron: reconcile time is required")
	}
	now = now.UTC()
	type autoKey struct {
		factory    string
		automation string
	}
	parsed := make(map[scheduleID]Expr, len(desired))
	seen := make(map[scheduleID]struct{}, len(desired))
	wantByAuto := make(map[autoKey][]Desired)
	for _, d := range desired {
		if d.FactoryID == "" || d.Automation == "" {
			return nil, errors.New("cron: factory and automation are required")
		}
		id := scheduleID{d.FactoryID, d.Automation, d.Name}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("cron: duplicate schedule %s/%s/%q", d.FactoryID, d.Automation, d.Name)
		}
		seen[id] = struct{}{}
		expr, err := Parse(d.Cron)
		if err != nil {
			return nil, err
		}
		if expr.Next(now).IsZero() {
			return nil, fmt.Errorf("cron: %s: no next fire", d.Cron)
		}
		parsed[id] = expr
		ak := autoKey{d.FactoryID, d.Automation}
		wantByAuto[ak] = append(wantByAuto[ak], d)
	}
	haveByAuto := make(map[autoKey][]Schedule)
	for _, row := range existing {
		ak := autoKey{row.FactoryID, row.Automation}
		haveByAuto[ak] = append(haveByAuto[ak], row)
	}
	keys := make([]autoKey, 0, len(wantByAuto)+len(haveByAuto))
	seenAuto := make(map[autoKey]struct{}, len(wantByAuto)+len(haveByAuto))
	for ak := range wantByAuto {
		seenAuto[ak] = struct{}{}
		keys = append(keys, ak)
	}
	for ak := range haveByAuto {
		if _, ok := seenAuto[ak]; ok {
			continue
		}
		keys = append(keys, ak)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].factory != keys[j].factory {
			return keys[i].factory < keys[j].factory
		}
		return keys[i].automation < keys[j].automation
	})
	var changes []Change
	for _, ak := range keys {
		part, err := reconcileAuto(haveByAuto[ak], wantByAuto[ak], parsed, now)
		if err != nil {
			return nil, err
		}
		changes = append(changes, part...)
	}
	return changes, nil
}

type scheduleID struct {
	factory    string
	automation string
	name       string
}

func reconcileAuto(have []Schedule, want []Desired, parsed map[scheduleID]Expr, now time.Time) ([]Change, error) {
	haveName := make(map[string]Schedule, len(have))
	for _, row := range have {
		haveName[row.Name] = row
	}
	wantName := make(map[string]Desired, len(want))
	for _, d := range want {
		wantName[d.Name] = d
	}
	var removed, added, shared []string
	for name := range haveName {
		if _, ok := wantName[name]; !ok {
			removed = append(removed, name)
		}
	}
	for name := range wantName {
		if _, ok := haveName[name]; ok {
			shared = append(shared, name)
			continue
		}
		added = append(added, name)
	}
	sort.Strings(removed)
	sort.Strings(added)
	sort.Strings(shared)
	var changes []Change
	switch {
	case len(removed) == 1 && len(added) == 1:
		old := haveName[removed[0]]
		d := wantName[added[0]]
		row, err := freshSchedule(d, parsed, now)
		if err != nil {
			return nil, err
		}
		changes = append(changes, Change{Kind: ChangeReplace, OldName: old.Name, Schedule: row})
	default:
		for _, name := range removed {
			row := haveName[name]
			changes = append(changes, Change{Kind: ChangeDelete, Schedule: Schedule{
				FactoryID:  row.FactoryID,
				Automation: row.Automation,
				Name:       row.Name,
			}})
		}
		for _, name := range added {
			row, err := freshSchedule(wantName[name], parsed, now)
			if err != nil {
				return nil, err
			}
			changes = append(changes, Change{Kind: ChangeUpsert, Schedule: row})
		}
	}
	for _, name := range shared {
		d := wantName[name]
		row := haveName[name]
		if row.Cron == d.Cron && !row.NextFireAt.IsZero() {
			continue
		}
		id := scheduleID{d.FactoryID, d.Automation, d.Name}
		expr, ok := parsed[id]
		if !ok {
			return nil, fmt.Errorf("cron: missing parsed schedule %s/%s/%q", d.FactoryID, d.Automation, d.Name)
		}
		next := expr.Next(now)
		if next.IsZero() {
			return nil, fmt.Errorf("cron: %s: no next fire", d.Cron)
		}
		row.Cron = d.Cron
		row.NextFireAt = next
		changes = append(changes, Change{Kind: ChangeUpsert, Schedule: row})
	}
	return changes, nil
}

func freshSchedule(d Desired, parsed map[scheduleID]Expr, now time.Time) (Schedule, error) {
	id := scheduleID{d.FactoryID, d.Automation, d.Name}
	expr, ok := parsed[id]
	if !ok {
		return Schedule{}, fmt.Errorf("cron: missing parsed schedule %s/%s/%q", d.FactoryID, d.Automation, d.Name)
	}
	next := expr.Next(now)
	if next.IsZero() {
		return Schedule{}, fmt.Errorf("cron: %s: no next fire", d.Cron)
	}
	return Schedule{
		FactoryID:  d.FactoryID,
		Automation: d.Automation,
		Name:       d.Name,
		Cron:       d.Cron,
		NextFireAt: next,
	}, nil
}

// Apply lists the catalog, reconciles it with desired, and writes the diff.
// A parse error writes nothing.
func Apply(ctx context.Context, cat Catalog, desired []Desired, now time.Time) error {
	if cat == nil {
		return errors.New("cron: catalog is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	existing, err := cat.ListSchedules(ctx)
	if err != nil {
		return err
	}
	changes, err := Reconcile(existing, desired, now)
	if err != nil {
		return err
	}
	for _, c := range changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch c.Kind {
		case ChangeUpsert:
			err = cat.UpsertSchedule(ctx, c.Schedule)
		case ChangeReplace:
			err = cat.ReplaceSchedule(ctx, c.OldName, c.Schedule)
		case ChangeDelete:
			err = cat.DeleteSchedule(ctx, c.Schedule.FactoryID, c.Schedule.Automation, c.Schedule.Name)
		default:
			err = fmt.Errorf("cron: unknown change %d", int(c.Kind))
		}
		if err != nil {
			return err
		}
	}
	return nil
}
