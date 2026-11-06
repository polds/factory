package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// NormalizeInboxLimit maps 0 to the default of 50 and rejects values outside 1..100.
func NormalizeInboxLimit(limit int) (int, error) {
	switch {
	case limit == 0:
		return InboxDefaultLimit, nil
	case limit < 1 || limit > InboxMaxLimit:
		return 0, fmt.Errorf("%w: limit must be 1..%d", ErrInputInvalid, InboxMaxLimit)
	default:
		return limit, nil
	}
}

// EncodeInboxCursor returns an opaque cursor for one delivery position.
func EncodeInboxCursor(seq int64, deliveryID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("n1.%d.%s", seq, deliveryID)))
}

// DecodeInboxCursor parses an opaque cursor. An empty cursor starts at the beginning.
func DecodeInboxCursor(cursor string) (int64, string, error) {
	if cursor == "" {
		return 0, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", fmt.Errorf("%w: cursor", ErrInputInvalid)
	}
	rest, ok := strings.CutPrefix(string(raw), "n1.")
	if !ok {
		return 0, "", fmt.Errorf("%w: cursor", ErrInputInvalid)
	}
	seqText, deliveryID, ok := strings.Cut(rest, ".")
	if !ok || seqText == "" || deliveryID == "" || strings.TrimLeft(seqText, "0123456789") != "" {
		return 0, "", fmt.Errorf("%w: cursor", ErrInputInvalid)
	}
	seq, err := strconv.ParseInt(seqText, 10, 64)
	if err != nil || seq < 0 {
		return 0, "", fmt.Errorf("%w: cursor", ErrInputInvalid)
	}
	return seq, deliveryID, nil
}

// ReadInbox projects canonical events for one assignee. It does not acknowledge,
// including when Wait long-polls until a delivery arrives or the wait ends.
func (s *Store) ReadInbox(ctx context.Context, q InboxQuery) (InboxPage, error) {
	if q.FactoryID == "" || q.Assignee == "" {
		return InboxPage{}, fmt.Errorf("%w: factory and assignee are required", ErrInputInvalid)
	}
	limit, err := NormalizeInboxLimit(q.Limit)
	if err != nil {
		return InboxPage{}, err
	}
	after, afterID, err := DecodeInboxCursor(q.Cursor)
	if err != nil {
		return InboxPage{}, err
	}
	if q.Wait < 0 {
		return InboxPage{}, fmt.Errorf("%w: wait", ErrInputInvalid)
	}
	deadline := time.Now().Add(q.Wait)
	for {
		page, err := s.readInboxOnce(ctx, q.FactoryID, q.Assignee, after, afterID, limit)
		if err != nil {
			return InboxPage{}, err
		}
		if len(page.Deliveries) > 0 || q.Wait == 0 || !time.Now().Before(deadline) {
			return page, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return InboxPage{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) readInboxOnce(ctx context.Context, factoryID, assignee string, after int64, afterID string, limit int) (InboxPage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.message_id, d.invocation_id, d.task_id, d.assignee,
		       e.body, e.kind, e.ledger_seq
		FROM native_deliveries d
		JOIN invocation_events e
		  ON e.invocation_id = d.invocation_id AND e.ledger_seq = d.ledger_seq
		JOIN native_tasks t ON t.id = d.task_id
		WHERE t.factory_id = $1
		  AND d.assignee = $2
		  AND (
		    e.ledger_seq > $3
		    OR (e.ledger_seq = $3 AND d.id > $4)
		  )
		ORDER BY e.ledger_seq, d.id
		LIMIT $5`, factoryID, assignee, after, afterID, limit)
	if err != nil {
		return InboxPage{}, fmt.Errorf("store: read inbox: %w", err)
	}
	defer rows.Close()
	page := InboxPage{Deliveries: []InboxDelivery{}}
	for rows.Next() {
		var row InboxDelivery
		if err := rows.Scan(
			&row.DeliveryID, &row.MessageID, &row.InvocationID, &row.TaskID, &row.Assignee,
			&row.Body, &row.Kind, &row.LedgerSeq,
		); err != nil {
			return InboxPage{}, fmt.Errorf("store: read inbox: %w", err)
		}
		page.Deliveries = append(page.Deliveries, row)
	}
	if err := rows.Err(); err != nil {
		return InboxPage{}, fmt.Errorf("store: read inbox: %w", err)
	}
	if len(page.Deliveries) == limit {
		last := page.Deliveries[len(page.Deliveries)-1]
		page.NextCursor = EncodeInboxCursor(last.LedgerSeq, last.DeliveryID)
	}
	return page, nil
}

// ApplyMessage records a durable apply once per message. A repeat returns applied=false
// and does not increment apply_count.
func (s *Store) ApplyMessage(ctx context.Context, messageID, taskID, principal string) (bool, error) {
	if messageID == "" || taskID == "" || principal == "" {
		return false, fmt.Errorf("%w: message, task, and principal are required", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("store: apply message: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var eventTask string
	err = tx.QueryRow(ctx, `SELECT task_id FROM invocation_events WHERE message_id = $1`, messageID).Scan(&eventTask)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("store: apply message: %w", err)
	}
	if eventTask != taskID {
		return false, ErrNotFound
	}
	var inserted string
	err = tx.QueryRow(ctx, `
		INSERT INTO native_applies (message_id, task_id, principal, apply_count)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (message_id) DO NOTHING
		RETURNING message_id`, messageID, taskID, principal).Scan(&inserted)
	applied := true
	if errors.Is(err, pgx.ErrNoRows) {
		applied = false
		var existingTask, existingPrincipal string
		err = tx.QueryRow(ctx, `
			SELECT task_id, principal FROM native_applies WHERE message_id = $1`, messageID).
			Scan(&existingTask, &existingPrincipal)
		if err != nil {
			return false, fmt.Errorf("store: apply message: %w", err)
		}
		if existingTask != taskID {
			return false, ErrNotFound
		}
		if existingPrincipal != principal {
			return false, ErrPrincipalRejected
		}
	} else if err != nil {
		return false, fmt.Errorf("store: apply message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("store: apply message: %w", err)
	}
	return applied, nil
}

// AckDelivery inserts an idempotent ack on (delivery_id, principal, task_id)
// after a durable apply. It does not apply the message again.
func (s *Store) AckDelivery(ctx context.Context, deliveryID, principal, taskID string) (AckResult, error) {
	if deliveryID == "" || principal == "" || taskID == "" {
		return AckResult{}, fmt.Errorf("%w: delivery, principal, and task are required", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AckResult{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var messageID, deliveryTask string
	err = tx.QueryRow(ctx, `
		SELECT message_id, task_id FROM native_deliveries WHERE id = $1 FOR UPDATE`, deliveryID).
		Scan(&messageID, &deliveryTask)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AckResult{}, ErrNotFound
		}
		return AckResult{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	if deliveryTask != taskID {
		return AckResult{}, ErrNotFound
	}
	var applyPrincipal, applyTask string
	err = tx.QueryRow(ctx, `
		SELECT principal, task_id FROM native_applies WHERE message_id = $1`, messageID).
		Scan(&applyPrincipal, &applyTask)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AckResult{}, ErrNotApplied
		}
		return AckResult{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	if applyTask != taskID {
		return AckResult{}, ErrNotFound
	}
	if applyPrincipal != principal {
		return AckResult{}, ErrPrincipalRejected
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO native_acks (delivery_id, principal, task_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (delivery_id, principal, task_id) DO NOTHING`,
		deliveryID, principal, taskID)
	if err != nil {
		return AckResult{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AckResult{}, fmt.Errorf("store: ack delivery: %w", err)
	}
	return AckResult{
		DeliveryID: deliveryID,
		MessageID:  messageID,
		TaskID:     taskID,
		Principal:  principal,
		Duplicate:  tag.RowsAffected() == 0,
	}, nil
}

// Redeliver inserts a new delivery id for the same message. A dropped read
// leaves the previous delivery unacked. This does not apply or acknowledge.
// The message task is checked before the insert, in this transaction. A
// different task, including one in another factory, inserts nothing.
func (s *Store) Redeliver(ctx context.Context, messageID, taskID string) (InboxDelivery, error) {
	if messageID == "" || taskID == "" {
		return InboxDelivery{}, fmt.Errorf("%w: message and task are required", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InboxDelivery{}, fmt.Errorf("store: redeliver: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var eventTask string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(task_id, '')
		FROM invocation_events
		WHERE message_id = $1
		FOR UPDATE`, messageID).Scan(&eventTask)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxDelivery{}, ErrNotFound
		}
		return InboxDelivery{}, fmt.Errorf("store: redeliver: %w", err)
	}
	if eventTask != taskID {
		return InboxDelivery{}, ErrNotFound
	}
	id, err := NewID(PrefixNativeDelivery)
	if err != nil {
		return InboxDelivery{}, err
	}
	var row InboxDelivery
	err = tx.QueryRow(ctx, `
		INSERT INTO native_deliveries (
		    id, message_id, invocation_id, ledger_seq, task_id, assignee)
		SELECT $1, message_id, invocation_id, ledger_seq, task_id, assignee
		FROM native_deliveries
		WHERE message_id = $2 AND task_id = $3
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		RETURNING id, message_id, invocation_id, task_id, assignee`,
		id, messageID, taskID).Scan(&row.DeliveryID, &row.MessageID, &row.InvocationID, &row.TaskID, &row.Assignee)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxDelivery{}, ErrNotFound
		}
		return InboxDelivery{}, fmt.Errorf("store: redeliver: %w", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT body, kind, ledger_seq FROM invocation_events WHERE message_id = $1`, messageID).
		Scan(&row.Body, &row.Kind, &row.LedgerSeq)
	if err != nil {
		return InboxDelivery{}, fmt.Errorf("store: redeliver: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxDelivery{}, fmt.Errorf("store: redeliver: %w", err)
	}
	return row, nil
}

// ClaimInbox gives the first holder fence 1. A second claim loses with
// claim_fenced. The holder and fence stay unchanged.
func (s *Store) ClaimInbox(ctx context.Context, taskID, holder string) (InboxClaim, error) {
	if taskID == "" || strings.TrimSpace(holder) == "" {
		return InboxClaim{}, fmt.Errorf("%w: task and holder are required", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InboxClaim{}, fmt.Errorf("store: claim inbox: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists string
	err = tx.QueryRow(ctx, `SELECT id FROM native_tasks WHERE id = $1`, taskID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxClaim{}, ErrNotFound
		}
		return InboxClaim{}, fmt.Errorf("store: claim inbox: %w", err)
	}
	var gotHolder string
	var fence int64
	err = tx.QueryRow(ctx, `
		INSERT INTO native_inbox_claims (task_id, holder, fence)
		VALUES ($1, $2, 1)
		ON CONFLICT (task_id) DO NOTHING
		RETURNING holder, fence`, taskID, holder).Scan(&gotHolder, &fence)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			SELECT holder, fence FROM native_inbox_claims WHERE task_id = $1`, taskID).
			Scan(&gotHolder, &fence)
		if err != nil {
			return InboxClaim{}, fmt.Errorf("store: claim inbox: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return InboxClaim{}, err
		}
		return InboxClaim{}, &ClaimLost{InboxClaim: InboxClaim{TaskID: taskID, Holder: gotHolder, Fence: fence}}
	}
	if err != nil {
		return InboxClaim{}, fmt.Errorf("store: claim inbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxClaim{}, fmt.Errorf("store: claim inbox: %w", err)
	}
	return InboxClaim{TaskID: taskID, Holder: gotHolder, Fence: fence, Accepted: true}, nil
}

// GetInboxClaim returns the current fence or ErrNotFound.
func (s *Store) GetInboxClaim(ctx context.Context, taskID string) (InboxClaim, error) {
	var claim InboxClaim
	err := s.pool.QueryRow(ctx, `
		SELECT task_id, holder, fence FROM native_inbox_claims WHERE task_id = $1`, taskID).
		Scan(&claim.TaskID, &claim.Holder, &claim.Fence)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InboxClaim{}, ErrNotFound
		}
		return InboxClaim{}, fmt.Errorf("store: get inbox claim: %w", err)
	}
	claim.Accepted = true
	return claim, nil
}

// CountAcks returns ack rows for one delivery.
func (s *Store) CountAcks(ctx context.Context, deliveryID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM native_acks WHERE delivery_id = $1`, deliveryID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count acks: %w", err)
	}
	return n, nil
}

// ApplyCount returns the durable apply counter for a message. Missing is zero.
func (s *Store) ApplyCount(ctx context.Context, messageID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT apply_count FROM native_applies WHERE message_id = $1`, messageID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: apply count: %w", err)
	}
	return n, nil
}
