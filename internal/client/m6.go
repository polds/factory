package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/polds/factory/internal/api"
)

// M6 response and request types are the API types. The fields are not copied.
type (
	ScoreRequest            = api.ScoreRequest
	ScoreAccepted           = api.ScoreAccepted
	ScorerResultQuery       = api.ScorerResultQuery
	ScorerResult            = api.ScorerResult
	BenchmarkRunRequest     = api.BenchmarkRunRequest
	BenchmarkAccepted       = api.BenchmarkAccepted
	BenchmarkTrials         = api.BenchmarkTrials
	BenchmarkCell           = api.BenchmarkCell
	ModelTokens             = api.ModelTokens
	BenchmarkReport         = api.BenchmarkReport
	ImprovementFailure      = api.ImprovementFailure
	ImprovementBatch        = api.ImprovementBatch
	ImprovementRunNow       = api.ImprovementRunNow
	DayPassRate             = api.DayPassRate
	ScorerSummary           = api.ScorerSummary
	RunScore                = api.RunScore
	BenchmarkLastRun        = api.BenchmarkLastRun
	BenchmarkSummary        = api.BenchmarkSummary
	CaptureBenchmarkRequest = api.CaptureBenchmarkRequest
	HeadRef                 = api.HeadRef
	CapturedBenchmarkTask   = api.CapturedBenchmarkTask
	FactoryMetrics          = api.FactoryMetrics
	RunTotals               = api.RunTotals
	NamedCount              = api.NamedCount
	AutonomyMetric          = api.AutonomyMetric
	CycleTimeMetric         = api.CycleTimeMetric
	CycleTimeStage          = api.CycleTimeStage
	CostPerPRMetric         = api.CostPerPRMetric
	PRSizeCost              = api.PRSizeCost
	ExpensivePR             = api.ExpensivePR
	ScorerCard              = api.ScorerCard
	ImprovementPR           = api.ImprovementPR
	FactoryLifecycleData    = api.FactoryLifecycleData
	ErrRunIneligible        = api.ErrRunIneligible
)

// ScoreRun posts one scorer for a run. The previous result for that scorer
// is replaced.
func (c *Client) ScoreRun(ctx context.Context, runID, scorer string) (ScoreAccepted, error) {
	var out ScoreAccepted
	path := "/api/v1/agent/runs/" + url.PathEscape(runID) + "/score"
	err := c.doJSON(ctx, http.MethodPost, path, ScoreRequest{Scorer: scorer}, &out, http.StatusAccepted)
	return out, err
}

// ScorerResults lists one scorer's results. The scorer argument is the
// directory slug.
func (c *Client) ScorerResults(ctx context.Context, factory, scorer string, q ScorerResultQuery) ([]ScorerResult, error) {
	var out []ScorerResult
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/scorers/" + url.PathEscape(scorer) + "/results"
	v := url.Values{}
	setTimeQuery(v, "from", q.From)
	setTimeQuery(v, "to", q.To)
	if q.Passed != nil {
		v.Set("passed", strconv.FormatBool(*q.Passed))
	}
	err := c.doJSONQuery(ctx, http.MethodGet, path, v, nil, &out, http.StatusOK)
	return out, err
}

// StartBenchmark starts a suite. A nil repetitions leaves the choice to the server.
func (c *Client) StartBenchmark(ctx context.Context, factory, suite string, repetitions *int) (BenchmarkAccepted, error) {
	var out BenchmarkAccepted
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/benchmarks/" + url.PathEscape(suite) + ":run"
	err := c.doJSON(ctx, http.MethodPost, path, BenchmarkRunRequest{Repetitions: repetitions}, &out, http.StatusAccepted)
	return out, err
}

// BenchmarkTrials returns the aggregate cells for a suite.
func (c *Client) BenchmarkTrials(ctx context.Context, factory, suite, benchmarkRunID string) (BenchmarkTrials, error) {
	var out BenchmarkTrials
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/benchmarks/" + url.PathEscape(suite) + "/trials"
	q := url.Values{}
	if benchmarkRunID != "" {
		q.Set("benchmark_run_id", benchmarkRunID)
	}
	err := c.doJSONQuery(ctx, http.MethodGet, path, q, nil, &out, http.StatusOK)
	return out, err
}

// ImprovementBatches lists improvement batches.
func (c *Client) ImprovementBatches(ctx context.Context, factory string) ([]ImprovementBatch, error) {
	var out []ImprovementBatch
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/improvement-batches"
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	return out, err
}

// RunImprovementNow forces the improvement check.
func (c *Client) RunImprovementNow(ctx context.Context, factory string) (ImprovementRunNow, error) {
	var out ImprovementRunNow
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/improvement:run-now"
	err := c.doJSON(ctx, http.MethodPost, path, struct{}{}, &out, http.StatusAccepted)
	return out, err
}

// Scorers lists scorer summaries.
func (c *Client) Scorers(ctx context.Context, factory string) ([]ScorerSummary, error) {
	var out []ScorerSummary
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/scorers"
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	return out, err
}

// RunScores lists the scores recorded for one run.
func (c *Client) RunScores(ctx context.Context, runID string) ([]RunScore, error) {
	var out []RunScore
	path := "/api/v1/agent/runs/" + url.PathEscape(runID) + "/scores"
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	return out, err
}

// Benchmarks lists benchmark suites.
func (c *Client) Benchmarks(ctx context.Context, factory string) ([]BenchmarkSummary, error) {
	var out []BenchmarkSummary
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/benchmarks"
	err := c.doJSON(ctx, http.MethodGet, path, nil, &out, http.StatusOK)
	return out, err
}

// CaptureBenchmarkTask returns the task YAML. It does not write a file.
func (c *Client) CaptureBenchmarkTask(ctx context.Context, runID string, req CaptureBenchmarkRequest) (CapturedBenchmarkTask, error) {
	var out CapturedBenchmarkTask
	path := "/api/v1/agent/runs/" + url.PathEscape(runID) + ":capture-benchmark-task"
	err := c.doJSON(ctx, http.MethodPost, path, req, &out, http.StatusOK)
	return out, err
}

// Metrics reads the factory measure summary for the window.
func (c *Client) Metrics(ctx context.Context, factory string, from, to time.Time) (FactoryMetrics, error) {
	var out FactoryMetrics
	path := "/api/v1/factory/" + url.PathEscape(factory) + "/metrics"
	q := url.Values{}
	setTimeQuery(q, "from", from)
	setTimeQuery(q, "to", to)
	err := c.doJSONQuery(ctx, http.MethodGet, path, q, nil, &out, http.StatusOK)
	return out, err
}

func setTimeQuery(v url.Values, key string, t time.Time) {
	if t.IsZero() {
		return
	}
	v.Set(key, t.UTC().Format(time.RFC3339))
}
