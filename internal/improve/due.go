package improve

import (
	"sort"
	"time"

	"github.com/polds/factory/internal/definition"
)

const (
	defaultFailedRunThreshold = 25
	defaultStaleFailureDays   = 7
)

// Failure is one unreviewed scorer result the checker may batch.
type Failure struct {
	ResultID  string
	RunID     string
	Agent     string
	Scorer    string
	Label     string
	Reasoning string
	At        time.Time
}

// Due returns agents that should get an improvement batch. An agent is due
// when its unreviewed count reaches the threshold, or its oldest unreviewed
// failure is at least staleFailureDays old. Agents in inFlight are skipped.
// Zero threshold and stale days use the docs/08 defaults (25 and 7).
func Due(now time.Time, unreviewed []Failure, cfg definition.SelfImprovement, inFlight map[string]bool) []string {
	threshold := cfg.FailedRunThreshold
	if threshold <= 0 {
		threshold = defaultFailedRunThreshold
	}
	days := cfg.StaleFailureDays
	if days <= 0 {
		days = defaultStaleFailureDays
	}
	type bucket struct {
		n      int
		oldest time.Time
	}
	by := map[string]*bucket{}
	for _, f := range unreviewed {
		if f.Agent == "" || inFlight[f.Agent] {
			continue
		}
		b := by[f.Agent]
		if b == nil {
			b = &bucket{}
			by[f.Agent] = b
		}
		b.n++
		if f.At.IsZero() {
			continue
		}
		if b.oldest.IsZero() || f.At.Before(b.oldest) {
			b.oldest = f.At
		}
	}
	var agents []string
	for agent, b := range by {
		stale := !b.oldest.IsZero() && !b.oldest.Add(time.Duration(days)*24*time.Hour).After(now)
		if b.n >= threshold || stale {
			agents = append(agents, agent)
		}
	}
	sort.Strings(agents)
	return agents
}
