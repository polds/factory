package scheduler

import (
	"flag"
	"testing"
	"time"
)

func TestSim(t *testing.T) {
	seeds := *simSeeds
	if seeds < 1 {
		t.Fatalf("sim.seeds = %d, want >= 1", seeds)
	}
	const steps = 200
	for seed := int64(0); seed < int64(seeds); seed++ {
		sim := Sim{Seed: seed}
		if err := sim.Run(steps); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
}

func TestSimCountersPersist(t *testing.T) {
	sim := Sim{Seed: 1}
	if err := sim.Run(50); err != nil {
		t.Fatal(err)
	}
	if sim.Counters.Assignments == 0 || sim.Counters.Rejects == 0 {
		t.Fatalf("counters = %+v, want assignments and rejects on sim", sim.Counters)
	}
}

func TestCounters(t *testing.T) {
	var c Counters
	if c.TimeToAssignment() != 0 || c.RejectRate() != 0 {
		t.Fatalf("zero counters: wait %s rate %v", c.TimeToAssignment(), c.RejectRate())
	}
	c.RecordAssignment(2 * time.Second)
	c.RecordAssignment(4 * time.Second)
	c.RecordReject()
	if c.TimeToAssignment() != 3*time.Second {
		t.Fatalf("time_to_assignment = %s, want 3s", c.TimeToAssignment())
	}
	if c.RejectRate() != 0.5 {
		t.Fatalf("reject_rate = %v, want 0.5", c.RejectRate())
	}
}

var simSeeds = flag.Int("sim.seeds", 1000, "number of seeds for TestSim")
