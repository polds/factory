package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the Postgres system of record and run queue.
type Store struct {
	pool *pgxpool.Pool
	// pendingLookupHook runs after RequestApproval has read a pending approval
	// id and before it locks that row. Tests use it to commit a decision in
	// that window. Production leaves it nil.
	pendingLookupHook func(context.Context)
	// openGateHook runs inside OpenApprovalGate after the approval row and
	// the work-item update are written and before commit. A non-nil error
	// rolls the transaction back. Production leaves it nil.
	openGateHook func(context.Context) error

	// claims holds one transaction per ClaimReceived batch. The row locks
	// stay until every id in the batch is passed to MarkDelivery.
	claimMu sync.Mutex
	claims  map[string]*deliveryClaim
}

// SetPendingLookupHookForTest sets pendingLookupHook. Pass nil to clear it.
func (s *Store) SetPendingLookupHookForTest(fn func(context.Context)) {
	s.pendingLookupHook = fn
}

// SetOpenGateHookForTest sets openGateHook. Pass nil to clear it.
func (s *Store) SetOpenGateHookForTest(fn func(context.Context) error) {
	s.openGateHook = fn
}

// Open connects to url and checks the connection. Call Migrate before use.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases claim transactions, then the pool. It waits until acquired
// connections are returned.
func (s *Store) Close() {
	s.abortDeliveryClaims()
	s.pool.Close()
}
