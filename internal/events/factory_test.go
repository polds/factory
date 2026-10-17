package events

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFactoryCursorMatchesPerRun(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/events?from_seq=1", nil)
	req.Header.Set("Last-Event-ID", "4")
	got, err := ResumeGID(req)
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 {
		t.Fatalf("cursor %d, want 4 (Last-Event-ID wins)", got)
	}
	req = httptest.NewRequest(http.MethodGet, "/events?from_seq=-1", nil)
	if _, err := ResumeGID(req); err == nil {
		t.Fatal("negative from_seq was accepted")
	}
}

func TestFactoryExclusiveAndTerminalStaysOpen(t *testing.T) {
	ch := make(chan FactoryEvent, 4)
	ch <- FactoryEvent{GID: 2, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":2}`)}
	ch <- FactoryEvent{GID: 3, RunID: "run_1", Type: "run.terminal", Data: json.RawMessage(`{"seq":3,"status":"failed"}`)}
	ch <- FactoryEvent{GID: 4, RunID: "run_2", Type: "message.delta", Data: json.RawMessage(`{"seq":1,"text":"next"}`)}
	close(ch)

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Last-Event-ID", "2")
	rr := httptest.NewRecorder()
	WriteFactory(rr, req, ch)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	msgs := parseFactorySSE(t, rr.Body.Bytes())
	if ids := factoryIDs(msgs); ids != "3,4" {
		t.Fatalf("ids %s, want 3,4 (cursor row dropped, stream stayed open after terminal)", ids)
	}
	if msgs[0].Event != "run.terminal" || msgs[1].Event != "message.delta" {
		t.Fatalf("events %+v", msgs)
	}
}

func TestFactoryEnvelopeAddsRunAndGID(t *testing.T) {
	ev := FactoryEvent{
		GID:   10,
		RunID: "run_1",
		Type:  "message.delta",
		Data:  json.RawMessage(`{"seq":4,"turn":1,"role":"assistant","text":"hello","run_id":"planted"}`),
	}
	ch := make(chan FactoryEvent, 1)
	ch <- ev
	close(ch)
	rr := httptest.NewRecorder()
	WriteFactory(rr, httptest.NewRequest(http.MethodGet, "/events", nil), ch)
	msgs := parseFactorySSE(t, rr.Body.Bytes())
	if len(msgs) != 1 || msgs[0].ID != "10" || msgs[0].Event != "message.delta" {
		t.Fatalf("%+v", msgs)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(msgs[0].Data), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["run_id"] != "run_1" || obj["gid"] != float64(10) || obj["text"] != "hello" {
		t.Fatalf("envelope %+v", obj)
	}
}

func TestFactoryReconnectNoGap(t *testing.T) {
	log := []FactoryEvent{
		{GID: 1, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":1}`)},
		{GID: 2, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":2}`)},
		{GID: 3, RunID: "run_1", Type: "message.delta", Data: json.RawMessage(`{"seq":3}`)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan FactoryEvent)
	prod := make(chan struct{})
	go func() {
		defer close(prod)
		defer close(ch)
		for _, ev := range log[:2] {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
		<-ctx.Done()
	}()
	pr, pw := io.Pipe()
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = pw.Close() }()
		rw := &statusOnce{h: make(http.Header), w: pw, ready: make(chan struct{})}
		WriteFactory(rw, req, ch)
	}()
	first := readN(t, bufio.NewReader(pr), 2)
	cancel()
	_ = pr.Close()
	<-done
	<-prod
	if ids := factoryIDs(first); ids != "1,2" {
		t.Fatalf("first connection %s", ids)
	}

	replay := make(chan FactoryEvent, len(log))
	for _, ev := range log {
		replay <- ev
	}
	close(replay)
	req = httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Last-Event-ID", "2")
	rr := httptest.NewRecorder()
	WriteFactory(rr, req, replay)
	rest := parseFactorySSE(t, rr.Body.Bytes())
	if ids := factoryIDs(append(first, rest...)); ids != "1,2,3" {
		t.Fatalf("ids %s, want 1,2,3 with no gap or duplicate", ids)
	}
}

func parseFactorySSE(t *testing.T, body []byte) []sseMsg {
	t.Helper()
	rd := bufio.NewReader(&bytesReader{body: body})
	var out []sseMsg
	for {
		msg, err := readOne(rd, time.Second)
		if err != nil {
			return out
		}
		if msg.Comment != "" && msg.ID == "" && msg.Data == "" {
			continue
		}
		out = append(out, msg)
	}
}

type bytesReader struct {
	body []byte
	off  int
}

func (b *bytesReader) Read(p []byte) (int, error) {
	if b.off >= len(b.body) {
		return 0, io.EOF
	}
	n := copy(p, b.body[b.off:])
	b.off += n
	return n, nil
}

func factoryIDs(msgs []sseMsg) string {
	return ids(msgs)
}
