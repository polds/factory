package budget

import "testing"

func TestNoteThresholdEpisodes(t *testing.T) {
	if got := NoteThreshold(1000, []int64{900, -200, 200}); got != 2 {
		t.Fatalf("alerts %d", got)
	}
}

func TestRollupCountsEachLeafOnce(t *testing.T) {
	invocation, lead, agent, factory := Rollup([]int64{100, 50})
	if invocation != 150 || lead != 150 || agent != 150 || factory != 150 {
		t.Fatalf("rollup %d %d %d %d", invocation, lead, agent, factory)
	}
}

func TestOvershootStaysZeroInsideCeiling(t *testing.T) {
	if got := OvershootBeyondCeiling(300, 200, 1000); got != 0 {
		t.Fatalf("overshoot %d", got)
	}
}

func TestGuaranteesCeilingRequiresAllocation(t *testing.T) {
	if GuaranteesCeiling(true, false) {
		t.Fatal("shared key guaranteed a ceiling")
	}
	if !GuaranteesCeiling(false, true) {
		t.Fatal("allocation did not guarantee a ceiling")
	}
	if GuaranteesCeiling(false, false) {
		t.Fatal("missing allocation guaranteed a ceiling")
	}
}
