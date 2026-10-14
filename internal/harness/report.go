package harness

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	reportBatchMax    = 64
	reportBatchWait   = 500 * time.Millisecond
	reportRetryBudget = 30 * time.Second
	reportFlushLimit  = 10 * time.Second
	reportBackoffMax  = 5 * time.Second
	reportAttemptMax  = 10 * time.Second
)

// reporterConfig overrides timers in tests. Zero batchWait sends a partial
// batch immediately; startEventReporter sets the production 500ms wait.
type reporterConfig struct {
	now          func() time.Time
	sleep        func(context.Context, time.Duration) error
	retryBudget  time.Duration
	batchWait    time.Duration
	flushTimeout time.Duration
	client       *http.Client
}

// eventReporter batches NDJSON lines and POSTs them to the control plane.
// push never waits on the network. A batch is abandoned after its retry
// budget so one dead endpoint cannot hold later events.
type eventReporter struct {
	url          string
	token        string
	client       *http.Client
	now          func() time.Time
	sleep        func(context.Context, time.Duration) error
	retryBudget  time.Duration
	batchWait    time.Duration
	flushTimeout time.Duration

	base   context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	pending  [][]byte
	next     int
	closed   bool
	deadline time.Time

	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	flushOnce sync.Once
}

// startEventReporter returns nil unless both push variables are set, which
// leaves the harness writing events.ndjson only.
func startEventReporter(env []string) *eventReporter {
	url := strings.TrimSpace(lookupEnv(env, "FACTORY_EVENTS_URL"))
	token := lookupEnv(env, "FACTORY_RUN_TOKEN")
	if url == "" || token == "" {
		return nil
	}
	return newEventReporter(url, token, reporterConfig{
		retryBudget:  reportRetryBudget,
		batchWait:    reportBatchWait,
		flushTimeout: reportFlushLimit,
	})
}

func newEventReporter(url, token string, cfg reporterConfig) *eventReporter {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.retryBudget == 0 {
		cfg.retryBudget = reportRetryBudget
	}
	if cfg.flushTimeout == 0 {
		cfg.flushTimeout = reportFlushLimit
	}
	client := cfg.client
	if client == nil {
		client = &http.Client{CheckRedirect: refuseRedirect}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &eventReporter{
		url:          url,
		token:        token,
		client:       client,
		now:          cfg.now,
		sleep:        cfg.sleep,
		retryBudget:  cfg.retryBudget,
		batchWait:    cfg.batchWait,
		flushTimeout: cfg.flushTimeout,
		base:         ctx,
		cancel:       cancel,
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *eventReporter) push(line []byte) {
	if r == nil || len(line) == 0 {
		return
	}
	cp := make([]byte, len(line))
	copy(cp, line)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.pending = append(r.pending, cp)
	r.mu.Unlock()
	r.notify()
}

// flush sends any queued lines and returns when the sender stops or the
// flush limit elapses. It does not report delivery: the NDJSON file is the
// record either way.
func (r *eventReporter) flush() {
	if r == nil {
		return
	}
	var deadline time.Time
	r.flushOnce.Do(func() {
		deadline = time.Now().Add(r.flushTimeout)
		r.mu.Lock()
		r.closed = true
		r.deadline = deadline
		r.mu.Unlock()
		close(r.stop)
		r.notify()
	})
	if deadline.IsZero() {
		r.mu.Lock()
		deadline = r.deadline
		r.mu.Unlock()
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
		r.cancel()
		select {
		case <-r.done:
		case <-time.After(time.Second):
		}
	}
}

func (r *eventReporter) notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *eventReporter) loop() {
	defer close(r.done)
	var batchStart time.Time
	for {
		if r.base.Err() != nil {
			return
		}
		r.mu.Lock()
		n := len(r.pending)
		closed := r.closed
		start := r.next
		r.mu.Unlock()

		if n == 0 {
			if closed {
				return
			}
			batchStart = time.Time{}
			select {
			case <-r.wake:
			case <-r.stop:
			case <-r.base.Done():
				return
			}
			continue
		}

		if n < reportBatchMax && !closed && r.batchWait > 0 {
			if batchStart.IsZero() {
				batchStart = r.clock()
			}
			wait := r.batchWait - r.clock().Sub(batchStart)
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-r.wake:
					timer.Stop()
				case <-timer.C:
				case <-r.stop:
					timer.Stop()
				case <-r.base.Done():
					timer.Stop()
					return
				}
				continue
			}
		}

		take := n
		if take > reportBatchMax {
			take = reportBatchMax
		}
		r.mu.Lock()
		batch := make([][]byte, take)
		copy(batch, r.pending[:take])
		r.mu.Unlock()

		ok := r.sendBatch(start, bytes.Join(batch, nil))
		r.mu.Lock()
		r.pending = append([][]byte(nil), r.pending[take:]...)
		r.next += take
		closed = r.closed
		deadline := r.deadline
		r.mu.Unlock()
		batchStart = time.Time{}
		if r.base.Err() != nil {
			return
		}
		// A rejected or abandoned batch does not cancel the ones behind it.
		// Stop only when the flush window itself is over.
		if !ok && closed && !deadline.IsZero() && !time.Now().Before(deadline) {
			return
		}
	}
}

func (r *eventReporter) sendBatch(start int, payload []byte) bool {
	retryDeadline := r.clock().Add(r.retryBudget)
	backoff := 100 * time.Millisecond
	for {
		if r.base.Err() != nil || !r.clock().Before(retryDeadline) {
			return false
		}
		flushDeadline, flushing := r.flushDeadline()
		if flushing && !time.Now().Before(flushDeadline) {
			return false
		}

		attempt := reportAttemptMax
		if flushing {
			if remain := time.Until(flushDeadline); remain < attempt {
				attempt = remain
			}
		}
		if attempt <= 0 {
			return false
		}
		ctx, cancel := context.WithTimeout(r.base, attempt)
		status, err := r.post(ctx, start, payload)
		cancel()
		if err == nil && status >= 200 && status < 300 {
			return true
		}
		if err == nil && status > 0 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout {
			return false
		}
		if r.base.Err() != nil {
			return false
		}

		remain := retryDeadline.Sub(r.clock())
		if flushing {
			if wall := time.Until(flushDeadline); wall < remain {
				remain = wall
			}
		}
		if remain <= 0 {
			return false
		}
		step := backoff
		if step > remain {
			step = remain
		}
		if err := r.wait(r.base, step); err != nil {
			return false
		}
		if backoff < reportBackoffMax {
			backoff *= 2
		}
	}
}

func (r *eventReporter) post(ctx context.Context, start int, payload []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("X-Factory-Event-Index", strconv.Itoa(start))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}

func (r *eventReporter) wait(ctx context.Context, d time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *eventReporter) clock() time.Time { return r.now() }

func (r *eventReporter) flushDeadline() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed || r.deadline.IsZero() {
		return time.Time{}, false
	}
	return r.deadline, true
}

// reportWriter tees each NDJSON line to the reporter. Log stays unaware of
// the network; a push failure cannot change what the file receives.
type reportWriter struct {
	w    io.Writer
	push func([]byte)
}

func (w reportWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil || w.push == nil || n == 0 {
		return n, err
	}
	w.push(p[:n])
	return n, nil
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
