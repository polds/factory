package events

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// from_seq and Last-Event-ID are exclusive cursors: the stream emits events
// with seq greater than the cursor. The event at the cursor was already
// delivered. When both are present, Last-Event-ID wins so an EventSource
// reconnect does not replay history still named in the original query string.

func TestFromSeqExclusive(t *testing.T) {
	src := &memSource{evs: seqEvents(1, 5)}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "from_seq=3", nil)
	got := readN(t, rd, 2)
	if ids(got) != "4,5" {
		t.Fatalf("events = %s, want 4,5", ids(got))
	}
	if got[0].Event != "message.delta" || got[0].Data != `{"seq":4,"text":"d4"}` {
		t.Fatalf("first event %+v", got[0])
	}
}

func TestLastEventID(t *testing.T) {
	src := &memSource{evs: seqEvents(1, 5)}
	hdr := http.Header{"Last-Event-ID": []string{"4"}}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "", hdr)
	got := readN(t, rd, 1)
	if ids(got) != "5" {
		t.Fatalf("events = %s, want 5", ids(got))
	}
}

func TestLastEventIDOverridesFromSeq(t *testing.T) {
	src := &memSource{evs: seqEvents(1, 5)}
	hdr := http.Header{"Last-Event-ID": []string{"4"}}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "from_seq=1", hdr)
	got := readN(t, rd, 1)
	if ids(got) != "5" {
		t.Fatalf("events = %s, want 5 (Last-Event-ID must win)", ids(got))
	}
}

func TestPublishRacesReplayNoDuplicates(t *testing.T) {
	src := &memSource{
		evs:     seqEvents(1, 5),
		hold:    true,
		held:    make(chan struct{}),
		release: make(chan struct{}),
	}
	hub := NewHub()
	rd, st := dial(t, Handler(src, hub), "run_1", "", nil)

	select {
	case <-src.held:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not reach initial replay")
	}

	// Publish while the first EventsFrom is still in flight, after it has
	// snapshotted 1..5. A handler that subscribes after replay loses this wakeup.
	src.add(ev(6, "message.delta", `{"seq":6,"text":"d6"}`))
	src.add(ev(7, "run.terminal", `{"seq":7,"status":"succeeded"}`))
	hub.Publish("run_1")
	close(src.release)

	waitReady(t, st)
	if st.code != http.StatusOK {
		t.Fatalf("status %d", st.code)
	}
	got := readUntilEOF(t, rd, 8)
	if ids(got) != "1,2,3,4,5,6,7" {
		t.Fatalf("events = %s, want 1,2,3,4,5,6,7 with no gaps or duplicates", ids(got))
	}
	if got[6].Event != "run.terminal" {
		t.Fatalf("last event %+v", got[6])
	}
}

func TestTerminalEventClosesStream(t *testing.T) {
	src := &memSource{evs: []Event{
		ev(1, "message.delta", `{"seq":1,"text":"hi"}`),
		ev(2, "run.terminal", `{"seq":2,"status":"failed","failure_reason":"boom"}`),
	}}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "", nil)
	got := readUntilEOF(t, rd, 4)
	if ids(got) != "1,2" {
		t.Fatalf("events = %s, want 1,2 then EOF", ids(got))
	}
	if got[1].Event != "run.terminal" {
		t.Fatalf("terminal %+v", got[1])
	}
}

func TestAlreadyTerminalClosesWithoutReplay(t *testing.T) {
	src := &memSource{
		evs:      seqEvents(1, 2),
		terminal: true,
	}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "from_seq=2", nil)
	got := readUntilEOF(t, rd, 2)
	if len(got) != 0 {
		t.Fatalf("events = %s, want none then EOF", ids(got))
	}
}

func TestPublishDoesNotBlockSlowSubscriber(t *testing.T) {
	hub := NewHub()
	ch, unsub := hub.Subscribe("run_1")
	defer unsub()

	start := time.Now()
	for i := 0; i < 10000; i++ {
		hub.Publish("run_1")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Publish blocked for %s on an unread subscriber", elapsed)
	}
	select {
	case <-ch:
	default:
		t.Fatal("expected one coalesced wakeup")
	}
	select {
	case <-ch:
		t.Fatal("wakeups were queued instead of coalesced")
	default:
	}

	// A second publish after the buffer drains delivers again.
	hub.Publish("run_1")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("wakeup lost after drain")
	}
}

func TestPublishWhileHandlerWriteBlocked(t *testing.T) {
	src := &memSource{evs: []Event{ev(1, "message.delta", `{"seq":1,"text":"hi"}`)}}
	hub := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bw := &blockWriter{
		h:       make(http.Header),
		blocked: make(chan struct{}),
		release: make(chan struct{}),
		ctx:     ctx,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/runs/run_1/events", nil)
	req.SetPathValue("runId", "run_1")
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		Handler(src, hub).ServeHTTP(bw, req)
		close(done)
	}()

	select {
	case <-bw.blocked:
	case <-done:
		t.Fatal("handler returned before the client blocked")
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not reach the slow write")
	}

	start := time.Now()
	for i := 0; i < 1000; i++ {
		hub.Publish("run_1")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Publish blocked for %s while the client write was stuck", elapsed)
	}
	close(bw.release)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after the client unblocked")
	}
}

func TestHeartbeatInjectedClock(t *testing.T) {
	ticks := make(chan time.Time, 1)
	restore := setHeartbeatTicker(func(d time.Duration) (<-chan time.Time, func()) {
		if d != 15*time.Second {
			t.Errorf("heartbeat interval = %s, want 15s", d)
		}
		return ticks, func() {}
	})
	t.Cleanup(restore)

	src := &memSource{evs: []Event{ev(1, "usage", `{"seq":1,"input_tokens":3}`)}}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "", nil)
	got := readN(t, rd, 1)
	if got[0].Event != "usage" {
		t.Fatalf("event %+v", got[0])
	}

	ticks <- time.Unix(0, 0)
	if c1 := readFrame(t, rd); c1.Comment != "ping" {
		t.Fatalf("comment %+v, want ping", c1)
	}
	ticks <- time.Unix(15, 0)
	if c2 := readFrame(t, rd); c2.Comment != "ping" {
		t.Fatalf("comment %+v, want ping", c2)
	}
}

func TestHeadersAndFlushPerEvent(t *testing.T) {
	src := &memSource{
		evs: []Event{
			ev(1, "tool.call", `{"seq":1,"tool":"git","redacted_input":"status"}`),
			ev(2, "tool.result", `{"seq":2,"tool":"git","ok":true}`),
		},
		terminal: true,
	}
	rec := &flushRec{h: make(http.Header)}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/runs/run_1/events?from_seq=0", nil)
	req.SetPathValue("runId", "run_1")
	Handler(src, NewHub()).ServeHTTP(rec, req)

	if rec.code != http.StatusOK {
		t.Fatalf("status %d", rec.code)
	}
	if got := rec.h.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type %q", got)
	}
	if got := rec.h.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control %q", got)
	}
	sawPartial := false
	for _, snap := range rec.snaps {
		has1 := strings.Contains(snap, "id: 1\n")
		has2 := strings.Contains(snap, "id: 2\n")
		if has1 && !has2 {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Fatalf("events were not flushed individually: %#v", rec.snaps)
	}
	if !strings.Contains(rec.buf.String(), "event: tool.call\n") || !strings.Contains(rec.buf.String(), "event: tool.result\n") {
		t.Fatalf("body %q", rec.buf.String())
	}
}

func TestReplayPagesUntilCaughtUp(t *testing.T) {
	prev := pageSize
	pageSize = 2
	t.Cleanup(func() { pageSize = prev })

	src := &memSource{evs: seqEvents(1, 5), terminal: true}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "from_seq=0", nil)
	got := readUntilEOF(t, rd, 8)
	if ids(got) != "1,2,3,4,5" {
		t.Fatalf("events = %s, want 1,2,3,4,5", ids(got))
	}
}

func TestLiveFanout(t *testing.T) {
	src := &memSource{}
	hub := NewHub()
	h := Handler(src, hub)
	rd1 := openStream(t, h, "run_1", "", nil)
	rd2 := openStream(t, h, "run_1", "", nil)

	// Both handlers have subscribed and completed their empty replay once
	// EventsFrom has been entered twice.
	deadline := time.Now().Add(2 * time.Second)
	for src.reads() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("readers = %d, want 2", src.reads())
		}
		time.Sleep(time.Millisecond)
	}

	src.add(ev(1, "message.delta", `{"seq":1,"text":"live"}`))
	hub.Publish("run_1")

	for _, rd := range []*bufio.Reader{rd1, rd2} {
		got := readN(t, rd, 1)
		if ids(got) != "1" || got[0].Data != `{"seq":1,"text":"live"}` {
			t.Fatalf("fan-out event %+v", got)
		}
	}
}

func TestInvalidCursorAndMissingRun(t *testing.T) {
	h := Handler(&memSource{}, NewHub())
	for _, q := range []string{"from_seq=nope", "from_seq=-1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/runs/run_1/events?"+q, nil)
		req.SetPathValue("runId", "run_1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status %d", q, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing run id status %d", rec.Code)
	}
}

func TestMultilineData(t *testing.T) {
	src := &memSource{evs: []Event{
		{Seq: 1, Type: "message.delta", Data: json.RawMessage("{\"text\":\"a\"}\n{\"text\":\"b\"}")},
	}, terminal: true}
	rd := openStream(t, Handler(src, NewHub()), "run_1", "", nil)
	got := readUntilEOF(t, rd, 2)
	if len(got) != 1 || got[0].Data != "{\"text\":\"a\"}\n{\"text\":\"b\"}" {
		t.Fatalf("%+v", got)
	}
}

func TestHubUnsubscribeAndForeignRun(t *testing.T) {
	hub := NewHub()
	ch, unsub := hub.Subscribe("run_1")
	unsub()
	unsub() // idempotent
	hub.Publish("run_1")
	hub.Publish("other")
	select {
	case <-ch:
		t.Fatal("received wakeup after unsubscribe")
	default:
	}
}

type sseMsg struct {
	ID      string
	Event   string
	Data    string
	Comment string
}

func seqEvents(from, to int64) []Event {
	out := make([]Event, 0, to-from+1)
	for n := from; n <= to; n++ {
		s := strconv.FormatInt(n, 10)
		out = append(out, ev(n, "message.delta", `{"seq":`+s+`,"text":"d`+s+`"}`))
	}
	return out
}

func ev(seq int64, typ, data string) Event {
	return Event{Seq: seq, Type: typ, Data: json.RawMessage(data)}
}

func ids(msgs []sseMsg) string {
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.ID
	}
	return strings.Join(parts, ",")
}

func openStream(t *testing.T, h http.Handler, runID, query string, hdr http.Header) *bufio.Reader {
	t.Helper()
	rd, st := dial(t, h, runID, query, hdr)
	waitReady(t, st)
	if st.code != http.StatusOK {
		t.Fatalf("status %d", st.code)
	}
	if got := st.snap.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type %q", got)
	}
	if got := st.snap.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control %q", got)
	}
	return rd
}

// dial runs the handler against an in-process pipe. The reader blocks until
// the handler writes, and closing the pipe on cleanup unblocks both sides.
func dial(t *testing.T, h http.Handler, runID, query string, hdr http.Header) (*bufio.Reader, *statusOnce) {
	t.Helper()
	pr, pw := io.Pipe()
	st := &statusOnce{h: make(http.Header), w: pw, ready: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	target := "/api/v1/agent/runs/" + runID + "/events"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("runId", runID)
	req = req.WithContext(ctx)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(st, req)
		_ = pw.Close()
	}()
	t.Cleanup(func() {
		cancel()
		_ = pr.Close()
		<-done
	})
	return bufio.NewReader(pr), st
}

func waitReady(t *testing.T, st *statusOnce) {
	t.Helper()
	select {
	case <-st.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("headers never arrived")
	}
}

type statusOnce struct {
	h     http.Header
	w     io.Writer
	mu    sync.Mutex
	code  int
	snap  http.Header
	wrote bool
	ready chan struct{}
}

func (s *statusOnce) Header() http.Header { return s.h }

func (s *statusOnce) WriteHeader(code int) { s.mark(code) }

func (s *statusOnce) Write(p []byte) (int, error) {
	s.mark(http.StatusOK)
	return s.w.Write(p)
}

func (s *statusOnce) Flush() {}

func (s *statusOnce) mark(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wrote {
		return
	}
	s.wrote = true
	s.code = code
	s.snap = s.h.Clone()
	close(s.ready)
}

func readN(t *testing.T, r *bufio.Reader, n int) []sseMsg {
	t.Helper()
	out := make([]sseMsg, 0, n)
	for len(out) < n {
		msg, err := readOne(r, 2*time.Second)
		if err != nil {
			t.Fatalf("event %d: %v", len(out), err)
		}
		if msg.Comment != "" && msg.ID == "" && msg.Event == "" && msg.Data == "" {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func readFrame(t *testing.T, r *bufio.Reader) sseMsg {
	t.Helper()
	msg, err := readOne(r, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func readUntilEOF(t *testing.T, r *bufio.Reader, limit int) []sseMsg {
	t.Helper()
	var out []sseMsg
	for len(out) < limit {
		msg, err := readOne(r, 2*time.Second)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return out
			}
			t.Fatal(err)
		}
		if msg.Comment != "" && msg.ID == "" && msg.Event == "" && msg.Data == "" {
			continue
		}
		out = append(out, msg)
	}
	t.Fatalf("stream did not close after %d events", limit)
	return nil
}

func readOne(r *bufio.Reader, timeout time.Duration) (sseMsg, error) {
	type result struct {
		msg sseMsg
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := parseSSE(r)
		ch <- result{msg, err}
	}()
	select {
	case res := <-ch:
		return res.msg, res.err
	case <-time.After(timeout):
		return sseMsg{}, errors.New("timed out waiting for SSE event")
	}
}

func parseSSE(r *bufio.Reader) (sseMsg, error) {
	var msg sseMsg
	var data []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return sseMsg{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			msg.Data = strings.Join(data, "\n")
			return msg, nil
		}
		if strings.HasPrefix(line, ":") {
			msg.Comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			msg.ID = value
		case "event":
			msg.Event = value
		case "data":
			data = append(data, value)
		default:
		}
	}
}

type memSource struct {
	mu       sync.Mutex
	evs      []Event
	terminal bool
	readN    int

	hold     bool
	holdOnce sync.Once
	held     chan struct{}
	release  chan struct{}
}

func (m *memSource) EventsFrom(_ context.Context, _ string, fromSeq int64, limit int) ([]Event, error) {
	m.mu.Lock()
	out := make([]Event, 0)
	for _, e := range m.evs {
		if e.Seq <= fromSeq {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	m.readN++
	m.mu.Unlock()

	var block bool
	if m.hold {
		m.holdOnce.Do(func() { block = true })
	}
	if block {
		close(m.held)
		<-m.release
	}
	return out, nil
}

func (m *memSource) Terminal(_ context.Context, _ string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terminal, nil
}

func (m *memSource) add(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, e)
}

func (m *memSource) reads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readN
}

type flushRec struct {
	h     http.Header
	code  int
	wrote bool
	buf   bytes.Buffer
	snaps []string
}

func (f *flushRec) Header() http.Header { return f.h }

func (f *flushRec) WriteHeader(code int) {
	if f.wrote {
		return
	}
	f.wrote = true
	f.code = code
}

func (f *flushRec) Write(p []byte) (int, error) {
	if !f.wrote {
		f.WriteHeader(http.StatusOK)
	}
	return f.buf.Write(p)
}

func (f *flushRec) Flush() {
	f.snaps = append(f.snaps, f.buf.String())
}

type blockWriter struct {
	h       http.Header
	mu      sync.Mutex
	buf     bytes.Buffer
	code    int
	wrote   bool
	blocked chan struct{}
	release chan struct{}
	ctx     context.Context
	once    sync.Once
}

func (b *blockWriter) Header() http.Header { return b.h }

func (b *blockWriter) WriteHeader(code int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wrote {
		return
	}
	b.wrote = true
	b.code = code
}

func (b *blockWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	if !b.wrote {
		b.wrote = true
		b.code = http.StatusOK
	}
	n, err := b.buf.Write(p)
	ready := bytes.Contains(b.buf.Bytes(), []byte("\n\n"))
	b.mu.Unlock()
	if err != nil {
		return n, err
	}
	if ready {
		b.once.Do(func() { close(b.blocked) })
		select {
		case <-b.release:
		case <-b.ctx.Done():
			return n, b.ctx.Err()
		}
	}
	return n, nil
}

func (b *blockWriter) Flush() {}
