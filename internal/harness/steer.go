package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	steerInterrupt = "interrupt"
	steerFollowup  = "followup"

	// The server holds a long-poll for at most 25s. The client waits a
	// little longer so a full hold still returns a body, and shuts down
	// immediately when the run context is canceled.
	steerPollTimeout = 30 * time.Second
	steerBackoffMin  = 100 * time.Millisecond
	steerBackoffMax  = 5 * time.Second
	// An empty poll that the server did not hold must not busy-loop.
	steerEmptyFloor = 50 * time.Millisecond
)

// secretValue is a run token. Formatting, JSON, and slog redact it.
// The raw value is available only through reveal.
type secretValue string

func (secretValue) String() string               { return redacted }
func (secretValue) GoString() string             { return redacted }
func (secretValue) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, redacted) }
func (secretValue) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (secretValue) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (secretValue) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (s secretValue) reveal() string             { return string(s) }

// steerEndpoint is the long-poll URL for this run.
//
// FACTORY_EVENTS_URL is the ingest route
//
//	POST {origin}/api/v1/agent/runs/{id}/events:ingest
//
// Steering uses the sibling route on that same origin and path prefix
//
//	GET {origin}/api/v1/agent/runs/{id}/steer?after={steerID}
//
// The after cursor is the acknowledgement. The next poll's after is the
// last steer id this process accepted, which is how the control plane
// marks that id delivered. There is no separate ack call and no extra
// environment variable.
func steerEndpoint(eventsURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(eventsURL))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("events URL %q is not absolute", eventsURL)
	}
	path := strings.TrimRight(u.Path, "/")
	const suffix = "/events:ingest"
	if !strings.HasSuffix(path, suffix) {
		return "", fmt.Errorf("events URL %q has no %s suffix", eventsURL, suffix)
	}
	u.Path = strings.TrimSuffix(path, suffix) + "/steer"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

type steerMsg struct {
	ID   string
	Kind string
	Text string
}

// steerHub is the poller's handoff to the agent loop. deliver never blocks.
// Each id is accepted at most once, including when a reconnect replays it.
type steerHub struct {
	mu         sync.Mutex
	followups  []steerMsg
	interrupts []steerMsg
	seen       map[string]struct{}
	wake       chan struct{}
	turnCancel context.CancelFunc
	generation int
}

func newSteerHub() *steerHub {
	return &steerHub{
		seen: map[string]struct{}{},
		wake: make(chan struct{}, 1),
	}
}

// deliver accepts an id once. It reports whether this call was the first
// sighting, so a reconnect that replays the same batch does not spin.
func (h *steerHub) deliver(m steerMsg) bool {
	if h == nil || m.ID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.seen[m.ID]; ok {
		return false
	}
	h.seen[m.ID] = struct{}{}
	switch m.Kind {
	case steerInterrupt:
		// No turn is in flight: acknowledge the id and drop it. The next
		// turn must not be canceled by an interrupt that already missed.
		if h.turnCancel == nil {
			return true
		}
		h.interrupts = append(h.interrupts, m)
		h.turnCancel()
	case steerFollowup:
		h.followups = append(h.followups, m)
		h.notifyLocked()
	default:
		// The id is still acknowledged by the poller. An unknown kind
		// does not enter the conversation.
	}
	return true
}

func (h *steerHub) notifyLocked() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// bindTurn registers the in-flight turn. An interrupt that is already
// queued cancels it before the model call or tool starts.
func (h *steerHub) bindTurn(cancel context.CancelFunc) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	h.turnCancel = cancel
	if len(h.interrupts) > 0 {
		cancel()
	}
	return h.generation
}

func (h *steerHub) unbindTurn(gen int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.generation == gen {
		h.turnCancel = nil
	}
}

func (h *steerHub) takeFollowup() (steerMsg, bool) {
	if h == nil {
		return steerMsg{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.followups) == 0 {
		return steerMsg{}, false
	}
	m := h.followups[0]
	h.followups = h.followups[1:]
	return m, true
}

func (h *steerHub) takeInterrupt() (steerMsg, bool) {
	if h == nil {
		return steerMsg{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.interrupts) == 0 {
		return steerMsg{}, false
	}
	m := h.interrupts[0]
	h.interrupts = h.interrupts[1:]
	return m, true
}

type steerPoller struct {
	endpoint string
	token    secretValue
	log      *Log
	hub      *steerHub
	client   *http.Client
}

func (p steerPoller) String() string               { return "steerPoller" }
func (p steerPoller) GoString() string             { return "steerPoller" }
func (p steerPoller) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "steerPoller") }
func (p steerPoller) LogValue() slog.Value         { return slog.StringValue("steerPoller") }
func (p steerPoller) MarshalJSON() ([]byte, error) { return []byte(`"steerPoller"`), nil }

// startSteerPoller long-polls until stop is called or ctx is canceled.
// stop waits for the goroutine so it cannot emit after the terminal event.
func startSteerPoller(ctx context.Context, endpoint string, token secretValue, log *Log, hub *steerHub) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p := &steerPoller{
		endpoint: endpoint,
		token:    token,
		log:      log,
		hub:      hub,
		client:   &http.Client{CheckRedirect: refuseRedirect},
	}
	go func() {
		defer close(done)
		p.loop(ctx)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		})
	}
}

func (p *steerPoller) loop(ctx context.Context) {
	backoff := steerBackoffMin
	var after string
	announced := false
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		items, status, err := p.poll(ctx, after)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextSteerBackoff(backoff)
			continue
		}
		if status == http.StatusNotFound || status == http.StatusNotImplemented {
			// A factoryd without steering. Stop quietly and leave the
			// capability unannounced so the run looks like any other.
			return
		}
		if status != http.StatusOK {
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextSteerBackoff(backoff)
			continue
		}
		backoff = steerBackoffMin
		if !announced {
			if err := p.log.Emit(Event{Type: "run.capabilities", Steer: true}); err == nil {
				announced = true
			}
		}
		fresh := 0
		for _, item := range items {
			if item.ID == "" {
				continue
			}
			if p.hub.deliver(item) {
				fresh++
			}
			// after advances for replays too. The id was accepted on the
			// first sighting; the cursor is what tells the server to stop
			// sending it.
			after = item.ID
		}
		if fresh == 0 && time.Since(started) < steerEmptyFloor {
			if !sleepCtx(ctx, steerEmptyFloor-time.Since(started)) {
				return
			}
		}
	}
}

func nextSteerBackoff(current time.Duration) time.Duration {
	if current >= steerBackoffMax {
		return steerBackoffMax
	}
	next := current * 2
	if next > steerBackoffMax {
		return steerBackoffMax
	}
	return next
}

func (p *steerPoller) poll(ctx context.Context, after string) ([]steerMsg, int, error) {
	u, err := url.Parse(p.endpoint)
	if err != nil {
		return nil, 0, err
	}
	if after != "" {
		q := u.Query()
		q.Set("after", after)
		u.RawQuery = q.Encode()
	}
	reqCtx, cancel := context.WithTimeout(ctx, steerPollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token.reveal())
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, resp.StatusCode, nil
	}
	var raw []struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, resp.StatusCode, err
	}
	out := make([]steerMsg, 0, len(raw))
	for _, item := range raw {
		out = append(out, steerMsg{ID: item.ID, Kind: item.Kind, Text: item.Text})
	}
	return out, resp.StatusCode, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
