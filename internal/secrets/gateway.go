package secrets

import (
	"context"
	"errors"
	"fmt"
)

// ErrGatewayUnavailable means no per-engagement gateway allocation is configured.
// Actual gateway proof is M10-G; this stub fails closed.
var ErrGatewayUnavailable = errors.New("gateway allocation unavailable")

// IsGatewayUnavailable reports gateway stub failures.
func IsGatewayUnavailable(err error) bool {
	return errors.Is(err, ErrGatewayUnavailable)
}

// EngagementAllocation is a reserved per-engagement spend cap at the gateway.
type EngagementAllocation struct {
	EngagementID    string
	GatewayCapCents int
	SharedKey       bool
}

// EngagementAllocator reserves an isolated gateway budget for one engagement.
// INV-GATEWAY remains GAP until M10-G proves a real allocation seam.
type EngagementAllocator interface {
	AllocateEngagement(ctx context.Context, engagementID string, ceilingCents int) (EngagementAllocation, error)
}

// FailClosedAllocator is the default stub: it never claims guaranteesCeiling or paidProbeAllowed.
type FailClosedAllocator struct{}

// AllocateEngagement always fails closed.
func (FailClosedAllocator) AllocateEngagement(ctx context.Context, engagementID string, ceilingCents int) (EngagementAllocation, error) {
	_ = ctx
	_ = engagementID
	_ = ceilingCents
	return EngagementAllocation{}, fmt.Errorf("%w: per-engagement gateway proof is M10-G", ErrGatewayUnavailable)
}

// GuaranteesCeiling reports whether this allocator enforces the engagement ceiling at the gateway.
func (FailClosedAllocator) GuaranteesCeiling() bool { return false }
