package github

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// claimedEvents are the canonical names this package emits. Each one has a
// testdata/<event>/input.json → event.json pair.
var claimedEvents = []string{
	"issue_created",
	"issue_assigned",
	"issue_labeled",
	"issue_commented",
	"issue_mentioned",
	"pull_request_opened",
	"pull_request_closed",
	"pull_request_merged",
	"pull_request_reopened",
	"pull_request_ready",
	"pull_request_labeled",
	"pull_request_assigned",
	"pull_request_synchronized",
	"pull_request_review_requested",
	"pull_request_enqueued",
	"pull_request_dequeued",
	"pull_request_commented",
	"pull_request_mentioned",
	"pull_request_review_submitted",
	"push",
	"check_run_rerequested",
	"check_suite_completed",
	"check_suite_rerequested",
	"workflow_run_completed",
	"installation_changed",
}

type goldenInput struct {
	DeliveryID string            `json:"delivery_id"`
	AppSlug    string            `json:"app_slug,omitempty"`
	Headers    map[string]string `json:"headers"`
	Body       json.RawMessage   `json:"body"`
}

func TestGoldenPairs(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("testdata has no golden pairs")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("testdata/%s is not a directory", entry.Name())
		}
		name := entry.Name()
		if !claimed(name) {
			t.Errorf("testdata/%s is not a claimed event", name)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inRaw := readGolden(t, filepath.Join("testdata", name, "input.json"))
			var in goldenInput
			if err := json.Unmarshal(inRaw, &in); err != nil {
				t.Fatal(err)
			}
			got, err := Normalizer{AppSlug: in.AppSlug}.Normalize(Delivery{
				DeliveryID: in.DeliveryID,
				Headers:    in.Headers,
				Body:       in.Body,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !containsEvent(got, name) {
				t.Fatalf("events %v do not include %s", eventNames(got), name)
			}
			gotJSON := mustIndent(t, got)
			wantRaw := readGolden(t, filepath.Join("testdata", name, "event.json"))
			var want []Event
			if err := json.Unmarshal(wantRaw, &want); err != nil {
				t.Fatal(err)
			}
			wantJSON := mustIndent(t, want)
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", gotJSON, wantJSON)
			}
		})
		for _, ev := range eventNames(loadEvents(t, filepath.Join("testdata", name, "event.json"))) {
			seen[ev] = true
		}
	}
	for _, name := range claimedEvents {
		if !seen[name] {
			t.Errorf("no golden emits %s", name)
		}
	}
}

func claimed(name string) bool {
	for _, event := range claimedEvents {
		if event == name {
			return true
		}
	}
	return false
}

func containsEvent(ev []Event, name string) bool {
	for i := range ev {
		if ev[i].Event == name {
			return true
		}
	}
	return false
}

func loadEvents(t *testing.T, path string) []Event {
	t.Helper()
	var ev []Event
	if err := json.Unmarshal(readGolden(t, path), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustIndent(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}
