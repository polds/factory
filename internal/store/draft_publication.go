package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// DraftPending is committed before a publication token is minted.
	DraftPending = "pending"
	// DraftPushed means the ref exists and the pull request does not.
	DraftPushed = "pushed"
	// DraftOpened means GitHub returned a draft pull request at the SHA.
	DraftOpened = "opened"
	// DraftFailed is terminal. A later attempt does not mint or push.
	DraftFailed = "failed"
)

// DraftPublication is the durable publication record for one invocation.
// It has no token and no bundle bytes.
type DraftPublication struct {
	InvocationID string
	TargetRepo   string
	IssueKey     string
	State        string
	HeadSHA      string
	GitRef       string
	PRNumber     int
	PRURL        string
	Draft        bool
	Detail       string
}

// DraftAdvance is the guarded update applied with the current state.
type DraftAdvance struct {
	GitRef   string
	PRNumber int
	PRURL    string
	Draft    bool
	Detail   string
}

// InsertDraftPending commits a pending row. The same invocation returns the
// stored row. A different invocation for the same target issue returns
// ErrConflict and does not replace the row.
func (s *Store) InsertDraftPending(ctx context.Context, row DraftPublication) (DraftPublication, error) {
	if err := validateDraftPending(row); err != nil {
		return DraftPublication{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DraftPublication{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	existing, err := draftByInvocationTx(ctx, tx, row.InvocationID)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return DraftPublication{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return DraftPublication{}, err
	}
	inserted, err := insertDraftTx(ctx, tx, row)
	if err != nil {
		issue, conflict := draftConflict(err)
		if conflict && !issue {
			existing, readErr := draftByInvocationTx(ctx, tx, row.InvocationID)
			if readErr != nil {
				return DraftPublication{}, readErr
			}
			if err := tx.Commit(ctx); err != nil {
				return DraftPublication{}, err
			}
			return existing, nil
		}
		if issue {
			return DraftPublication{}, ErrConflict
		}
		return DraftPublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DraftPublication{}, err
	}
	return inserted, nil
}

// AdvanceDraft moves a row from one state to the next in one update.
// A row that is no longer in from is ErrConflict.
func (s *Store) AdvanceDraft(ctx context.Context, invocationID, from, to string, adv DraftAdvance) (DraftPublication, error) {
	if !draftTransition(from, to) {
		return DraftPublication{}, fmt.Errorf("%w: publication state", ErrInputInvalid)
	}
	if err := rejectSecretDetail(adv.Detail); err != nil {
		return DraftPublication{}, err
	}
	if to == DraftOpened && (!adv.Draft || adv.PRNumber <= 0 || strings.TrimSpace(adv.PRURL) == "") {
		return DraftPublication{}, fmt.Errorf("%w: publication open", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DraftPublication{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pr any
	if adv.PRNumber > 0 {
		pr = adv.PRNumber
	}
	var url any
	if strings.TrimSpace(adv.PRURL) != "" {
		url = adv.PRURL
	}
	var draft any
	if to == DraftOpened || to == DraftFailed {
		draft = adv.Draft
	}
	gitRef := adv.GitRef
	row, err := scanDraft(tx.QueryRow(ctx, `
		UPDATE draft_publications
		SET state = $3,
		    git_ref = CASE WHEN $4 = '' THEN git_ref ELSE $4 END,
		    pr_number = COALESCE($5, pr_number),
		    pr_url = COALESCE($6, pr_url),
		    draft = COALESCE($7, draft),
		    detail = $8,
		    updated_at = now()
		WHERE invocation_id = $1 AND state = $2
		RETURNING `+draftColumns,
		invocationID, from, to, gitRef, pr, url, draft, adv.Detail))
	if errors.Is(err, ErrNotFound) {
		return DraftPublication{}, ErrConflict
	}
	if err != nil {
		return DraftPublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DraftPublication{}, err
	}
	return row, nil
}

// GetDraftPublication returns the row for the invocation.
func (s *Store) GetDraftPublication(ctx context.Context, invocationID string) (DraftPublication, error) {
	row, err := scanDraft(s.pool.QueryRow(ctx, `
		SELECT `+draftColumns+`
		FROM draft_publications WHERE invocation_id = $1`, invocationID))
	if err != nil {
		return DraftPublication{}, err
	}
	return row, nil
}

// SubtreeGate is the cancel or pause gate on a task. ErrNotFound means
// the task has no gate.
func (s *Store) SubtreeGate(ctx context.Context, taskID string) (string, error) {
	var state string
	err := s.pool.QueryRow(ctx, `
		SELECT state FROM native_subtree_gates WHERE task_id = $1`, taskID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: subtree gate: %w", err)
	}
	return state, nil
}

// PublicationSpendBlocked reports a paused scope or missing usage.
func (s *Store) PublicationSpendBlocked(ctx context.Context, invocationID string) (bool, error) {
	var blocked bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM budget_scopes
		    WHERE invocation_id = $1 AND (paused OR missing_usage)
		)`, invocationID).Scan(&blocked)
	if err != nil {
		return false, fmt.Errorf("store: publication spend: %w", err)
	}
	return blocked, nil
}

// TaskRun is the newest activation run for a task. ErrNotFound means the
// task has not been claimed.
func (s *Store) TaskRun(ctx context.Context, taskID string) (string, error) {
	var runID string
	err := s.pool.QueryRow(ctx, `
		SELECT run_id FROM native_activations
		WHERE task_id = $1
		ORDER BY fence DESC
		LIMIT 1`, taskID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: task run: %w", err)
	}
	return runID, nil
}

// InvocationRootStatus is the parentless task status. ErrNotFound means
// the invocation has no root task.
func (s *Store) InvocationRootStatus(ctx context.Context, invocationID string) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `
		SELECT task_status FROM native_tasks
		WHERE invocation_id = $1 AND parent_task_id IS NULL`, invocationID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: invocation root: %w", err)
	}
	return status, nil
}

const draftColumns = `invocation_id, target_repo, issue_key, state, head_sha, git_ref,
	pr_number, pr_url, draft, detail`

func validateDraftPending(row DraftPublication) error {
	if row.InvocationID == "" || row.TargetRepo == "" || row.IssueKey == "" || row.GitRef == "" {
		return fmt.Errorf("%w: publication", ErrInputInvalid)
	}
	if len(row.HeadSHA) != 40 {
		return fmt.Errorf("%w: publication sha", ErrInputInvalid)
	}
	if strings.Contains(row.GitRef, "..") || strings.HasPrefix(row.GitRef, "/") || strings.Contains(row.GitRef, " ") {
		return fmt.Errorf("%w: publication ref", ErrInputInvalid)
	}
	return rejectSecretDetail(row.Detail)
}

func rejectSecretDetail(detail string) error {
	lower := strings.ToLower(detail)
	if strings.Contains(detail, "ghs_") || strings.Contains(lower, "bearer ") || strings.Contains(detail, "-----BEGIN") {
		return fmt.Errorf("%w: publication detail", ErrInputInvalid)
	}
	if len(detail) > 200 {
		return fmt.Errorf("%w: publication detail", ErrInputInvalid)
	}
	return nil
}

func draftTransition(from, to string) bool {
	switch from {
	case DraftPending:
		return to == DraftPushed || to == DraftFailed
	case DraftPushed:
		return to == DraftOpened || to == DraftFailed
	default:
		return false
	}
}

func insertDraftTx(ctx context.Context, tx pgx.Tx, row DraftPublication) (DraftPublication, error) {
	inserted, err := scanDraft(tx.QueryRow(ctx, `
		INSERT INTO draft_publications (
		    invocation_id, target_repo, issue_key, state, head_sha, git_ref, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+draftColumns,
		row.InvocationID, row.TargetRepo, row.IssueKey, DraftPending, row.HeadSHA, row.GitRef, row.Detail))
	if err != nil {
		return DraftPublication{}, fmt.Errorf("store: publication: %w", err)
	}
	return inserted, nil
}

func draftByInvocationTx(ctx context.Context, tx pgx.Tx, invocationID string) (DraftPublication, error) {
	return scanDraft(tx.QueryRow(ctx, `
		SELECT `+draftColumns+`
		FROM draft_publications WHERE invocation_id = $1 FOR UPDATE`, invocationID))
}

func scanDraft(row pgx.Row) (DraftPublication, error) {
	var out DraftPublication
	var number *int
	var url *string
	var draft *bool
	err := row.Scan(
		&out.InvocationID, &out.TargetRepo, &out.IssueKey, &out.State, &out.HeadSHA, &out.GitRef,
		&number, &url, &draft, &out.Detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return DraftPublication{}, ErrNotFound
	}
	if err != nil {
		return DraftPublication{}, fmt.Errorf("store: publication: %w", err)
	}
	if number != nil {
		out.PRNumber = *number
	}
	if url != nil {
		out.PRURL = *url
	}
	if draft != nil {
		out.Draft = *draft
	}
	return out, nil
}

func draftConflict(err error) (issue bool, conflict bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false, false
	}
	if pgErr.ConstraintName == "draft_publications_target_issue" {
		return true, true
	}
	return false, true
}
