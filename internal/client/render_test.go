package client

import (
	"os"
	"strings"
	"testing"
)

func TestRenderGolden(t *testing.T) {
	events := []Event{
		{ID: "1", Type: "message.delta", Data: `{"seq":1,"turn":1,"role":"assistant","text":"Reading the test…"}`},
		{ID: "2", Type: "tool.call", Data: `{"seq":2,"turn":1,"tool":"bash","input_digest":"sha256:abc","redacted_input":"pnpm test -- link"}`},
		{ID: "3", Type: "tool.result", Data: `{"seq":3,"turn":1,"tool":"bash","ok":true,"duration_ms":41230,"output_digest":"sha256:def"}`},
		{ID: "4", Type: "usage", Data: `{"seq":4,"model_id":"claude-5-sonnet-high","input_tokens":18240,"output_tokens":1120,"cost_cents":31}`},
		{ID: "5", Type: "terminal", Data: `{"seq":5,"status":"succeeded","summary":"Opened PR #1182"}`},
	}
	var b strings.Builder
	for _, ev := range events {
		line, ok := RenderLine(ev)
		if !ok {
			t.Fatalf("expected a line for %s", ev.Type)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	want, err := os.ReadFile("testdata/watch.golden")
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("render mismatch\n got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestRenderSkipsUnknown(t *testing.T) {
	_, ok := RenderLine(Event{ID: "1", Type: "turn.started", Data: `{"turn":1}`})
	if ok {
		t.Fatal("unknown event was rendered")
	}
}
