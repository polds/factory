package scheduler

import (
	"math/rand/v2"
	"time"
)

// Sim is a deterministic in-memory run queue. It applies the same transitions
// as the store contract (lease, start, reject/requeue, lease expiry, idempotent
// terminal, cancel) and checks that no run is lost or double-run.
type Sim struct {
	Seed     int64
	Counters Counters
}

// Run advances the simulation steps times. A non-nil error names the seed.
func (s Sim) Run(steps int) error {
	if steps < 0 {
		steps = 0
	}
	w := newWorld(s.Seed, &s.Counters)
	for step := 0; step < steps; step++ {
		w.reconnect()
		if err := w.act(w.rng.IntN(actionCount)); err != nil {
			return &simFail{seed: s.Seed, step: step, msg: err.Error()}
		}
		if err := w.check(); err != nil {
			return &simFail{seed: s.Seed, step: step, msg: err.Error()}
		}
		w.now = w.now.Add(time.Second)
	}
	w.now = w.now.Add(time.Hour)
	w.expire(w.now)
	if err := w.check(); err != nil {
		return &simFail{seed: s.Seed, step: steps, msg: err.Error()}
	}
	if err := w.checkDrained(); err != nil {
		return &simFail{seed: s.Seed, step: steps, msg: err.Error()}
	}
	return nil
}

const (
	actionEnqueue = iota
	actionSchedule
	actionReject
	actionTimeout
	actionDisconnect
	actionDuplicateTerminal
	actionLateTerminal
	actionCancel
	actionCount
)

const leaseTTL = 30 * time.Second

type runPhase int

const (
	phaseQueued runPhase = iota
	phaseAssigned
	phaseRunning
	phaseSucceeded
	phaseFailed
	phaseCancelled
)

type execPhase int

const (
	execAssigned execPhase = iota
	execRunning
	execCompleted
	execFailed
	execOrphaned
	execCancelled
)

type execution struct {
	ID       string
	WorkerID string
	State    execPhase
}

type runRec struct {
	Pending    Pending
	Status     runPhase
	Execs      []execution
	LeaseUntil time.Time
	Attempts   int
	Terminals  int
}

type simWorker struct {
	base      Candidate
	heartbeat time.Time
	idleSince time.Time
	idle      bool
}

type world struct {
	now      time.Time
	rng      *rand.Rand
	runs     map[string]*runRec
	order    []string
	workers  []simWorker
	caps     Caps
	counters *Counters
	nextExec int
	nextRun  int
	fault    error
}

func newWorld(seed int64, counters *Counters) *world {
	now := time.Unix(1_700_000_000, 0).UTC()
	w := &world{
		now:      now,
		rng:      rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15)),
		runs:     make(map[string]*runRec),
		caps:     Caps{DefaultPerFactory: 3, DefaultPerAgent: 2},
		counters: counters,
		workers: []simWorker{
			{base: Candidate{WorkerID: "w0", Pool: "default", Platform: "linux", MaxConcurrent: 2}},
			{base: Candidate{WorkerID: "w1", Pool: "default", Platform: "linux", MaxConcurrent: 2}},
			{base: Candidate{WorkerID: "w2", Pool: "default", Platform: "darwin", MaxConcurrent: 1}},
			{base: Candidate{WorkerID: "w3", Pool: "gpu", Platform: "linux", MaxConcurrent: 1}},
		},
	}
	for i := range w.workers {
		w.workers[i].heartbeat = now
		w.workers[i].idleSince = now
		w.workers[i].idle = true
	}
	return w
}

func (w *world) reconnect() {
	for i := range w.workers {
		w.workers[i].heartbeat = w.now
	}
}

func (w *world) act(kind int) error {
	switch kind {
	case actionEnqueue:
		w.enqueue(w.rng, false)
	case actionSchedule:
		if err := w.schedule(); err != nil {
			return err
		}
	case actionReject:
		if err := w.reject(); err != nil {
			return err
		}
	case actionTimeout:
		w.timeout()
	case actionDisconnect:
		w.disconnect()
	case actionDuplicateTerminal:
		if err := w.duplicateTerminal(); err != nil {
			return err
		}
	case actionLateTerminal:
		if err := w.lateTerminal(); err != nil {
			return err
		}
	case actionCancel:
		w.cancel()
	default:
		return errStr("unknown action " + itoa(int64(kind)))
	}
	return nil
}

func (w *world) enqueue(rng *rand.Rand, force bool) *runRec {
	if !force && w.nonTerminal() >= 24 {
		return nil
	}
	id := "run_" + itoa(int64(w.nextRun))
	w.nextRun++
	factories := []string{"fac-a", "fac-b"}
	agents := []string{"agt-1", "agt-2"}
	pools := []string{"default", "default", "default", "gpu"}
	platforms := []string{"linux", "linux", "linux", "darwin"}
	classes := []Class{Interactive, Pipeline, Batch}
	p := Pending{
		RunID:      id,
		FactoryID:  factories[rng.IntN(len(factories))],
		AgentID:    agents[rng.IntN(len(agents))],
		Pool:       "default",
		Platform:   "linux",
		Priority:   classes[rng.IntN(len(classes))],
		EnqueuedAt: w.now,
	}
	if !force {
		p.Pool = pools[rng.IntN(len(pools))]
		p.Platform = platforms[rng.IntN(len(platforms))]
	} else {
		p.FactoryID = factories[rng.IntN(len(factories))]
		p.AgentID = "agt-f" + itoa(int64(w.nextRun))
		p.Priority = Interactive
	}
	r := &runRec{Pending: p, Status: phaseQueued}
	w.runs[id] = r
	w.order = append(w.order, id)
	return r
}

func (w *world) schedule() error {
	pending, running := w.snapshot()
	seen := make(map[string]struct{})
	for _, a := range Plan(w.now, pending, w.candidates(), running, w.caps) {
		if _, dup := seen[a.RunID]; dup {
			return errStr("plan assigned " + a.RunID + " twice")
		}
		seen[a.RunID] = struct{}{}
		r := w.runs[a.RunID]
		if r == nil || r.Status != phaseQueued {
			return errStr("plan assigned " + a.RunID + " which is not queued")
		}
		w.start(r, a.WorkerID, w.rng.IntN(2) == 0)
		w.counters.RecordAssignment(w.now.Sub(r.Pending.EnqueuedAt))
	}
	return nil
}

func (w *world) start(r *runRec, workerID string, claim bool) {
	r.LeaseUntil = w.now.Add(leaseTTL)
	st := execAssigned
	r.Status = phaseAssigned
	if claim {
		st = execRunning
		r.Status = phaseRunning
	}
	r.Execs = append(r.Execs, execution{
		ID:       "exec_" + itoa(int64(w.nextExec)),
		WorkerID: workerID,
		State:    st,
	})
	w.nextExec++
}

func (w *world) reject() error {
	r := w.pick(func(r *runRec) bool { return r.Status == phaseAssigned })
	if r == nil {
		w.forceLeased(false)
		r = w.pick(func(r *runRec) bool { return r.Status == phaseAssigned })
	}
	if r == nil {
		return nil
	}
	// Close the attempt without a failure record (store Requeue). cancelled
	// is the non-failed closed execution state; the run itself returns to queued.
	if e := r.active(); e != nil {
		e.State = execCancelled
	}
	r.Status = phaseQueued
	r.Attempts++
	r.LeaseUntil = time.Time{}
	w.counters.RecordReject()
	return nil
}

func (w *world) timeout() {
	r := w.pick(w.isLeased)
	if r == nil {
		w.forceLeased(false)
		r = w.pick(w.isLeased)
	}
	if r == nil {
		return
	}
	if w.now.Before(r.LeaseUntil) {
		w.now = r.LeaseUntil
	}
	w.expire(w.now)
}

func (w *world) disconnect() {
	r := w.pick(w.isLeased)
	if r == nil {
		w.forceLeased(true)
		r = w.pick(w.isLeased)
	}
	if r == nil {
		return
	}
	e := r.active()
	if e == nil {
		return
	}
	for i := range w.workers {
		if w.workers[i].base.WorkerID == e.WorkerID {
			w.workers[i].heartbeat = w.now.Add(-HeartbeatWindow - time.Second)
		}
	}
	r.LeaseUntil = w.now
	w.expire(w.now)
}

func (w *world) duplicateTerminal() error {
	r := w.pick(w.isLeased)
	if r == nil {
		w.forceLeased(true)
		r = w.pick(w.isLeased)
	}
	if r == nil {
		return nil
	}
	e := r.active()
	if e == nil {
		return nil
	}
	if r.Status == phaseAssigned {
		r.Status = phaseRunning
		e.State = execRunning
	}
	ok := w.rng.IntN(2) == 0
	if !w.finish(e.ID, ok) {
		return errStr("first terminal was not applied")
	}
	status := r.Status
	if w.finish(e.ID, !ok) {
		return errStr("duplicate terminal was applied")
	}
	if r.Status != status || r.Terminals != 1 {
		return errStr("duplicate terminal changed the run")
	}
	return nil
}

func (w *world) lateTerminal() error {
	r := w.pick(w.isLeased)
	if r == nil {
		w.forceLeased(true)
		r = w.pick(w.isLeased)
	}
	if r == nil {
		return nil
	}
	e := r.active()
	if e == nil {
		return nil
	}
	execID := e.ID
	r.LeaseUntil = w.now
	w.expire(w.now)
	if r.Status != phaseQueued {
		return errStr("lease expiry did not requeue the run")
	}
	status := r.Status
	attempts := r.Attempts
	terminals := r.Terminals
	if w.finish(execID, true) {
		return errStr("late terminal for an orphaned execution was applied")
	}
	if r.Status != status || r.Attempts != attempts || r.Terminals != terminals {
		return errStr("late terminal changed run status")
	}
	return nil
}

func (w *world) cancel() {
	r := w.pick(func(r *runRec) bool {
		return r.Status == phaseQueued || r.Status == phaseAssigned || r.Status == phaseRunning
	})
	if r == nil {
		r = w.enqueue(w.rng, true)
	}
	if r == nil {
		return
	}
	switch r.Status {
	case phaseQueued:
		r.Status = phaseCancelled
		r.Terminals++
	case phaseAssigned, phaseRunning:
		if e := r.active(); e != nil {
			e.State = execCancelled
		}
		r.Status = phaseCancelled
		r.Terminals++
		r.LeaseUntil = time.Time{}
	case phaseSucceeded, phaseFailed, phaseCancelled:
	default:
		panic(r.Status)
	}
}

func (w *world) snapshot() (pending, running []Pending) {
	for _, id := range w.order {
		r := w.runs[id]
		switch r.Status {
		case phaseQueued:
			pending = append(pending, r.Pending)
		case phaseAssigned, phaseRunning:
			running = append(running, r.Pending)
		case phaseSucceeded, phaseFailed, phaseCancelled:
		default:
			panic(r.Status)
		}
	}
	return pending, running
}

func (w *world) forceLeased(claim bool) {
	if w.pick(w.isLeased) != nil {
		return
	}
	w.enqueue(w.rng, true)
	pending, running := w.snapshot()
	seen := make(map[string]struct{})
	for _, a := range Plan(w.now, pending, w.candidates(), running, w.caps) {
		if _, dup := seen[a.RunID]; dup {
			w.fault = errStr("plan assigned " + a.RunID + " twice")
			return
		}
		seen[a.RunID] = struct{}{}
		r := w.runs[a.RunID]
		if r == nil || r.Status != phaseQueued {
			w.fault = errStr("plan assigned " + a.RunID + " which is not queued")
			return
		}
		w.start(r, a.WorkerID, claim)
		w.counters.RecordAssignment(w.now.Sub(r.Pending.EnqueuedAt))
		if !claim {
			return
		}
	}
}

func (w *world) expire(now time.Time) {
	for _, id := range w.order {
		r := w.runs[id]
		if r.Status != phaseAssigned && r.Status != phaseRunning {
			continue
		}
		if now.Before(r.LeaseUntil) {
			continue
		}
		if e := r.active(); e != nil {
			e.State = execOrphaned
		}
		r.Status = phaseQueued
		r.LeaseUntil = time.Time{}
	}
}

func (w *world) finish(execID string, ok bool) bool {
	for _, id := range w.order {
		r := w.runs[id]
		for i := range r.Execs {
			e := &r.Execs[i]
			if e.ID != execID {
				continue
			}
			switch e.State {
			case execAssigned, execRunning:
				if ok {
					e.State = execCompleted
					r.Status = phaseSucceeded
				} else {
					e.State = execFailed
					r.Status = phaseFailed
				}
				r.Terminals++
				r.LeaseUntil = time.Time{}
				return true
			case execCompleted, execFailed, execOrphaned, execCancelled:
				return false
			default:
				panic(e.State)
			}
		}
	}
	return false
}

func (w *world) candidates() []Candidate {
	out := make([]Candidate, len(w.workers))
	for i, wk := range w.workers {
		c := wk.base
		c.LastHeartbeat = wk.heartbeat
		c.IdleSince = wk.idleSince
		c.InFlight = w.inflight(c.WorkerID)
		if c.InFlight == 0 {
			if !w.workers[i].idle {
				w.workers[i].idle = true
				w.workers[i].idleSince = w.now
				c.IdleSince = w.now
			}
		} else {
			w.workers[i].idle = false
		}
		out[i] = c
	}
	return out
}

func (w *world) inflight(workerID string) int {
	n := 0
	for _, id := range w.order {
		r := w.runs[id]
		if e := r.active(); e != nil && e.WorkerID == workerID {
			n++
		}
	}
	return n
}

func (w *world) activeCount(r *runRec) (n int, state execPhase) {
	for i := range r.Execs {
		st := r.Execs[i].State
		if st == execAssigned || st == execRunning {
			n++
			state = st
		}
	}
	return n, state
}

func (r *runRec) active() *execution {
	for i := range r.Execs {
		st := r.Execs[i].State
		if st == execAssigned || st == execRunning {
			return &r.Execs[i]
		}
	}
	return nil
}

func (w *world) isLeased(r *runRec) bool {
	return r.Status == phaseAssigned || r.Status == phaseRunning
}

func (w *world) pick(pred func(*runRec) bool) *runRec {
	n := 0
	var chosen *runRec
	for _, id := range w.order {
		r := w.runs[id]
		if !pred(r) {
			continue
		}
		n++
		if w.rng.IntN(n) == 0 {
			chosen = r
		}
	}
	return chosen
}

func (w *world) nonTerminal() int {
	n := 0
	for _, id := range w.order {
		switch w.runs[id].Status {
		case phaseQueued, phaseAssigned, phaseRunning:
			n++
		case phaseSucceeded, phaseFailed, phaseCancelled:
		default:
			panic(w.runs[id].Status)
		}
	}
	return n
}

func (w *world) check() error {
	if w.fault != nil {
		return w.fault
	}
	if len(w.runs) != len(w.order) {
		return errStr("run set size does not match creation order")
	}
	seen := make(map[string]struct{}, len(w.order))
	for _, id := range w.order {
		if _, dup := seen[id]; dup {
			return errStr("run id " + id + " listed twice")
		}
		seen[id] = struct{}{}
		r := w.runs[id]
		if r == nil {
			return errStr("run " + id + " is missing")
		}
		if r.Pending.RunID != id {
			return errStr("run " + id + " lost its identity")
		}
		active, activeState := w.activeCount(r)
		if active > 1 {
			return errStr("run " + id + " has two non-orphaned executions")
		}
		if err := uniqueExecIDs(r); err != nil {
			return err
		}
		if r.Terminals > 1 {
			return errStr("run " + id + " accepted more than one terminal")
		}
		switch r.Status {
		case phaseQueued:
			if active != 0 || r.Terminals != 0 {
				return errStr("run " + id + " queued state is inconsistent")
			}
		case phaseAssigned:
			if active != 1 || activeState != execAssigned || r.Terminals != 0 {
				return errStr("run " + id + " assigned state is inconsistent")
			}
		case phaseRunning:
			if active != 1 || activeState != execRunning || r.Terminals != 0 {
				return errStr("run " + id + " running state is inconsistent")
			}
		case phaseSucceeded:
			if active != 0 || r.Terminals != 1 || !w.hasExec(r, execCompleted) {
				return errStr("run " + id + " succeeded state is inconsistent")
			}
		case phaseFailed:
			if active != 0 || r.Terminals != 1 || !w.hasExec(r, execFailed) {
				return errStr("run " + id + " failed state is inconsistent")
			}
		case phaseCancelled:
			if active != 0 || r.Terminals != 1 {
				return errStr("run " + id + " cancelled state is inconsistent")
			}
		default:
			panic(r.Status)
		}
	}
	return nil
}

func uniqueExecIDs(r *runRec) error {
	seen := make(map[string]struct{}, len(r.Execs))
	for i := range r.Execs {
		id := r.Execs[i].ID
		if _, dup := seen[id]; dup {
			return errStr("run " + r.Pending.RunID + " reused execution id " + id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (w *world) hasExec(r *runRec, st execPhase) bool {
	for i := range r.Execs {
		if r.Execs[i].State == st {
			return true
		}
	}
	return false
}

func (w *world) checkDrained() error {
	for _, id := range w.order {
		switch w.runs[id].Status {
		case phaseQueued, phaseSucceeded, phaseFailed, phaseCancelled:
		case phaseAssigned, phaseRunning:
			return errStr("run " + id + " was neither queued nor terminal after lease expiry")
		default:
			panic(w.runs[id].Status)
		}
	}
	return nil
}

type simFail struct {
	seed int64
	step int
	msg  string
}

func (f *simFail) Error() string {
	return "seed " + itoa(f.seed) + " step " + itoa(int64(f.step)) + ": " + f.msg
}

type strErr string

func (e strErr) Error() string { return string(e) }

func errStr(msg string) error { return strErr(msg) }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
