package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type ledgerHoldKey struct{}

// WithLedgerHold runs hold after the event, state, and outbox inserts and
// before commit, while the invocation ledger row is still locked.
// Tests use it to prove a later writer cannot commit first.
func WithLedgerHold(ctx context.Context, hold func()) context.Context {
	return context.WithValue(ctx, ledgerHoldKey{}, hold)
}

func ledgerHold(ctx context.Context) func() {
	hold, _ := ctx.Value(ledgerHoldKey{}).(func())
	return hold
}

// AppendLedger locks invocation_ledgers FOR UPDATE, allocates the next
// ledger_seq, and commits the canonical invocation_events row, the state
// row, and the outbox row in that same transaction.
// Per-run SSE cursors stay on run_events.seq (Last-Event-ID). This ledger
// does not read or write that sequence, and it does not use nextval or MAX(seq).
func (s *Store) AppendLedger(ctx context.Context, invocationID string, entry LedgerEntry) (LedgerRecord, error) {
	if err := validateLedgerEntry(entry); err != nil {
		return LedgerRecord{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rec, err := appendLedgerTx(ctx, tx, invocationID, entry)
	if err != nil {
		return LedgerRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	return rec, nil
}

func appendLedgerTx(ctx context.Context, tx pgx.Tx, invocationID string, entry LedgerEntry) (LedgerRecord, error) {
	if !validPrefixedID(invocationID, PrefixInvocation) {
		return LedgerRecord{}, fmt.Errorf("%w: invocation id", ErrInputInvalid)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_ledgers (invocation_id, ledger_seq)
		VALUES ($1, 0)
		ON CONFLICT (invocation_id) DO NOTHING`, invocationID); err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	var seq int64
	err := tx.QueryRow(ctx, `
		SELECT ledger_seq FROM invocation_ledgers
		WHERE invocation_id = $1
		FOR UPDATE`, invocationID).Scan(&seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LedgerRecord{}, ErrNotFound
		}
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	next := seq + 1
	if _, err := tx.Exec(ctx, `
		UPDATE invocation_ledgers SET ledger_seq = $2 WHERE invocation_id = $1`,
		invocationID, next); err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	if entry.TaskID != "" {
		if err := lockNativeTask(ctx, tx, invocationID, entry.TaskID); err != nil {
			return LedgerRecord{}, err
		}
	}
	status, assignee := "", ""
	if entry.TaskUpdate != nil {
		status = entry.TaskUpdate.Status
		assignee = entry.TaskUpdate.Assignee
		if err := updateNativeTaskTx(ctx, tx, entry.TaskID, entry.TaskUpdate); err != nil {
			return LedgerRecord{}, err
		}
	}
	messageID, err := NewID(PrefixNativeMessage)
	if err != nil {
		return LedgerRecord{}, err
	}
	payload := entry.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_events (
		    invocation_id, ledger_seq, message_id, kind, task_id, body, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)`,
		invocationID, next, messageID, entry.Kind, nullableText(entry.TaskID), entry.Body, []byte(payload)); err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_ledger_state (
		    invocation_id, ledger_seq, task_id, task_status, assignee)
		VALUES ($1, $2, $3, $4, $5)`,
		invocationID, next, nullableText(entry.TaskID), status, assignee); err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_outbox (invocation_id, ledger_seq, kind, payload)
		VALUES ($1, $2, $3, $4::jsonb)`,
		invocationID, next, entry.Kind, []byte(payload)); err != nil {
		return LedgerRecord{}, fmt.Errorf("store: append ledger: %w", err)
	}
	var deliveryID string
	if entry.Assignee != "" {
		deliveryID, err = insertDeliveryTx(ctx, tx, messageID, invocationID, next, entry.TaskID, entry.Assignee)
		if err != nil {
			return LedgerRecord{}, err
		}
	}
	if entry.Wake {
		reason := entry.WakeReason
		if reason == "" {
			reason = entry.Kind
		}
		if _, err := coalesceWakeTx(ctx, tx, WakeIntent{
			TaskID:       entry.TaskID,
			InvocationID: invocationID,
			Assignee:     entry.Assignee,
			Reason:       reason,
		}); err != nil {
			return LedgerRecord{}, err
		}
	}
	if hold := ledgerHold(ctx); hold != nil {
		hold()
	}
	return LedgerRecord{
		InvocationID: invocationID,
		LedgerSeq:    next,
		MessageID:    messageID,
		DeliveryID:   deliveryID,
	}, nil
}

func lockNativeTask(ctx context.Context, tx pgx.Tx, invocationID, taskID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM native_tasks
		WHERE id = $1 AND invocation_id = $2
		FOR UPDATE`, taskID, invocationID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("store: lock native task: %w", err)
	}
	return nil
}

func updateNativeTaskTx(ctx context.Context, tx pgx.Tx, taskID string, update *NativeTaskUpdate) error {
	if taskID == "" || update == nil {
		return fmt.Errorf("%w: task update", ErrInputInvalid)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE native_tasks
		SET assignee = CASE WHEN $2 = '' THEN assignee ELSE $2 END,
		    task_status = CASE WHEN $3 = '' THEN task_status ELSE $3 END,
		    updated_at = now()
		WHERE id = $1`, taskID, update.Assignee, update.Status)
	if err != nil {
		return fmt.Errorf("store: update native task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func insertDeliveryTx(ctx context.Context, tx pgx.Tx, messageID, invocationID string, seq int64, taskID, assignee string) (string, error) {
	if taskID == "" || assignee == "" {
		return "", fmt.Errorf("%w: delivery requires a task and assignee", ErrInputInvalid)
	}
	id, err := NewID(PrefixNativeDelivery)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO native_deliveries (
		    id, message_id, invocation_id, ledger_seq, task_id, assignee)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, messageID, invocationID, seq, taskID, assignee); err != nil {
		return "", fmt.Errorf("store: insert delivery: %w", err)
	}
	return id, nil
}

func validateLedgerEntry(entry LedgerEntry) error {
	switch entry.Kind {
	case LedgerKindComment, LedgerKindMention, LedgerKindAssign, LedgerKindResult, LedgerKindCompletion:
	default:
		return fmt.Errorf("%w: ledger kind", ErrInputInvalid)
	}
	if len(entry.Body) > NativeBodyCap {
		return fmt.Errorf("%w: body exceeds %d bytes", ErrInputInvalid, NativeBodyCap)
	}
	if entry.Payload != nil && !json.Valid(entry.Payload) {
		return fmt.Errorf("%w: payload", ErrInputInvalid)
	}
	if entry.Assignee != "" && entry.TaskID == "" {
		return fmt.Errorf("%w: delivery requires a task", ErrInputInvalid)
	}
	if entry.Wake && (entry.TaskID == "" || entry.Assignee == "") {
		return fmt.Errorf("%w: wake requires a task and assignee", ErrInputInvalid)
	}
	if entry.Assignee != "" && looksLikeRecordID(entry.Assignee) {
		return fmt.Errorf("%w: assignee is not a task, activation, or execution id", ErrInputInvalid)
	}
	if entry.TaskUpdate != nil && entry.TaskUpdate.Assignee != "" && looksLikeRecordID(entry.TaskUpdate.Assignee) {
		return fmt.Errorf("%w: assignee is not a task, activation, or execution id", ErrInputInvalid)
	}
	return nil
}

// ReadOutbox returns committed outbox rows with ledger_seq greater than afterSeq.
func (s *Store) ReadOutbox(ctx context.Context, invocationID string, afterSeq int64, limit int) ([]OutboxRow, error) {
	limit, err := NormalizeInboxLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT invocation_id, ledger_seq, kind, payload
		FROM invocation_outbox
		WHERE invocation_id = $1 AND ledger_seq > $2
		ORDER BY ledger_seq
		LIMIT $3`, invocationID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		var row OutboxRow
		var payload []byte
		if err := rows.Scan(&row.InvocationID, &row.LedgerSeq, &row.Kind, &payload); err != nil {
			return nil, fmt.Errorf("store: read outbox: %w", err)
		}
		row.Payload = payload
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read outbox: %w", err)
	}
	if out == nil {
		out = []OutboxRow{}
	}
	return out, nil
}
