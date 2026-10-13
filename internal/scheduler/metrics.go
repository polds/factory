package scheduler

import "time"

// Counters are the instrument hooks T12 exposes for docs/12 open question U3.
// time_to_assignment is the mean wait from enqueue to assignment.
// reject_rate is Rejects / Assignments (zero when nothing has been assigned).
// The scheduler does not publish them; T12 reads a Counters value it owns.
type Counters struct {
	Assignments         int64
	TimeToAssignmentSum time.Duration
	Rejects             int64
}

// RecordAssignment adds one placement and its queue wait.
func (c *Counters) RecordAssignment(wait time.Duration) {
	if c == nil {
		return
	}
	if wait < 0 {
		wait = 0
	}
	c.Assignments++
	c.TimeToAssignmentSum += wait
}

// RecordReject adds one task_rejected requeue.
func (c *Counters) RecordReject() {
	if c == nil {
		return
	}
	c.Rejects++
}

// TimeToAssignment is the mean recorded queue wait.
func (c *Counters) TimeToAssignment() time.Duration {
	if c == nil || c.Assignments == 0 {
		return 0
	}
	return c.TimeToAssignmentSum / time.Duration(c.Assignments)
}

// RejectRate is Rejects divided by Assignments.
func (c *Counters) RejectRate() float64 {
	if c == nil || c.Assignments == 0 {
		return 0
	}
	return float64(c.Rejects) / float64(c.Assignments)
}
