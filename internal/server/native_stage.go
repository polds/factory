package server

import "context"

// refuseChildAdvance is true when this run is a native child.
// Only the stage owner may move the work item. A lookup error fails closed.
func (s *Server) refuseChildAdvance(ctx context.Context, runID string) bool {
	if s.store == nil || runID == "" {
		return false
	}
	may, known, err := s.store.NativeRunMayAdvance(ctx, runID)
	if err != nil {
		if s.log != nil {
			s.log.Error("stage owner", "run_id", runID, "err", err)
		}
		return true
	}
	if !known {
		return false
	}
	return !may
}
