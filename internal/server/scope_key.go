package server

import (
	"context"
	"crypto/sha256"

	"github.com/polds/factory/internal/store"
)

// FactoryIDForKey resolves a factory-scoped API key. The deployment admin key
// is compared separately and is not an argument here.
func (s *Server) FactoryIDForKey(ctx context.Context, token string) (string, bool, error) {
	if s == nil || s.store == nil || token == "" {
		return "", false, nil
	}
	sum := sha256.Sum256([]byte(token))
	id, err := s.store.FactoryByKeyHash(ctx, sum[:])
	if err != nil {
		if err == store.ErrNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

// ResolveFactoryID maps a factory id or name to the factory id.
func (s *Server) ResolveFactoryID(ctx context.Context, nameOrID string) (string, error) {
	fac, err := s.factory(ctx, nameOrID)
	if err != nil {
		return "", err
	}
	return fac.ID, nil
}

// ApprovalFactory is the factory that owns the approval.
func (s *Server) ApprovalFactory(ctx context.Context, approvalID string) (string, error) {
	if s.db == nil {
		return "", store.ErrNotFound
	}
	var factoryID string
	err := s.db.QueryRow(ctx, `
		SELECT w.factory_id
		FROM approvals a
		JOIN work_items w ON w.id = a.work_item_id
		WHERE a.id = $1`, approvalID).Scan(&factoryID)
	if err != nil {
		return "", err
	}
	return factoryID, nil
}

// DeliveryFactory is the factory that owns the delivery.
func (s *Server) DeliveryFactory(ctx context.Context, deliveryID string) (string, error) {
	if s.db == nil {
		return "", store.ErrNotFound
	}
	var factoryID string
	err := s.db.QueryRow(ctx, `SELECT factory_id FROM deliveries WHERE id = $1`, deliveryID).Scan(&factoryID)
	if err != nil {
		return "", err
	}
	return factoryID, nil
}
