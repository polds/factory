package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/polds/factory/internal/scheduler"
	"github.com/polds/factory/internal/secrets"
)

const (
	// DefaultTaskServerURL is the Docker bridge gateway. Task containers are
	// not on the compose network; they reach the published host port here.
	DefaultTaskServerURL = "http://172.17.0.1:8080"

	defaultWorkerPool     = "standard"
	defaultWorkerPlatform = "linux"

	// Lease is twice the 90s heartbeat window and is renewed while the worker
	// is executing and still pinging.
	defaultLeaseTTL = 2 * scheduler.HeartbeatWindow

	defaultCancelGrace = 60 * time.Second

	// defaultDeliveryRetention is D11: matched deliveries are kept for seven days.
	defaultDeliveryRetention = 7 * 24 * time.Hour
	// defaultScheduleCatchUp is the D8 window.
	defaultScheduleCatchUp = time.Hour

	tokenModeApp = "app"
	tokenModePAT = "pat"

	dispatchInterval = 200 * time.Millisecond
	cronLeaseName    = "factory-cron"

	// serverEventBase is above any harness X-Factory-Event-Index so a
	// server-written event cannot collide with an ingested batch.
	serverEventBase = 1_000_000_000

	leaderLockClass int32 = 748293
	leaderLockObj   int32 = 2

	// serverSessionLockSalt is the hashtextextended salt for a process
	// session lock. It is not the definition salt (2), the run-event
	// salt (3), or the two-int leader lock.
	serverSessionLockSalt int64 = 4

	// maxReviewerDetach is how many times the leader may put one
	// reviewing run back on the queue after its owner session is gone.
	// The next detach fails the run instead of queueing it again.
	maxReviewerDetach = 3
)

// defaultCaps are positive on purpose. A zero cap admits no runs.
func defaultCaps() scheduler.Caps {
	return scheduler.Caps{DefaultPerFactory: 8, DefaultPerAgent: 4}
}

// secretNames is the task env allowlist: factory secrets, then agent secrets.
func secretNames(factory, agent []string) []string {
	seen := make(map[string]struct{}, len(factory)+len(agent))
	out := make([]string, 0, len(factory)+len(agent))
	for _, list := range [][]string{factory, agent} {
		for _, name := range list {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// SecretMountSettings configures the secrets.Resolver mount seam for factoryd.
// Production injection wiring is owned by M9-H; this only parses and validates config.
type SecretMountSettings struct {
	MountDir  string
	Allowlist []string
}

// ParseSecretMountSettings reads FACTORY_SECRET_MOUNT_DIR and FACTORY_SECRET_ALLOWLIST.
func ParseSecretMountSettings(getenv func(string) string) SecretMountSettings {
	raw := strings.TrimSpace(getenv("FACTORY_SECRET_ALLOWLIST"))
	var allow []string
	if raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				allow = append(allow, part)
			}
		}
	}
	return SecretMountSettings{
		MountDir:  strings.TrimSpace(getenv("FACTORY_SECRET_MOUNT_DIR")),
		Allowlist: allow,
	}
}

// BuildResolver constructs a Resolver when mount settings and env secrets are present.
func (s SecretMountSettings) BuildResolver(envSecrets map[string]string) (secrets.Resolver, error) {
	if strings.TrimSpace(s.MountDir) == "" && len(s.Allowlist) == 0 {
		return nil, nil
	}
	cfg := secrets.MountConfig{
		MountDir:  s.MountDir,
		Allowlist: s.Allowlist,
		Env:       envSecrets,
	}
	if len(cfg.Allowlist) == 0 {
		for name := range envSecrets {
			cfg.Allowlist = append(cfg.Allowlist, name)
		}
	}
	res, err := secrets.ResolverFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("secret mount: %w", err)
	}
	return res, nil
}
