package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/polds/factory/internal/leader"
)

// errLeaseLost stops a sampler sweep when this process no longer holds the
// lease. The sweep renews before each run so a pass longer than the TTL
// cannot overlap a second holder.
var errLeaseLost = errors.New("lost measure lease")

const (
	samplerLeaseName     = "m6-sampler"
	improvementLeaseName = "m6-improve"
)

// SampleTick runs one sampler pass when this process holds the sampler lease.
// The bool is false when another instance owns the lease.
func (s *Server) SampleTick(ctx context.Context) (bool, error) {
	return s.measureTick(ctx, samplerLeaseName, s.gatedSample)
}

// ImproveTick runs one self-improvement pass when this process holds the
// improvement lease. The bool is false when another instance owns the lease.
func (s *Server) ImproveTick(ctx context.Context) (bool, error) {
	return s.measureTick(ctx, improvementLeaseName, s.gatedImprove)
}

func (s *Server) measureTick(ctx context.Context, name string, tick func(context.Context, time.Time) error) (bool, error) {
	el := &leader.Elector{
		Store:  s.store,
		Name:   name,
		Holder: s.integ.holder,
		TTL:    leader.LeaseTTL,
		Now:    s.now,
		Tick:   tick,
	}
	return el.TickOnce(ctx)
}

func (s *Server) sampleLoop() {
	defer s.wg.Done()
	s.runMeasureLoop(samplerLeaseName, s.gatedSample, "sampler")
}

func (s *Server) improveLoop() {
	defer s.wg.Done()
	s.runMeasureLoop(improvementLeaseName, s.gatedImprove, "improvement")
}

func (s *Server) runMeasureLoop(name string, tick func(context.Context, time.Time) error, label string) {
	el := &leader.Elector{
		Store:  s.store,
		Name:   name,
		Holder: s.integ.holder,
		TTL:    leader.LeaseTTL,
		Renew:  leader.RenewInterval,
		Now:    s.now,
		Tick:   tick,
		OnError: func(err error) {
			if s.ctx.Err() == nil {
				s.log.Error(label, "err", s.redact(err.Error()))
			}
		},
	}
	if err := el.Run(s.ctx); err != nil && s.ctx.Err() == nil {
		s.log.Error(label+" stopped", "err", s.redact(err.Error()))
	}
}

func (s *Server) gatedSample(ctx context.Context, now time.Time) error {
	if !s.measureDue(&s.m6SampledAt, s.cfg.SamplerInterval, now) {
		return nil
	}
	return s.samplePass(ctx)
}

func (s *Server) gatedImprove(ctx context.Context, now time.Time) error {
	// The leader loop wakes every RenewInterval (5s). A branch that
	// appears a few seconds after the push has to be cloned again inside
	// the advertise window, which is shorter than ImprovementInterval.
	// Only that waiting sweep runs on the fast tick. The interval still
	// gates scheduling new batches.
	due := s.measureDue(&s.m6ImprovedAt, s.cfg.ImprovementInterval, now)
	if !due && !s.improveRefPending() {
		return nil
	}
	if !due {
		return s.sweepFactoryImprovements(ctx)
	}
	return s.improvePass(ctx)
}

// measureDue reports whether a leader tick should do work. A zero interval
// always runs, which is how tests call TickOnce without starting the loop.
// A positive interval keeps the lease while spacing the work.
// renewMeasureLease extends the sampler lease to now+TTL. An empty holder
// or a server with no store is a no-op, which is how unit tests run a pass
// without electing. A lost lease returns errLeaseLost so the sweep stops.
func (s *Server) renewMeasureLease(ctx context.Context, name string) error {
	if s.store == nil || s.integ.holder == "" {
		return nil
	}
	ok, err := s.store.AcquireLease(ctx, name, s.integ.holder, leader.LeaseTTL, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("server: lost lease %s: %w", name, errLeaseLost)
	}
	return nil
}

func (s *Server) measureDue(last *time.Time, every time.Duration, now time.Time) bool {
	if every <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !last.IsZero() && now.Sub(*last) < every {
		return false
	}
	*last = now
	return true
}
