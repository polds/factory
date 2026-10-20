package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestM6RoutesMatchContract(t *testing.T) {
	want := []string{
		"POST /api/v1/agent/runs/{runId}/score",
		"GET /api/v1/factory/{uid}/scorers/{id}/results",
		"POST /api/v1/factory/{uid}/benchmarks/{suite}",
		"GET /api/v1/factory/{uid}/benchmarks/{suite}/trials",
		"GET /api/v1/factory/{uid}/improvement-batches",
		"POST /api/v1/factory/{uid}/improvement:run-now",
		"GET /api/v1/factory/{uid}/scorers",
		"GET /api/v1/agent/runs/{runId}/scores",
		"GET /api/v1/factory/{uid}/benchmarks",
		"POST /api/v1/agent/runs/{runId}",
		"GET /api/v1/factory/{uid}/metrics",
	}
	got := m6Routes()
	if len(got) != len(want) {
		t.Fatalf("routes %d, want %d", len(got), len(want))
	}
	for i, rt := range got {
		if rt.pattern != want[i] {
			t.Fatalf("route %d %s, want %s", i, rt.pattern, want[i])
		}
		if rt.runToken {
			t.Fatalf("%s is harness-authenticated", rt.name)
		}
		if rt.golden == "" {
			t.Fatalf("%s has no golden", rt.name)
		}
	}
}

func TestM6Goldens(t *testing.T) {
	f := newMeasure(t)
	h := NewHandler(f, testAPIKey, nil)
	for _, rt := range m6Routes() {
		res := call(t, h, rt.method, rt.sample, rt.body, apiHeaders())
		if res.status != rt.status {
			t.Fatalf("%s status %d body %s", rt.name, res.status, res.body)
		}
		assertGolden(t, res.body, rt.golden)
		assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
	}
	if f.scoreName != "correctness" || f.scoreRunID != "run_1" {
		t.Fatalf("score %s %s", f.scoreRunID, f.scoreName)
	}
	if f.resultScorer != "correctness" || f.resultPassed == nil || *f.resultPassed {
		t.Fatalf("results scorer %s passed %v", f.resultScorer, f.resultPassed)
	}
	if !f.resultFrom.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) || !f.resultTo.Equal(time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("results window %s %s", f.resultFrom, f.resultTo)
	}
	if f.benchSuite != "pr-review" || f.benchReps == nil || *f.benchReps != 5 {
		t.Fatalf("benchmark suite %s reps %v", f.benchSuite, f.benchReps)
	}
	if f.trialSuite != "pr-review" || f.trialRunID != "br_1" {
		t.Fatalf("trials %s %s", f.trialSuite, f.trialRunID)
	}
	if f.captureSuite != "pr-review" || f.captureName != "broken-doc-link" || f.captureRunID != "run_1" {
		t.Fatalf("capture %s %s %s", f.captureRunID, f.captureSuite, f.captureName)
	}
	if !f.metricsFrom.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) || !f.metricsTo.Equal(time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("metrics window %s %s", f.metricsFrom, f.metricsTo)
	}
	inbox := call(t, h, http.MethodGet, "/api/v1/factory/fac_1/inbox", "", apiHeaders())
	assertGolden(t, inbox.body, "inbox.json")
	life := decodeGolden[FactoryLifecycleData](t, "factory-event-lifecycle.json")
	if life.GID != 42 || life.RunID != "run_1" || life.Status != "running" || life.Stage != "building" {
		t.Fatalf("lifecycle %+v", life)
	}
	items := decodeGolden[[]InboxItem](t, "m6-inbox-improvement.json")
	if len(items) != 1 || items[0].Kind != InboxKindImprovementPR {
		t.Fatalf("inbox kind %+v", items)
	}
}

func TestM6AuthNotFoundAndUnwired(t *testing.T) {
	for _, rt := range m6Routes() {
		t.Run(rt.name+"/401", func(t *testing.T) {
			h := NewHandler(newMeasure(t), testAPIKey, nil)
			res := call(t, h, rt.method, rt.sample, rt.body, nil)
			if res.status != http.StatusUnauthorized {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
			assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
		})
		t.Run(rt.name+"/404", func(t *testing.T) {
			f := newMeasure(t)
			f.notFound = true
			h := NewHandler(f, testAPIKey, nil)
			res := call(t, h, rt.method, rt.sample, rt.body, apiHeaders())
			if res.status != http.StatusNotFound {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
		})
		t.Run(rt.name+"/501", func(t *testing.T) {
			h := NewHandler(newFake(), testAPIKey, nil)
			res := call(t, h, rt.method, rt.sample, rt.body, apiHeaders())
			if res.status != http.StatusNotImplemented {
				t.Fatalf("status %d body %s", res.status, res.body)
			}
			assertProblem(t, res)
			var p Problem
			if err := json.Unmarshal(res.body, &p); err != nil {
				t.Fatal(err)
			}
			if p.Detail != measureUnwiredDetail {
				t.Fatalf("detail %q", p.Detail)
			}
			assertBodyClean(t, res.body, plantedSecret, testAPIKey)
		})
	}
}

func TestM6Errors(t *testing.T) {
	f := newMeasure(t)
	h := NewHandler(f, testAPIKey, nil)

	res := call(t, h, http.MethodPost, "/api/v1/factory/fac_1/benchmarks/pr-review:run", `{"repetitions":0}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusUnprocessableEntity, "repetitions must be at least 1")
	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/benchmarks/pr-review:run", `{"repetitions":-3}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusUnprocessableEntity, "repetitions must be at least 1")
	if f.benchCalls != 0 {
		t.Fatalf("invalid repetitions reached the backend %d times", f.benchCalls)
	}

	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/benchmarks/pr-review:run", ``, apiHeaders())
	if res.status != http.StatusAccepted {
		t.Fatalf("omitted repetitions status %d body %s", res.status, res.body)
	}
	if f.benchReps != nil {
		t.Fatalf("omitted repetitions was sent as %d", *f.benchReps)
	}

	f.scoreErr = fmt.Errorf("catalog: %w", ErrUnknownScorer)
	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/score", `{"scorer":"missing"}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusUnprocessableEntity, "unknown scorer")
	f.scoreErr = nil

	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/score", `{"scorer":" "}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusBadRequest, "scorer is required")

	f.captureErr = ErrRunIneligible{Reason: "run purpose is scorer"}
	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1:capture-benchmark-task", `{"suite":"pr-review","name":"broken-doc-link"}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusUnprocessableEntity, "run purpose is scorer")
	f.captureErr = nil

	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1:capture-benchmark-task", `{"name":"broken-doc-link"}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusBadRequest, "suite is required")
	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1:capture-benchmark-task", `{"suite":"pr-review"}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusBadRequest, "name is required")

	f.runNowErr = fmt.Errorf("guard: %w", ErrBatchInFlight)
	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/improvement:run-now", ``, apiHeaders())
	assertProblemStatus(t, res, http.StatusConflict, ErrBatchInFlight.Error())
	f.runNowErr = nil

	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/missing/score", `{"scorer":"correctness"}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusNotFound, "not found")
	res = call(t, h, http.MethodGet, "/api/v1/factory/missing/scorers/correctness/results", "", apiHeaders())
	assertProblemStatus(t, res, http.StatusNotFound, "not found")
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/benchmarks/missing/trials", "", apiHeaders())
	assertProblemStatus(t, res, http.StatusNotFound, "not found")
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/scorers/missing/results", "", apiHeaders())
	assertProblemStatus(t, res, http.StatusNotFound, "not found")

	res = call(t, h, http.MethodPost, "/api/v1/factory/fac_1/benchmarks/pr-review", `{"repetitions":5}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusNotFound, "not found")
	res = call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1", `{"suite":"pr-review","name":"n"}`, apiHeaders())
	assertProblemStatus(t, res, http.StatusNotFound, "not found")

	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/scorers/correctness/results?passed=maybe", "", apiHeaders())
	assertProblemStatus(t, res, http.StatusBadRequest, "passed is invalid")
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/metrics?from=yesterday", "", apiHeaders())
	assertProblemStatus(t, res, http.StatusBadRequest, "from is invalid")
	res = call(t, h, http.MethodGet, "/api/v1/factory/fac_1/metrics?to=nope", "", apiHeaders())
	assertProblemStatus(t, res, http.StatusBadRequest, "to is invalid")
	if f.metricsCalls != 0 {
		t.Fatalf("invalid metrics window reached the backend %d times", f.metricsCalls)
	}
}

func TestM6NoSecretMaterial(t *testing.T) {
	f := newMeasure(t)
	plantMeasureSecrets(f)
	h := NewHandler(f, testAPIKey, nil)
	for _, rt := range m6Routes() {
		res := call(t, h, rt.method, rt.sample, rt.body, apiHeaders())
		if res.status != rt.status {
			t.Fatalf("%s status %d body %s", rt.name, res.status, res.body)
		}
		assertBodyClean(t, res.body, plantedSecret, testAPIKey, runTokenValue)
		if rt.name == "scorer-results" && !bytes.Contains(res.body, []byte("without running tests")) {
			t.Fatalf("results dropped reasoning: %s", res.body)
		}
		if rt.name == "improvement-batches" && !bytes.Contains(res.body, []byte("https://github.com/acme/factory/pull/42")) {
			t.Fatalf("batches dropped pr_url: %s", res.body)
		}
		if rt.name == "benchmark-trials" && bytes.Contains(res.body, []byte(`"env"`)) {
			t.Fatalf("trials exposed env: %s", res.body)
		}
	}
	res := call(t, h, http.MethodPost, "/api/v1/agent/runs/run_1/score", `{"scorer":"correctness","token":"`+plantedSecret+`"}`, apiHeaders())
	if res.status != http.StatusAccepted {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertBodyClean(t, res.body, plantedSecret, testAPIKey)

	f.leakErr = true
	for _, rt := range m6Routes() {
		res := call(t, h, rt.method, rt.sample, rt.body, apiHeaders())
		if res.status == http.StatusBadRequest || res.status == http.StatusNotFound {
			continue
		}
		if bytes.Contains(res.body, []byte(plantedSecret)) || bytes.Contains(res.body, []byte(testAPIKey)) {
			t.Fatalf("%s leaked backend error: %s", rt.name, res.body)
		}
	}

	for _, v := range []any{
		ScorerResult{Reasoning: "shown", Held: hidden(plantedSecret)},
		ImprovementBatch{PRURL: "https://example.test/pull/1", HeldPR: hidden(plantedSecret)},
		BenchmarkCell{Task: "t", HeldEnv: hidden(plantedSecret)},
		RunScore{Reasoning: "shown", Held: hidden(plantedSecret)},
	} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			printed := fmt.Sprintf(format, v)
			if bytes.Contains([]byte(printed), []byte(plantedSecret)) {
				t.Fatalf("%s printed a secret: %s", format, printed)
			}
		}
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		assertBodyClean(t, body, plantedSecret)
	}
}

func assertProblemStatus(t *testing.T, res httpResult, status int, detail string) {
	t.Helper()
	if res.status != status {
		t.Fatalf("status %d body %s", res.status, res.body)
	}
	assertProblem(t, res)
	var p Problem
	if err := json.Unmarshal(res.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Detail != detail {
		t.Fatalf("detail %q, want %q", p.Detail, detail)
	}
	if res.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content-type %q", res.header.Get("Content-Type"))
	}
}

type measureFake struct {
	*consoleFake
	scoreAccepted ScoreAccepted
	results       []ScorerResult
	benchAccepted BenchmarkAccepted
	trials        BenchmarkTrials
	batches       []ImprovementBatch
	runNow        ImprovementRunNow
	scorers       []ScorerSummary
	scores        []RunScore
	benchmarks    []BenchmarkSummary
	captured      CapturedBenchmarkTask
	metrics       FactoryMetrics

	scoreErr   error
	captureErr error
	runNowErr  error

	scoreRunID   string
	scoreName    string
	resultScorer string
	resultFrom   time.Time
	resultTo     time.Time
	resultPassed *bool
	benchSuite   string
	benchReps    *int
	benchCalls   int
	trialSuite   string
	trialRunID   string
	captureRunID string
	captureSuite string
	captureName  string
	metricsFrom  time.Time
	metricsTo    time.Time
	metricsCalls int
}

func newMeasure(t *testing.T) *measureFake {
	t.Helper()
	f := &measureFake{consoleFake: newConsole(t)}
	f.scoreAccepted = decodeGolden[ScoreAccepted](t, "m6-score-accepted.json")
	f.results = decodeGolden[[]ScorerResult](t, "m6-scorer-results.json")
	f.benchAccepted = decodeGolden[BenchmarkAccepted](t, "m6-benchmark-accepted.json")
	f.trials = decodeGolden[BenchmarkTrials](t, "m6-benchmark-trials.json")
	f.batches = decodeGolden[[]ImprovementBatch](t, "m6-improvement-batches.json")
	f.runNow = decodeGolden[ImprovementRunNow](t, "m6-improvement-run-now.json")
	f.scorers = decodeGolden[[]ScorerSummary](t, "m6-scorers.json")
	f.scores = decodeGolden[[]RunScore](t, "m6-run-scores.json")
	f.benchmarks = decodeGolden[[]BenchmarkSummary](t, "m6-benchmarks.json")
	f.captured = decodeGolden[CapturedBenchmarkTask](t, "m6-capture-benchmark-task.json")
	f.metrics = decodeGolden[FactoryMetrics](t, "m6-metrics.json")
	return f
}

func plantMeasureSecrets(f *measureFake) {
	if len(f.results) > 0 {
		f.results[0].Held = hidden(plantedSecret)
	}
	if len(f.scores) > 0 {
		f.scores[0].Held = hidden(plantedSecret)
	}
	if len(f.batches) > 0 {
		f.batches[0].HeldPR = hidden(plantedSecret)
	}
	if len(f.runNow.Batches) > 0 {
		f.runNow.Batches[0].HeldPR = hidden(plantedSecret)
	}
	if len(f.trials.Cells) > 0 {
		f.trials.Cells[0].HeldEnv = hidden(plantedSecret)
	}
}

func (f *measureFake) ready(ctx context.Context, id string) error {
	if err := f.fail(id); err != nil {
		return err
	}
	return f.blocked(ctx)
}

func (f *measureFake) ScoreRun(ctx context.Context, runID, scorer string) (ScoreAccepted, error) {
	f.scoreRunID = runID
	f.scoreName = scorer
	if err := f.ready(ctx, runID); err != nil {
		return ScoreAccepted{}, err
	}
	if f.scoreErr != nil {
		return ScoreAccepted{}, f.scoreErr
	}
	return f.scoreAccepted, nil
}

func (f *measureFake) ScorerResults(ctx context.Context, factory, scorer string, q ScorerResultQuery) ([]ScorerResult, error) {
	f.resultScorer = scorer
	f.resultFrom = q.From
	f.resultTo = q.To
	f.resultPassed = q.Passed
	if err := f.ready(ctx, factory); err != nil {
		return nil, err
	}
	if err := f.ready(ctx, scorer); err != nil {
		return nil, err
	}
	return f.results, nil
}

func (f *measureFake) StartBenchmark(ctx context.Context, factory, suite string, repetitions *int) (BenchmarkAccepted, error) {
	f.benchCalls++
	f.benchSuite = suite
	f.benchReps = repetitions
	if err := f.ready(ctx, factory); err != nil {
		return BenchmarkAccepted{}, err
	}
	if err := f.ready(ctx, suite); err != nil {
		return BenchmarkAccepted{}, err
	}
	return f.benchAccepted, nil
}

func (f *measureFake) BenchmarkTrials(ctx context.Context, factory, suite, benchmarkRunID string) (BenchmarkTrials, error) {
	f.trialSuite = suite
	f.trialRunID = benchmarkRunID
	if err := f.ready(ctx, factory); err != nil {
		return BenchmarkTrials{}, err
	}
	if err := f.ready(ctx, suite); err != nil {
		return BenchmarkTrials{}, err
	}
	return f.trials, nil
}

func (f *measureFake) ImprovementBatches(ctx context.Context, factory string) ([]ImprovementBatch, error) {
	if err := f.ready(ctx, factory); err != nil {
		return nil, err
	}
	return f.batches, nil
}

func (f *measureFake) RunImprovementNow(ctx context.Context, factory string) (ImprovementRunNow, error) {
	if err := f.ready(ctx, factory); err != nil {
		return ImprovementRunNow{}, err
	}
	if f.runNowErr != nil {
		return ImprovementRunNow{}, f.runNowErr
	}
	return f.runNow, nil
}

func (f *measureFake) Scorers(ctx context.Context, factory string) ([]ScorerSummary, error) {
	if err := f.ready(ctx, factory); err != nil {
		return nil, err
	}
	return f.scorers, nil
}

func (f *measureFake) RunScores(ctx context.Context, runID string) ([]RunScore, error) {
	if err := f.ready(ctx, runID); err != nil {
		return nil, err
	}
	return f.scores, nil
}

func (f *measureFake) Benchmarks(ctx context.Context, factory string) ([]BenchmarkSummary, error) {
	if err := f.ready(ctx, factory); err != nil {
		return nil, err
	}
	return f.benchmarks, nil
}

func (f *measureFake) CaptureBenchmarkTask(ctx context.Context, runID string, req CaptureBenchmarkRequest) (CapturedBenchmarkTask, error) {
	f.captureRunID = runID
	f.captureSuite = req.Suite
	f.captureName = req.Name
	if err := f.ready(ctx, runID); err != nil {
		return CapturedBenchmarkTask{}, err
	}
	if f.captureErr != nil {
		return CapturedBenchmarkTask{}, f.captureErr
	}
	return f.captured, nil
}

func (f *measureFake) Metrics(ctx context.Context, factory string, from, to time.Time) (FactoryMetrics, error) {
	f.metricsCalls++
	f.metricsFrom = from
	f.metricsTo = to
	if err := f.ready(ctx, factory); err != nil {
		return FactoryMetrics{}, err
	}
	return f.metrics, nil
}
