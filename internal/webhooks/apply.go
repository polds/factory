package webhooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/polds/factory/internal/store"
)

// SecretSource resolves a credential name to its current value.
// Implementations must not put the value in the error text.
type SecretSource interface {
	Secret(ctx context.Context, name string) (string, error)
}

// Sync is the registration store used at definition apply.
// *store.Store implements it. ApplyWebhooks deletes registrations omitted
// from regs.
type Sync interface {
	ListWebhookRegistrations(ctx context.Context, factoryID string) ([]store.WebhookRegistration, error)
	ApplyWebhooks(ctx context.Context, factoryID string, regs []store.WebhookRegistration) error
}

// Apply syncs specs onto one factory. Existing rows are matched by name and
// keep their uid. A change to authMode or signatureScheme returns an error
// that names ImmutableRule, unwraps to store.ErrImmutable, and does not
// write. Omitted names are deleted by Sync.ApplyWebhooks.
func Apply(ctx context.Context, sync Sync, secrets SecretSource, factoryID string, specs []Spec) error {
	if strings.TrimSpace(factoryID) == "" {
		return fmt.Errorf("webhooks: factory is required")
	}
	if sync == nil {
		return fmt.Errorf("webhooks: registration store is required")
	}
	existing, err := sync.ListWebhookRegistrations(ctx, factoryID)
	if err != nil {
		return fmt.Errorf("webhooks: list registrations: %w", err)
	}
	byName := make(map[string]store.WebhookRegistration, len(existing))
	for _, row := range existing {
		byName[row.Name] = row
	}
	ordered := append([]Spec(nil), specs...)
	slices.SortFunc(ordered, func(a, b Spec) int { return strings.Compare(a.Name, b.Name) })
	out := make([]store.WebhookRegistration, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, raw := range ordered {
		spec, err := normalizeSpec(raw)
		if err != nil {
			return err
		}
		if _, ok := seen[spec.Name]; ok {
			return fmt.Errorf("webhooks: duplicate webhook %q", spec.Name)
		}
		seen[spec.Name] = struct{}{}
		var prev *store.WebhookRegistration
		if row, ok := byName[spec.Name]; ok {
			prev = &row
			if row.AuthMode != spec.AuthMode || row.SignatureScheme != spec.SignatureScheme {
				return immutable(spec.Name)
			}
		}
		reg := store.WebhookRegistration{
			FactoryID:        factoryID,
			Name:             spec.Name,
			AuthMode:         spec.AuthMode,
			SignatureScheme:  spec.SignatureScheme,
			SecretRef:        spec.SecretName,
			DeliveryIDHeader: spec.DeliveryIDHeader,
			Enabled:          spec.Enabled,
		}
		if prev != nil {
			reg.UID = prev.UID
		}
		if authMode(spec.AuthMode) == authURLToken {
			sum, err := urlTokenHash(ctx, secrets, spec, prev)
			if err != nil {
				return err
			}
			reg.URLTokenHash = sum
		}
		out = append(out, reg)
	}
	if err := sync.ApplyWebhooks(ctx, factoryID, out); err != nil {
		if errors.Is(err, store.ErrImmutable) {
			return immutable("")
		}
		return fmt.Errorf("webhooks: apply registrations: %w", err)
	}
	return nil
}

func immutable(name string) error {
	return &immutableError{name: name}
}

type immutableError struct {
	name string
}

func (e *immutableError) Error() string {
	if e.name == "" {
		return "webhooks: authMode and signatureScheme are immutable; " + ImmutableRule
	}
	return fmt.Sprintf("webhooks: webhook %q: changing authMode or signatureScheme is not allowed; %s", e.name, ImmutableRule)
}

func (e *immutableError) Unwrap() error { return store.ErrImmutable }

func urlTokenHash(ctx context.Context, secrets SecretSource, spec Spec, prev *store.WebhookRegistration) ([]byte, error) {
	var previous []byte
	if prev != nil {
		previous = bytes.Clone(prev.URLTokenHash)
	}
	if secrets == nil {
		return keepHash(spec, previous)
	}
	value, err := secrets.Secret(ctx, spec.SecretName)
	if err != nil || value == "" {
		return keepHash(spec, previous)
	}
	if !ValidURLToken(value) {
		return nil, fmt.Errorf("webhooks: webhook %q: url_token secret must use charset A-Za-z0-9-._~", spec.Name)
	}
	sum := sha256.Sum256([]byte(value))
	return bytes.Clone(sum[:]), nil
}

func keepHash(spec Spec, previous []byte) ([]byte, error) {
	if !spec.Enabled {
		return previous, nil
	}
	return nil, fmt.Errorf("webhooks: webhook %q: url_token secret %q is not available", spec.Name, spec.SecretName)
}
