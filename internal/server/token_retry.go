package server

import (
	"context"
	"time"

	ghclient "github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
)

const (
	tokenRetryAttempts = 4
	tokenRetryBase     = 100 * time.Millisecond
	tokenRetryCap      = 2 * time.Second
)

func tokenRetryWait(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// 100ms << 5 is 3.2s and past the cap. Stop before the shift overflows.
	if attempt >= 5 {
		return tokenRetryCap
	}
	d := tokenRetryBase << attempt
	if d <= 0 || d > tokenRetryCap {
		return tokenRetryCap
	}
	return d
}

func (s *Server) tokenRetrySleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if s.cfg.TokenRetrySleep != nil {
		return s.cfg.TokenRetrySleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// taskEnvRetry mints an installation token again when the failure is a
// network error or an HTTP 5xx. The run stays queued when every attempt
// fails that way. An auth failure is returned on the first attempt.
func (s *Server) taskEnvRetry(ctx context.Context, run store.Run, bound boundAgent) (map[string]string, error) {
	return s.retryTransient(ctx, run.ID, func() (map[string]string, error) {
		return s.taskEnv(ctx, run, bound)
	})
}

func (s *Server) retryTransient(ctx context.Context, runID string, op func() (map[string]string, error)) (map[string]string, error) {
	var env map[string]string
	var err error
	for attempt := 0; attempt < tokenRetryAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		env, err = op()
		if err == nil || !ghclient.Transient(err) {
			return env, err
		}
		if attempt+1 == tokenRetryAttempts {
			break
		}
		wait := tokenRetryWait(attempt)
		if s.log != nil {
			s.log.Warn("github token retry", "run_id", runID, "attempt", attempt+1, "wait", wait.String(), "err", err)
		}
		if sleepErr := s.tokenRetrySleep(ctx, wait); sleepErr != nil {
			return nil, sleepErr
		}
	}
	return nil, err
}

// leaveQueued reports a token failure that must not finish the run.
// The claim is released and a later scheduler tick tries again.
func (s *Server) leaveQueued(ctx context.Context, err error) bool {
	return ctx.Err() != nil || ghclient.Transient(err)
}
