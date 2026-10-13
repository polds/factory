package harness

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestEventTimestampsIncreaseAndTerminalIsOnce(t *testing.T) {
	frozen := time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	log := NewLog(&buf, func() time.Time { return frozen })
	if err := log.Emit(Event{Type: "turn.started", Turn: 1, ModelID: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Emit(Event{Type: "terminal", Status: "succeeded", Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := log.Emit(Event{Type: "terminal", Status: "succeeded", Summary: "again"}); err == nil {
		t.Fatal("second terminal was accepted")
	}
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("lines %d", len(lines))
	}
	var first, second Event
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatal(err)
	}
	t1, err := time.Parse(time.RFC3339Nano, first.TS)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := time.Parse(time.RFC3339Nano, second.TS)
	if err != nil {
		t.Fatal(err)
	}
	if !t1.Equal(frozen) {
		t.Fatalf("first %s", first.TS)
	}
	if !t2.Equal(frozen.Add(time.Nanosecond)) {
		t.Fatalf("second %s", second.TS)
	}
	if second.Type != "terminal" || second.Status != "succeeded" || second.Summary != "done" {
		t.Fatalf("%+v", second)
	}
}
