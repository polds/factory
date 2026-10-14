package scheduler

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestPlan(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	open := Caps{DefaultPerFactory: 100, DefaultPerAgent: 100}

	fresh := func(id string, inflight, maxc int, idle time.Time) Candidate {
		return Candidate{
			WorkerID:      id,
			Pool:          "default",
			Platform:      "linux",
			LastHeartbeat: now,
			IdleSince:     idle,
			InFlight:      inflight,
			MaxConcurrent: maxc,
		}
	}
	pending := func(id, factory, agent string, pri Class, enqueued time.Time) Pending {
		return Pending{
			RunID:      id,
			FactoryID:  factory,
			AgentID:    agent,
			Pool:       "default",
			Platform:   "linux",
			Priority:   pri,
			EnqueuedAt: enqueued,
		}
	}

	t.Run("stale heartbeat excluded", func(t *testing.T) {
		stale := fresh("w-stale", 0, 1, now.Add(-time.Hour))
		stale.LastHeartbeat = now.Add(-91 * time.Second)
		live := fresh("w-live", 1, 2, now)
		got := Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{stale, live}, nil, open)
		assertAssigned(t, got, "r1", "w-live")

		onlyStale := Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{stale}, nil, open)
		if len(onlyStale) != 0 {
			t.Fatalf("91s heartbeat assigned %#v", onlyStale)
		}

		boundary := fresh("w-boundary", 0, 1, now)
		boundary.LastHeartbeat = now.Add(-90 * time.Second)
		got = Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{boundary}, nil, open)
		assertAssigned(t, got, "r1", "w-boundary")
	})

	t.Run("platform mismatch excluded", func(t *testing.T) {
		darwin := fresh("w-darwin", 0, 1, now)
		darwin.Platform = "darwin"
		linux := fresh("w-linux", 0, 1, now.Add(-time.Minute))
		run := pending("r1", "fac", "agt", Batch, now)
		run.Platform = "linux"
		got := Plan(now, []Pending{run}, []Candidate{darwin, linux}, nil, open)
		assertAssigned(t, got, "r1", "w-linux")
	})

	t.Run("pool mismatch excluded", func(t *testing.T) {
		gpu := fresh("w-gpu", 0, 1, now)
		gpu.Pool = "gpu"
		cpu := fresh("w-cpu", 0, 1, now.Add(-time.Minute))
		cpu.Pool = "cpu"
		run := pending("r1", "fac", "agt", Batch, now)
		run.Pool = "cpu"
		got := Plan(now, []Pending{run}, []Candidate{gpu, cpu}, nil, open)
		assertAssigned(t, got, "r1", "w-cpu")
	})

	t.Run("full worker excluded", func(t *testing.T) {
		full := fresh("w-full", 2, 2, now.Add(-time.Hour))
		room := fresh("w-room", 1, 2, now)
		got := Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{full, room}, nil, open)
		assertAssigned(t, got, "r1", "w-room")

		onlyFull := Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{full}, nil, open)
		if len(onlyFull) != 0 {
			t.Fatalf("full worker assigned %#v", onlyFull)
		}
	})

	t.Run("least-loaded then longest idle", func(t *testing.T) {
		busy := fresh("w-busy", 2, 4, now.Add(-time.Hour))
		idle := fresh("w-idle", 0, 4, now)
		got := Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{busy, idle}, nil, open)
		assertAssigned(t, got, "r1", "w-idle")

		shortIdle := fresh("w-short", 1, 4, now.Add(-time.Second))
		longIdle := fresh("w-long", 1, 4, now.Add(-time.Hour))
		got = Plan(now, []Pending{pending("r1", "fac", "agt", Batch, now)}, []Candidate{shortIdle, longIdle}, nil, open)
		assertAssigned(t, got, "r1", "w-long")
	})

	t.Run("per-agent cap", func(t *testing.T) {
		caps := Caps{DefaultPerFactory: 10, DefaultPerAgent: 1}
		running := []Pending{pending("r-running", "fac", "agent-a", Batch, now.Add(-time.Hour))}
		queued := []Pending{
			pending("r-a2", "fac", "agent-a", Interactive, now),
			pending("r-b", "fac", "agent-b", Batch, now.Add(-time.Minute)),
		}
		got := Plan(now, queued, []Candidate{fresh("w", 0, 4, now)}, running, caps)
		assertAssigned(t, got, "r-b", "w")
		if len(got) != 1 {
			t.Fatalf("agent cap plan = %#v, want only agent-b", got)
		}
	})

	t.Run("per-factory cap", func(t *testing.T) {
		caps := Caps{
			PerFactory:        map[string]int{"fac-a": 1},
			DefaultPerFactory: 1,
			DefaultPerAgent:   10,
		}
		running := []Pending{pending("a-live", "fac-a", "agent-a", Batch, now.Add(-2*time.Hour))}
		queued := []Pending{
			pending("a2", "fac-a", "agent-a2", Interactive, now.Add(-time.Hour)),
			pending("b1", "fac-b", "agent-b", Batch, now),
		}
		got := Plan(now, queued, []Candidate{fresh("w", 0, 4, now)}, running, caps)
		assertAssigned(t, got, "b1", "w")
	})

	t.Run("interactive beats batch", func(t *testing.T) {
		queued := []Pending{
			pending("r-batch", "fac", "agt", Batch, now.Add(-2*time.Hour)),
			pending("r-pipe", "fac", "agt", Pipeline, now.Add(-time.Hour)),
			pending("r-hot", "fac", "agt", Interactive, now),
		}
		got := Plan(now, queued, []Candidate{fresh("w", 0, 1, now)}, nil, open)
		assertAssigned(t, got, "r-hot", "w")

		noInteractive := []Pending{queued[0], queued[1]}
		got = Plan(now, noInteractive, []Candidate{fresh("w", 0, 1, now)}, nil, open)
		assertAssigned(t, got, "r-pipe", "w")
	})

	t.Run("factories do not starve", func(t *testing.T) {
		var queued []Pending
		for i := 0; i < 10; i++ {
			queued = append(queued,
				pending("a"+strconv.Itoa(i), "fac-a", "agent-a", Batch, now.Add(-time.Duration(100-i)*time.Minute)),
				pending("b"+strconv.Itoa(i), "fac-b", "agent-b", Batch, now.Add(-time.Duration(10-i)*time.Minute)),
			)
		}
		workers := []Candidate{
			fresh("w1", 0, 1, now.Add(-time.Hour)),
			fresh("w2", 0, 1, now.Add(-time.Minute)),
		}
		before := slices.Clone(queued)
		got := Plan(now, queued, workers, nil, open)
		if len(got) != 2 {
			t.Fatalf("plan = %#v, want 2 assignments", got)
		}
		seen := map[string]string{}
		for _, a := range got {
			fac := "fac-b"
			if a.RunID[0] == 'a' {
				fac = "fac-a"
			}
			seen[fac] = a.RunID
		}
		if len(seen) != 2 {
			t.Fatalf("assignments %#v, want one run from each factory", got)
		}
		if !slices.Equal(queued, before) {
			t.Fatal("Plan mutated the pending slice")
		}
	})

	t.Run("busy factory yields the one slot", func(t *testing.T) {
		running := []Pending{pending("a-live", "fac-a", "agent-a", Batch, now.Add(-3*time.Hour))}
		queued := []Pending{
			pending("a-next", "fac-a", "agent-a2", Batch, now.Add(-2*time.Hour)),
			pending("b-next", "fac-b", "agent-b", Batch, now.Add(-time.Minute)),
		}
		got := Plan(now, queued, []Candidate{fresh("w", 0, 1, now)}, running, open)
		assertAssigned(t, got, "b-next", "w")
	})

	t.Run("busy factory alternates across calls", func(t *testing.T) {
		running := []Pending{pending("a-live", "fac-a", "agent-a", Batch, now.Add(-3*time.Hour))}
		var queued []Pending
		for i := 0; i < 8; i++ {
			queued = append(queued,
				pending("a"+strconv.Itoa(i), "fac-a", "agent-a2", Batch, now.Add(-time.Duration(100-i)*time.Minute)),
				pending("b"+strconv.Itoa(i), "fac-b", "agent-b", Batch, now.Add(-time.Duration(10-i)*time.Minute)),
			)
		}
		var got []string
		for call := 0; call < 8; call++ {
			plan := Plan(now, queued, []Candidate{fresh("w", 0, 1, now)}, running, open)
			if len(plan) != 1 {
				t.Fatalf("call %d plan = %#v, want 1 assignment", call, plan)
			}
			fac := "fac-b"
			if plan[0].RunID[0] == 'a' {
				fac = "fac-a"
			}
			got = append(got, fac)
			var rest []Pending
			for _, p := range queued {
				if p.RunID == plan[0].RunID {
					running = append(running, p)
					continue
				}
				rest = append(rest, p)
			}
			queued = rest
		}
		want := []string{"fac-b", "fac-a", "fac-b", "fac-a", "fac-b", "fac-a", "fac-b", "fac-a"}
		if !slices.Equal(got, want) {
			t.Fatalf("factories = %v, want %v", got, want)
		}
	})

	t.Run("no capacity stays queued", func(t *testing.T) {
		queued := []Pending{pending("r1", "fac", "agt", Interactive, now.Add(-time.Hour))}
		full := fresh("w", 1, 1, now)
		before := slices.Clone(queued)
		got := Plan(now, queued, []Candidate{full}, nil, open)
		if len(got) != 0 {
			t.Fatalf("plan = %#v, want empty", got)
		}
		if !slices.Equal(queued, before) {
			t.Fatal("Plan mutated the pending slice")
		}
		got = Plan(now, queued, nil, nil, open)
		if len(got) != 0 {
			t.Fatalf("no workers plan = %#v", got)
		}
	})
}

func assertAssigned(t *testing.T, got []Assignment, runID, workerID string) {
	t.Helper()
	if len(got) != 1 || got[0].RunID != runID || got[0].WorkerID != workerID {
		t.Fatalf("plan = %#v, want %s on %s", got, runID, workerID)
	}
}
