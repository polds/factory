package defsync

import (
	"context"
	"errors"
	"time"

	"github.com/polds/factory/internal/definition"
)

// newTicker is replaced in tests so interval waits do not sleep.
var newTicker = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Waker is the optional signal that a sync should run before the next
// interval. Webhook ingress is M4; this interface is the seam only.
type Waker interface {
	Wake() <-chan struct{}
}

// Poller calls Sync once at start, then on Interval and on Wake.
// Sync failures are delivered to Handler and do not stop the loop.
type Poller struct {
	Interval time.Duration
	Source   Source
	Dest     string
	Cloner   Cloner
	Wake     Waker
	Handler  func(sha string, changed bool, diags []definition.Diagnostic, err error)
}

// Run blocks until ctx is cancelled. The first sync runs immediately.
func (p *Poller) Run(ctx context.Context) error {
	if p.Interval <= 0 {
		return errors.New("defsync: poll interval must be positive")
	}
	if p.Cloner == nil {
		return errors.New("defsync: cloner is required")
	}
	if p.Dest == "" {
		return errors.New("defsync: dest is required")
	}
	p.sync(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	ticks, stop := newTicker(p.Interval)
	defer stop()
	var wake <-chan struct{}
	if p.Wake != nil {
		wake = p.Wake.Wake()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticks:
			p.sync(ctx)
		case _, ok := <-wake:
			if !ok {
				wake = nil
				continue
			}
			p.sync(ctx)
		}
	}
}

func (p *Poller) sync(ctx context.Context) {
	sha, changed, diags, err := Sync(ctx, p.Source, p.Dest, p.Cloner)
	if p.Handler != nil {
		p.Handler(sha, changed, diags, err)
	}
}
