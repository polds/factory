package client

import (
	"strings"
	"testing"
)

func TestParseSSEMultilineDataCommentsAndID(t *testing.T) {
	stream := strings.Join([]string{
		": heartbeat",
		"",
		"id: 1",
		"event: message.delta",
		`data: {"role":"assistant","text":"hello`,
		`data: world"}`,
		"",
		": comment without a space",
		"id: 2",
		"event: tool.call",
		`data: {"tool":"bash","redacted_input":"ls"}`,
		"",
		"",
	}, "\n")

	got, err := ParseSSE(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("events %d: %+v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Type != "message.delta" {
		t.Fatalf("first %+v", got[0])
	}
	if got[0].Data != "{\"role\":\"assistant\",\"text\":\"hello\nworld\"}" {
		t.Fatalf("data %q", got[0].Data)
	}
	if got[1].ID != "2" || got[1].Type != "tool.call" {
		t.Fatalf("second %+v", got[1])
	}
	if got[1].Data != `{"tool":"bash","redacted_input":"ls"}` {
		t.Fatalf("data %q", got[1].Data)
	}
}
