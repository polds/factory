package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/automation"
)

// Automation adapts Store to automation.Store.
//
// *Store cannot implement that interface: GetWorkItem and GetExternalRef
// already return store types. Methods that the engine treats as "missing"
// return automation.ErrNotFound, not store.ErrNotFound.
type Automation struct {
	Store *Store
	// Log records a losing duplicate building run. Nil discards it.
	Log *slog.Logger
}

var _ automation.Store = (*Automation)(nil)

// pairedError keeps an existing store sentinel and an automation sentinel
// on one error so errors.Is matches both.
type pairedError struct {
	msg   string
	left  error
	right error
}

func (e pairedError) Error() string { return e.msg }

func (e pairedError) Unwrap() []error { return []error{e.left, e.right} }

func withAutomationSentinel(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return pairedError{msg: err.Error(), left: err, right: automation.ErrNotFound}
	case errors.Is(err, ErrConflict):
		return pairedError{msg: err.Error(), left: err, right: automation.ErrConflict}
	default:
		return err
	}
}

func (a *Automation) need() error {
	if a == nil || a.Store == nil {
		return errors.New("store: automation store is not configured")
	}
	return nil
}

func validThreadProvider(provider string) error {
	if provider == SourceFactory {
		return nil
	}
	return validProvider(provider)
}

func toAutoItem(item WorkItem) automation.WorkItem {
	return automation.WorkItem{
		ID:             item.ID,
		FactoryID:      item.FactoryID,
		Source:         item.Source,
		SourceRef:      item.SourceRef,
		Title:          item.Title,
		Body:           item.Body,
		Stage:          item.Stage,
		Status:         item.Status,
		CreatedBy:      item.CreatedBy,
		ConversationID: item.ConversationID,
	}
}

// OpenWorkItem inserts the work item and the external ref in one transaction.
// An empty conversation id is assigned. A (provider, external_ref) conflict
// rolls the new work item back and returns the row that won, created=false.
func (a *Automation) OpenWorkItem(ctx context.Context, provider, externalRef string, w automation.NewWorkItem) (automation.WorkItem, bool, error) {
	if err := a.need(); err != nil {
		return automation.WorkItem{}, false, err
	}
	if err := validThreadProvider(provider); err != nil {
		return automation.WorkItem{}, false, err
	}
	if externalRef == "" {
		return automation.WorkItem{}, false, errors.New("store: external ref is required")
	}
	source := w.Source
	if source == "" {
		source = provider
	}
	prepared, err := prepareWorkItem(NewWorkItem{
		FactoryID: w.FactoryID,
		Source:    source,
		SourceRef: w.SourceRef,
		Title:     w.Title,
		Body:      w.Body,
		Stage:     w.Stage,
		Status:    w.Status,
		CreatedBy: w.CreatedBy,
	})
	if err != nil {
		return automation.WorkItem{}, false, err
	}

	tx, err := a.Store.pool.Begin(ctx)
	if err != nil {
		return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := scanExternalRef(tx.QueryRow(ctx, `
		SELECT provider, external_ref, work_item_id, conversation_id
		FROM external_refs
		WHERE provider = $1 AND external_ref = $2`, provider, externalRef))
	if err == nil {
		item, err := scanWorkItem(tx.QueryRow(ctx, `SELECT `+workItemColumns+` FROM work_items WHERE id = $1`, existing.WorkItemID))
		if errors.Is(err, ErrNotFound) {
			return automation.WorkItem{}, false, automation.ErrNotFound
		}
		if err != nil {
			return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
		}
		return toAutoItem(item), false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
	}

	item, err := insertPreparedWorkItem(ctx, tx, prepared)
	if err != nil {
		return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO external_refs (provider, external_ref, work_item_id, conversation_id)
		VALUES ($1, $2, $3, $4)`,
		provider, externalRef, item.ID, item.ConversationID)
	if uniqueOn(err, "external_refs") {
		_ = tx.Rollback(ctx)
		return a.workItemForRef(ctx, provider, externalRef)
	}
	if err != nil {
		return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return automation.WorkItem{}, false, fmt.Errorf("store: open work item: %w", err)
	}
	return toAutoItem(item), true, nil
}

func (a *Automation) workItemForRef(ctx context.Context, provider, externalRef string) (automation.WorkItem, bool, error) {
	ref, err := a.Store.GetExternalRef(ctx, provider, externalRef)
	if errors.Is(err, ErrNotFound) {
		return automation.WorkItem{}, false, automation.ErrNotFound
	}
	if err != nil {
		return automation.WorkItem{}, false, err
	}
	item, err := a.Store.GetWorkItem(ctx, ref.WorkItemID)
	if errors.Is(err, ErrNotFound) {
		return automation.WorkItem{}, false, automation.ErrNotFound
	}
	if err != nil {
		return automation.WorkItem{}, false, err
	}
	return toAutoItem(item), false, nil
}

// EnsureIntake locks the work item, then inserts every missing slot and that
// slot's run in the same transaction. A failure rolls all of them back.
// Another delivery that already owns the slots returns Continuation and
// inserts nothing. A slot this delivery already has is Inserted=false.
// A primary-key conflict follows the row that won.
func (a *Automation) EnsureIntake(ctx context.Context, workItemID, deliveryID string, slots []automation.IntakeSlot) (automation.IntakeResult, error) {
	if err := a.need(); err != nil {
		return automation.IntakeResult{}, err
	}
	if workItemID == "" || deliveryID == "" {
		return automation.IntakeResult{}, errors.New("store: work item and delivery are required")
	}
	for _, slot := range slots {
		if slot.Automation == "" {
			return automation.IntakeResult{}, errors.New("store: automation is required")
		}
	}

	tx, err := a.Store.pool.Begin(ctx)
	if err != nil {
		return automation.IntakeResult{}, fmt.Errorf("store: ensure intake: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	item, err := lockWorkItem(ctx, tx, workItemID)
	if errors.Is(err, ErrNotFound) {
		return automation.IntakeResult{}, automation.ErrNotFound
	}
	if err != nil {
		return automation.IntakeResult{}, fmt.Errorf("store: ensure intake: %w", err)
	}
	existing, err := listSlots(ctx, tx, workItemID)
	if err != nil {
		return automation.IntakeResult{}, err
	}
	same, other := false, false
	byName := make(map[string]slotRow, len(existing))
	for _, slot := range existing {
		byName[slot.automation] = slot
		if slot.deliveryID == deliveryID {
			same = true
		} else {
			other = true
		}
	}
	if other && !same {
		return automation.IntakeResult{Item: toAutoItem(item), Continuation: true}, nil
	}

	out := automation.IntakeResult{Item: toAutoItem(item)}
	var notify []string
	wrote := 0
	for _, slot := range slots {
		if got, found := byName[slot.Automation]; found {
			run, err := loadAutoRun(ctx, tx, got.runID)
			if err != nil {
				return automation.IntakeResult{}, err
			}
			out.Slots = append(out.Slots, automation.IntakeSlotResult{
				Automation: slot.Automation,
				Run:        run,
				Inserted:   false,
			})
			continue
		}
		run, inserted, cont, err := a.insertSlot(ctx, tx, item, deliveryID, slot, same || wrote > 0)
		if err != nil {
			return automation.IntakeResult{}, err
		}
		if cont {
			return automation.IntakeResult{Item: toAutoItem(item), Continuation: true}, nil
		}
		out.Slots = append(out.Slots, automation.IntakeSlotResult{
			Automation: slot.Automation,
			Run:        run,
			Inserted:   inserted,
		})
		if inserted {
			wrote++
			byName[slot.Automation] = slotRow{automation: slot.Automation, deliveryID: deliveryID, runID: run.ID}
			if run.ID != "" {
				notify = append(notify, run.ID)
			}
			if wrote == 1 && a.Store.intakeHook != nil {
				if err := a.Store.intakeHook(ctx); err != nil {
					return automation.IntakeResult{}, err
				}
			}
		}
	}
	if err := notifyRuns(ctx, tx, notify); err != nil {
		return automation.IntakeResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return automation.IntakeResult{}, fmt.Errorf("store: ensure intake: %w", err)
	}
	return out, nil
}

// insertSlot writes one missing slot. inserted is false when the primary key
// was already taken. cont is true when another delivery owns it and this
// call has not written a slot.
func (a *Automation) insertSlot(ctx context.Context, tx pgx.Tx, item WorkItem, deliveryID string, slot automation.IntakeSlot, haveOwn bool) (automation.Run, bool, bool, error) {
	var run automation.Run
	runID := ""
	ownRun := false
	if slot.Run != nil {
		copied := *slot.Run
		copied.WorkItemID = item.ID
		copied.ConversationID = item.ConversationID
		if copied.FactoryID == "" {
			copied.FactoryID = item.FactoryID
		}
		var err error
		run, ownRun, err = insertAutoRun(ctx, tx, copied)
		if err != nil {
			return automation.Run{}, false, false, err
		}
		runID = run.ID
	}
	var stored string
	err := tx.QueryRow(ctx, `
		INSERT INTO intake_slots (work_item_id, automation, delivery_id, run_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (work_item_id, automation) DO NOTHING
		RETURNING automation`,
		item.ID, slot.Automation, deliveryID, runID).Scan(&stored)
	if err == nil {
		return run, true, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return automation.Run{}, false, false, fmt.Errorf("store: ensure intake: %w", err)
	}
	if ownRun {
		if err := deleteRun(ctx, tx, runID); err != nil {
			return automation.Run{}, false, false, err
		}
	}
	winner, err := readSlot(ctx, tx, item.ID, slot.Automation)
	if err != nil {
		return automation.Run{}, false, false, err
	}
	if winner.deliveryID != deliveryID && !haveOwn {
		return automation.Run{}, false, true, nil
	}
	loaded, err := loadAutoRun(ctx, tx, winner.runID)
	if err != nil {
		return automation.Run{}, false, false, err
	}
	return loaded, false, false, nil
}

// Owners returns the automations stored on the work item, in name order.
func (a *Automation) Owners(ctx context.Context, workItemID string) ([]string, error) {
	if err := a.need(); err != nil {
		return nil, err
	}
	if _, err := a.Store.GetWorkItem(ctx, workItemID); errors.Is(err, ErrNotFound) {
		return nil, automation.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := a.Store.pool.Query(ctx, `
		SELECT automation FROM intake_slots
		WHERE work_item_id = $1
		ORDER BY automation`, workItemID)
	return collect(rows, err, "store: owners", func(row pgx.Row) (string, error) {
		var name string
		if err := row.Scan(&name); err != nil {
			return "", err
		}
		return name, nil
	})
}

// GetWorkItem returns the work item or automation.ErrNotFound.
func (a *Automation) GetWorkItem(ctx context.Context, id string) (automation.WorkItem, error) {
	if err := a.need(); err != nil {
		return automation.WorkItem{}, err
	}
	item, err := a.Store.GetWorkItem(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return automation.WorkItem{}, automation.ErrNotFound
	}
	if err != nil {
		return automation.WorkItem{}, err
	}
	return toAutoItem(item), nil
}

// GetExternalRef returns the binding or automation.ErrNotFound.
func (a *Automation) GetExternalRef(ctx context.Context, provider, externalRef string) (automation.Ref, error) {
	if err := a.need(); err != nil {
		return automation.Ref{}, err
	}
	ref, err := a.Store.GetExternalRef(ctx, provider, externalRef)
	if errors.Is(err, ErrNotFound) {
		return automation.Ref{}, automation.ErrNotFound
	}
	if err != nil {
		return automation.Ref{}, err
	}
	return automation.Ref{
		Provider:       ref.Provider,
		ExternalRef:    ref.ExternalRef,
		WorkItemID:     ref.WorkItemID,
		ConversationID: ref.ConversationID,
	}, nil
}

// FollowUp inserts the run, moves stage and status, and records the effect
// in one transaction. The same delivery id returns that run and created=false.
// A building run already in flight is also created=false: the reply is
// appended to that run's prompt and to the work item body, and no second
// run is inserted.
func (a *Automation) FollowUp(ctx context.Context, deliveryID string, run automation.NewRun, stage, status string) (automation.Run, bool, error) {
	if err := a.need(); err != nil {
		return automation.Run{}, false, err
	}
	if deliveryID == "" {
		return automation.Run{}, false, errors.New("store: delivery id is required")
	}
	if run.WorkItemID == "" {
		return automation.Run{}, false, errors.New("store: work item is required")
	}
	if err := validStage(stage, false); err != nil {
		return automation.Run{}, false, err
	}
	if err := validWorkItemStatus(status); err != nil {
		return automation.Run{}, false, err
	}

	tx, err := a.Store.pool.Begin(ctx)
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	item, err := lockWorkItem(ctx, tx, run.WorkItemID)
	if errors.Is(err, ErrNotFound) {
		return automation.Run{}, false, automation.ErrNotFound
	}
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	if prev, ok, err := effectByDelivery(ctx, tx, deliveryID); err != nil {
		return automation.Run{}, false, err
	} else if ok {
		if prev.kind != "follow_up" {
			return automation.Run{}, false, errors.New("store: delivery already applied")
		}
		loaded, err := loadAutoRun(ctx, tx, prev.runID)
		if err != nil {
			return automation.Run{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
		}
		return loaded, false, nil
	}

	if run.FactoryID == "" {
		run.FactoryID = item.FactoryID
	}
	if run.ConversationID == "" {
		run.ConversationID = item.ConversationID
	}
	created, inserted, err := insertAutoRun(ctx, tx, run)
	if uniqueOn(err, "runs") {
		saved := err
		_ = tx.Rollback(ctx)
		return a.lostBuildingRun(ctx, deliveryID, run, saved)
	}
	if err != nil {
		return automation.Run{}, false, err
	}
	if !inserted {
		return a.keepLostFollowUp(ctx, tx, deliveryID, item, created, run.Prompt)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE work_items SET stage = $2, status = $3 WHERE id = $1`,
		item.ID, stage, status)
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return automation.Run{}, false, automation.ErrNotFound
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO delivery_effects (delivery_id, work_item_id, kind, run_id, note)
		VALUES ($1, $2, 'follow_up', $3, '')`,
		deliveryID, item.ID, created.ID)
	if uniqueOn(err, "delivery_effects") {
		_ = tx.Rollback(ctx)
		return a.replayFollowUp(ctx, deliveryID)
	}
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	if err := notifyRuns(ctx, tx, []string{created.ID}); err != nil {
		return automation.Run{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	return created, true, nil
}

func (a *Automation) replayFollowUp(ctx context.Context, deliveryID string) (automation.Run, bool, error) {
	var kind, runID string
	err := a.Store.pool.QueryRow(ctx, `
		SELECT kind, run_id FROM delivery_effects WHERE delivery_id = $1`, deliveryID).Scan(&kind, &runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return automation.Run{}, false, automation.ErrNotFound
	}
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	if kind != "follow_up" {
		return automation.Run{}, false, errors.New("store: delivery already applied")
	}
	loaded, err := loadAutoRun(ctx, a.Store.pool, runID)
	if err != nil {
		return automation.Run{}, false, err
	}
	return loaded, false, nil
}

// AppendContext records the note once per delivery and sets body = body || note
// under a work-item lock. A replay returns applied=false and does not append.
func (a *Automation) AppendContext(ctx context.Context, workItemID, deliveryID, text string) (bool, error) {
	if err := a.need(); err != nil {
		return false, err
	}
	if workItemID == "" || deliveryID == "" {
		return false, errors.New("store: work item and delivery are required")
	}
	tx, err := a.Store.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("store: append context: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := lockWorkItem(ctx, tx, workItemID); errors.Is(err, ErrNotFound) {
		return false, automation.ErrNotFound
	} else if err != nil {
		return false, fmt.Errorf("store: append context: %w", err)
	}
	if _, ok, err := effectByDelivery(ctx, tx, deliveryID); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO delivery_effects (delivery_id, work_item_id, kind, run_id, note)
		VALUES ($1, $2, 'context', '', $3)`,
		deliveryID, workItemID, text)
	if uniqueOn(err, "delivery_effects") {
		_ = tx.Rollback(ctx)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: append context: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE work_items SET body = body || $2 WHERE id = $1`, workItemID, text)
	if err != nil {
		return false, fmt.Errorf("store: append context: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, automation.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("store: append context: %w", err)
	}
	return true, nil
}

// Context returns post-intake notes in append order. The original body is not included.
func (a *Automation) Context(ctx context.Context, workItemID string) (string, error) {
	if err := a.need(); err != nil {
		return "", err
	}
	rows, err := a.Store.pool.Query(ctx, `
		SELECT note FROM delivery_effects
		WHERE work_item_id = $1 AND kind = 'context'
		ORDER BY seq`, workItemID)
	if err != nil {
		return "", fmt.Errorf("store: context: %w", err)
	}
	defer rows.Close()
	var b []byte
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			return "", fmt.Errorf("store: context: %w", err)
		}
		b = append(b, note...)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("store: context: %w", err)
	}
	return string(b), nil
}

// LatestRun returns the newest run for the work item, or automation.ErrNotFound.
func (a *Automation) LatestRun(ctx context.Context, workItemID string) (automation.Run, error) {
	if err := a.need(); err != nil {
		return automation.Run{}, err
	}
	var id string
	err := a.Store.pool.QueryRow(ctx, `
		SELECT id FROM runs
		WHERE work_item_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, workItemID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return automation.Run{}, automation.ErrNotFound
	}
	if err != nil {
		return automation.Run{}, fmt.Errorf("store: latest run: %w", err)
	}
	return loadAutoRun(ctx, a.Store.pool, id)
}

// OpenPullRequest returns the stored pull request URL.
// ok is false when the work item has no open pull request.
// SetOpenPullRequest records the URL. The automation interface has no writer.
func (a *Automation) OpenPullRequest(ctx context.Context, workItemID string) (string, bool, error) {
	if err := a.need(); err != nil {
		return "", false, err
	}
	var url string
	err := a.Store.pool.QueryRow(ctx, `
		SELECT open_pr_url FROM work_items WHERE id = $1`, workItemID).Scan(&url)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: pull request: %w", err)
	}
	return url, url != "", nil
}

// ClaimOpenPullRequest stores url when the work item has no open pull
// request. claimed is false when one is already stored: stored is that URL
// and it is left in place. The work-item row lock makes two writers keep
// the first URL. An empty url is rejected. SetOpenPullRequest still
// overwrites, including a clear.
func (s *Store) ClaimOpenPullRequest(ctx context.Context, workItemID, url string) (bool, string, error) {
	url = strings.TrimSpace(url)
	if workItemID == "" || url == "" {
		return false, "", errors.New("store: work item and pull request are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, "", fmt.Errorf("store: pull request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockWorkItem(ctx, tx, workItemID); errors.Is(err, ErrNotFound) {
		return false, "", ErrNotFound
	} else if err != nil {
		return false, "", fmt.Errorf("store: pull request: %w", err)
	}
	var current string
	if err := tx.QueryRow(ctx, `SELECT open_pr_url FROM work_items WHERE id = $1`, workItemID).Scan(&current); err != nil {
		return false, "", fmt.Errorf("store: pull request: %w", err)
	}
	if current != "" {
		return false, current, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE work_items SET open_pr_url = $2
		WHERE id = $1 AND open_pr_url = ''`, workItemID, url)
	if err != nil {
		return false, "", fmt.Errorf("store: pull request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.QueryRow(ctx, `SELECT open_pr_url FROM work_items WHERE id = $1`, workItemID).Scan(&current); err != nil {
			return false, "", fmt.Errorf("store: pull request: %w", err)
		}
		return false, current, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, "", fmt.Errorf("store: pull request: %w", err)
	}
	return true, url, nil
}

// SetOpenPullRequest stores the open pull request URL for a work item.
// An empty URL clears it. A second URL for an item that already has one
// goes through ClaimOpenPullRequest, which keeps the first.
func (s *Store) SetOpenPullRequest(ctx context.Context, workItemID, url string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE work_items SET open_pr_url = $2 WHERE id = $1`, workItemID, url)
	if err != nil {
		return fmt.Errorf("store: pull request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

type slotRow struct {
	automation string
	deliveryID string
	runID      string
}

type effectRow struct {
	kind   string
	runID  string
	itemID string
}

type rowQuery interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func lockWorkItem(ctx context.Context, tx pgx.Tx, id string) (WorkItem, error) {
	item, err := scanWorkItem(tx.QueryRow(ctx, `
		SELECT `+workItemColumns+` FROM work_items WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return WorkItem{}, err
	}
	return item, nil
}

func listSlots(ctx context.Context, tx pgx.Tx, workItemID string) ([]slotRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT automation, delivery_id, run_id
		FROM intake_slots
		WHERE work_item_id = $1`, workItemID)
	return collect(rows, err, "store: ensure intake", func(row pgx.Row) (slotRow, error) {
		var slot slotRow
		if err := row.Scan(&slot.automation, &slot.deliveryID, &slot.runID); err != nil {
			return slotRow{}, err
		}
		return slot, nil
	})
}

func readSlot(ctx context.Context, q rowQuery, workItemID, name string) (slotRow, error) {
	var slot slotRow
	err := q.QueryRow(ctx, `
		SELECT automation, delivery_id, run_id
		FROM intake_slots
		WHERE work_item_id = $1 AND automation = $2`, workItemID, name).Scan(&slot.automation, &slot.deliveryID, &slot.runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return slotRow{}, ErrNotFound
	}
	if err != nil {
		return slotRow{}, fmt.Errorf("store: ensure intake: %w", err)
	}
	return slot, nil
}

func effectByDelivery(ctx context.Context, q rowQuery, deliveryID string) (effectRow, bool, error) {
	var row effectRow
	err := q.QueryRow(ctx, `
		SELECT kind, run_id, work_item_id
		FROM delivery_effects
		WHERE delivery_id = $1`, deliveryID).Scan(&row.kind, &row.runID, &row.itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return effectRow{}, false, nil
	}
	if err != nil {
		return effectRow{}, false, fmt.Errorf("store: delivery effect: %w", err)
	}
	return row, true, nil
}

func insertAutoRun(ctx context.Context, tx pgx.Tx, r automation.NewRun) (automation.Run, bool, error) {
	if r.FactoryID == "" || r.Pool == "" {
		return automation.Run{}, false, fmt.Errorf("store: factory and pool are required")
	}
	switch Priority(r.Priority) {
	case PriorityBatch, PriorityPipeline, PriorityInteractive:
	default:
		return automation.Run{}, false, fmt.Errorf("store: priority %d is not valid", r.Priority)
	}
	if err := validStage(r.Stage, true); err != nil {
		return automation.Run{}, false, err
	}
	if r.Stage == StageBuilding && r.WorkItemID != "" {
		existing, ok, err := inflightAuto(ctx, tx, r.WorkItemID)
		if err != nil {
			return automation.Run{}, false, err
		}
		if ok {
			return existing, false, nil
		}
	}
	if err := bindRunLinkage(ctx, tx, NewRun{
		FactoryID:   r.FactoryID,
		WorkItemID:  r.WorkItemID,
		ParentRunID: r.ParentRunID,
	}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return automation.Run{}, false, automation.ErrNotFound
		}
		return automation.Run{}, false, err
	}
	id, err := NewID(PrefixRun)
	if err != nil {
		return automation.Run{}, false, err
	}
	var agentID any
	if r.Agent != "" {
		var found string
		err := tx.QueryRow(ctx, `
			SELECT a.id
			FROM agents AS a
			JOIN factories AS f ON f.current_definition_id = a.definition_id
			WHERE f.id = $1 AND a.name = $2`, r.FactoryID, r.Agent).Scan(&found)
		if err == nil {
			agentID = found
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return automation.Run{}, false, fmt.Errorf("store: agent: %w", err)
		}
	}
	// automation.NewRun has no purpose field. Intake and follow-up runs are work.
	purpose, err := normalizePurpose("")
	if err != nil {
		return automation.Run{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (
		    id, factory_id, agent_id, status, priority, pool, prompt, title, repo,
		    work_item_id, parent_run_id, conversation_id, stage, model_id, purpose)
		VALUES (
		    $1, $2, $3, $4, $5, $6, $7, $8, '',
		    NULLIF($9, ''), NULLIF($10, ''), $11, $12, $13, $14)`,
		id, r.FactoryID, agentID, StatusQueued, r.Priority, r.Pool, r.Prompt, r.Title,
		r.WorkItemID, r.ParentRunID, r.ConversationID, r.Stage, r.ModelID, purpose,
	); err != nil {
		return automation.Run{}, false, fmt.Errorf("store: create run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_queue (run_id, factory_id, pool, priority, enqueued_at)
		VALUES ($1, $2, $3, $4, clock_timestamp())`,
		id, r.FactoryID, r.Pool, r.Priority,
	); err != nil {
		return automation.Run{}, false, fmt.Errorf("store: enqueue run: %w", err)
	}
	return automation.Run{
		ID:             id,
		FactoryID:      r.FactoryID,
		Agent:          r.Agent,
		Pool:           r.Pool,
		Priority:       r.Priority,
		Prompt:         r.Prompt,
		Title:          r.Title,
		WorkItemID:     r.WorkItemID,
		ParentRunID:    r.ParentRunID,
		ConversationID: r.ConversationID,
		Stage:          r.Stage,
		ModelID:        r.ModelID,
	}, true, nil
}

func inflightAuto(ctx context.Context, q rowQuery, workItemID string) (automation.Run, bool, error) {
	run, ok, err := inflightBuildingRun(ctx, q, workItemID)
	if err != nil || !ok {
		return automation.Run{}, false, err
	}
	loaded, err := loadAutoRun(ctx, q, run.ID)
	if err != nil {
		return automation.Run{}, false, err
	}
	return loaded, true, nil
}

func (a *Automation) lostBuildingRun(ctx context.Context, deliveryID string, run automation.NewRun, saved error) (automation.Run, bool, error) {
	tx, err := a.Store.pool.Begin(ctx)
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	item, err := lockWorkItem(ctx, tx, run.WorkItemID)
	if errors.Is(err, ErrNotFound) {
		return automation.Run{}, false, automation.ErrNotFound
	}
	if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	if prev, ok, err := effectByDelivery(ctx, tx, deliveryID); err != nil {
		return automation.Run{}, false, err
	} else if ok {
		if prev.kind != "follow_up" {
			return automation.Run{}, false, errors.New("store: delivery already applied")
		}
		loaded, err := loadAutoRun(ctx, tx, prev.runID)
		if err != nil {
			return automation.Run{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
		}
		return loaded, false, nil
	}
	loaded, ok, err := inflightAuto(ctx, tx, run.WorkItemID)
	if err != nil {
		return automation.Run{}, false, err
	}
	if !ok {
		return automation.Run{}, false, saved
	}
	return a.keepLostFollowUp(ctx, tx, deliveryID, item, loaded, run.Prompt)
}

// keepLostFollowUp stores the reply that lost the building-run race.
// The in-flight run's prompt gains the text, and the work item body gains
// it for the next building run. created stays false, so no second run starts.
func (a *Automation) keepLostFollowUp(ctx context.Context, tx pgx.Tx, deliveryID string, item WorkItem, existing automation.Run, prompt string) (automation.Run, bool, error) {
	if err := keepLostReply(ctx, tx, deliveryID, item, existing.ID, existing.Prompt, prompt); uniqueOn(err, "delivery_effects") {
		_ = tx.Rollback(ctx)
		return a.replayFollowUp(ctx, deliveryID)
	} else if err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return automation.Run{}, false, fmt.Errorf("store: follow up: %w", err)
	}
	loaded, err := loadAutoRun(ctx, a.Store.pool, existing.ID)
	if err != nil {
		return automation.Run{}, false, err
	}
	a.logInFlight(item.ID, loaded.ID)
	return loaded, false, nil
}

func keepLostReply(ctx context.Context, tx pgx.Tx, deliveryID string, item WorkItem, runID, existingPrompt, extra string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO delivery_effects (delivery_id, work_item_id, kind, run_id, note)
		VALUES ($1, $2, 'follow_up', $3, $4)`,
		deliveryID, item.ID, runID, extra); err != nil {
		return err
	}
	if extra == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE work_items SET body = $2 WHERE id = $1`,
		item.ID, joinPrompt(item.Body, extra)); err != nil {
		return fmt.Errorf("store: follow up: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE runs SET prompt = $2
		WHERE id = $1 AND status IN ('queued', 'assigned', 'running')`,
		runID, joinPrompt(existingPrompt, extra))
	if err != nil {
		return fmt.Errorf("store: follow up: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	return nil
}

func joinPrompt(existing, extra string) string {
	if extra == "" {
		return existing
	}
	if existing == "" || strings.HasSuffix(existing, "\n") {
		return existing + extra
	}
	return existing + "\n" + extra
}

func (a *Automation) logInFlight(workItemID, runID string) {
	if a == nil || a.Log == nil {
		return
	}
	a.Log.Info("building run already in flight", "work_item_id", workItemID, "run_id", runID)
}

func loadAutoRun(ctx context.Context, q rowQuery, id string) (automation.Run, error) {
	if id == "" {
		return automation.Run{}, nil
	}
	var run automation.Run
	err := q.QueryRow(ctx, `
		SELECT r.id, r.factory_id, COALESCE(a.name, ''), r.pool, r.priority,
		       r.prompt, r.title, COALESCE(r.work_item_id, ''), COALESCE(r.parent_run_id, ''),
		       r.conversation_id, r.stage, r.model_id
		FROM runs AS r
		LEFT JOIN agents AS a ON a.id = r.agent_id
		WHERE r.id = $1`, id).Scan(
		&run.ID, &run.FactoryID, &run.Agent, &run.Pool, &run.Priority,
		&run.Prompt, &run.Title, &run.WorkItemID, &run.ParentRunID,
		&run.ConversationID, &run.Stage, &run.ModelID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return automation.Run{}, automation.ErrNotFound
	}
	if err != nil {
		return automation.Run{}, fmt.Errorf("store: get run: %w", err)
	}
	return run, nil
}

func deleteRun(ctx context.Context, tx pgx.Tx, id string) error {
	if id == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM run_queue WHERE run_id = $1`, id); err != nil {
		return fmt.Errorf("store: delete run: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM runs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("store: delete run: %w", err)
	}
	return nil
}

func notifyRuns(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `SELECT pg_notify('run_queue', $1)`, id); err != nil {
			return fmt.Errorf("store: notify run: %w", err)
		}
	}
	return nil
}
