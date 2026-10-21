package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/polds/factory/internal/api"
)

// batchInFlightConstraint is the partial unique index. A violation means
// the agent already has a queued, running, or pr_open batch.
const batchInFlightConstraint = "improvement_batches_one_inflight"

const batchColumns = `
    id, factory_id, agent_name, status, pr_url, COALESCE(pr_number, 0),
    definition_repo, rejection_note, COALESCE(improvement_run_id, ''),
    created_at, closed_at`

func constraintIs(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == name
}

func scanBatch(row pgx.Row) (ImprovementBatch, error) {
	var out ImprovementBatch
	err := row.Scan(
		&out.ID, &out.FactoryID, &out.Agent, &out.Status, &out.PRURL, &out.PRNumber,
		&out.DefinitionRepo, &out.RejectionNote, &out.ImprovementRunID,
		&out.CreatedAt, &out.ClosedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImprovementBatch{}, ErrNotFound
	}
	if err != nil {
		return ImprovementBatch{}, err
	}
	out.Failures = []ImprovementFailure{}
	return out, nil
}

func scanBatchFailure(row pgx.Row) (batchFailure, error) {
	var out batchFailure
	err := row.Scan(&out.BatchID, &out.RunID, &out.ResultID, &out.Scorer, &out.Label)
	if err != nil {
		return batchFailure{}, err
	}
	return out, nil
}

type batchFailure struct {
	BatchID  string
	RunID    string
	ResultID string
	Scorer   string
	Label    string
}

func (s *Store) loadBatch(ctx context.Context, q rowQuery, id string) (ImprovementBatch, error) {
	batch, err := scanBatch(q.QueryRow(ctx, `SELECT `+batchColumns+` FROM improvement_batches WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ImprovementBatch{}, err
		}
		return ImprovementBatch{}, fmt.Errorf("store: get batch: %w", err)
	}
	failRows, qerr := q.Query(ctx, `
		SELECT f.batch_id, sr.run_id, sr.id, sr.scorer_name, sr.label
		FROM improvement_batch_failures AS f
		JOIN scorer_results AS sr ON sr.id = f.scorer_result_id
		WHERE f.batch_id = $1
		ORDER BY sr.created_at ASC, sr.id ASC`, id)
	failures, err := collect(failRows, qerr, "store: batch failures", scanBatchFailure)
	if err != nil {
		return ImprovementBatch{}, err
	}
	for _, failure := range failures {
		batch.Failures = append(batch.Failures, ImprovementFailure{
			RunID: failure.RunID, ResultID: failure.ResultID, Scorer: failure.Scorer, Label: failure.Label,
		})
	}
	return batch, nil
}

// CreateBatch inserts a queued batch and its failure links.
// A partial unique index violation returns api.ErrBatchInFlight.
func (s *Store) CreateBatch(ctx context.Context, in NewImprovementBatch) (ImprovementBatch, error) {
	if in.FactoryID == "" || in.Agent == "" {
		return ImprovementBatch{}, fmt.Errorf("store: factory and agent are required")
	}
	if len(in.FailureIDs) == 0 {
		return ImprovementBatch{}, fmt.Errorf("store: batch failures are required")
	}
	if err := s.requireFactory(ctx, s.pool, in.FactoryID); err != nil {
		return ImprovementBatch{}, err
	}
	id, err := NewID(PrefixImproveBatch)
	if err != nil {
		return ImprovementBatch{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ImprovementBatch{}, fmt.Errorf("store: create batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	foundRows, qerr := tx.Query(ctx, `
		SELECT id, factory_id FROM scorer_results WHERE id = ANY($1)`, in.FailureIDs)
	found, err := collect(foundRows, qerr, "store: create batch", scanResultFactory)
	if err != nil {
		return ImprovementBatch{}, err
	}
	byID := map[string]string{}
	for _, row := range found {
		byID[row.ID] = row.FactoryID
	}
	for _, resultID := range in.FailureIDs {
		factoryID, ok := byID[resultID]
		if !ok {
			return ImprovementBatch{}, ErrNotFound
		}
		if factoryID != in.FactoryID {
			return ImprovementBatch{}, fmt.Errorf("store: scorer result %s belongs to another factory", resultID)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO improvement_batches (id, factory_id, agent_name, status)
		VALUES ($1, $2, $3, $4)`,
		id, in.FactoryID, in.Agent, api.BatchQueued,
	); constraintIs(err, batchInFlightConstraint) {
		return ImprovementBatch{}, fmt.Errorf("store: create batch: %w", api.ErrBatchInFlight)
	} else if err != nil {
		return ImprovementBatch{}, fmt.Errorf("store: create batch: %w", err)
	}
	for _, resultID := range in.FailureIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO improvement_batch_failures (batch_id, scorer_result_id)
			VALUES ($1, $2)`, id, resultID); err != nil {
			return ImprovementBatch{}, fmt.Errorf("store: create batch: %w", err)
		}
	}
	batch, err := s.loadBatch(ctx, tx, id)
	if err != nil {
		return ImprovementBatch{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ImprovementBatch{}, fmt.Errorf("store: create batch: %w", err)
	}
	return batch, nil
}

type resultFactory struct {
	ID        string
	FactoryID string
}

func scanResultFactory(row pgx.Row) (resultFactory, error) {
	var out resultFactory
	err := row.Scan(&out.ID, &out.FactoryID)
	if err != nil {
		return resultFactory{}, err
	}
	return out, nil
}

// MarkBatchReviewed sets the batch to merged or closed and stamps every
// linked failure in the same transaction. The write is guarded on an
// in-flight status. A batch that is already merged, closed, or failed
// is left untouched.
func (s *Store) MarkBatchReviewed(ctx context.Context, batchID, status, rejectionNote string) error {
	switch status {
	case api.BatchMerged, api.BatchClosed:
	default:
		return fmt.Errorf("store: batch status %q is not a review outcome", status)
	}
	if batchID == "" {
		return fmt.Errorf("store: batch is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: review batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := lockBatchStatus(ctx, tx, batchID)
	if err != nil {
		return err
	}
	switch current {
	case api.BatchMerged, api.BatchClosed, api.BatchFailed:
		return nil
	case api.BatchQueued, api.BatchRunning, api.BatchPROpen:
	default:
		return fmt.Errorf("store: batch status %q cannot be reviewed", current)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE improvement_batches
		SET status = $2,
		    rejection_note = CASE WHEN $3 = '' THEN rejection_note ELSE $3 END,
		    closed_at = clock_timestamp()
		WHERE id = $1 AND status = $4`,
		batchID, status, rejectionNote, current)
	if err != nil {
		return fmt.Errorf("store: review batch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("store: review batch: status changed")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE scorer_results
		SET reviewed_at = clock_timestamp(),
		    reviewed_by_batch_id = $1
		WHERE id IN (
		    SELECT scorer_result_id
		    FROM improvement_batch_failures
		    WHERE batch_id = $1
		)
		  AND reviewed_at IS NULL`, batchID); err != nil {
		return fmt.Errorf("store: review batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: review batch: %w", err)
	}
	return nil
}

func lockBatchStatus(ctx context.Context, tx pgx.Tx, batchID string) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM improvement_batches WHERE id = $1 FOR UPDATE`, batchID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: batch: %w", err)
	}
	return status, nil
}

// SetBatchStatus moves an in-flight batch to running or failed.
// failed stores the rejection note and closed_at, and leaves failures
// unreviewed. A batch already at status is a no-op.
func (s *Store) SetBatchStatus(ctx context.Context, batchID, status, rejectionNote string) error {
	switch status {
	case api.BatchRunning, api.BatchFailed:
	default:
		return fmt.Errorf("store: batch status %q is not running or failed", status)
	}
	if batchID == "" {
		return fmt.Errorf("store: batch is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: set batch status: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := lockBatchStatus(ctx, tx, batchID)
	if err != nil {
		return err
	}
	if current == status {
		return nil
	}
	switch status {
	case api.BatchRunning:
		if current != api.BatchQueued {
			return fmt.Errorf("store: batch is %s", current)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE improvement_batches
			SET status = $2
			WHERE id = $1 AND status = $3`,
			batchID, api.BatchRunning, current); err != nil {
			return fmt.Errorf("store: set batch status: %w", err)
		}
	case api.BatchFailed:
		if current != api.BatchQueued && current != api.BatchRunning {
			return fmt.Errorf("store: batch is %s", current)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE improvement_batches
			SET status = $2,
			    rejection_note = CASE WHEN $3 = '' THEN rejection_note ELSE $3 END,
			    closed_at = clock_timestamp()
			WHERE id = $1 AND status = $4`,
			batchID, api.BatchFailed, rejectionNote, current); err != nil {
			return fmt.Errorf("store: set batch status: %w", err)
		}
	default:
		return fmt.Errorf("store: batch status %q is not running or failed", status)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: set batch status: %w", err)
	}
	return nil
}

// SetBatchRun stores the improvement run and moves a queued batch to running.
// Repeating the same run id is a no-op. A different run id conflicts.
func (s *Store) SetBatchRun(ctx context.Context, batchID, runID string) error {
	if batchID == "" || runID == "" {
		return fmt.Errorf("store: batch and run are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: set batch run: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var factoryID, status, currentRun string
	err = tx.QueryRow(ctx, `
		SELECT factory_id, status, COALESCE(improvement_run_id, '')
		FROM improvement_batches
		WHERE id = $1
		FOR UPDATE`, batchID).Scan(&factoryID, &status, &currentRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: set batch run: %w", err)
	}
	if err := s.runInFactory(ctx, tx, runID, factoryID); err != nil {
		return err
	}
	if currentRun == runID && (status == api.BatchRunning || status == api.BatchPROpen) {
		return nil
	}
	if currentRun != "" && currentRun != runID {
		return ErrConflict
	}
	if status != api.BatchQueued && status != api.BatchRunning {
		return fmt.Errorf("store: batch is %s", status)
	}
	next := status
	if status == api.BatchQueued {
		next = api.BatchRunning
	}
	if _, err := tx.Exec(ctx, `
		UPDATE improvement_batches
		SET improvement_run_id = $2,
		    status = $3
		WHERE id = $1 AND status = $4`,
		batchID, runID, next, status); err != nil {
		return fmt.Errorf("store: set batch run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: set batch run: %w", err)
	}
	return nil
}

// SetBatchPR records the pull request and moves the batch to pr_open.
// A second call with the same url and number is a no-op. A different
// pull request conflicts. Empty fields are not written over stored ones.
func (s *Store) SetBatchPR(ctx context.Context, batchID, prURL string, prNumber int, definitionRepo string) error {
	if batchID == "" || prURL == "" || prNumber <= 0 {
		return fmt.Errorf("store: batch, pull request url, and number are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: set batch pr: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status, currentURL string
	var currentNumber int
	err = tx.QueryRow(ctx, `
		SELECT status, pr_url, COALESCE(pr_number, 0)
		FROM improvement_batches
		WHERE id = $1
		FOR UPDATE`, batchID).Scan(&status, &currentURL, &currentNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: set batch pr: %w", err)
	}
	if status == api.BatchPROpen && currentURL == prURL && currentNumber == prNumber {
		return nil
	}
	if currentURL != "" && (currentURL != prURL || currentNumber != prNumber) {
		return ErrConflict
	}
	if status != api.BatchQueued && status != api.BatchRunning && status != api.BatchPROpen {
		return fmt.Errorf("store: batch is %s", status)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE improvement_batches
		SET pr_url = $2,
		    pr_number = $3,
		    definition_repo = CASE WHEN $4 = '' THEN definition_repo ELSE $4 END,
		    status = $5
		WHERE id = $1 AND status = $6`,
		batchID, prURL, prNumber, definitionRepo, api.BatchPROpen, status); err != nil {
		return fmt.Errorf("store: set batch pr: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: set batch pr: %w", err)
	}
	return nil
}

// ImprovementBatches returns a factory's batches, newest first, with failures.
func (s *Store) ImprovementBatches(ctx context.Context, factoryID string) ([]ImprovementBatch, error) {
	if err := s.requireFactory(ctx, s.pool, factoryID); err != nil {
		return nil, err
	}
	batchRows, qerr := s.pool.Query(ctx, `
		SELECT `+batchColumns+`
		FROM improvement_batches
		WHERE factory_id = $1
		ORDER BY created_at DESC, id DESC`, factoryID)
	batches, err := collect(batchRows, qerr, "store: list batches", scanBatch)
	if err != nil {
		return nil, err
	}
	failRows, qerr := s.pool.Query(ctx, `
		SELECT f.batch_id, sr.run_id, sr.id, sr.scorer_name, sr.label
		FROM improvement_batch_failures AS f
		JOIN improvement_batches AS b ON b.id = f.batch_id
		JOIN scorer_results AS sr ON sr.id = f.scorer_result_id
		WHERE b.factory_id = $1
		ORDER BY sr.created_at ASC, sr.id ASC`, factoryID)
	failures, err := collect(failRows, qerr, "store: list batch failures", scanBatchFailure)
	if err != nil {
		return nil, err
	}
	byBatch := map[string][]ImprovementFailure{}
	for _, failure := range failures {
		byBatch[failure.BatchID] = append(byBatch[failure.BatchID], ImprovementFailure{
			RunID: failure.RunID, ResultID: failure.ResultID, Scorer: failure.Scorer, Label: failure.Label,
		})
	}
	for i := range batches {
		if rows := byBatch[batches[i].ID]; rows != nil {
			batches[i].Failures = rows
		}
	}
	return batches, nil
}
