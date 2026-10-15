package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/polds/factory/internal/chat/slack"
	"github.com/polds/factory/internal/forge/github"
	"github.com/polds/factory/internal/store"
	"github.com/polds/factory/internal/tracker/linear"
)

// Commenter is the Linear comment surface. *linear.Client implements it.
type Commenter interface {
	CreateComment(ctx context.Context, issueID, markdown string) (string, error)
	UpdateComment(ctx context.Context, commentID, markdown string) error
}

// Config builds a Notifier.
// ExternalURL is FACTORY_EXTERNAL_URL. SlackChannel is integrations[].slack.channel.
// Slack, GitHub, and Linear are omitted when that integration is off.
// Now, Sleep, and Log are optional. Sleep is the backoff and coalesce wait.
// Formatting or logging a Config does not walk the client values.
type Config struct {
	ExternalURL  string
	SlackChannel string
	Slack        slack.Poster
	GitHub       github.App
	Linear       Commenter
	Threads      Threads
	Refs         Refs
	Now          func() time.Time
	Sleep        func(context.Context, time.Duration) error
	Log          *slog.Logger
}

// Format prints which providers are set. It does not walk client values.
func (c Config) Format(f fmt.State, verb rune) {
	_, _ = io.WriteString(f, c.summary(verb == 'v' && f.Flag('#')))
}

// String is the redacted config description.
func (c Config) String() string { return c.summary(false) }

// GoString is the redacted Go-syntax config description.
func (c Config) GoString() string { return c.summary(true) }

// LogValue is the redacted slog value.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("slack", c.Slack != nil),
		slog.Bool("github", c.GitHub != nil),
		slog.Bool("linear", c.Linear != nil),
	)
}

func (c Config) summary(sharp bool) string {
	if sharp {
		return fmt.Sprintf("outbound.Config{slack:%t, github:%t, linear:%t}", c.Slack != nil, c.GitHub != nil, c.Linear != nil)
	}
	return fmt.Sprintf("outbound.Config{slack:%t github:%t linear:%t}", c.Slack != nil, c.GitHub != nil, c.Linear != nil)
}

// Notifier delivers work-item events to the configured providers.
// Notify is safe for concurrent use. Close cancels a coalesced edit that
// has not been sent yet.
type Notifier struct {
	externalURL  string
	slackChannel string
	slack        slack.Poster
	github       github.App
	linear       Commenter
	threads      Threads
	refs         Refs
	nowFn        func() time.Time
	sleepFn      func(context.Context, time.Duration) error
	log          *slog.Logger

	rootCtx context.Context
	stop    context.CancelFunc

	mu             sync.Mutex
	idle           sync.Cond
	locks          map[string]*sync.Mutex
	slots          map[string]*editSlot
	waitingFlushes int
}

type editSlot struct {
	last    time.Time
	pending func(context.Context) error
	waiting bool
}

// New returns a notifier that stores threads through cfg.Threads.
func New(cfg Config) (*Notifier, error) {
	if cfg.Threads == nil {
		return nil, errors.New("outbound: thread store is required")
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	root, stop := context.WithCancel(context.Background())
	n := &Notifier{
		externalURL:  cfg.ExternalURL,
		slackChannel: cfg.SlackChannel,
		slack:        cfg.Slack,
		github:       cfg.GitHub,
		linear:       cfg.Linear,
		threads:      cfg.Threads,
		refs:         cfg.Refs,
		nowFn:        cfg.Now,
		sleepFn:      cfg.Sleep,
		log:          log,
		rootCtx:      root,
		stop:         stop,
		locks:        map[string]*sync.Mutex{},
		slots:        map[string]*editSlot{},
	}
	n.idle.L = &n.mu
	return n, nil
}

// Close cancels coalesced edits that are still waiting.
func (n *Notifier) Close() error {
	if n != nil && n.stop != nil {
		n.stop()
	}
	return nil
}

// Settle waits until coalesced edits scheduled before this call have finished.
func (n *Notifier) Settle() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for n.waitingFlushes > 0 {
		n.idle.Wait()
	}
}

// Notify delivers notice. Provider failures are logged and returned as nil.
// A canceled ctx is returned so the caller can stop.
func (n *Notifier) Notify(ctx context.Context, notice Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := notice.valid(); err != nil {
		n.log.Warn("outbound notice dropped", "error", err.Error())
		return nil
	}
	var canceled error
	if err := n.deliverSlack(ctx, notice); err != nil {
		canceled = keepCancel(canceled, err)
	}
	if err := n.deliverGitHub(ctx, notice); err != nil {
		canceled = keepCancel(canceled, err)
	}
	if err := n.deliverLinear(ctx, notice); err != nil {
		canceled = keepCancel(canceled, err)
	}
	return canceled
}

func keepCancel(prev, err error) error {
	if prev != nil {
		return prev
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// Format prints which providers are configured. It does not walk client values.
func (n *Notifier) Format(f fmt.State, verb rune) {
	if n == nil {
		_, _ = io.WriteString(f, "<nil>")
		return
	}
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, n.GoString())
		return
	}
	_, _ = io.WriteString(f, n.String())
}

// String is the redacted notifier description.
func (n *Notifier) String() string {
	if n == nil {
		return "<nil>"
	}
	return fmt.Sprintf("outbound.Notifier{slack:%t github:%t linear:%t}", n.slack != nil, n.github != nil, n.linear != nil)
}

// GoString is the redacted Go-syntax notifier description.
func (n *Notifier) GoString() string {
	if n == nil {
		return "(*outbound.Notifier)(nil)"
	}
	return fmt.Sprintf("&outbound.Notifier{slack:%t, github:%t, linear:%t}", n.slack != nil, n.github != nil, n.linear != nil)
}

// LogValue is the redacted slog value. Clients and tokens are absent.
func (n *Notifier) LogValue() slog.Value {
	if n == nil {
		return slog.StringValue("<nil>")
	}
	return slog.GroupValue(
		slog.Bool("slack", n.slack != nil),
		slog.Bool("github", n.github != nil),
		slog.Bool("linear", n.linear != nil),
	)
}

func (n *Notifier) now() time.Time {
	if n.nowFn != nil {
		return n.nowFn()
	}
	return time.Now()
}

func (n *Notifier) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if n.sleepFn != nil {
		return n.sleepFn(ctx, d)
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

func (n *Notifier) lock(key string) func() {
	n.mu.Lock()
	mu := n.locks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		n.locks[key] = mu
	}
	n.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (n *Notifier) load(ctx context.Context, workItemID, provider string) (Thread, error) {
	row, err := n.threads.GetOutboundThread(ctx, workItemID, provider)
	if errors.Is(err, store.ErrNotFound) {
		return Thread{WorkItemID: workItemID, Provider: provider}, nil
	}
	if err != nil {
		return Thread{}, err
	}
	return fromStore(row), nil
}

func (n *Notifier) save(ctx context.Context, t Thread) error {
	return n.retry(ctx, func(ctx context.Context) error {
		_, err := n.threads.UpsertOutboundThread(ctx, toStore(t))
		return err
	})
}

func (n *Notifier) link(runID string) string {
	return runLink(n.externalURL, runID)
}

var _ Commenter = (*linear.Client)(nil)
