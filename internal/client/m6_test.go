package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestM6ClientGoldensRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		dest any
	}{
		{"m6-score-accepted.json", &ScoreAccepted{}},
		{"m6-scorer-results.json", &[]ScorerResult{}},
		{"m6-benchmark-accepted.json", &BenchmarkAccepted{}},
		{"m6-benchmark-trials.json", &BenchmarkTrials{}},
		{"m6-improvement-batches.json", &[]ImprovementBatch{}},
		{"m6-improvement-run-now.json", &ImprovementRunNow{}},
		{"m6-scorers.json", &[]ScorerSummary{}},
		{"m6-run-scores.json", &[]RunScore{}},
		{"m6-benchmarks.json", &[]BenchmarkSummary{}},
		{"m6-capture-benchmark-task.json", &CapturedBenchmarkTask{}},
		{"m6-metrics.json", &FactoryMetrics{}},
		{"m6-inbox-improvement.json", &[]InboxItem{}},
		{"factory-event-lifecycle.json", &FactoryLifecycleData{}},
	}
	for _, tc := range cases {
		raw := readConsoleGolden(t, tc.name)
		if err := json.Unmarshal(raw, tc.dest); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		back, err := json.Marshal(tc.dest)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(normalizeClientJSON(t, raw), normalizeClientJSON(t, back)) {
			t.Fatalf("%s\n got %s\nwant %s", tc.name, normalizeClientJSON(t, back), normalizeClientJSON(t, raw))
		}
	}
}

func TestM6ClientMethods(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		name := map[string]string{
			"POST /api/v1/agent/runs/run_1/score":                   "m6-score-accepted.json",
			"GET /api/v1/factory/fac_1/scorers/correctness/results": "m6-scorer-results.json",
			"POST /api/v1/factory/fac_1/benchmarks/pr-review:run":   "m6-benchmark-accepted.json",
			"GET /api/v1/factory/fac_1/benchmarks/pr-review/trials": "m6-benchmark-trials.json",
			"GET /api/v1/factory/fac_1/improvement-batches":         "m6-improvement-batches.json",
			"POST /api/v1/factory/fac_1/improvement:run-now":        "m6-improvement-run-now.json",
			"GET /api/v1/factory/fac_1/scorers":                     "m6-scorers.json",
			"GET /api/v1/agent/runs/run_1/scores":                   "m6-run-scores.json",
			"GET /api/v1/factory/fac_1/benchmarks":                  "m6-benchmarks.json",
			"POST /api/v1/agent/runs/run_1:capture-benchmark-task":  "m6-capture-benchmark-task.json",
			"GET /api/v1/factory/fac_1/metrics":                     "m6-metrics.json",
		}[r.Method+" "+r.URL.Path]
		if name == "" {
			http.NotFound(w, r)
			return
		}
		status := http.StatusOK
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/agent/runs/run_1:capture-benchmark-task" {
			status = http.StatusAccepted
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(readConsoleGolden(t, name))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	ctx := context.Background()
	passed := false
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	reps := 5

	if _, err := c.ScoreRun(ctx, "run_1", "correctness"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/agent/runs/run_1/score" || gotBody != `{"scorer":"correctness"}` {
		t.Fatalf("score %s %s %s", gotMethod, gotPath, gotBody)
	}

	if _, err := c.ScorerResults(ctx, "fac_1", "correctness", ScorerResultQuery{From: from, To: to, Passed: &passed}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/factory/fac_1/scorers/correctness/results" || gotQuery != "from=2026-04-01T00%3A00%3A00Z&passed=false&to=2026-04-02T00%3A00%3A00Z" {
		t.Fatalf("results %s %s %s", gotMethod, gotPath, gotQuery)
	}

	if _, err := c.StartBenchmark(ctx, "fac_1", "pr-review", &reps); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/factory/fac_1/benchmarks/pr-review:run" || gotBody != `{"repetitions":5}` {
		t.Fatalf("benchmark run %s %s %s", gotMethod, gotPath, gotBody)
	}

	if _, err := c.BenchmarkTrials(ctx, "fac_1", "pr-review", "br_1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/fac_1/benchmarks/pr-review/trials" || gotQuery != "benchmark_run_id=br_1" {
		t.Fatalf("trials %s %s", gotPath, gotQuery)
	}

	if _, err := c.ImprovementBatches(ctx, "fac_1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/fac_1/improvement-batches" {
		t.Fatal(gotPath)
	}

	if _, err := c.RunImprovementNow(ctx, "fac_1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/factory/fac_1/improvement:run-now" {
		t.Fatalf("run-now %s %s", gotMethod, gotPath)
	}

	if _, err := c.Scorers(ctx, "fac_1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/fac_1/scorers" {
		t.Fatal(gotPath)
	}

	if _, err := c.RunScores(ctx, "run_1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/runs/run_1/scores" {
		t.Fatal(gotPath)
	}

	if _, err := c.Benchmarks(ctx, "fac_1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/fac_1/benchmarks" {
		t.Fatal(gotPath)
	}

	got, err := c.CaptureBenchmarkTask(ctx, "run_1", CaptureBenchmarkRequest{Suite: "pr-review", Name: "broken-doc-link"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/agent/runs/run_1:capture-benchmark-task" || gotBody != `{"suite":"pr-review","name":"broken-doc-link"}` {
		t.Fatalf("capture %s %s", gotPath, gotBody)
	}
	if got.Path == "" || got.Content == "" || len(got.HeadRefs) != 1 {
		t.Fatalf("capture body %+v", got)
	}

	if _, err := c.Metrics(ctx, "fac_1", from, to); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/factory/fac_1/metrics" || gotQuery != "from=2026-04-01T00%3A00%3A00Z&to=2026-04-02T00%3A00%3A00Z" {
		t.Fatalf("metrics %s %s", gotPath, gotQuery)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth %q", gotAuth)
	}
}

func TestM6ClientProblems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		switch r.URL.Path {
		case "/api/v1/agent/runs/run_1/score":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"title":"Unprocessable Entity","status":422,"detail":"unknown scorer"}`))
		case "/api/v1/factory/fac_1/improvement:run-now":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"title":"Conflict","status":409,"detail":"improvement batch already in flight"}`))
		default:
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = io.WriteString(w, `{"title":"Not Implemented","status":501,"detail":"measure backend not wired test-key"}`)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "test-key"}
	ctx := context.Background()
	_, err := c.ScoreRun(ctx, "run_1", "nope")
	if err == nil || err.Error() != "api 422: unknown scorer" {
		t.Fatalf("score err %v", err)
	}
	_, err = c.RunImprovementNow(ctx, "fac_1")
	if err == nil || err.Error() != "api 409: improvement batch already in flight" {
		t.Fatalf("run-now err %v", err)
	}
	_, err = c.Scorers(ctx, "fac_1")
	if err == nil || err.Error() != "api 501: measure backend not wired test-key" {
		t.Fatalf("scorers err %v", err)
	}
}
