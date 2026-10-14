// Package events streams a run's append-only log over server-sent events.
//
// Reconnection is exact. from_seq and the Last-Event-ID header are exclusive
// cursors: the stream emits events with seq greater than the cursor, so the
// event already delivered is not sent again. When both are present,
// Last-Event-ID wins. That matches an EventSource reconnect, which repeats the
// original query string and sends the id of the last event it actually saw.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// heartbeatInterval is how often a waiting stream writes an SSE comment.
// Proxies and clients treat the comment as a keepalive.
const heartbeatInterval = 15 * time.Second

// pageSize is the maximum number of events read from Source per query.
// The handler pages until it is caught up, then waits for the next wakeup.
// Tests shrink this to prove the loop.
var pageSize = 256

// newTicker starts the heartbeat clock. Tests replace it to inject ticks
// without waiting out heartbeatInterval.
var newTicker = func(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// setHeartbeatTicker replaces the heartbeat clock and returns a restore func.
func setHeartbeatTicker(fn func(time.Duration) (<-chan time.Time, func())) func() {
	prev := newTicker
	newTicker = fn
	return func() { newTicker = prev }
}

// Event is one record in a run's sequenced log.
//
// Seq is the per-run monotonic id (1-based). Type is the SSE event name
// (message.delta, tool.call, run.terminal, …). Data is the JSON object
// written on the data field; it is not re-encoded.
type Event struct {
	Seq  int64
	Type string
	Data json.RawMessage
}

// Source is the read side of the run log. EventsFrom returns events with
// seq > fromSeq in ascending order, at most limit of them (limit <= 0 means
// the source chooses). Terminal reports whether the run has already reached
// a terminal status, including when the terminal event itself is at or behind
// the caller's cursor.
type Source interface {
	EventsFrom(ctx context.Context, runID string, fromSeq int64, limit int) ([]Event, error)
	Terminal(ctx context.Context, runID string) (bool, error)
}

// Notifier wakes subscribers when a run's log grows. The channel has a
// coalesced buffer: a slow consumer drops intermediate wakeups, and the next
// read of the log fills any gap. The func unsubscribes and is idempotent.
type Notifier interface {
	Subscribe(runID string) (<-chan struct{}, func())
}

// terminalType is the event that ends a stream after it has been written.
const terminalType = "run.terminal"

// Handler is the SSE endpoint for one run.
//
// Mount it so the run id is available as the path value "runId", for example
// GET /api/v1/agent/runs/{runId}/events. The handler subscribes before the
// initial replay, then on every wakeup reads again from the last seq it
// sent. A slow client cannot block publishers: wakeups are coalesced, and
// writing the response happens only on the request goroutine.
func Handler(src Source, n Notifier) http.Handler {
	return &handler{src: src, notify: n}
}

type handler struct {
	src    Source
	notify Notifier
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := r.PathValue("runId")
	if runID == "" {
		http.Error(w, "missing run id", http.StatusBadRequest)
		return
	}
	cursor, err := resumeCursor(r)
	if err != nil {
		http.Error(w, "invalid sequence", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	wakes, unsub := h.notify.Subscribe(runID)
	defer unsub()

	limit := pageSize
	if limit <= 0 {
		limit = 256
	}
	ticks, stop := newTicker(heartbeatInterval)
	defer stop()

	last := cursor
	headersSent := false
	sendHeaders := func() {
		if headersSent {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		headersSent = true
		flusher.Flush()
	}

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		start := last
		evs, err := h.src.EventsFrom(ctx, runID, last, limit)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !headersSent {
				http.Error(w, "event source unavailable", http.StatusInternalServerError)
			}
			return
		}
		sendHeaders()

		page := append([]Event(nil), evs...)
		sort.Slice(page, func(i, j int) bool { return page[i].Seq < page[j].Seq })
		for _, ev := range page {
			if ev.Seq <= last {
				continue
			}
			if _, err := w.Write(formatEvent(ev)); err != nil {
				return
			}
			flusher.Flush()
			last = ev.Seq
			if ev.Type == terminalType {
				return
			}
		}
		if len(evs) >= limit && last > start {
			continue
		}

		// A publish during the read sits in the coalesced buffer. Drain it
		// before deciding the run is finished so that event is not skipped.
		if consumed(wakes) {
			continue
		}
		done, err := h.src.Terminal(ctx, runID)
		if err != nil {
			if ctx.Err() != nil || headersSent {
				return
			}
			http.Error(w, "event source unavailable", http.StatusInternalServerError)
			return
		}
		if consumed(wakes) {
			continue
		}
		if done {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-wakes:
		case <-ticks:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// consumed reports whether a coalesced wakeup is pending.
func consumed(wakes <-chan struct{}) bool {
	select {
	case <-wakes:
		return true
	default:
		return false
	}
}

// resumeCursor resolves the exclusive sequence cursor.
// Last-Event-ID overrides from_seq when the header is non-empty.
func resumeCursor(r *http.Request) (int64, error) {
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		return parseSeq(id)
	}
	if q := r.URL.Query().Get("from_seq"); q != "" {
		return parseSeq(q)
	}
	return 0, nil
}

func parseSeq(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid sequence")
	}
	return n, nil
}

// formatEvent renders one SSE record: id, event, data, blank line.
// Data is written as the client stored it. Newlines become extra data lines
// so the payload round-trips through an SSE parser.
func formatEvent(ev Event) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "id: %d\nevent: %s\n", ev.Seq, ev.Type)
	data := ev.Data
	if len(bytes.TrimSpace(data)) == 0 {
		data = fmt.Appendf(nil, `{"seq":%d}`, ev.Seq)
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	for {
		line, rest, found := bytes.Cut(data, []byte("\n"))
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
		if !found {
			break
		}
		data = rest
	}
	b.WriteByte('\n')
	return b.Bytes()
}
