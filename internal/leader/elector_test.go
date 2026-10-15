package leader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polds/factory/internal/cron"
)

func TestConcurrentAcquireOneWinner(t *testing.T) {
	store := newMemLease()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	const n = 8
	for i := 0; i < n; i++ {
		holder := "h" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			e := &Elector{
				Store:  store,
				Name:   "cron",
				Holder: holder,
				Now:    func() time.Time { return now },
			}
			ok, err := e.TickOnce(context.Background())
			if err != nil {
				t.Errorf("acquire %s: %v", holder, err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("winners=%d", wins.Load())
	}
}

func TestLeaseStealAfterExpiry(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	now := start
	store := newMemLease()
	ticks := 0
	a := &Elector{
		Store:  store,
		Name:   "cron",
		Holder: "a",
		Now:    func() time.Time { return now },
		Tick: func(context.Context, time.Time) error {
			ticks++
			return nil
		},
	}
	b := &Elector{
		Store:  store,
		Name:   "cron",
		Holder: "b",
		Now:    func() time.Time { return now },
	}
	ok, err := a.TickOnce(context.Background())
	if err != nil || !ok || ticks != 1 {
		t.Fatalf("first ok=%v err=%v ticks=%d", ok, err, ticks)
	}
	now = now.Add(RenewInterval)
	ok, err = a.TickOnce(context.Background())
	if err != nil || !ok || ticks != 2 {
		t.Fatalf("renew ok=%v err=%v ticks=%d", ok, err, ticks)
	}
	ok, err = b.TickOnce(context.Background())
	if err != nil || ok {
		t.Fatalf("b stole during the ttl ok=%v err=%v", ok, err)
	}

	// expires_at == now still belongs to A. The grant uses a strict <.
	now = start.Add(RenewInterval).Add(LeaseTTL)
	ok, err = b.TickOnce(context.Background())
	if err != nil || ok {
		t.Fatalf("b stole at exact expiry ok=%v err=%v", ok, err)
	}
	now = now.Add(time.Nanosecond)
	ok, err = b.TickOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("b steal ok=%v err=%v", ok, err)
	}
	ok, err = a.TickOnce(context.Background())
	if err != nil || ok {
		t.Fatalf("a still leader ok=%v err=%v", ok, err)
	}
	if ticks != 2 {
		t.Fatalf("a ticked after loss: %d", ticks)
	}
}

func TestLossStopsWithinOneRenew(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	now := start
	store := newMemLease()
	ticks := 0
	a := &Elector{
		Store:  store,
		Name:   "cron",
		Holder: "a",
		Now:    func() time.Time { return now },
		Tick: func(context.Context, time.Time) error {
			ticks++
			return nil
		},
	}
	if _, err := a.TickOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(RenewInterval)
	if _, err := a.TickOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ticks != 2 {
		t.Fatalf("ticks=%d", ticks)
	}
	// The loop polls every RenewInterval. A stall longer than the ttl lets
	// B take the lease. A's next poll is one renew after it last checked,
	// and that poll must not tick.
	lastCheck := now
	now = lastCheck.Add(LeaseTTL + time.Nanosecond)
	b := &Elector{Store: store, Name: "cron", Holder: "b", Now: func() time.Time { return now }}
	ok, err := b.TickOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("b steal ok=%v err=%v", ok, err)
	}
	ok, err = a.TickOnce(context.Background())
	if err != nil || ok {
		t.Fatalf("a after loss ok=%v err=%v", ok, err)
	}
	if ticks != 2 {
		t.Fatalf("ticked after loss: %d", ticks)
	}
	if now.Sub(lastCheck) > LeaseTTL+time.Nanosecond {
		t.Fatalf("detection lag %s", now.Sub(lastCheck))
	}
}

func TestTwoContendersOneLeader(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)
	store := newMemLease()
	var mu sync.Mutex
	ticks := map[string]int{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		holder := id
		e := &Elector{
			Store:  store,
			Name:   "cron",
			Holder: holder,
			Now:    clk.Now,
			After:  clk.After,
			Tick: func(context.Context, time.Time) error {
				mu.Lock()
				ticks[holder]++
				mu.Unlock()
				return nil
			},
		}
		go func() { errc <- e.Run(ctx) }()
	}
	clk.WaitForWaiters(2)
	assertOneLeader(t, &mu, ticks, 1)
	for i := 0; i < 5; i++ {
		clk.Advance(RenewInterval)
		clk.WaitForWaiters(2)
	}
	assertOneLeader(t, &mu, ticks, 6)
	cancel()
	for i := 0; i < 2; i++ {
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Fatalf("run err %v", err)
		}
	}
}

func TestRunStopsTickingWithinOneRenewAfterLoss(t *testing.T) {
	start := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	clk := newFakeClock(start)
	store := newMemLease()
	started := make(chan struct{})
	release := make(chan struct{})
	lost := make(chan struct{})
	var startOnce sync.Once
	var lostOnce sync.Once
	var ticks atomic.Int32
	wrapped := &observeLease{inner: store, onMiss: func(holder string) {
		if holder == "a" {
			lostOnce.Do(func() { close(lost) })
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Elector{
		Store:  wrapped,
		Name:   "cron",
		Holder: "a",
		Now:    clk.Now,
		After:  clk.After,
		Tick: func(context.Context, time.Time) error {
			ticks.Add(1)
			startOnce.Do(func() {
				close(started)
				<-release
			})
			return nil
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- a.Run(ctx) }()
	<-started
	clk.Advance(LeaseTTL + time.Nanosecond)
	b := &Elector{Store: store, Name: "cron", Holder: "b", Now: clk.Now}
	ok, err := b.TickOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("b steal ok=%v err=%v", ok, err)
	}
	close(release)
	// The in-flight tick was started while A held the lease. The next
	// acquire is one renew interval later; that is when the loss is noticed.
	clk.WaitForWaiters(1)
	clk.Advance(RenewInterval)
	<-lost
	if ticks.Load() != 1 {
		t.Fatalf("ticks=%d", ticks.Load())
	}
	clk.WaitForWaiters(1)
	if ticks.Load() != 1 {
		t.Fatalf("ticked after loss: %d", ticks.Load())
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("run err %v", err)
	}
}

func TestLeaderCatchUpFiresOnce(t *testing.T) {
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	now := start.Add(2*time.Hour + 30*time.Minute)
	sched := newSchedStore(cron.Schedule{
		FactoryID:  "fac",
		Automation: "audit",
		Name:       "hourly",
		Cron:       "@hourly",
		NextFireAt: start,
	})
	sink := &capSink{}
	tk := &cron.Ticker{Store: sched, Sink: sink}
	leases := newMemLease()
	var ticks atomic.Int32
	tick := func(ctx context.Context, at time.Time) error {
		ticks.Add(1)
		return tk.Tick(ctx, at)
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	startCh := make(chan struct{})
	for _, id := range []string{"a", "b"} {
		holder := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startCh
			e := &Elector{
				Store:  leases,
				Name:   "cron",
				Holder: holder,
				Now:    func() time.Time { return now },
				Tick:   tick,
			}
			if _, err := e.TickOnce(ctx); err != nil {
				t.Errorf("%s: %v", holder, err)
			}
		}()
	}
	close(startCh)
	wg.Wait()
	if ticks.Load() != 1 {
		t.Fatalf("tick calls=%d", ticks.Load())
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ev) != 1 || sink.ev[0].Event != cron.EventCronFired || sink.ev[0].Priority != cron.PriorityBatch {
		t.Fatalf("events %+v", sink.ev)
	}
}

func assertOneLeader(t *testing.T, mu *sync.Mutex, ticks map[string]int, want int) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	total := ticks["a"] + ticks["b"]
	if ticks["a"] > 0 && ticks["b"] > 0 {
		t.Fatalf("both ticked: %v", ticks)
	}
	if total != want {
		t.Fatalf("ticks=%v want %d", ticks, want)
	}
}

type memLease struct {
	mu      sync.Mutex
	holder  string
	expires time.Time
	held    bool
}

func newMemLease() *memLease { return &memLease{} }

func (m *memLease) AcquireLease(ctx context.Context, name, holder string, ttl time.Duration, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held && !m.expires.Before(now) && m.holder != holder {
		return false, nil
	}
	m.held = true
	m.holder = holder
	m.expires = now.Add(ttl)
	return true, nil
}

type observeLease struct {
	inner  *memLease
	onMiss func(holder string)
}

func (o *observeLease) AcquireLease(ctx context.Context, name, holder string, ttl time.Duration, now time.Time) (bool, error) {
	ok, err := o.inner.AcquireLease(ctx, name, holder, ttl, now)
	if err == nil && !ok && o.onMiss != nil {
		o.onMiss(holder)
	}
	return ok, err
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

type fakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []waiter
}

func newFakeClock(now time.Time) *fakeClock {
	c := &fakeClock{now: now}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	c.cond.Broadcast()
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	fire := make([]chan time.Time, 0)
	keep := make([]waiter, 0, len(c.waiters))
	for _, w := range c.waiters {
		if !w.at.After(now) {
			fire = append(fire, w.ch)
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
	c.mu.Unlock()
	for _, ch := range fire {
		ch <- now
	}
}

func (c *fakeClock) WaitForWaiters(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.waiters) < n {
		c.cond.Wait()
	}
}

type schedStore struct {
	mu   sync.Mutex
	rows []cron.Schedule
}

func newSchedStore(rows ...cron.Schedule) *schedStore {
	cp := make([]cron.Schedule, len(rows))
	copy(cp, rows)
	return &schedStore{rows: cp}
}

func (s *schedStore) DueSchedules(_ context.Context, now time.Time) ([]cron.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cron.Schedule
	for _, row := range s.rows {
		if row.Paused || row.NextFireAt.IsZero() || row.NextFireAt.After(now) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *schedStore) MarkFired(_ context.Context, row cron.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].FactoryID == row.FactoryID && s.rows[i].Automation == row.Automation && s.rows[i].Name == row.Name {
			s.rows[i] = row
			return nil
		}
	}
	return errNotFound
}

var errNotFound = errors.New("leader: schedule not found")

type capSink struct {
	mu sync.Mutex
	ev []cron.Event
}

func (c *capSink) Emit(_ context.Context, e cron.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ev = append(c.ev, e)
	return nil
}
