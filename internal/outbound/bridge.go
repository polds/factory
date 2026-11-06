package outbound

import "sync"

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
// enabled and sendAuthorized are set. Sends are in-memory; this type has no
// Slack, Linear, or GitHub client.
type Bridge struct {
	mu             sync.Mutex
	enabled        bool
	sendAuthorized bool
	outage         bool
	sendCount      int
	queued         []BridgeNotice
	seen           map[string]struct{}
	bindings       map[string]struct{}
}

// NewBridge returns a disabled bridge. sendCount starts at 0.
func NewBridge() *Bridge {
	return &Bridge{
		seen:     map[string]struct{}{},
		bindings: map[string]struct{}{},
	}
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

// SendCount is the number of authorized sends.
func (b *Bridge) SendCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendCount
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

// Enqueue records a report. Outage, echo, and replay do not block execution.
func (b *Bridge) Enqueue(n BridgeNotice) BridgeResult {
	if b == nil {
		return BridgeResult{BlocksExecution: false}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen == nil {
		b.seen = map[string]struct{}{}
	}
	if b.bindings == nil {
		b.bindings = map[string]struct{}{}
	}
	res := BridgeResult{BlocksExecution: false, SendCount: b.sendCount}
	if n.Origin == BridgeOwner || (n.Binding != "" && hasKey(b.bindings, n.Binding)) {
		res.Suppressed = "echo"
		res.EchoSuppressed = true
		return res
	}
	if n.IdempotencyKey != "" && hasKey(b.seen, n.IdempotencyKey) {
		res.Suppressed = "replay"
		res.ReplaySuppressed = true
		return res
	}
	b.queued = append(b.queued, n)
	if n.IdempotencyKey != "" {
		b.seen[n.IdempotencyKey] = struct{}{}
		b.bindings[n.IdempotencyKey] = struct{}{}
		res.Binding = n.IdempotencyKey
	}
	res.Queued = true
	res.OutageQueues = "reporting"
	if b.outage || !b.enabled || !b.sendAuthorized {
		return res
	}
	b.sendCount++
	res.Sent = true
	res.SendCount = b.sendCount
	return res
}

func hasKey(m map[string]struct{}, key string) bool {
	_, ok := m[key]
	return ok
}
