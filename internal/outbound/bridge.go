package outbound

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/store"
)

// BridgeOwner is factory-outbound-bridge, the only native publication owner.
const BridgeOwner = "factory-outbound-bridge"

// BridgeNotice is one native report waiting on the bridge.
// Summary is report text, never a credential.
type BridgeNotice struct {
	IdempotencyKey string
	Origin         string
	Binding        string
	TaskRef        string
	Summary        string
}

// BridgeResult is the enqueue outcome. BlocksExecution is always false.
type BridgeResult struct {
	Queued           bool
	Sent             bool
	Suppressed       string
	BlocksExecution  bool
	SendCount        int
	Binding          string
	OutageQueues     string
	EchoSuppressed   bool
	ReplaySuppressed bool
}

// Bridge is the optional native reporting owner. It is disabled unless both
// enabled and sendAuthorized are set. The queue, bindings, and idempotency
// keys are rows in Postgres. This type has no Slack, Linear, or GitHub client.
type Bridge struct {
	mu             sync.Mutex
	store          *store.Store
	enabled        bool
	sendAuthorized bool
	outage         bool
}

// NewBridge returns a disabled bridge. Bind a store before enqueueing
// notices that must survive a restart. sendCount starts at 0.
func NewBridge() *Bridge {
	return &Bridge{}
}

// BindStore keeps the outbox, bindings, and replay keys in st.
func (b *Bridge) BindStore(st *store.Store) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.store = st
	b.mu.Unlock()
}

// Owners is the single publication owner.
func (b *Bridge) Owners() []string {
	return []string{BridgeOwner}
}

// Enabled reports the default-off switch.
func (b *Bridge) Enabled() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled
}

// SendAuthorized reports whether a send policy is on.
func (b *Bridge) SendAuthorized() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendAuthorized
}

// SendCount is the number of authorized sends recorded in the store.
func (b *Bridge) SendCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	st := b.store
	b.mu.Unlock()
	if st == nil {
		return 0
	}
	n, err := st.BridgeSendCount(context.Background())
	if err != nil {
		return 0
	}
	return n
}

// SetEnabled turns the owner on. Sends still require SetSendAuthorized.
func (b *Bridge) SetEnabled(v bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.enabled = v
	b.mu.Unlock()
}

// SetSendAuthorized records an explicit send policy.
func (b *Bridge) SetSendAuthorized(v bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.sendAuthorized = v
	b.mu.Unlock()
}

// SetOutage queues reporting and does not change sendCount.
func (b *Bridge) SetOutage(v bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.outage = v
	b.mu.Unlock()
}

// Policy is the process-local send switch. The store does not keep it.
func (b *Bridge) Policy() store.BridgePolicy {
	if b == nil {
		return store.BridgePolicy{Owner: BridgeOwner}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return store.BridgePolicy{
		Enabled:    b.enabled,
		Authorized: b.sendAuthorized,
		Outage:     b.outage,
		Owner:      BridgeOwner,
	}
}

// Enqueue records a report. Outage, echo, and replay do not block execution.
// A nil store queues nothing durable and does not send.
func (b *Bridge) Enqueue(n BridgeNotice) BridgeResult {
	if b == nil {
		return BridgeResult{BlocksExecution: false}
	}
	b.mu.Lock()
	st := b.store
	pol := store.BridgePolicy{
		Enabled:    b.enabled,
		Authorized: b.sendAuthorized,
		Outage:     b.outage,
		Owner:      BridgeOwner,
	}
	b.mu.Unlock()
	if st == nil {
		return BridgeResult{BlocksExecution: false}
	}
	out, err := st.CommitBridge(context.Background(), noticeRow(n), pol)
	if err != nil {
		return BridgeResult{BlocksExecution: false}
	}
	return fromOutcome(out)
}

// CommitTx records a report on an open transaction. The caller commits.
// A database error is returned. Outage and replay are not errors.
func (b *Bridge) CommitTx(ctx context.Context, tx pgx.Tx, n BridgeNotice) (BridgeResult, error) {
	if b == nil {
		return BridgeResult{BlocksExecution: false}, nil
	}
	out, err := store.CommitBridgeTx(ctx, tx, noticeRow(n), b.Policy())
	if err != nil {
		return BridgeResult{BlocksExecution: false}, err
	}
	return fromOutcome(out), nil
}

func noticeRow(n BridgeNotice) store.BridgeNotice {
	return store.BridgeNotice{
		IdempotencyKey: n.IdempotencyKey,
		Origin:         n.Origin,
		Binding:        n.Binding,
		TaskRef:        n.TaskRef,
		Summary:        n.Summary,
	}
}

func fromOutcome(out store.BridgeOutcome) BridgeResult {
	res := BridgeResult{
		Queued:          out.Queued,
		Sent:            out.Sent,
		Suppressed:      out.Suppressed,
		BlocksExecution: false,
		SendCount:       out.SendCount,
		Binding:         out.Binding,
	}
	switch out.Suppressed {
	case "echo":
		res.EchoSuppressed = true
	case "replay":
		res.ReplaySuppressed = true
	}
	if out.Queued {
		res.OutageQueues = "reporting"
	}
	return res
}
