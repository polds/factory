package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
)

const benchmarkRunColumns = `
    id, factory_id, suite, requested_by, repetitions, created_at`

const benchmarkTrialColumns = `
    id, benchmark_run_id, configuration, task, repetition, run_id,
    outcome, cost_cents, duration_ms, COALESCE(correctness_result_id, ''),
    definition_sha`

func scanBenchmarkRun(row pgx.Row) (BenchmarkRun, error) {
	var out BenchmarkRun
	err := row.Scan(&out.ID, &out.FactoryID, &out.Suite, &out.RequestedBy, &out.Repetitions, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BenchmarkRun{}, ErrNotFound
	}
	if err != nil {
		return BenchmarkRun{}, err
	}
	return out, nil
}

func scanBenchmarkTrial(row pgx.Row) (BenchmarkTrial, error) {
	var out BenchmarkTrial
	err := row.Scan(
		&out.ID, &out.BenchmarkRunID, &out.Configuration, &out.Task, &out.Repetition,
		&out.RunID, &out.Outcome, &out.CostCents, &out.DurationMS, &out.CorrectnessResultID,
		&out.DefinitionSHA,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BenchmarkTrial{}, ErrNotFound
	}
	if err != nil {
		return BenchmarkTrial{}, err
	}
	return out, nil
}

func validTrialOutcome(outcome string) error {
	switch outcome {
	case api.TrialPass, api.TrialFail, api.TrialErrored, api.TrialPending:
		return nil
	default:
		return fmt.Errorf("store: trial outcome %q is not valid", outcome)
	}
}

// CreateBenchmarkRun inserts one suite run. Repetitions below 1 is rejected.
func (s *Store) CreateBenchmarkRun(ctx context.Context, factory, suite, requestedBy string, repetitions int) (BenchmarkRun, error) {
	if suite == "" {
		return BenchmarkRun{}, fmt.Errorf("store: suite is required")
	}
	if repetitions < 1 {
		return BenchmarkRun{}, fmt.Errorf("store: repetitions must be >= 1")
	}
	if err := s.requireFactory(ctx, s.pool, factory); err != nil {
		return BenchmarkRun{}, err
	}
	id, err := NewID(PrefixBenchmarkRun)
	if err != nil {
		return BenchmarkRun{}, err
	}
	out, err := scanBenchmarkRun(s.pool.QueryRow(ctx, `
		INSERT INTO benchmark_runs (id, factory_id, suite, requested_by, repetitions)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+benchmarkRunColumns,
		id, factory, suite, requestedBy, repetitions,
	))
	if err != nil {
		return BenchmarkRun{}, fmt.Errorf("store: create benchmark run: %w", err)
	}
	return out, nil
}

// LatestBenchmarkRun is the newest run of a suite, or ErrNotFound.
func (s *Store) LatestBenchmarkRun(ctx context.Context, factory, suite string) (BenchmarkRun, error) {
	if suite == "" {
		return BenchmarkRun{}, fmt.Errorf("store: suite is required")
	}
	if err := s.requireFactory(ctx, s.pool, factory); err != nil {
		return BenchmarkRun{}, err
	}
	out, err := scanBenchmarkRun(s.pool.QueryRow(ctx, `
		SELECT `+benchmarkRunColumns+`
		FROM benchmark_runs
		WHERE factory_id = $1 AND suite = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, factory, suite))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return BenchmarkRun{}, err
		}
		return BenchmarkRun{}, fmt.Errorf("store: latest benchmark run: %w", err)
	}
	return out, nil
}

// RecordTrial inserts one trial row. The benchmark run and the trial run
// must already exist.
func (s *Store) RecordTrial(ctx context.Context, in BenchmarkTrial) (BenchmarkTrial, error) {
	if in.BenchmarkRunID == "" || in.Configuration == "" || in.Task == "" || in.RunID == "" {
		return BenchmarkTrial{}, fmt.Errorf("store: benchmark run, configuration, task, and run are required")
	}
	if in.Repetition < 1 {
		return BenchmarkTrial{}, fmt.Errorf("store: repetition must be >= 1")
	}
	if err := validTrialOutcome(in.Outcome); err != nil {
		return BenchmarkTrial{}, err
	}
	var factoryID string
	err := s.pool.QueryRow(ctx, `
		SELECT factory_id FROM benchmark_runs WHERE id = $1`, in.BenchmarkRunID).Scan(&factoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BenchmarkTrial{}, ErrNotFound
	}
	if err != nil {
		return BenchmarkTrial{}, fmt.Errorf("store: record trial: %w", err)
	}
	if err := s.runInFactory(ctx, s.pool, in.RunID, factoryID); err != nil {
		return BenchmarkTrial{}, err
	}
	if in.CorrectnessResultID != "" {
		var resultFactory string
		err := s.pool.QueryRow(ctx, `
			SELECT factory_id FROM scorer_results WHERE id = $1`, in.CorrectnessResultID).Scan(&resultFactory)
		if errors.Is(err, pgx.ErrNoRows) {
			return BenchmarkTrial{}, ErrNotFound
		}
		if err != nil {
			return BenchmarkTrial{}, fmt.Errorf("store: record trial: %w", err)
		}
		if resultFactory != factoryID {
			return BenchmarkTrial{}, fmt.Errorf("store: scorer result %s belongs to another factory", in.CorrectnessResultID)
		}
	}
	id := in.ID
	if id == "" {
		id, err = NewID(PrefixBenchmarkTrial)
		if err != nil {
			return BenchmarkTrial{}, err
		}
	}
	out, err := scanBenchmarkTrial(s.pool.QueryRow(ctx, `
		INSERT INTO benchmark_trials (
		    id, benchmark_run_id, configuration, task, repetition, run_id,
		    outcome, cost_cents, duration_ms, correctness_result_id, definition_sha)
		VALUES (
		    $1, $2, $3, $4, $5, $6,
		    $7, $8, $9, NULLIF($10, ''), $11)
		RETURNING `+benchmarkTrialColumns,
		id, in.BenchmarkRunID, in.Configuration, in.Task, in.Repetition, in.RunID,
		in.Outcome, in.CostCents, in.DurationMS, in.CorrectnessResultID, in.DefinitionSHA,
	))
	if err != nil {
		return BenchmarkTrial{}, fmt.Errorf("store: record trial: %w", err)
	}
	return out, nil
}

// TrialCells aggregates a benchmark run by configuration and task.
// n is pass plus fail. Errored is counted beside n. Pending is omitted
// from both. Medians use the pass and fail rows only.
func (s *Store) TrialCells(ctx context.Context, benchmarkRunID string) ([]TrialCell, error) {
	if benchmarkRunID == "" {
		return nil, fmt.Errorf("store: benchmark run is required")
	}
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM benchmark_runs WHERE id = $1`, benchmarkRunID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: trial cells: %w", err)
	}
	trialRows, qerr := s.pool.Query(ctx, `
		SELECT `+benchmarkTrialColumns+`
		FROM benchmark_trials
		WHERE benchmark_run_id = $1
		ORDER BY configuration ASC, task ASC, repetition ASC, id ASC`, benchmarkRunID)
	trials, err := collect(trialRows, qerr, "store: list trials", scanBenchmarkTrial)
	if err != nil {
		return nil, err
	}
	return trialCells(trials), nil
}

type trialKey struct {
	Configuration string
	Task          string
}

func trialCells(trials []BenchmarkTrial) []TrialCell {
	type acc struct {
		cell     TrialCell
		costs    []int64
		duration []int64
	}
	by := map[trialKey]*acc{}
	var order []trialKey
	for _, trial := range trials {
		key := trialKey{trial.Configuration, trial.Task}
		row := by[key]
		if row == nil {
			row = &acc{cell: TrialCell{Configuration: trial.Configuration, Task: trial.Task}}
			by[key] = row
			order = append(order, key)
		}
		switch trial.Outcome {
		case api.TrialPass:
			row.cell.N++
			row.cell.Pass++
			row.costs = append(row.costs, trial.CostCents)
			row.duration = append(row.duration, trial.DurationMS)
		case api.TrialFail:
			row.cell.N++
			row.costs = append(row.costs, trial.CostCents)
			row.duration = append(row.duration, trial.DurationMS)
		case api.TrialErrored:
			row.cell.Errored++
		case api.TrialPending:
		default:
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].Configuration != order[j].Configuration {
			return order[i].Configuration < order[j].Configuration
		}
		return order[i].Task < order[j].Task
	})
	out := make([]TrialCell, 0, len(order))
	for _, key := range order {
		row := by[key]
		row.cell.MedianCostCents = medianInt64(row.costs)
		row.cell.MedianDurationMS = medianInt64(row.duration)
		out = append(out, row.cell)
	}
	return out
}

// GetBenchmarkRun returns one suite run or ErrNotFound.
func (s *Store) GetBenchmarkRun(ctx context.Context, id string) (BenchmarkRun, error) {
	if id == "" {
		return BenchmarkRun{}, fmt.Errorf("store: benchmark run is required")
	}
	out, err := scanBenchmarkRun(s.pool.QueryRow(ctx, `
		SELECT `+benchmarkRunColumns+` FROM benchmark_runs WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return BenchmarkRun{}, err
		}
		return BenchmarkRun{}, fmt.Errorf("store: get benchmark run: %w", err)
	}
	return out, nil
}

// BenchmarkTrialByRun returns the trial whose run is runID, or ErrNotFound.
func (s *Store) BenchmarkTrialByRun(ctx context.Context, runID string) (BenchmarkTrial, error) {
	if runID == "" {
		return BenchmarkTrial{}, fmt.Errorf("store: run is required")
	}
	out, err := scanBenchmarkTrial(s.pool.QueryRow(ctx, `
		SELECT `+benchmarkTrialColumns+`
		FROM benchmark_trials
		WHERE run_id = $1`, runID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return BenchmarkTrial{}, err
		}
		return BenchmarkTrial{}, fmt.Errorf("store: trial by run: %w", err)
	}
	return out, nil
}

// ListBenchmarkTrials returns every trial of a benchmark run, in cell order.
func (s *Store) ListBenchmarkTrials(ctx context.Context, benchmarkRunID string) ([]BenchmarkTrial, error) {
	if benchmarkRunID == "" {
		return nil, fmt.Errorf("store: benchmark run is required")
	}
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM benchmark_runs WHERE id = $1`, benchmarkRunID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: list trials: %w", err)
	}
	rows, qerr := s.pool.Query(ctx, `
		SELECT `+benchmarkTrialColumns+`
		FROM benchmark_trials
		WHERE benchmark_run_id = $1
		ORDER BY configuration ASC, task ASC, repetition ASC, id ASC`, benchmarkRunID)
	return collect(rows, qerr, "store: list trials", scanBenchmarkTrial)
}

// SetTrialOutcome records a terminal outcome on the trial for runID.
// A trial that is already past pending is left unchanged, so a second
// finish after a restart does not overwrite the first verdict.
func (s *Store) SetTrialOutcome(ctx context.Context, runID, outcome string, costCents, durationMS int64, correctnessResultID string) error {
	if runID == "" {
		return fmt.Errorf("store: run is required")
	}
	if err := validTrialOutcome(outcome); err != nil {
		return err
	}
	if outcome == api.TrialPending {
		return fmt.Errorf("store: trial outcome pending is not terminal")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE benchmark_trials
		SET outcome = $2,
		    cost_cents = $3,
		    duration_ms = $4,
		    correctness_result_id = COALESCE(NULLIF($5, ''), correctness_result_id)
		WHERE run_id = $1 AND outcome = $6`,
		runID, outcome, costCents, durationMS, correctnessResultID, api.TrialPending)
	if err != nil {
		return fmt.Errorf("store: set trial outcome: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if _, err := s.BenchmarkTrialByRun(ctx, runID); err != nil {
		return err
	}
	return nil
}
