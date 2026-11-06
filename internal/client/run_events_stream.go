package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// defaultStreamIdle is the no-byte deadline for Watch and StreamRunEvents.
// factoryd writes an SSE comment every 15s (internal/events heartbeatInterval),
// so 60s is four missed heartbeats.
const defaultStreamIdle = 60 * time.Second

// ErrStreamSettled ends Watch and StreamRunEvents after OnIdleReconnect
// decides the run is already terminal. The stream returns success.
var ErrStreamSettled = errors.New("run stream settled")

// errStreamIdle is the read error of a connection closed because no bytes
// arrived before the idle deadline. Both callers reconnect. Other read
// errors keep the caller's existing policy.
var errStreamIdle = errors.New("run event stream idle")

// eventCursor is the resume and duplicate state shared by Watch and
// StreamRunEvents. accept reports whether ev should be delivered.
type eventCursor struct {
	lastID  string
	seq     int64
	haveSeq bool
	seen    map[string]struct{}
}

func (cur *eventCursor) accept(ev Event) bool {
	if ev.ID == "" {
		return true
	}
	if cur.seen == nil {
		cur.seen = map[string]struct{}{}
	}
	if _, ok := cur.seen[ev.ID]; ok {
		return false
	}
	cur.seen[ev.ID] = struct{}{}
	cur.lastID = ev.ID
	n, err := strconv.ParseInt(ev.ID, 10, 64)
	if err == nil {
		cur.seq = n
		cur.haveSeq = true
	}
	return true
}

// streamTransportError is a dropped connection. Callers retry it.
// Construction errors and non-200 problem errors are not this type.
type streamTransportError struct {
	err error
}

func (e *streamTransportError) Error() string { return e.err.Error() }

func (e *streamTransportError) Unwrap() error { return e.err }

// openRunEvents sends the run-events GET. A 200 body counts every received
// byte, including SSE comments, toward the idle deadline.
func (c *Client) openRunEvents(ctx context.Context, runID string, cur *eventCursor) (*http.Response, error) {
	q := url.Values{}
	if cur != nil && cur.haveSeq {
		q.Set("from_seq", strconv.FormatInt(cur.seq, 10))
	}
	endpoint, err := c.endpoint("/api/v1/agent/runs/"+url.PathEscape(runID)+"/events", q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	if cur != nil && cur.lastID != "" {
		req.Header.Set("Last-Event-ID", cur.lastID)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, &streamTransportError{err: err}
	}
	if resp.StatusCode != http.StatusOK {
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		return nil, problemError(resp.StatusCode, raw)
	}
	resp.Body = newIdleReadCloser(resp.Body, c.streamIdle())
	return resp, nil
}

func (c *Client) streamIdle() time.Duration {
	if c != nil && c.StreamIdle > 0 {
		return c.StreamIdle
	}
	return defaultStreamIdle
}

// notifyIdle runs only after an idle close. A nil hook continues the reconnect.
func (c *Client) notifyIdle(ctx context.Context, runID string) error {
	if c == nil || c.OnIdleReconnect == nil {
		return nil
	}
	return c.OnIdleReconnect(ctx, runID)
}

// idleReadCloser closes rc when no Read returns a byte for d.
// Closing the wrapper stops the deadline. It does not poll while bytes flow:
// each non-empty Read resets one timer.
type idleReadCloser struct {
	rc     io.ReadCloser
	d      time.Duration
	mu     sync.Mutex
	timer  *time.Timer
	fired  bool
	closed bool
	once   sync.Once
}

func newIdleReadCloser(rc io.ReadCloser, d time.Duration) io.ReadCloser {
	b := &idleReadCloser{rc: rc, d: d}
	b.timer = time.AfterFunc(d, b.fire)
	return b
}

func (b *idleReadCloser) fire() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.fired = true
	b.mu.Unlock()
	_ = b.closeUnderlying()
}

func (b *idleReadCloser) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	fired := b.fired
	if n > 0 && !fired && !b.closed {
		b.timer.Reset(b.d)
	}
	b.mu.Unlock()
	return n, idleReadErr(n, fired, err)
}

// idleReadErr turns the close that the deadline provoked into errStreamIdle
// once the bytes from that read are delivered. A later read then reconnects.
func idleReadErr(n int, fired bool, err error) error {
	if (fired && n == 0) || errors.Is(err, errStreamIdle) {
		return errStreamIdle
	}
	if !fired || err == nil {
		return err
	}
	return nil
}

func (b *idleReadCloser) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.timer.Stop()
	return b.closeUnderlying()
}

func (b *idleReadCloser) closeUnderlying() error {
	var err error
	b.once.Do(func() {
		err = b.rc.Close()
	})
	return err
}
