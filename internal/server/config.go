package server

import (
	"time"

	"github.com/polds/factory/internal/scheduler"
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
