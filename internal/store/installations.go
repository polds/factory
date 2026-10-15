package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UpsertInstallation records which factory owns a provider installation.
// The row stores ids only. Secret material is not a column.
func (s *Store) UpsertInstallation(ctx context.Context, provider, externalID, factoryID string) error {
	if err := validProvider(provider); err != nil {
		return err
	}
	if externalID == "" || factoryID == "" {
		return fmt.Errorf("store: installation id and factory are required")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO integration_installations (provider, external_id, factory_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (provider, external_id) DO UPDATE
		SET factory_id = EXCLUDED.factory_id`,
		provider, externalID, factoryID,
	); err != nil {
		return fmt.Errorf("store: upsert installation: %w", err)
	}
	return nil
}

// GetInstallation returns the factory id for a provider installation, or ErrNotFound.
func (s *Store) GetInstallation(ctx context.Context, provider, externalID string) (string, error) {
	var factoryID string
	err := s.pool.QueryRow(ctx, `
		SELECT factory_id
		FROM integration_installations
		WHERE provider = $1 AND external_id = $2`, provider, externalID).Scan(&factoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: get installation: %w", err)
	}
	return factoryID, nil
}
