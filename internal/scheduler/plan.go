// Package scheduler decides which queued runs to place on which workers.
// It is pure: Plan does not perform I/O, and it does not mutate its inputs.
package scheduler

import (
	"sort"
	"time"
)

// Class is a run priority. Higher values preempt lower ones.
type Class int

const (
	// Batch is the lowest class (scheduled work, scorers, benchmarks).
	Batch Class = 1
	// Pipeline is a stage advance.
	Pipeline Class = 2
	// Interactive is a human waiting.
	Interactive Class = 3
)

// HeartbeatWindow is the maximum age of a worker heartbeat that Plan still
// treats as live. docs/02 and docs/04 server obligation 1 fix this at 90s.
const HeartbeatWindow = 90 * time.Second

// Candidate is a worker Plan may assign to.
type Candidate struct {
	WorkerID      string
	Pool          string
	Platform      string
	LastHeartbeat time.Time
	IdleSince     time.Time
	InFlight      int
	MaxConcurrent int
}

// Pending is a queued run, or a running run passed in for cap accounting.
type Pending struct {
	RunID      string
	FactoryID  string
	AgentID    string
	Pool       string
	Platform   string
	Priority   Class
	EnqueuedAt time.Time
}

// Caps limits how many runs of one factory or agent may be in flight.
// A key present in PerFactory or PerAgent overrides the matching default.
// A limit of zero or less admits no further run for that key.
type Caps struct {
	PerFactory        map[string]int
	PerAgent          map[string]int
	DefaultPerFactory int
	DefaultPerAgent   int
}

// Assignment places one queued run on one worker.
type Assignment struct {
	RunID    string
	WorkerID string
}

// Plan selects assignments for this tick.
//
// A worker is eligible when its pool and platform equal the run's, its
// heartbeat is within HeartbeatWindow, and InFlight is below MaxConcurrent.
// Among eligible workers the least loaded wins, then the one idle longest
// (earliest IdleSince), then the lowest WorkerID.
//
// Higher priority classes are placed before lower ones. Inside a class,
// factories share slots by weighted round-robin. The weight is that
// factory's concurrency cap, so equal caps split slots evenly and one
// factory cannot take every slot while another factory in the same class
// is still waiting. Runs that fit nowhere are left queued.
func Plan(now time.Time, pending []Pending, workers []Candidate, running []Pending, caps Caps) []Assignment {
	if len(pending) == 0 || len(workers) == 0 {
		return nil
	}
	ws := make([]Candidate, len(workers))
	copy(ws, workers)

	facN := make(map[string]int, len(running))
	agN := make(map[string]int, len(running))
	busy := make(map[string]struct{}, len(running))
	for _, r := range running {
		facN[r.FactoryID]++
		agN[r.AgentID]++
		busy[r.RunID] = struct{}{}
	}

	var interactive, pipeline, batch, other []Pending
	for _, p := range pending {
		if _, skip := busy[p.RunID]; skip {
			continue
		}
		switch p.Priority {
		case Interactive:
			interactive = append(interactive, p)
		case Pipeline:
			pipeline = append(pipeline, p)
		case Batch:
			batch = append(batch, p)
		default:
			other = append(other, p)
		}
	}

	var out []Assignment
	out = append(out, scheduleClass(now, interactive, ws, facN, agN, caps)...)
	out = append(out, scheduleClass(now, pipeline, ws, facN, agN, caps)...)
	out = append(out, scheduleClass(now, batch, ws, facN, agN, caps)...)
	out = append(out, scheduleClass(now, other, ws, facN, agN, caps)...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// vtScale keeps weighted-round-robin virtual time in integers.
const vtScale = 1_000_000

type factoryQueue struct {
	id    string
	runs  []Pending
	next  int
	vt    int
	limit int
}

func scheduleClass(now time.Time, runs []Pending, workers []Candidate, facN, agN map[string]int, caps Caps) []Assignment {
	if len(runs) == 0 {
		return nil
	}
	groups := make(map[string][]Pending)
	var ids []string
	for _, r := range runs {
		if _, ok := groups[r.FactoryID]; !ok {
			ids = append(ids, r.FactoryID)
		}
		groups[r.FactoryID] = append(groups[r.FactoryID], r)
	}
	qs := make([]*factoryQueue, 0, len(ids))
	for _, id := range ids {
		rr := groups[id]
		sort.Slice(rr, func(i, j int) bool {
			if !rr[i].EnqueuedAt.Equal(rr[j].EnqueuedAt) {
				return rr[i].EnqueuedAt.Before(rr[j].EnqueuedAt)
			}
			return rr[i].RunID < rr[j].RunID
		})
		limit := capFor(caps.PerFactory, id, caps.DefaultPerFactory)
		qs = append(qs, &factoryQueue{id: id, runs: rr, limit: limit})
	}

	var out []Assignment
	for len(out) < len(runs) {
		q := pickFactory(qs)
		if q == nil {
			break
		}
		if facN[q.id] >= q.limit {
			q.next = len(q.runs)
			continue
		}
		run := q.runs[q.next]
		if agN[run.AgentID] >= capFor(caps.PerAgent, run.AgentID, caps.DefaultPerAgent) {
			q.next++
			continue
		}
		wi, ok := bestWorker(now, run, workers)
		if !ok {
			q.next++
			continue
		}
		out = append(out, Assignment{RunID: run.RunID, WorkerID: workers[wi].WorkerID})
		workers[wi].InFlight++
		workers[wi].IdleSince = now
		facN[q.id]++
		agN[run.AgentID]++
		q.next++
		q.vt += virtualTimeBump(q.limit)
	}
	return out
}

func pickFactory(qs []*factoryQueue) *factoryQueue {
	var best *factoryQueue
	for _, q := range qs {
		if q.next >= len(q.runs) || q.limit <= 0 {
			continue
		}
		if best == nil || factoryBefore(q, best) {
			best = q
		}
	}
	return best
}

func factoryBefore(a, b *factoryQueue) bool {
	if a.vt != b.vt {
		return a.vt < b.vt
	}
	ar := a.runs[a.next]
	br := b.runs[b.next]
	if !ar.EnqueuedAt.Equal(br.EnqueuedAt) {
		return ar.EnqueuedAt.Before(br.EnqueuedAt)
	}
	if a.id != b.id {
		return a.id < b.id
	}
	return ar.RunID < br.RunID
}

func virtualTimeBump(weight int) int {
	if weight < 1 {
		return vtScale
	}
	inc := vtScale / weight
	if inc < 1 {
		inc = 1
	}
	return inc
}

func capFor(m map[string]int, key string, def int) int {
	if m != nil {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return def
}

func bestWorker(now time.Time, run Pending, workers []Candidate) (int, bool) {
	best := -1
	for i, c := range workers {
		if c.Pool != run.Pool || c.Platform != run.Platform {
			continue
		}
		if !heartbeatFresh(now, c.LastHeartbeat) || c.InFlight >= c.MaxConcurrent {
			continue
		}
		if best < 0 || workerBefore(c, workers[best]) {
			best = i
		}
	}
	return best, best >= 0
}

func heartbeatFresh(now, last time.Time) bool {
	if last.IsZero() {
		return false
	}
	return now.Sub(last) <= HeartbeatWindow
}

func workerBefore(a, b Candidate) bool {
	if a.InFlight != b.InFlight {
		return a.InFlight < b.InFlight
	}
	if !a.IdleSince.Equal(b.IdleSince) {
		return a.IdleSince.Before(b.IdleSince)
	}
	return a.WorkerID < b.WorkerID
}
