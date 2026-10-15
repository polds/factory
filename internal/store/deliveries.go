package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const deliveryColumns = `
    id, factory_id, provider, webhook_uid, delivery_id, received_at,
    headers, body, truncated, status, matched_automations, expires_at`

func scanDelivery(row pgx.Row) (Delivery, error) {
	var d Delivery
	var headers, matched []byte
	err := row.Scan(
		&d.ID, &d.FactoryID, &d.Provider, &d.WebhookUID, &d.DeliveryID, &d.ReceivedAt,
		&headers, &d.Body, &d.Truncated, &d.Status, &matched, &d.ExpiresAt,
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
		VALUES (
		    $1, $2, $3, $4, $5,
		    COALESCE($6::timestamptz, clock_timestamp()),
		    $7::jsonb, $8, $9, 'received', '[]'::jsonb,
		    COALESCE(
		        $10::timestamptz,
		        COALESCE($6::timestamptz, clock_timestamp()) + interval '7 days'))
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

// MarkDelivery sets status and the automations that matched.
// A zero expiresAt leaves retention unchanged, except unmatched deliveries
// are capped at 72 hours from received_at.
func (s *Store) MarkDelivery(ctx context.Context, id, status string, matched []string, expiresAt time.Time) (Delivery, error) {
	if err := validDeliveryStatus(status); err != nil {
		return Delivery{}, err
	}
	raw, err := jsonStringList(matched)
	if err != nil {
		return Delivery{}, err
	}
	d, err := scanDelivery(s.pool.QueryRow(ctx, `
		UPDATE deliveries
		SET status = $2,
		    matched_automations = $3::jsonb,
		    expires_at = CASE
		        WHEN $2 = 'unmatched' AND $4::timestamptz IS NULL
		            THEN LEAST(expires_at, received_at + interval '72 hours')
		        ELSE COALESCE($4::timestamptz, expires_at)
		    END
		WHERE id = $1
		RETURNING `+deliveryColumns, id, status, raw, nullTime(expiresAt)))
	if errors.Is(err, ErrNotFound) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("store: mark delivery: %w", err)
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
