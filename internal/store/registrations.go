package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const webhookColumns = `
    uid, factory_id, name, auth_mode, signature_scheme, secret_ref,
    url_token_hash, delivery_id_header, enabled`

func scanWebhook(row pgx.Row) (WebhookRegistration, error) {
	var w WebhookRegistration
	err := row.Scan(
		&w.UID, &w.FactoryID, &w.Name, &w.AuthMode, &w.SignatureScheme, &w.SecretRef,
		&w.URLTokenHash, &w.DeliveryIDHeader, &w.Enabled,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookRegistration{}, ErrNotFound
	}
	if err != nil {
		return WebhookRegistration{}, err
	}
	return w, nil
}

func validateWebhook(w WebhookRegistration) error {
	if w.FactoryID == "" {
		return fmt.Errorf("store: factory is required")
	}
	if w.Name == "" {
		return fmt.Errorf("store: webhook name is required")
	}
	if err := validWebhookAuth(w.AuthMode); err != nil {
		return err
	}
	if len(w.DeliveryIDHeader) > 64 {
		return fmt.Errorf("store: delivery id header is too long")
	}
	if len(w.URLTokenHash) != 0 && len(w.URLTokenHash) != 32 {
		return fmt.Errorf("store: url token hash must be 32 bytes")
	}
	return nil
}

func hashArg(sum []byte) any {
	if len(sum) == 0 {
		return nil
	}
	return sum
}

// UpsertWebhookRegistration inserts a registration or updates its mutable
// fields. Changing auth_mode or signature_scheme returns ErrImmutable and
// writes nothing. SecretRef is a credential name, never a secret value.
func (s *Store) UpsertWebhookRegistration(ctx context.Context, w WebhookRegistration) (WebhookRegistration, error) {
	if err := validateWebhook(w); err != nil {
		return WebhookRegistration{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WebhookRegistration{}, fmt.Errorf("store: upsert webhook: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	saved, err := upsertWebhookTx(ctx, tx, w)
	if err != nil {
		return WebhookRegistration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WebhookRegistration{}, fmt.Errorf("store: upsert webhook: %w", err)
	}
	return saved, nil
}

func upsertWebhookTx(ctx context.Context, tx pgx.Tx, w WebhookRegistration) (WebhookRegistration, error) {
	if w.UID == "" {
		id, err := NewID(PrefixWebhook)
		if err != nil {
			return WebhookRegistration{}, err
		}
		w.UID = id
	}
	inserted, err := scanWebhook(tx.QueryRow(ctx, `
		INSERT INTO webhook_registrations (
		    uid, factory_id, name, auth_mode, signature_scheme, secret_ref,
		    url_token_hash, delivery_id_header, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (uid) DO NOTHING
		RETURNING `+webhookColumns,
		w.UID, w.FactoryID, w.Name, w.AuthMode, w.SignatureScheme, w.SecretRef,
		hashArg(w.URLTokenHash), w.DeliveryIDHeader, w.Enabled,
	))
	if err == nil {
		return inserted, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return WebhookRegistration{}, fmt.Errorf("store: upsert webhook: %w", err)
	}
	current, err := scanWebhook(tx.QueryRow(ctx, `
		SELECT `+webhookColumns+`
		FROM webhook_registrations
		WHERE uid = $1
		FOR UPDATE`, w.UID))
	if err != nil {
		return WebhookRegistration{}, fmt.Errorf("store: upsert webhook: %w", err)
	}
	if current.FactoryID != w.FactoryID {
		return WebhookRegistration{}, fmt.Errorf("store: webhook belongs to another factory")
	}
	if current.AuthMode != w.AuthMode || current.SignatureScheme != w.SignatureScheme {
		return WebhookRegistration{}, ErrImmutable
	}
	saved, err := scanWebhook(tx.QueryRow(ctx, `
		UPDATE webhook_registrations
		SET name = $2,
		    secret_ref = $3,
		    url_token_hash = $4,
		    delivery_id_header = $5,
		    enabled = $6
		WHERE uid = $1
		RETURNING `+webhookColumns,
		w.UID, w.Name, w.SecretRef, hashArg(w.URLTokenHash), w.DeliveryIDHeader, w.Enabled,
	))
	if err != nil {
		return WebhookRegistration{}, fmt.Errorf("store: upsert webhook: %w", immutableOr(err))
	}
	return saved, nil
}

// ApplyWebhooks syncs one factory's registrations. A uid that is absent from
// regs is deleted. An auth_mode or signature_scheme change returns ErrImmutable
// and leaves the previous rows in place.
func (s *Store) ApplyWebhooks(ctx context.Context, factoryID string, regs []WebhookRegistration) error {
	if factoryID == "" {
		return fmt.Errorf("store: factory is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: apply webhooks: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT uid FROM webhook_registrations
		WHERE factory_id = $1
		FOR UPDATE`, factoryID); err != nil {
		return fmt.Errorf("store: apply webhooks: %w", err)
	}
	uids := make([]string, 0, len(regs))
	for _, reg := range regs {
		reg.FactoryID = factoryID
		if err := validateWebhook(reg); err != nil {
			return err
		}
		saved, err := upsertWebhookTx(ctx, tx, reg)
		if err != nil {
			return err
		}
		uids = append(uids, saved.UID)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM webhook_registrations
		WHERE factory_id = $1
		  AND NOT (uid = ANY($2::text[]))`, factoryID, uids); err != nil {
		return fmt.Errorf("store: apply webhooks: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: apply webhooks: %w", err)
	}
	return nil
}

// GetWebhookRegistration returns the registration or ErrNotFound.
func (s *Store) GetWebhookRegistration(ctx context.Context, uid string) (WebhookRegistration, error) {
	row, err := scanWebhook(s.pool.QueryRow(ctx, `
		SELECT `+webhookColumns+` FROM webhook_registrations WHERE uid = $1`, uid))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WebhookRegistration{}, err
		}
		return WebhookRegistration{}, fmt.Errorf("store: get webhook: %w", err)
	}
	return row, nil
}

// GetWebhookRegistrationByTokenHash returns the registration for a SHA-256
// url token digest, or ErrNotFound.
func (s *Store) GetWebhookRegistrationByTokenHash(ctx context.Context, sum []byte) (WebhookRegistration, error) {
	if len(sum) != 32 {
		return WebhookRegistration{}, fmt.Errorf("store: url token hash must be 32 bytes")
	}
	row, err := scanWebhook(s.pool.QueryRow(ctx, `
		SELECT `+webhookColumns+`
		FROM webhook_registrations
		WHERE url_token_hash = $1`, sum))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return WebhookRegistration{}, err
		}
		return WebhookRegistration{}, fmt.Errorf("store: get webhook by hash: %w", err)
	}
	return row, nil
}

// ListWebhookRegistrations returns a factory's registrations by name.
func (s *Store) ListWebhookRegistrations(ctx context.Context, factoryID string) ([]WebhookRegistration, error) {
	if factoryID == "" {
		return nil, fmt.Errorf("store: factory is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+webhookColumns+`
		FROM webhook_registrations
		WHERE factory_id = $1
		ORDER BY name ASC, uid ASC`, factoryID)
	return collect(rows, err, "store: list webhooks", scanWebhook)
}
