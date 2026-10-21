package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const prFactColumns = `
    factory_id, repo, number, COALESCE(work_item_id, ''),
    opened_at, merged_at, closed_at, first_review_at,
    human_push_seen, is_improvement, changed_lines`

func scanPRFact(row pgx.Row) (PRFact, error) {
	var out PRFact
	var human, improvement bool
	err := row.Scan(
		&out.FactoryID, &out.Repo, &out.Number, &out.WorkItemID,
		&out.OpenedAt, &out.MergedAt, &out.ClosedAt, &out.FirstReviewAt,
		&human, &improvement, &out.ChangedLines,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PRFact{}, ErrNotFound
	}
	if err != nil {
		return PRFact{}, err
	}
	out.HumanPushSeen = &human
	out.IsImprovement = &improvement
	return out, nil
}

// UpsertPRFact inserts or fills one pull request, keyed by factory, repo,
// and number. A later event writes a field that is still null and does not
// clear one that is already set. A later changed-line count replaces the
// stored count. human_push_seen and is_improvement stay true once set.
// The first work item id sticks.
func (s *Store) UpsertPRFact(ctx context.Context, in PRFact) (PRFact, error) {
	if in.FactoryID == "" || in.Repo == "" || in.Number <= 0 {
		return PRFact{}, fmt.Errorf("store: factory, repo, and number are required")
	}
	if in.ChangedLines != nil && *in.ChangedLines < 0 {
		return PRFact{}, fmt.Errorf("store: changed lines must be >= 0")
	}
	if err := s.requireFactory(ctx, s.pool, in.FactoryID); err != nil {
		return PRFact{}, err
	}
	if in.WorkItemID != "" {
		var factoryID string
		err := s.pool.QueryRow(ctx, `SELECT factory_id FROM work_items WHERE id = $1`, in.WorkItemID).Scan(&factoryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return PRFact{}, ErrNotFound
		}
		if err != nil {
			return PRFact{}, fmt.Errorf("store: upsert pr fact: %w", err)
		}
		if factoryID != in.FactoryID {
			return PRFact{}, fmt.Errorf("store: work item %s belongs to another factory", in.WorkItemID)
		}
	}
	out, err := scanPRFact(s.pool.QueryRow(ctx, `
		INSERT INTO pr_facts (
		    factory_id, repo, number, work_item_id,
		    opened_at, merged_at, closed_at, first_review_at,
		    human_push_seen, is_improvement, changed_lines)
		VALUES (
		    $1, $2, $3, NULLIF($4, ''),
		    $5, $6, $7, $8,
		    COALESCE($9, false), COALESCE($10, false), $11)
		ON CONFLICT (factory_id, repo, number) DO UPDATE
		SET work_item_id = COALESCE(pr_facts.work_item_id, EXCLUDED.work_item_id),
		    opened_at = COALESCE(pr_facts.opened_at, EXCLUDED.opened_at),
		    merged_at = COALESCE(pr_facts.merged_at, EXCLUDED.merged_at),
		    closed_at = COALESCE(pr_facts.closed_at, EXCLUDED.closed_at),
		    first_review_at = COALESCE(pr_facts.first_review_at, EXCLUDED.first_review_at),
		    human_push_seen = pr_facts.human_push_seen OR EXCLUDED.human_push_seen,
		    is_improvement = pr_facts.is_improvement OR EXCLUDED.is_improvement,
		    changed_lines = COALESCE(EXCLUDED.changed_lines, pr_facts.changed_lines)
		RETURNING `+prFactColumns,
		in.FactoryID, in.Repo, in.Number, in.WorkItemID,
		in.OpenedAt, in.MergedAt, in.ClosedAt, in.FirstReviewAt,
		in.HumanPushSeen, in.IsImprovement, in.ChangedLines,
	))
	if err != nil {
		return PRFact{}, fmt.Errorf("store: upsert pr fact: %w", err)
	}
	return out, nil
}
