package outbound

import (
	"context"
	"regexp"
	"time"
)

const (
	maxAttempts    = 8
	backoffBase    = time.Second
	backoffCap     = 5 * time.Minute
	coalesceWindow = 2 * time.Second
)

func backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// 2^9 seconds exceeds the cap. Stop the shift before it overflows.
	if attempt >= 9 {
		return backoffCap
	}
	d := backoffBase << attempt
	if d <= 0 || d > backoffCap {
		return backoffCap
	}
	return d
}

func (n *Notifier) retry(ctx context.Context, op func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = op(ctx)
		if err == nil {
			return nil
		}
		if attempt+1 == maxAttempts {
			break
		}
		wait := backoff(attempt)
		n.log.Warn("outbound delivery retry",
			"attempt", attempt+1,
			"wait", wait.String(),
			"error", redact(err),
		)
		if sleepErr := n.sleep(ctx, wait); sleepErr != nil {
			return sleepErr
		}
	}
	n.log.Error("outbound delivery stopped", "attempts", maxAttempts, "error", redact(err))
	return err
}

var (
	secretPattern = regexp.MustCompile(`(?:ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|xox[baprs]-|sk-|lin_api_)[^\s"']+`)
	pemPattern    = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
)

func redact(err error) string {
	if err == nil {
		return ""
	}
	msg := pemPattern.ReplaceAllString(err.Error(), "[REDACTED]")
	return secretPattern.ReplaceAllString(msg, "[REDACTED]")
}
