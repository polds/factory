// Package budget is the integer-cent admission math native coordination commits.
// RunUsage is not an admission gate. Each leaf charge is counted once.
package budget

// ThresholdPercent is the soft alert line. One alert is emitted per episode.
const ThresholdPercent = 80

// Episode is the 80 percent alert state for one scope.
type Episode struct {
	Episode int
	Alerted bool
	Alerts  int
}

// Step applies one charge. A positive delta spends. A negative delta refunds.
// Crossing thresholdPercent emits one alert. Falling back under the line
// starts a new episode so the next crossing alerts again.
func Step(ceiling, spent, delta int64, ep Episode) (int64, Episode, bool) {
	if ep.Episode == 0 {
		ep.Episode = 1
	}
	nextSpent := spent + delta
	if nextSpent < 0 {
		nextSpent = 0
	}
	crossed := ceiling > 0 && nextSpent*100 >= ceiling*int64(ThresholdPercent)
	if crossed {
		if ep.Alerted {
			return nextSpent, ep, false
		}
		ep.Alerted = true
		ep.Alerts++
		return nextSpent, ep, true
	}
	if ep.Alerted {
		ep.Episode++
		ep.Alerted = false
	}
	return nextSpent, ep, false
}

// NoteThreshold counts 80 percent alerts for a sequence of charges.
func NoteThreshold(ceiling int64, charges []int64) int {
	var spent int64
	ep := Episode{Episode: 1}
	for _, charge := range charges {
		spent, ep, _ = Step(ceiling, spent, charge, ep)
	}
	return ep.Alerts
}

// OvershootBeyondCeiling is spent plus reserved minus the ceiling, or 0 when
// the account is inside the ceiling. overshootBeyondCeilingCents stays 0
// when admission refuses a reservation that would pass the ceiling.
func OvershootBeyondCeiling(spent, reserved, ceiling int64) int64 {
	over := spent + reserved - ceiling
	if over < 0 {
		return 0
	}
	return over
}

// GuaranteesCeiling is false when a shared key is the only control.
// guaranteesCeiling requires a per-engagement gateway allocation, which this
// process does not have. A shared key alone does not cap in-flight calls.
func GuaranteesCeiling(sharedKey, gatewayAllocation bool) bool {
	if sharedKey || !gatewayAllocation {
		return false
	}
	return true
}

// Rollup adds each leaf once to invocation, lead, agent, and factory.
// inclusive rollup does not add those totals together.
func Rollup(leaves []int64) (invocation, lead, agent, factory int64) {
	var sum int64
	for _, leaf := range leaves {
		sum += leaf
	}
	return sum, sum, sum, sum
}
