package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPullURLs(t *testing.T) {
	text := "noise https://pr.invalid/m7/factory-m7-target/pull/1 and again https://pr.invalid/m7/factory-m7-target/pull/1 plus https://github.com/acme/app/pull/9"
	got := PullURLs(text)
	if len(got) != 2 || got[0] != "https://pr.invalid/m7/factory-m7-target/pull/1" || got[1] != "https://github.com/acme/app/pull/9" {
		t.Fatalf("urls %#v", got)
	}
	if PullURLs("no pull here") != nil {
		t.Fatal("expected no urls")
	}
}

func TestToolResultJSONNamesPullURLs(t *testing.T) {
	raw, err := json.Marshal(Event{
		Type:   "tool.result",
		PRURLs: []string{"https://pr.invalid/m7/factory-m7-target/pull/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"prUrls":["https://pr.invalid/m7/factory-m7-target/pull/1"]`) {
		t.Fatalf("event %s", raw)
	}
}
