package leader

import (
	"context"
	"errors"
	"time"
)

const (
	// LeaseTTL is how long a grant lasts from the acquire time (D8).
	LeaseTTL = 15 * time.Second
	// RenewInterval is how often the holder renews. Loss of the lease is
	// noticed on the next renew, so ticking stops within one interval.
	RenewInterval = 5 * time.Second
)

// LeaseStore grants or renews a named lease.
//
// AcquireLease reports whether holder owns the lease after the call. An
// implementation must grant it only when expires_at < now or the current
// holder is already holder, then set expires_at = now+ttl. expires_at == now
// does not expire. T17's SQL is UPDATE ... WHERE expires_at < now OR holder
// = $holder.
type LeaseStore interface {
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration, now time.Time) (bool, error)
}

// Elector is one contender for a named lease.
//
// Now and After override the clock. Tests pass a fake clock so renew waits
// do not sleep on wall time. A nil Now uses time.Now. A nil After uses a
// real timer. Tick is called only after a successful acquire or renew. A
// tick error is reported to OnError and does not drop the lease; the next
// renew tries again. A failed acquire does not call Tick.
type Elector struct {
	Store   LeaseStore
	Name    string
	Holder  string
	TTL     time.Duration
	Renew   time.Duration
	Now     func() time.Time
	After   func(time.Duration) <-chan time.Time
	Tick    func(ctx context.Context, now time.Time) error
	OnError func(error)
}

// TickOnce renews the lease and, when this holder owns it, runs Tick.
// The bool is false when another holder owns an unexpired lease.
func (e *Elector) TickOnce(ctx context.Context) (bool, error) {
	if err := e.validate(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ok, err := e.Store.AcquireLease(ctx, e.Name, e.Holder, e.ttl(), e.now())
	if err != nil || !ok {
		return false, err
	}
	if e.Tick == nil {
		return true, nil
	}
	if err := e.Tick(ctx, e.now()); err != nil {
		return true, err
	}
	return true, nil
}

// Run renews until ctx is cancelled. While this holder owns the lease it
// ticks once per renew. After a lost acquire it does not tick, and the next
// check happens one renew interval later.
func (e *Elector) Run(ctx context.Context) error {
	if err := e.validate(); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := e.TickOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e.OnError != nil {
				e.OnError(err)
			}
		}
		if err := e.wait(ctx); err != nil {
			return err
		}
	}
}

func (e *Elector) wait(ctx context.Context) error {
	renew := e.renew()
	if e.After != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.After(renew):
			return nil
		}
	}
	timer := time.NewTimer(renew)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *Elector) validate() error {
	if e == nil || e.Store == nil {
		return errors.New("leader: store is required")
	}
	if e.Name == "" || e.Holder == "" {
		return errors.New("leader: name and holder are required")
	}
	ttl := e.ttl()
	renew := e.renew()
	if ttl <= 0 || renew <= 0 {
		return errors.New("leader: ttl and renew must be positive")
	}
	if renew >= ttl {
		return errors.New("leader: renew interval must be shorter than the lease ttl")
	}
	return nil
}

func (e *Elector) ttl() time.Duration {
	if e.TTL == 0 {
		return LeaseTTL
	}
	return e.TTL
}

func (e *Elector) renew() time.Duration {
	if e.Renew == 0 {
		return RenewInterval
	}
	return e.Renew
}

func (e *Elector) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
