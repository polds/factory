package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// FactoryEvent is one record on the factory-wide stream.
// GID is the exclusive cursor and the SSE id. Data is the per-run JSON
// object; WriteFactory adds run_id and gid to that object.
type FactoryEvent struct {
	GID   int64
	RunID string
	Type  string
	Data  json.RawMessage
}

// ResumeGID resolves the exclusive factory cursor.
// Last-Event-ID wins over from_seq, the same rule as a per-run stream.
func ResumeGID(r *http.Request) (int64, error) {
	return resumeCursor(r)
}

// WriteFactory writes the factory SSE stream.
// It reuses the per-run cursor and the per-run record formatter. id: is the
// gid because the formatter prints the event sequence, and the caller stores
// the gid there. The stream does not end on run.terminal: one run finishing
// is not the end of the factory log. gid values at or below the cursor are
// dropped, so a backend that replays the cursor row cannot duplicate it.
func WriteFactory(w http.ResponseWriter, r *http.Request, ch <-chan FactoryEvent) {
	from, err := resumeCursor(r)
	if err != nil {
		writeFactoryProblem(w, http.StatusBadRequest, "invalid sequence")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeFactoryProblem(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticks, stop := newTicker(heartbeatInterval)
	defer stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if ev.GID <= from {
				continue
			}
			if _, err := w.Write(formatEvent(Event{Seq: ev.GID, Type: ev.Type, Data: factoryEnvelope(ev)})); err != nil {
				return
			}
			flusher.Flush()
			from = ev.GID
		case <-ticks:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// factoryEnvelope merges run_id and gid into a per-run JSON object.
// A payload that is not a JSON object is wrapped under data.
func factoryEnvelope(ev FactoryEvent) json.RawMessage {
	fields := map[string]json.RawMessage{}
	raw := bytes.TrimSpace(ev.Data)
	merged := false
	if len(raw) > 0 && raw[0] == '{' {
		parsed := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &parsed); err == nil {
			fields = parsed
			merged = true
		}
	}
	if !merged && len(raw) > 0 {
		fields["data"] = append(json.RawMessage(nil), raw...)
	}
	rid, err := json.Marshal(ev.RunID)
	if err != nil {
		rid = []byte(`""`)
	}
	gid, err := json.Marshal(ev.GID)
	if err != nil {
		gid = []byte("0")
	}
	fields["run_id"] = rid
	fields["gid"] = gid
	out, err := json.Marshal(fields)
	if err != nil {
		return fmt.Appendf(nil, `{"run_id":"","gid":%d}`, ev.GID)
	}
	return out
}

func writeFactoryProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "{\"type\":\"about:blank\",\"title\":%q,\"status\":%d,\"detail\":%q}\n", http.StatusText(status), status, detail)
}
