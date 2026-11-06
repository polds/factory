package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// InvocationRow is a persisted receipt.
type InvocationRow struct {
	ID                  string
	FactoryID           string
	WorkItemID          string
	ProfileName         string
	SourceOccurrenceKey string
	CanonicalDigest     string
	DefinitionRevision  string
	DefinitionDigest    string
	PolicyDigest        string
	ProfileDigest       string
	PrincipalJSON       json.RawMessage
	State               string
	Outcome             string
	CountsAsCompleted   bool
	Intent              string
	Priority            string
	CorrelationJSON     json.RawMessage
	TargetRepository    string
	TargetRef           string
	TargetPullRequest   *int
	PinnedHeadSHA       string
	PinnedBaseSHA       string
	NeutralStage        bool
	CreatedAt           time.Time
}

// InsertInvocationParams is the first-write payload for a new receipt.
type InsertInvocationParams struct {
	FactoryID           string
	ProfileName         string
	SourceOccurrenceKey string
	CanonicalDigest     string
	DefinitionRevision  string
	DefinitionDigest    string
	PolicyDigest        string
	ProfileDigest       string
	PrincipalJSON       json.RawMessage
	State               string
	Outcome             string
	CountsAsCompleted   bool
	Intent              string
	Priority            string
	CorrelationJSON     json.RawMessage
	TargetRepository    string
	TargetRef           string
	TargetPullRequest   *int
	PinnedHeadSHA       string
	PinnedBaseSHA       string
	WorkItemID          string
	NeutralStage        bool
}

// InsertInvocationResult is either a fresh row or a replay/conflict outcome.
type InsertInvocationResult struct {
	Row      InvocationRow
	Created  bool
	Conflict bool
}

const invocationColumns = `
    id, factory_id, work_item_id, profile_name, source_occurrence_key,
    canonical_digest, definition_revision, definition_digest, policy_digest,
    profile_digest, principal, state, outcome, counts_as_completed_scan,
    intent, priority, correlation, target_repository, target_ref,
    target_pull_request, pinned_head_sha, pinned_base_sha, neutral_stage, created_at`

func scanInvocation(row pgx.Row) (InvocationRow, error) {
	var inv InvocationRow
	var corr []byte
	var pr *int
	var workItem sql.NullString
	err := row.Scan(
		&inv.ID, &inv.FactoryID, &workItem, &inv.ProfileName,
		&inv.SourceOccurrenceKey, &inv.CanonicalDigest, &inv.DefinitionRevision,
		&inv.DefinitionDigest, &inv.PolicyDigest, &inv.ProfileDigest,
		&inv.PrincipalJSON, &inv.State, &inv.Outcome, &inv.CountsAsCompleted,
		&inv.Intent, &inv.Priority, &corr, &inv.TargetRepository, &inv.TargetRef,
		&pr, &inv.PinnedHeadSHA, &inv.PinnedBaseSHA, &inv.NeutralStage, &inv.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationRow{}, ErrNotFound
	}
	if err != nil {
		return InvocationRow{}, err
	}
	if workItem.Valid {
		inv.WorkItemID = workItem.String
	}
	inv.CorrelationJSON = corr
	inv.TargetPullRequest = pr
	return inv, nil
}

// InsertInvocation inserts or replays on (factory_id, profile_name, source_occurrence_key).
func (s *Store) InsertInvocation(ctx context.Context, p InsertInvocationParams) (InsertInvocationResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InsertInvocationResult{}, fmt.Errorf("store: insert invocation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanInvocation(tx.QueryRow(ctx, `
		SELECT `+invocationColumns+`
		FROM invocations
		WHERE factory_id = $1 AND profile_name = $2 AND source_occurrence_key = $3
		FOR UPDATE`, p.FactoryID, p.ProfileName, p.SourceOccurrenceKey))
	if err == nil {
		if existing.CanonicalDigest != p.CanonicalDigest {
			return InsertInvocationResult{Conflict: true}, nil
		}
		if err := tx.Commit(ctx); err != nil {
			return InsertInvocationResult{}, err
		}
		return InsertInvocationResult{Row: existing, Created: false}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return InsertInvocationResult{}, fmt.Errorf("store: insert invocation: %w", err)
	}

	id, err := NewID(PrefixInvocation)
	if err != nil {
		return InsertInvocationResult{}, err
	}
	row, err := scanInvocation(tx.QueryRow(ctx, `
		INSERT INTO invocations (
		    id, factory_id, work_item_id, profile_name, source_occurrence_key,
		    canonical_digest, definition_revision, definition_digest, policy_digest,
		    profile_digest, principal, state, outcome, counts_as_completed_scan,
		    intent, priority, correlation, target_repository, target_ref,
		    target_pull_request, pinned_head_sha, pinned_base_sha, neutral_stage)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		RETURNING `+invocationColumns,
		id, p.FactoryID, nullableText(p.WorkItemID), p.ProfileName, p.SourceOccurrenceKey,
		p.CanonicalDigest, p.DefinitionRevision, p.DefinitionDigest, p.PolicyDigest,
		p.ProfileDigest, p.PrincipalJSON, p.State, p.Outcome, p.CountsAsCompleted,
		p.Intent, p.Priority, nullJSON(p.CorrelationJSON), p.TargetRepository, p.TargetRef,
		p.TargetPullRequest, p.PinnedHeadSHA, p.PinnedBaseSHA, p.NeutralStage,
	))
	if err != nil {
		return InsertInvocationResult{}, fmt.Errorf("store: insert invocation: %w", err)
	}
	if p.WorkItemID != "" {
		if _, err := tx.Exec(ctx, `UPDATE work_items SET invocation_id = $2 WHERE id = $1`, p.WorkItemID, id); err != nil {
			return InsertInvocationResult{}, fmt.Errorf("store: link invocation: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InsertInvocationResult{}, err
	}
	return InsertInvocationResult{Row: row, Created: true}, nil
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// GetInvocation loads one receipt by id within a factory.
func (s *Store) GetInvocation(ctx context.Context, factoryID, id string) (InvocationRow, error) {
	row, err := scanInvocation(s.pool.QueryRow(ctx, `
		SELECT `+invocationColumns+`
		FROM invocations WHERE factory_id = $1 AND id = $2`, factoryID, id))
	if err != nil {
		return InvocationRow{}, err
	}
	return row, nil
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// LinkInvocationWorkItem attaches a work item after the receipt row exists.
func (s *Store) LinkInvocationWorkItem(ctx context.Context, invocationID, workItemID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE invocations SET work_item_id = $2 WHERE id = $1`,
		invocationID, workItemID)
	if err != nil {
		return fmt.Errorf("store: link invocation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.pool.Exec(ctx, `UPDATE work_items SET invocation_id = $2 WHERE id = $1`, workItemID, invocationID)
	if err != nil {
		return fmt.Errorf("store: link invocation work item: %w", err)
	}
	return nil
}

// ActiveInvocationForSchedule reports a non-terminal invocation for overlap forbid.
func (s *Store) ActiveInvocationForSchedule(ctx context.Context, factoryID, scheduleID string) (InvocationRow, error) {
	prefix := "schedule:" + scheduleID + ":slot:"
	row, err := scanInvocation(s.pool.QueryRow(ctx, `
		SELECT `+invocationColumns+`
		FROM invocations
		WHERE factory_id = $1
		  AND source_occurrence_key LIKE $2 || '%'
		  AND state = 'accepted'
		  AND outcome = ''
		ORDER BY created_at DESC
		LIMIT 1`, factoryID, prefix))
	if err != nil {
		return InvocationRow{}, err
	}
	return row, nil
}
