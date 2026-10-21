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
    outcome, cost_cents, duration_ms, COALESCE(correctness_result_id, '')`

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
		    outcome, cost_cents, duration_ms, correctness_result_id)
		VALUES (
		    $1, $2, $3, $4, $5, $6,
		    $7, $8, $9, NULLIF($10, ''))
		RETURNING `+benchmarkTrialColumns,
		id, in.BenchmarkRunID, in.Configuration, in.Task, in.Repetition, in.RunID,
		in.Outcome, in.CostCents, in.DurationMS, in.CorrectnessResultID,
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
