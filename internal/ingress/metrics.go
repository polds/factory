package ingress

import "sync/atomic"

// Metrics counts ingress outcomes that are not persisted.
// The handler never logs secrets; failed verifies are counted here only.
// Do not copy a Metrics value.
type Metrics struct {
	verifyFailed atomic.Int64
	tooLarge     atomic.Int64
}

// VerifyFailed is the number of requests rejected before parse.
func (m *Metrics) VerifyFailed() int64 {
	if m == nil {
		return 0
	}
	return m.verifyFailed.Load()
}

// TooLarge is the number of requests rejected for exceeding MaxBody.
func (m *Metrics) TooLarge() int64 {
	if m == nil {
		return 0
	}
	return m.tooLarge.Load()
}

func (m *Metrics) addVerifyFailed() {
	if m != nil {
		m.verifyFailed.Add(1)
	}
}

func (m *Metrics) addTooLarge() {
	if m != nil {
		m.tooLarge.Add(1)
	}
}
