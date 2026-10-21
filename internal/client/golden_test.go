package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClientGoldensDisallowUnknown decodes every console golden into the
// client type that owns it. A fixture with no client type is skipped with a
// reason. An unclassified file fails the test.
func TestClientGoldensDisallowUnknown(t *testing.T) {
	dir := filepath.Join("..", "api", "testdata", "console")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	dest := map[string]func() any{
		"automations.json":               func() any { return &[]Automation{} },
		"definition.json":                func() any { return &Definition{} },
		"deliveries.json":                func() any { return &[]Delivery{} },
		"factory-event-lifecycle.json":   func() any { return &FactoryLifecycleData{} },
		"followup.json":                  func() any { return &SteerAck{} },
		"harness-usage.json":             func() any { return &Usage{} },
		"identities.json":                func() any { return &[]Identity{} },
		"inbox.json":                     func() any { return &[]InboxItem{} },
		"interrupt.json":                 func() any { return &SteerAck{} },
		"m6-benchmark-accepted.json":     func() any { return &BenchmarkAccepted{} },
		"m6-benchmark-trials.json":       func() any { return &BenchmarkTrials{} },
		"m6-benchmarks.json":             func() any { return &[]BenchmarkSummary{} },
		"m6-capture-benchmark-task.json": func() any { return &CapturedBenchmarkTask{} },
		"m6-improvement-batches.json":    func() any { return &[]ImprovementBatch{} },
		"m6-improvement-run-now.json":    func() any { return &ImprovementRunNow{} },
		"m6-inbox-improvement.json":      func() any { return &[]InboxItem{} },
		"m6-metrics.json":                func() any { return &FactoryMetrics{} },
		"m6-run-scores.json":             func() any { return &[]RunScore{} },
		"m6-score-accepted.json":         func() any { return &ScoreAccepted{} },
		"m6-scorer-results.json":         func() any { return &[]ScorerResult{} },
		"m6-scorers.json":                func() any { return &[]ScorerSummary{} },
		"run.json":                       func() any { return &RunDetail{} },
		"runs.json":                      func() any { return &[]RunRow{} },
		"secrets.json":                   func() any { return &[]Secret{} },
		"steer.json":                     func() any { return &[]Steer{} },
		"steers.json":                    func() any { return &[]SteerRecord{} },
		"summary.json":                   func() any { return &Summary{} },
		"timeline.json":                  func() any { return &[]Phase{} },
		"webhook-deliveries.json":        func() any { return &[]Delivery{} },
		"webhooks.json":                  func() any { return &[]Webhook{} },
		"workers.json":                   func() any { return &WorkersView{} },
	}
	// factory-event.json is an SSE data payload (message.delta: gid, role,
	// run_id, seq, text, turn). No client response type has these fields.
	skip := map[string]string{
		"factory-event.json": "SSE data payload (message.delta). No client response type has these fields.",
	}
	seen := 0
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		seen++
		if reason, ok := skip[name]; ok {
			if _, decoded := dest[name]; decoded {
				t.Fatalf("%s is both decoded and skipped", name)
			}
			t.Logf("skip %s: %s", name, reason)
			continue
		}
		newDest, ok := dest[name]
		if !ok {
			t.Fatalf("%s has no client type and no skip reason", name)
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(newDest()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if seen != len(dest)+len(skip) {
		t.Fatalf("classified %d files, directory has %d", len(dest)+len(skip), seen)
	}
}
