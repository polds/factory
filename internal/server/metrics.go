package server

import (
	"fmt"
	"net/http"
)

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s.countersMu.Lock()
	assignments := s.counters.Assignments
	rejects := s.counters.Rejects
	mean := s.counters.TimeToAssignment()
	rate := s.counters.RejectRate()
	s.countersMu.Unlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, `# HELP factory_time_to_assignment_seconds Mean seconds from enqueue to assignment.
# TYPE factory_time_to_assignment_seconds gauge
factory_time_to_assignment_seconds %f
# HELP factory_reject_rate Ratio of task_rejected to assignments.
# TYPE factory_reject_rate gauge
factory_reject_rate %f
# TYPE factory_assignments_total counter
factory_assignments_total %d
# TYPE factory_rejects_total counter
factory_rejects_total %d
`, mean.Seconds(), rate, assignments, rejects)
}
