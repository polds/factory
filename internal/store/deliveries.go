package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const deliveryColumns = `
    id, factory_id, provider, webhook_uid, delivery_id, received_at,
    headers, body, truncated, status, matched_automations, expires_at,
    normalized_json`

func scanDelivery(row pgx.Row) (Delivery, error) {
	var d Delivery
	var headers, matched, normalized []byte
	err := row.Scan(
		&d.ID, &d.FactoryID, &d.Provider, &d.WebhookUID, &d.DeliveryID, &d.ReceivedAt,
		&headers, &d.Body, &d.Truncated, &d.Status, &matched, &d.ExpiresAt,
		&normalized,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	d.Headers, err = decodeObject(headers)
	if err != nil {
		return Delivery{}, err
	}
	d.MatchedAutomations, err = decodeStringList(matched)
	if err != nil {
		return Delivery{}, err
	}
	if d.Body == nil {
		d.Body = []byte{}
	}
	d.Normalized = normalized
	return d, nil
}

// InsertDelivery stores a redacted, size-capped delivery.
// A unique (provider, webhook uid, delivery id) conflict returns the stored
// row and dup=true with a nil error. Status is always received.
// A zero ExpiresAt is received_at plus seven days.
func (s *Store) InsertDelivery(ctx context.Context, d Delivery) (Delivery, bool, error) {
	if d.FactoryID == "" {
		return Delivery{}, false, fmt.Errorf("store: factory is required")
	}
	if err := validProvider(d.Provider); err != nil {
		return Delivery{}, false, err
	}
	if d.DeliveryID == "" {
		return Delivery{}, false, fmt.Errorf("store: delivery id is required")
	}
	id := d.ID
	if id == "" {
		var err error
		id, err = NewID(PrefixDelivery)
		if err != nil {
			return Delivery{}, false, err
		}
	}
	body, truncated := capBody(d.Body, d.Truncated)
	headers, err := jsonObject(redactHeaders(d.Headers))
	if err != nil {
		return Delivery{}, false, err
	}
	row, err := scanDelivery(s.pool.QueryRow(ctx, `
		INSERT INTO deliveries (
		    id, factory_id, provider, webhook_uid, delivery_id, received_at,
		    headers, body, truncated, status, matched_automations, expires_at)
		SELECT $1, $2, $3, $4, $5,
		       clock.ts,
		       $7::jsonb, $8, $9, 'received', '[]'::jsonb,
		       COALESCE($10::timestamptz, clock.ts + interval '7 days')
		FROM (SELECT COALESCE($6::timestamptz, clock_timestamp()) AS ts) AS clock
		ON CONFLICT (provider, webhook_uid, delivery_id) DO NOTHING
		RETURNING `+deliveryColumns,
		id, d.FactoryID, d.Provider, d.WebhookUID, d.DeliveryID, nullTime(d.ReceivedAt),
		headers, body, truncated, nullTime(d.ExpiresAt),
	))
	if err == nil {
		return row, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Delivery{}, false, fmt.Errorf("store: insert delivery: %w", err)
	}
	existing, err := scanDelivery(s.pool.QueryRow(ctx, `
		SELECT `+deliveryColumns+`
		FROM deliveries
		WHERE provider = $1 AND webhook_uid = $2 AND delivery_id = $3`,
		d.Provider, d.WebhookUID, d.DeliveryID,
	))
	if err != nil {
		return Delivery{}, false, fmt.Errorf("store: insert delivery: %w", err)
	}
	return existing, true, nil
}

// GetDelivery returns the delivery or ErrNotFound.
func (s *Store) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	d, err := scanDelivery(s.pool.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM deliveries WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Delivery{}, err
		}
		return Delivery{}, fmt.Errorf("store: get delivery: %w", err)
	}
	return d, nil
}

// MarkDelivery sets status. It does not write matched_automations: a status-only
// call leaves that column unchanged. MarkDelivery(id, received) releases a
// ClaimReceived lock without finishing the delivery. Unmatched shortens
// retention to 72 hours from received_at when that is sooner.
//
// The signature matches ingress.Store. Claim locks are the exclusive state.
// Status stays one of received|matched|unmatched|ignored|failed, which is what
// the deliveries check allows. The in-memory ingress fake's private "claimed"
// value is not stored.
func (s *Store) MarkDelivery(ctx context.Context, id, status string) error {
	if err := validDeliveryStatus(status); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("store: delivery id is required")
	}
	if hold, ok := s.claimHold(id); ok {
		return s.markClaimed(ctx, hold, id, status)
	}
	return applyDeliveryStatus(ctx, s.pool, id, status)
}

// SetMatchedAutomations replaces matched_automations.
// A nil list leaves the column unchanged. An empty slice clears it.
func (s *Store) SetMatchedAutomations(ctx context.Context, id string, matched []string) (Delivery, error) {
	if id == "" {
		return Delivery{}, fmt.Errorf("store: delivery id is required")
	}
	db, unlock := s.deliveryDB(id)
	defer unlock()
	if matched == nil {
		return getDelivery(ctx, db, id)
	}
	raw, err := jsonStringList(matched)
	if err != nil {
		return Delivery{}, err
	}
	d, err := scanDelivery(db.QueryRow(ctx, `
		UPDATE deliveries
		SET matched_automations = $2::jsonb
		WHERE id = $1
		RETURNING `+deliveryColumns, id, raw))
	if errors.Is(err, ErrNotFound) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("store: set delivery automations: %w", err)
	}
	return d, nil
}

// ListDeliveries returns deliveries for one factory, newest first.
// An empty status is unconstrained. Limit <= 0 uses 100.
func (s *Store) ListDeliveries(ctx context.Context, factory, status string, limit int) ([]Delivery, error) {
	if factory == "" {
		return nil, fmt.Errorf("store: factory is required")
	}
	if status != "" {
		if err := validDeliveryStatus(status); err != nil {
			return nil, err
		}
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryColumns+`
		FROM deliveries
		WHERE factory_id = $1
		  AND ($2 = '' OR status = $2)
		ORDER BY received_at DESC, id DESC
		LIMIT $3`, factory, status, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list deliveries: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list deliveries: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list deliveries: %w", err)
	}
	return out, nil
}

// SetDeliveryNormalized stores the dispatcher event list for id.
// raw empty stores []. Invalid JSON is rejected. The update uses the
// open ClaimReceived transaction when one exists, so it does not wait
// on that claim's row lock.
func (s *Store) SetDeliveryNormalized(ctx context.Context, id string, raw json.RawMessage) error {
	if id == "" {
		return fmt.Errorf("store: delivery id is required")
	}
	payload := bytes.TrimSpace(raw)
	if len(payload) == 0 {
		payload = []byte("[]")
	}
	if !json.Valid(payload) || payload[0] != '[' {
		return fmt.Errorf("store: normalized events must be a json array")
	}
	db, unlock := s.deliveryDB(id)
	defer unlock()
	tag, err := db.Exec(ctx, `
		UPDATE deliveries SET normalized_json = $2::jsonb WHERE id = $1`, id, payload)
	if err != nil {
		return fmt.Errorf("store: set normalized events: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWebhookDeliveries returns one webhook's deliveries, newest first.
// status empty matches every status. limit <= 0 uses 100.
func (s *Store) ListWebhookDeliveries(ctx context.Context, factoryID, webhookUID, status string, limit int) ([]Delivery, error) {
	if factoryID == "" || webhookUID == "" {
		return nil, fmt.Errorf("store: factory and webhook are required")
	}
	if status != "" {
		if err := validDeliveryStatus(status); err != nil {
			return nil, err
		}
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryColumns+`
		FROM deliveries
		WHERE factory_id = $1
		  AND webhook_uid = $2
		  AND ($3 = '' OR status = $3)
		ORDER BY received_at DESC, id DESC
		LIMIT $4`, factoryID, webhookUID, status, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list webhook deliveries: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list webhook deliveries: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list webhook deliveries: %w", err)
	}
	return out, nil
}

// PurgeExpiredDeliveries deletes deliveries whose retention has elapsed.
func (s *Store) PurgeExpiredDeliveries(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		return 0, fmt.Errorf("store: now is required")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM deliveries WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("store: purge deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// deliveryClaim is one ClaimReceived transaction. Row locks stay until every
// id has been passed to MarkDelivery, then the transaction commits.
type deliveryClaim struct {
	mu     sync.Mutex
	tx     pgx.Tx
	ids    []string
	left   int
	done   map[string]bool
	closed bool
}

// deliveryDB is the pool or a claim transaction.
type deliveryDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ClaimReceived locks up to limit rows with status received.
// Two callers cannot observe the same row: the locks are FOR UPDATE SKIP
// LOCKED and stay held until MarkDelivery for every returned id.
// Status is unchanged, so a crash rolls the transaction back and leaves
// received. limit <= 0 claims nothing.
func (s *Store) ClaimReceived(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: claim deliveries: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT `+deliveryColumns+`
		FROM deliveries
		WHERE status = $1
		ORDER BY received_at ASC, id ASC
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, DeliveryReceived, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("store: claim deliveries: %w", err)
	}
	out := []Delivery{}
	var scanErr error
	for rows.Next() {
		var d Delivery
		d, scanErr = scanDelivery(rows)
		if scanErr != nil {
			break
		}
		out = append(out, d)
	}
	if scanErr == nil {
		scanErr = rows.Err()
	}
	rows.Close()
	if scanErr != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("store: claim deliveries: %w", scanErr)
	}
	if len(out) == 0 {
		if err := tx.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("store: claim deliveries: %w", err)
		}
		return nil, nil
	}
	hold := &deliveryClaim{
		tx:   tx,
		ids:  make([]string, len(out)),
		left: len(out),
		done: map[string]bool{},
	}
	for i := range out {
		hold.ids[i] = out[i].ID
	}
	s.claimMu.Lock()
	if s.claims == nil {
		s.claims = map[string]*deliveryClaim{}
	}
	for _, id := range hold.ids {
		s.claims[id] = hold
	}
	s.claimMu.Unlock()
	return out, nil
}

func (s *Store) claimHold(id string) (*deliveryClaim, bool) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	hold, ok := s.claims[id]
	if !ok {
		return nil, false
	}
	return hold, true
}

func (s *Store) markClaimed(ctx context.Context, hold *deliveryClaim, id, status string) error {
	hold.mu.Lock()
	err := s.markClaimedLocked(ctx, hold, id, status)
	release := hold.closed
	hold.mu.Unlock()
	if release {
		s.dropClaim(hold)
	}
	return err
}

func (s *Store) markClaimedLocked(ctx context.Context, hold *deliveryClaim, id, status string) error {
	if hold.done[id] {
		return nil
	}
	if hold.closed {
		return fmt.Errorf("store: delivery claim closed")
	}
	if err := applyDeliveryStatus(ctx, hold.tx, id, status); err != nil {
		if errors.Is(err, ErrNotFound) {
			if cerr := s.finishClaimLocked(ctx, hold, id); cerr != nil {
				return cerr
			}
			return ErrNotFound
		}
		// A statement error aborts the Postgres transaction.
		hold.closed = true
		_ = hold.tx.Rollback(context.Background())
		return err
	}
	return s.finishClaimLocked(ctx, hold, id)
}

func (s *Store) finishClaimLocked(ctx context.Context, hold *deliveryClaim, id string) error {
	hold.done[id] = true
	hold.left--
	if hold.left > 0 {
		return nil
	}
	if err := hold.tx.Commit(ctx); err != nil {
		hold.closed = true
		_ = hold.tx.Rollback(context.Background())
		return fmt.Errorf("store: commit delivery claim: %w", err)
	}
	hold.closed = true
	return nil
}

func (s *Store) dropClaim(hold *deliveryClaim) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	for _, id := range hold.ids {
		if s.claims[id] == hold {
			delete(s.claims, id)
		}
	}
}

func (s *Store) deliveryDB(id string) (deliveryDB, func()) {
	s.claimMu.Lock()
	hold := s.claims[id]
	s.claimMu.Unlock()
	if hold == nil {
		return s.pool, func() {}
	}
	hold.mu.Lock()
	if hold.closed {
		hold.mu.Unlock()
		return s.pool, func() {}
	}
	return hold.tx, hold.mu.Unlock
}

func getDelivery(ctx context.Context, db deliveryDB, id string) (Delivery, error) {
	d, err := scanDelivery(db.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM deliveries WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Delivery{}, err
		}
		return Delivery{}, fmt.Errorf("store: get delivery: %w", err)
	}
	return d, nil
}

func applyDeliveryStatus(ctx context.Context, db deliveryDB, id, status string) error {
	tag, err := db.Exec(ctx, `
		UPDATE deliveries
		SET status = $2,
		    expires_at = CASE
		        WHEN $2 = 'unmatched'
		            THEN LEAST(expires_at, received_at + interval '72 hours')
		        ELSE expires_at
		    END
		WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("store: mark delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// abortDeliveryClaims rolls back claim transactions still open at Close.
func (s *Store) abortDeliveryClaims() {
	s.claimMu.Lock()
	seen := map[*deliveryClaim]struct{}{}
	holds := []*deliveryClaim{}
	for _, hold := range s.claims {
		if _, ok := seen[hold]; ok {
			continue
		}
		seen[hold] = struct{}{}
		holds = append(holds, hold)
	}
	s.claims = nil
	s.claimMu.Unlock()
	for _, hold := range holds {
		hold.mu.Lock()
		if !hold.closed {
			hold.closed = true
			_ = hold.tx.Rollback(context.Background())
		}
		hold.mu.Unlock()
	}
}
