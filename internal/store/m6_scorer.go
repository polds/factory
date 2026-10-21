package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/api"
)

const scorerColumns = `
    id, factory_id, run_id, scorer_name, COALESCE(judge_run_id, ''),
    label, score, passed, reasoning, status, created_at, reviewed_at,
    COALESCE(reviewed_by_batch_id, '')`

func scanScorerResult(row pgx.Row) (ScorerResult, error) {
	var out ScorerResult
	err := row.Scan(
		&out.ID, &out.FactoryID, &out.RunID, &out.ScorerName, &out.JudgeRunID,
		&out.Label, &out.Score, &out.Passed, &out.Reasoning, &out.Status,
		&out.CreatedAt, &out.ReviewedAt, &out.ReviewedByBatchID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScorerResult{}, ErrNotFound
	}
	if err != nil {
		return ScorerResult{}, err
	}
	return out, nil
}

func validScorerStatus(status string) error {
	switch status {
	case api.ScorerStatusOK, api.ScorerStatusInvalid, api.ScorerStatusPending:
		return nil
	default:
		return fmt.Errorf("store: scorer status %q is not valid", status)
	}
}

func (s *Store) runInFactory(ctx context.Context, q rowQuery, runID, factoryID string) error {
	var got string
	err := q.QueryRow(ctx, `SELECT factory_id FROM runs WHERE id = $1`, runID).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: run: %w", err)
	}
	if got != factoryID {
		return fmt.Errorf("store: run %s belongs to another factory", runID)
	}
	return nil
}

// UpsertScorerResult inserts or replaces the row for (run, scorer).
// The id from the first insert is kept. A replacement clears review.
func (s *Store) UpsertScorerResult(ctx context.Context, in ScorerResult) (ScorerResult, error) {
	if in.FactoryID == "" || in.RunID == "" || in.ScorerName == "" {
		return ScorerResult{}, fmt.Errorf("store: factory, run, and scorer are required")
	}
	if err := validScorerStatus(in.Status); err != nil {
		return ScorerResult{}, err
	}
	if err := s.requireFactory(ctx, s.pool, in.FactoryID); err != nil {
		return ScorerResult{}, err
	}
	if err := s.runInFactory(ctx, s.pool, in.RunID, in.FactoryID); err != nil {
		return ScorerResult{}, err
	}
	if in.JudgeRunID != "" {
		if err := s.runInFactory(ctx, s.pool, in.JudgeRunID, in.FactoryID); err != nil {
			return ScorerResult{}, err
		}
	}
	id := in.ID
	if id == "" {
		var err error
		id, err = NewID(PrefixScorerResult)
		if err != nil {
			return ScorerResult{}, err
		}
	}
	out, err := scanScorerResult(s.pool.QueryRow(ctx, `
		INSERT INTO scorer_results (
		    id, factory_id, run_id, scorer_name, judge_run_id,
		    label, score, passed, reasoning, status)
		VALUES (
		    $1, $2, $3, $4, NULLIF($5, ''),
		    $6, $7, $8, $9, $10)
		ON CONFLICT (run_id, scorer_name) DO UPDATE
		SET judge_run_id = EXCLUDED.judge_run_id,
		    label = EXCLUDED.label,
		    score = EXCLUDED.score,
		    passed = EXCLUDED.passed,
		    reasoning = EXCLUDED.reasoning,
		    status = EXCLUDED.status,
		    created_at = clock_timestamp(),
		    reviewed_at = NULL,
		    reviewed_by_batch_id = NULL
		RETURNING `+scorerColumns,
		id, in.FactoryID, in.RunID, in.ScorerName, in.JudgeRunID,
		in.Label, in.Score, in.Passed, in.Reasoning, in.Status,
	))
	if err != nil {
		return ScorerResult{}, fmt.Errorf("store: upsert scorer result: %w", err)
	}
	return out, nil
}

// ScorerResults lists one scorer's results, oldest first.
// A zero from or to does not bound that side. A nil passed keeps both.
// The upper bound is exclusive.
func (s *Store) ScorerResults(ctx context.Context, factory, scorer string, from, to time.Time, passed *bool) ([]ScorerResult, error) {
	if scorer == "" {
		return nil, fmt.Errorf("store: scorer is required")
	}
	if err := s.requireFactory(ctx, s.pool, factory); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+scorerColumns+`
		FROM scorer_results
		WHERE factory_id = $1
		  AND scorer_name = $2
		  AND ($3::timestamptz IS NULL OR created_at >= $3)
		  AND ($4::timestamptz IS NULL OR created_at < $4)
		  AND ($5::boolean IS NULL OR passed = $5)
		ORDER BY created_at ASC, id ASC`,
		factory, scorer, nullTime(from), nullTime(to), passed,
	)
	return collect(rows, err, "store: list scorer results", scanScorerResult)
}

type passDay struct {
	Scorer string
	Day    string
	N      int
	Pass   int
}

func scanPassDay(row pgx.Row) (passDay, error) {
	var day passDay
	err := row.Scan(&day.Scorer, &day.Day, &day.N, &day.Pass)
	if err != nil {
		return passDay{}, err
	}
	return day, nil
}

func (s *Store) passDays(ctx context.Context, factory, scorer string, from, to time.Time) ([]passDay, error) {
	if err := s.requireFactory(ctx, s.pool, factory); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT scorer_name,
		       to_char(timezone('utc', created_at), 'YYYY-MM-DD'),
		       count(*)::int,
		       count(*) FILTER (WHERE passed)::int
		FROM scorer_results
		WHERE factory_id = $1
		  AND status = $2
		  AND ($3 = '' OR scorer_name = $3)
		  AND ($4::timestamptz IS NULL OR created_at >= $4)
		  AND ($5::timestamptz IS NULL OR created_at < $5)
		GROUP BY scorer_name, 2
		ORDER BY scorer_name ASC, 2 ASC`,
		factory, api.ScorerStatusOK, scorer, nullTime(from), nullTime(to),
	)
	return collect(rows, err, "store: scorer pass rate", scanPassDay)
}

func dayRates(days []passDay) []api.DayPassRate {
	out := make([]api.DayPassRate, 0, len(days))
	for _, day := range days {
		if day.N == 0 {
			continue
		}
		out = append(out, api.DayPassRate{
			Day:      day.Day,
			PassRate: float64(day.Pass) / float64(day.N),
			N:        day.N,
		})
	}
	return out
}

// ScorerPassRateByDay is the ok-result pass rate per UTC day.
// Pending and invalid rows are not in n. A zero bound is open.
func (s *Store) ScorerPassRateByDay(ctx context.Context, factory, scorer string, from, to time.Time) ([]api.DayPassRate, error) {
	if scorer == "" {
		return nil, fmt.Errorf("store: scorer is required")
	}
	days, err := s.passDays(ctx, factory, scorer, from, to)
	if err != nil {
		return nil, err
	}
	return dayRates(days), nil
}

// RunScores returns every scorer row for the run, scorer name order.
func (s *Store) RunScores(ctx context.Context, runID string) ([]ScorerResult, error) {
	if runID == "" {
		return nil, fmt.Errorf("store: run is required")
	}
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM runs WHERE id = $1`, runID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: run scores: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+scorerColumns+`
		FROM scorer_results
		WHERE run_id = $1
		ORDER BY scorer_name ASC, id ASC`, runID)
	return collect(rows, err, "store: run scores", scanScorerResult)
}

func scanFailure(row pgx.Row) (UnreviewedFailure, error) {
	var out UnreviewedFailure
	err := row.Scan(&out.ResultID, &out.RunID, &out.Agent, &out.Scorer, &out.Label, &out.Reasoning, &out.At)
	if err != nil {
		return UnreviewedFailure{}, err
	}
	return out, nil
}

// UnreviewedFailures lists ok results that failed and have not been reviewed.
// An empty agent returns every agent in the factory. Oldest first.
func (s *Store) UnreviewedFailures(ctx context.Context, factory, agent string) ([]UnreviewedFailure, error) {
	if err := s.requireFactory(ctx, s.pool, factory); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.run_id, COALESCE(a.name, ''), r.scorer_name,
		       r.label, r.reasoning, r.created_at
		FROM scorer_results AS r
		JOIN runs ON runs.id = r.run_id
		LEFT JOIN agents AS a ON a.id = runs.agent_id
		WHERE r.factory_id = $1
		  AND r.reviewed_at IS NULL
		  AND r.passed = false
		  AND r.status = $2
		  AND ($3 = '' OR a.name = $3)
		ORDER BY r.created_at ASC, r.id ASC`,
		factory, api.ScorerStatusOK, agent,
	)
	return collect(rows, err, "store: unreviewed failures", scanFailure)
}
