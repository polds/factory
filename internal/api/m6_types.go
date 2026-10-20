package api

import (
	"context"
	"errors"
	"time"
)

// measureUnwiredDetail is the 501 problem detail when Backend does not
// implement MeasureBackend. T63 wires the implementation.
const measureUnwiredDetail = "measure backend not wired"

// Purpose values for runs.purpose. T58 stores them. No M6 route takes a
// purpose in the body; scorers reject anything other than work.
const (
	PurposeWork        = "work"
	PurposeScorer      = "scorer"
	PurposeBenchmark   = "benchmark"
	PurposeImprovement = "improvement"
)

// Scorer result statuses. On-demand scoring replaces the previous row for
// the same run and scorer.
const (
	ScorerStatusOK      = "ok"
	ScorerStatusInvalid = "scorer_invalid"
	ScorerStatusPending = "pending"
)

// Improvement batch statuses. A queued, running, or pr_open batch for an
// agent blocks run-now with ErrBatchInFlight.
const (
	BatchQueued  = "queued"
	BatchRunning = "running"
	BatchPROpen  = "pr_open"
	BatchMerged  = "merged"
	BatchClosed  = "closed"
	BatchFailed  = "failed"
)

// Benchmark trial outcomes. Cells aggregate these; the trials route does
// not return one row per trial.
const (
	TrialPass    = "pass"
	TrialFail    = "fail"
	TrialErrored = "errored"
	TrialPending = "pending"
)

// InboxKindImprovementPR is the inbox kind for an improvement pull request
// waiting on review. The inbox route itself is unchanged.
const InboxKindImprovementPR = "improvement_pr"

var (
	// ErrUnknownScorer is the score route's 422. The scorer directory slug
	// is not in the factory definition.
	ErrUnknownScorer = errors.New("unknown scorer")
	// ErrBatchInFlight is the run-now 409. The agent already has a queued,
	// running, or pr_open batch.
	ErrBatchInFlight = errors.New("improvement batch already in flight")
)

// ErrRunIneligible is the capture route's 422. Reason is copied into the
// problem detail.
type ErrRunIneligible struct {
	Reason string
}

func (e ErrRunIneligible) Error() string {
	if e.Reason == "" {
		return "run is not eligible for capture"
	}
	return e.Reason
}

// MeasureBackend is the M6 surface. Handlers type-assert Backend to it.
// A Backend that does not implement it answers 501 on these routes.
// T63 implements the methods.
type MeasureBackend interface {
	ScoreRun(ctx context.Context, runID, scorer string) (ScoreAccepted, error)
	ScorerResults(ctx context.Context, factory, scorer string, q ScorerResultQuery) ([]ScorerResult, error)
	StartBenchmark(ctx context.Context, factory, suite string, repetitions *int) (BenchmarkAccepted, error)
	BenchmarkTrials(ctx context.Context, factory, suite, benchmarkRunID string) (BenchmarkTrials, error)
	ImprovementBatches(ctx context.Context, factory string) ([]ImprovementBatch, error)
	RunImprovementNow(ctx context.Context, factory string) (ImprovementRunNow, error)
	Scorers(ctx context.Context, factory string) ([]ScorerSummary, error)
	RunScores(ctx context.Context, runID string) ([]RunScore, error)
	Benchmarks(ctx context.Context, factory string) ([]BenchmarkSummary, error)
	CaptureBenchmarkTask(ctx context.Context, runID string, req CaptureBenchmarkRequest) (CapturedBenchmarkTask, error)
	Metrics(ctx context.Context, factory string, from, to time.Time) (FactoryMetrics, error)
}

// ScoreRequest is the POST /agent/runs/{id}/score body.
type ScoreRequest struct {
	Scorer string `json:"scorer"`
}

// ScoreAccepted is the 202 body. Scoring replaces that scorer's previous
// result for the run.
type ScoreAccepted struct {
	ResultID   string `json:"result_id"`
	JudgeRunID string `json:"judge_run_id"`
}

// ScorerResultQuery is the results list filter. A nil Passed means both.
type ScorerResultQuery struct {
	From   time.Time
	To     time.Time
	Passed *bool
}

// ScorerResult is one GET /factory/{uid}/scorers/{id}/results row.
// {id} is the scorer directory slug. Held is judge material stored beside
// Reasoning; the response encodes Reasoning only.
type ScorerResult struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	Label      string     `json:"label"`
	Score      float64    `json:"score"`
	Passed     bool       `json:"passed"`
	Reasoning  string     `json:"reasoning"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ReviewedAt *time.Time `json:"reviewed_at,omitempty"`
	Held       hidden     `json:"-"`
}

// BenchmarkRunRequest is the POST /benchmarks/{suite}:run body.
// Repetitions is optional. A value below 1 is 422.
type BenchmarkRunRequest struct {
	Repetitions *int `json:"repetitions,omitempty"`
}

// BenchmarkAccepted is the 202 body.
type BenchmarkAccepted struct {
	BenchmarkRunID string `json:"benchmark_run_id"`
}

// BenchmarkTrials is GET /benchmarks/{suite}/trials.
type BenchmarkTrials struct {
	Cells  []BenchmarkCell `json:"cells"`
	Report BenchmarkReport `json:"report"`
}

// BenchmarkCell is one configuration × task aggregate.
// HeldEnv is the trial environment. It is not on the wire.
type BenchmarkCell struct {
	Configuration    string        `json:"configuration"`
	Task             string        `json:"task"`
	N                int           `json:"n"`
	Pass             int           `json:"pass"`
	Errored          int           `json:"errored"`
	PassRate         float64       `json:"pass_rate"`
	CILow            float64       `json:"ci_low"`
	CIHigh           float64       `json:"ci_high"`
	MedianCostCents  int64         `json:"median_cost_cents"`
	MedianDurationMS int64         `json:"median_duration_ms"`
	TokensByModel    []ModelTokens `json:"tokens_by_model"`
	HeldEnv          hidden        `json:"-"`
}

// ModelTokens is token usage for one model. It is not a free-form map.
type ModelTokens struct {
	ModelID      string `json:"model_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// BenchmarkReport is the comparison verdict. Winner is empty when the
// report refuses to name one; Reason says why.
type BenchmarkReport struct {
	Winner string `json:"winner"`
	Reason string `json:"reason"`
}

// ImprovementFailure is one scored run inside a batch.
type ImprovementFailure struct {
	RunID    string `json:"run_id"`
	ResultID string `json:"result_id"`
	Scorer   string `json:"scorer"`
	Label    string `json:"label"`
}

// ImprovementBatch is one GET /improvement-batches row.
// HeldPR is the pull request body. The response carries pr_url only.
type ImprovementBatch struct {
	ID        string               `json:"id"`
	Agent     string               `json:"agent"`
	Status    string               `json:"status"`
	Failures  []ImprovementFailure `json:"failures"`
	PRURL     string               `json:"pr_url"`
	CreatedAt time.Time            `json:"created_at"`
	HeldPR    hidden               `json:"-"`
}

// ImprovementRunNow is the 202 body for POST /improvement:run-now.
type ImprovementRunNow struct {
	CheckedAgents []string           `json:"checked_agents"`
	Batches       []ImprovementBatch `json:"batches"`
}

// DayPassRate is one day's pass rate for a scorer.
type DayPassRate struct {
	Day      string  `json:"day"`
	PassRate float64 `json:"pass_rate"`
	N        int     `json:"n"`
}

// ScorerSummary is one GET /factory/{uid}/scorers row.
// Agents and Labels are directory slugs and label values.
type ScorerSummary struct {
	Name            string        `json:"name"`
	Agents          []string      `json:"agents"`
	Labels          []string      `json:"labels"`
	PassingScore    float64       `json:"passing_score"`
	SamplingRate    int           `json:"sampling_rate"`
	Model           string        `json:"model"`
	SelfImprovement bool          `json:"self_improvement"`
	PassRateByDay   []DayPassRate `json:"pass_rate_by_day"`
}

// RunScore is one GET /agent/runs/{id}/scores row.
// Held is judge material stored beside Reasoning.
type RunScore struct {
	Scorer    string  `json:"scorer"`
	Label     string  `json:"label"`
	Score     float64 `json:"score"`
	Passed    bool    `json:"passed"`
	Reasoning string  `json:"reasoning"`
	Status    string  `json:"status"`
	Held      hidden  `json:"-"`
}

// BenchmarkLastRun is the most recent run of a suite.
type BenchmarkLastRun struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	Repetitions int       `json:"repetitions"`
}

// BenchmarkSummary is one GET /factory/{uid}/benchmarks row.
// Configurations and Tasks are names, not the definition documents.
type BenchmarkSummary struct {
	Suite          string            `json:"suite"`
	Agent          string            `json:"agent"`
	Configurations []string          `json:"configurations"`
	Tasks          []string          `json:"tasks"`
	LastRun        *BenchmarkLastRun `json:"last_run,omitempty"`
}

// CaptureBenchmarkRequest is the POST /runs/{id}:capture-benchmark-task body.
// The handler returns the file. It does not write it.
type CaptureBenchmarkRequest struct {
	Suite string `json:"suite"`
	Name  string `json:"name"`
}

// HeadRef is one parsed starting repo ref: host, owner, repo, and a
// 40-character hex SHA.
type HeadRef struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	SHA   string `json:"sha"`
}

// CapturedBenchmarkTask is the capture response. Content is the task YAML.
type CapturedBenchmarkTask struct {
	Path     string    `json:"path"`
	Content  string    `json:"content"`
	HeadRefs []HeadRef `json:"head_refs"`
}

// FactoryMetrics is GET /factory/{uid}/metrics.
type FactoryMetrics struct {
	TotalRuns        RunTotals       `json:"total_runs"`
	PRsOpened        int             `json:"prs_opened"`
	PRsMerged        int             `json:"prs_merged"`
	Autonomy         AutonomyMetric  `json:"autonomy"`
	CycleTime        CycleTimeMetric `json:"cycle_time"`
	CostPerPR        CostPerPRMetric `json:"cost_per_pr"`
	MostExpensivePRs []ExpensivePR   `json:"most_expensive_prs"`
	ScorerCards      []ScorerCard    `json:"scorer_cards"`
	ImprovementPRs   []ImprovementPR `json:"improvement_prs"`
}

// RunTotals breaks total runs down without using a free-form map.
// Measurement is the count of scorer, benchmark, and improvement runs
// included in the other buckets.
type RunTotals struct {
	ByAgent     []NamedCount `json:"by_agent"`
	ByStatus    []NamedCount `json:"by_status"`
	BySource    []NamedCount `json:"by_source"`
	ByModel     []NamedCount `json:"by_model"`
	Measurement int          `json:"measurement"`
}

// NamedCount is one named bucket.
type NamedCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AutonomyMetric is the share of merged PRs with no human code push.
type AutonomyMetric struct {
	Share            float64    `json:"share"`
	N                int        `json:"n"`
	ForgeConnectedAt *time.Time `json:"forge_connected_at,omitempty"`
}

// CycleTimeMetric is the median kickoff-to-merge time and the stage medians.
type CycleTimeMetric struct {
	P50MS  int64            `json:"p50_ms"`
	Stages []CycleTimeStage `json:"stages"`
}

// CycleTimeStage is one median. Name is kickoff_to_pr, pr_to_first_review,
// or first_review_to_merge.
type CycleTimeStage struct {
	Name  string `json:"name"`
	P50MS int64  `json:"p50_ms"`
}

// CostPerPRMetric is the median cost of PRs opened in the range.
// BySize uses the S/M/L/XL split.
type CostPerPRMetric struct {
	MedianCents int64      `json:"median_cents"`
	BySize      PRSizeCost `json:"by_size"`
}

// PRSizeCost is the median cents for each size bucket.
type PRSizeCost struct {
	S  int64 `json:"S"`
	M  int64 `json:"M"`
	L  int64 `json:"L"`
	XL int64 `json:"XL"`
}

// ExpensivePR is one row of the most expensive PRs list.
type ExpensivePR struct {
	Repo         string `json:"repo"`
	Number       int    `json:"number"`
	CostCents    int64  `json:"cost_cents"`
	ChangedLines int    `json:"changed_lines"`
	URL          string `json:"url"`
}

// ScorerCard is one scorer's pass rate over the range.
type ScorerCard struct {
	Name          string        `json:"name"`
	PassRateByDay []DayPassRate `json:"pass_rate_by_day"`
}

// ImprovementPR is one improvement pull request. The list is not limited
// to the metrics date range.
type ImprovementPR struct {
	BatchID   string    `json:"batch_id"`
	Agent     string    `json:"agent"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// FactoryLifecycleData is the factory SSE data object for run.created and
// run.status. GID is the factory sequence. Stage is omitted when the event
// has no pipeline stage.
type FactoryLifecycleData struct {
	GID    int64  `json:"gid"`
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	Stage  string `json:"stage,omitempty"`
}
