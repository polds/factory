package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const workItemColumns = `
    id, factory_id, source, source_ref, title, body, stage, status, created_by,
    conversation_id, revision_count, created_at,
    suspended_run_id, suspended_execution_id, suspended_at`

const approvalColumns = `
    id, work_item_id, gate, requested_at, decided_at, decided_by, decision, note`

func scanWorkItem(row pgx.Row) (WorkItem, error) {
	var item WorkItem
	err := row.Scan(
		&item.ID, &item.FactoryID, &item.Source, &item.SourceRef, &item.Title, &item.Body,
		&item.Stage, &item.Status, &item.CreatedBy, &item.ConversationID, &item.RevisionCount,
		&item.CreatedAt, &item.SuspendedRunID, &item.SuspendedExecutionID, &item.SuspendedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkItem{}, ErrNotFound
	}
	if err != nil {
		return WorkItem{}, err
	}
	return item, nil
}

func scanApproval(row pgx.Row) (Approval, error) {
	var approval Approval
	err := row.Scan(
		&approval.ID, &approval.WorkItemID, &approval.Gate, &approval.RequestedAt, &approval.DecidedAt,
		&approval.DecidedBy, &approval.Decision, &approval.Note,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	if err != nil {
		return Approval{}, err
	}
	return approval, nil
}

// CreateWorkItem inserts a work item. Empty stage defaults to triage, empty
// status to open, and an empty conversation id is assigned.
func (s *Store) CreateWorkItem(ctx context.Context, w NewWorkItem) (WorkItem, error) {
	prepared, err := prepareWorkItem(w)
	if err != nil {
		return WorkItem{}, err
	}
	return insertPreparedWorkItem(ctx, s.pool, prepared)
}

type preparedWorkItem struct {
	id             string
	conversationID string
	w              NewWorkItem
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func prepareWorkItem(w NewWorkItem) (preparedWorkItem, error) {
	if w.FactoryID == "" {
		return preparedWorkItem{}, fmt.Errorf("store: factory is required")
	}
	if err := validSource(w.Source); err != nil {
		return preparedWorkItem{}, err
	}
	stage := w.Stage
	if stage == "" {
		stage = StageTriage
	}
	if err := validStage(stage, false); err != nil {
		return preparedWorkItem{}, err
	}
	status := w.Status
	if status == "" {
		status = WorkItemOpen
	}
	if err := validWorkItemStatus(status); err != nil {
		return preparedWorkItem{}, err
	}
	if w.RevisionCount < 0 {
		return preparedWorkItem{}, fmt.Errorf("store: revision count must be >= 0")
	}
	conversationID := w.ConversationID
	if conversationID == "" {
		var err error
		conversationID, err = NewID(PrefixConversation)
		if err != nil {
			return preparedWorkItem{}, err
		}
	}
	id, err := NewID(PrefixWorkItem)
	if err != nil {
		return preparedWorkItem{}, err
	}
	w.Stage = stage
	w.Status = status
	return preparedWorkItem{id: id, conversationID: conversationID, w: w}, nil
}

// insertPreparedWorkItem writes one work item on q. q is a pool or a transaction.
func insertPreparedWorkItem(ctx context.Context, q queryRower, p preparedWorkItem) (WorkItem, error) {
	item, err := scanWorkItem(q.QueryRow(ctx, `
		INSERT INTO work_items (
		    id, factory_id, source, source_ref, title, body, stage, status,
		    created_by, conversation_id, revision_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+workItemColumns,
		p.id, p.w.FactoryID, p.w.Source, p.w.SourceRef, p.w.Title, p.w.Body,
		p.w.Stage, p.w.Status, p.w.CreatedBy, p.conversationID, p.w.RevisionCount,
	))
	if err != nil {
		return WorkItem{}, fmt.Errorf("store: create work item: %w", err)
	}
	return item, nil
}

// GetWorkItem returns the work item or ErrNotFound.
func (s *Store) GetWorkItem(ctx context.Context, id string) (WorkItem, error) {
	item, err := scanWorkItem(s.pool.QueryRow(ctx, `SELECT `+workItemColumns+` FROM work_items WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WorkItem{}, err
		}
		return WorkItem{}, fmt.Errorf("store: get work item: %w", err)
	}
	return item, nil
}

// ListWorkItems returns work items matching f, newest first.
func (s *Store) ListWorkItems(ctx context.Context, f WorkItemFilter) ([]WorkItem, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+workItemColumns+`
		FROM work_items
		WHERE ($1 = '' OR factory_id = $1)
		  AND ($2 = '' OR stage = $2)
		  AND ($3 = '' OR status = $3)
		  AND ($4 = '' OR source = $4)
		ORDER BY created_at DESC, id DESC
		LIMIT $5`, f.FactoryID, f.Stage, f.Status, f.Source, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list work items: %w", err)
	}
	defer rows.Close()
	out := []WorkItem{}
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list work items: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list work items: %w", err)
	}
	return out, nil
}

// UpdateWorkItemStage sets stage and status. RevisionCount is left unchanged.
func (s *Store) UpdateWorkItemStage(ctx context.Context, id, stage, status string) (WorkItem, error) {
	if err := validStage(stage, false); err != nil {
		return WorkItem{}, err
	}
	if err := validWorkItemStatus(status); err != nil {
		return WorkItem{}, err
	}
	item, err := scanWorkItem(s.pool.QueryRow(ctx, `
		UPDATE work_items
		SET stage = $2, status = $3
		WHERE id = $1
		RETURNING `+workItemColumns, id, stage, status))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WorkItem{}, err
		}
		return WorkItem{}, fmt.Errorf("store: update work item: %w", err)
	}
	return item, nil
}

// SetWorkItemRevision persists the orchestrator's revision count.
// The store does not infer when a reviewing loop increments it.
func (s *Store) SetWorkItemRevision(ctx context.Context, id string, count int) (WorkItem, error) {
	if count < 0 {
		return WorkItem{}, fmt.Errorf("store: revision count must be >= 0")
	}
	item, err := scanWorkItem(s.pool.QueryRow(ctx, `
		UPDATE work_items
		SET revision_count = $2
		WHERE id = $1
		RETURNING `+workItemColumns, id, count))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WorkItem{}, err
		}
		return WorkItem{}, fmt.Errorf("store: set revision: %w", err)
	}
	return item, nil
}

// RequestApproval opens a gate, or returns the pending row when that gate is
// already open. A decided gate can be requested again; that inserts a new row.
// It does not change the work item. OpenApprovalGate does both in one transaction.
func (s *Store) RequestApproval(ctx context.Context, workItemID, gate string) (Approval, error) {
	approval, _, err := s.approvalWithStatus(ctx, workItemID, gate, "", "", 0, false)
	return approval, err
}

// OpenApprovalGate inserts a pending approval and sets the work item's stage,
// status, and revision in one transaction. A crash cannot leave a pending row
// without that status, and a reader of awaiting_approval sees the gate.
// Repeating an open gate returns the pending row and writes the status again.
func (s *Store) OpenApprovalGate(ctx context.Context, workItemID, gate, stage, status string, revision int) (Approval, WorkItem, error) {
	return s.approvalWithStatus(ctx, workItemID, gate, stage, status, revision, true)
}

func (s *Store) approvalWithStatus(ctx context.Context, workItemID, gate, stage, status string, revision int, updateItem bool) (Approval, WorkItem, error) {
	gate = strings.TrimSpace(gate)
	if workItemID == "" || gate == "" {
		return Approval{}, WorkItem{}, fmt.Errorf("store: work item and gate are required")
	}
	if updateItem {
		if err := validStage(stage, false); err != nil {
			return Approval{}, WorkItem{}, err
		}
		if err := validWorkItemStatus(status); err != nil {
			return Approval{}, WorkItem{}, err
		}
		if revision < 0 {
			return Approval{}, WorkItem{}, fmt.Errorf("store: revision count must be >= 0")
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Approval{}, WorkItem{}, fmt.Errorf("store: request approval: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists int
	err = tx.QueryRow(ctx, `SELECT 1 FROM work_items WHERE id = $1 FOR UPDATE`, workItemID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, WorkItem{}, ErrNotFound
	}
	if err != nil {
		return Approval{}, WorkItem{}, fmt.Errorf("store: request approval: %w", err)
	}
	created, err := s.pendingOrInsert(ctx, tx, workItemID, gate)
	if err != nil {
		return Approval{}, WorkItem{}, err
	}
	var item WorkItem
	if updateItem {
		item, err = scanWorkItem(tx.QueryRow(ctx, `
			UPDATE work_items
			SET stage = $2, status = $3, revision_count = $4
			WHERE id = $1
			RETURNING `+workItemColumns, workItemID, stage, status, revision))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Approval{}, WorkItem{}, err
			}
			return Approval{}, WorkItem{}, fmt.Errorf("store: open approval gate: %w", err)
		}
		if s.openGateHook != nil {
			if err := s.openGateHook(ctx); err != nil {
				return Approval{}, WorkItem{}, fmt.Errorf("store: open approval gate: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Approval{}, WorkItem{}, fmt.Errorf("store: request approval: %w", err)
	}
	return created, item, nil
}

func (s *Store) pendingOrInsert(ctx context.Context, tx pgx.Tx, workItemID, gate string) (Approval, error) {
	var pendingID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM approvals
		WHERE work_item_id = $1 AND gate = $2 AND decision = ''
		LIMIT 1`, workItemID, gate).Scan(&pendingID)
	if err == nil {
		if s.pendingLookupHook != nil {
			s.pendingLookupHook(ctx)
		}
		// Lock the same row DecideApproval locks. A decision that committed
		// after the id lookup is visible once this lock is held.
		locked, err := scanApproval(tx.QueryRow(ctx, `
			SELECT `+approvalColumns+`
			FROM approvals
			WHERE id = $1
			FOR UPDATE`, pendingID))
		if err != nil {
			return Approval{}, fmt.Errorf("store: request approval: %w", err)
		}
		if locked.Decision == "" {
			return locked, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, fmt.Errorf("store: request approval: %w", err)
	}
	id, err := NewID(PrefixApproval)
	if err != nil {
		return Approval{}, err
	}
	created, err := scanApproval(tx.QueryRow(ctx, `
		INSERT INTO approvals (id, work_item_id, gate)
		VALUES ($1, $2, $3)
		RETURNING `+approvalColumns, id, workItemID, gate))
	if err != nil {
		return Approval{}, fmt.Errorf("store: request approval: %w", err)
	}
	return created, nil
}

// DecideApproval records approve or reject once. Repeating that same decision
// returns the original row. A different decision returns ErrAlreadyDecided.
func (s *Store) DecideApproval(ctx context.Context, id, decision, decidedBy, note string) (Approval, error) {
	decision = strings.TrimSpace(decision)
	if err := validDecision(decision); err != nil {
		return Approval{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Approval{}, fmt.Errorf("store: decide approval: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanApproval(tx.QueryRow(ctx, `
		SELECT `+approvalColumns+`
		FROM approvals
		WHERE id = $1
		FOR UPDATE`, id))
	if errors.Is(err, ErrNotFound) {
		return Approval{}, ErrNotFound
	}
	if err != nil {
		return Approval{}, fmt.Errorf("store: decide approval: %w", err)
	}
	if current.Decision != "" {
		if err := tx.Commit(ctx); err != nil {
			return Approval{}, fmt.Errorf("store: decide approval: %w", err)
		}
		if current.Decision == decision {
			return current, nil
		}
		return Approval{}, ErrAlreadyDecided
	}
	updated, err := scanApproval(tx.QueryRow(ctx, `
		UPDATE approvals
		SET decision = $2,
		    decided_by = $3,
		    note = $4,
		    decided_at = clock_timestamp()
		WHERE id = $1
		RETURNING `+approvalColumns, id, decision, decidedBy, note))
	if err != nil {
		return Approval{}, fmt.Errorf("store: decide approval: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Approval{}, fmt.Errorf("store: decide approval: %w", err)
	}
	return updated, nil
}

// ListApprovals returns approvals for a factory. pendingOnly keeps rows that
// have not been decided.
func (s *Store) ListApprovals(ctx context.Context, factoryID string, pendingOnly bool) ([]Approval, error) {
	if factoryID == "" {
		return nil, fmt.Errorf("store: factory id is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.work_item_id, a.gate, a.requested_at, a.decided_at,
		       a.decided_by, a.decision, a.note
		FROM approvals a
		JOIN work_items w ON w.id = a.work_item_id
		WHERE w.factory_id = $1
		  AND ($2 = false OR a.decision = '')
		ORDER BY a.requested_at ASC, a.id ASC`, factoryID, pendingOnly)
	if err != nil {
		return nil, fmt.Errorf("store: list approvals: %w", err)
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		row, err := scanApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list approvals: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list approvals: %w", err)
	}
	return out, nil
}

func validSource(source string) error {
	switch source {
	case SourceIssue, SourceManual, SourceAPI, SourceWebhook, SourceChat, SourceSchedule, SourceMCP,
		ProviderGitHub, ProviderSlack, ProviderLinear, SourceFactory:
		return nil
	default:
		return fmt.Errorf("store: source %q is not valid", source)
	}
}

func validStage(stage string, allowEmpty bool) error {
	switch stage {
	case StageTriage, StagePlanning, StageBuilding, StageReviewing, StageHumanHandoff:
		return nil
	case "":
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("store: stage is required")
	default:
		return fmt.Errorf("store: stage %q is not valid", stage)
	}
}

func validWorkItemStatus(status string) error {
	switch status {
	case WorkItemOpen, WorkItemAwaitingApproval, WorkItemAwaitingInput, WorkItemComplete, WorkItemCancelled:
		return nil
	default:
		return fmt.Errorf("store: work item status %q is not valid", status)
	}
}

func validDecision(decision string) error {
	switch decision {
	case DecisionApprove, DecisionReject:
		return nil
	default:
		return fmt.Errorf("store: decision %q is not valid", decision)
	}
}
