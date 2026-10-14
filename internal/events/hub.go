package events

import "sync"

// Hub fans out "the log grew" signals for a run. Publish never blocks on a
// subscriber: each subscription is a buffer of one, and a second signal while
// that buffer is full is dropped. The subscriber closes the gap by reading
// the log from the last seq it sent.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// NewHub returns an idle hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe registers a coalesced wakeup for runID. The returned func removes
// the registration and may be called more than once.
func (h *Hub) Subscribe(runID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[string]map[chan struct{}]struct{})
	}
	set := h.subs[runID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		h.subs[runID] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			set := h.subs[runID]
			if set == nil {
				return
			}
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, runID)
			}
		})
	}
}

// Publish wakes current subscribers of runID. It does not wait for them to
// read, and it is a no-op when nobody is listening.
func (h *Hub) Publish(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[runID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Compile-time check that Hub is the Notifier the handler consumes.
var _ Notifier = (*Hub)(nil)
