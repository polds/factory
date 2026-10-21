package scorer

import (
	"fmt"
	"strings"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/failure"
)

// RunFacts is the slice of a run Eligible is allowed to see.
type RunFacts struct {
	RunID        string
	Agent        string
	Status       string
	FailureClass string
	Purpose      string
	Terminal     bool
	ParentAgents []string
}

// Eligible reports whether a work run may be judged by s.
// Rejection reasons name the first failed docs/08 rule: agent match
// (the run's agent is listed, or a parent is listed with
// includeDescendants — the caller passes ParentAgents), terminal, the D2
// failure-class predicate, then purpose work. An empty agents list matches
// no run. The built-in correctness scorer has an empty list and a sampling
// rate of 0; benchmarks call the judge directly.
func Eligible(s definition.Scorer, r RunFacts) (bool, string) {
	if !agentMatches(s, r) {
		return false, fmt.Sprintf("agent %q is not covered by scorer %q", r.Agent, s.Name)
	}
	if !runTerminal(r) {
		return false, "run is not terminal"
	}
	if ok, why := eligibleFailureClass(r.Status, r.FailureClass); !ok {
		return false, why
	}
	if r.Purpose != api.PurposeWork {
		return false, fmt.Sprintf("purpose %q is not %s", r.Purpose, api.PurposeWork)
	}
	return true, ""
}

// eligibleFailureClass is the D2 predicate. A run is eligible when its
// failure class is agent, or empty on a run that succeeded (or that the
// caller already marked terminal). Infrastructure, configuration, and
// class cancelled are not. An empty class on status cancelled or failed
// is the unswept NULL that sweepFailureClasses later fills; both are
// rejected. Change this function to switch readings.
func eligibleFailureClass(status, class string) (bool, string) {
	switch class {
	case string(failure.ClassAgent):
		return true, ""
	case "":
		switch status {
		case failure.StatusCancelled, failure.StatusFailed:
			return false, fmt.Sprintf("status %q with an empty failure class is not eligible", status)
		default:
			return true, ""
		}
	default:
		return false, fmt.Sprintf("failure class %q is not eligible", class)
	}
}

func runTerminal(r RunFacts) bool {
	if r.Terminal {
		return true
	}
	switch r.Status {
	case failure.StatusSucceeded, failure.StatusFailed, failure.StatusCancelled:
		return true
	default:
		return false
	}
}

func agentMatches(s definition.Scorer, r RunFacts) bool {
	listed := map[string]bool{}
	descendants := map[string]bool{}
	for _, agent := range s.Agents {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			continue
		}
		listed[name] = true
		if agent.IncludeDescendants {
			descendants[name] = true
		}
	}
	if listed[r.Agent] {
		return true
	}
	for _, parent := range r.ParentAgents {
		if descendants[parent] {
			return true
		}
	}
	return false
}
