package ingress

import (
	"context"
	"errors"
)

// ErrIgnored marks a claimed delivery ignored.
// A normalizer that returns zero events is the usual reason.
var ErrIgnored = errors.New("ingress: ignored")

// Store is the delivery queue the dispatcher polls.
//
// ClaimReceived returns up to limit rows with status received. The
// implementation must take them with SELECT ... FOR UPDATE SKIP LOCKED so
// two dispatchers never observe the same row. Each returned row stays
// exclusive until MarkDelivery. MarkDelivery(id, StatusReceived) releases
// the claim without treating the delivery as finished.
type Store interface {
	ClaimReceived(ctx context.Context, limit int) ([]Delivery, error)
	MarkDelivery(ctx context.Context, id, status string) error
}

// HandleFunc matches one delivery. matched is false when nothing matched.
// ErrIgnored records status ignored. Any other error records status failed.
type HandleFunc func(ctx context.Context, d Delivery) (matched bool, err error)

// Dispatcher moves received deliveries off the request path.
type Dispatcher struct {
	Store  Store
	Handle HandleFunc
	Limit  int
}

// Poll claims one batch and marks each row. It returns how many rows were
// claimed. A handle or mark error is joined into the returned error after
// the rest of the batch is marked, so one failure does not skip the others.
func (d *Dispatcher) Poll(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if d == nil || d.Store == nil || d.Handle == nil {
		return 0, errors.New("ingress: dispatcher is not configured")
	}
	limit := d.Limit
	if limit <= 0 {
		limit = 32
	}
	batch, err := d.Store.ClaimReceived(ctx, limit)
	if err != nil {
		return 0, err
	}
	var errs []error
	markCtx := context.WithoutCancel(ctx)
	for i, delivery := range batch {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			errs = append(errs, releaseClaims(markCtx, d.Store, batch[i:])...)
			break
		}
		status, callErr := d.outcome(ctx, delivery)
		if callErr != nil && !errors.Is(callErr, ErrIgnored) {
			errs = append(errs, callErr)
		}
		if err := d.Store.MarkDelivery(markCtx, delivery.ID, status); err != nil {
			errs = append(errs, err)
		}
	}
	return len(batch), errors.Join(errs...)
}

func (d *Dispatcher) outcome(ctx context.Context, delivery Delivery) (string, error) {
	matched, err := d.Handle(ctx, delivery)
	switch {
	case errors.Is(err, ErrIgnored):
		return StatusIgnored, err
	case err != nil:
		return StatusFailed, err
	case matched:
		return StatusMatched, nil
	default:
		return StatusUnmatched, nil
	}
}

func releaseClaims(ctx context.Context, store Store, batch []Delivery) []error {
	var errs []error
	for _, delivery := range batch {
		if err := store.MarkDelivery(ctx, delivery.ID, StatusReceived); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
