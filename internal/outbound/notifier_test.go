package outbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAgent = "implementer"
	testModel = "claude-test"
	testLink  = "https://factory.example/api/v1/agent/runs/run_9"
	testChan  = "C0FACTORY"
)

func baseNotice() Notice {
	return Notice{
		WorkItemID:     "wi_01",
		RunID:          "run_9",
		Agent:          testAgent,
		Model:          testModel,
		Title:          "Fix the parser",
		Repo:           "polds/factory",
		Number:         17,
		HeadSHA:        "abc123",
		SlackTeam:      "T0FACTORY",
		ConversationID: "conv_1",
	}
}

type harness struct {
	clock   *manualClock
	slack   *fakeSlack
	gh      *fakeGH
	linear  *fakeLinear
	threads *memThreads
	refs    *fakeRefs
	log     *bytes.Buffer
}

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock {
	return &manualClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newHarness() *harness {
	return &harness{
		clock:   newManualClock(),
		slack:   newFakeSlack(),
		gh:      &fakeGH{},
		linear:  &fakeLinear{},
		threads: newMemThreads(),
		refs:    &fakeRefs{},
		log:     &bytes.Buffer{},
	}
}

func (h *harness) notifier(t *testing.T, sleep func(context.Context, time.Duration) error) *Notifier {
	t.Helper()
	if sleep == nil {
		sleep = func(context.Context, time.Duration) error { return nil }
	}
	n, err := New(Config{
		ExternalURL:  "https://factory.example",
		SlackChannel: testChan,
		Slack:        h.slack,
		GitHub:       h.gh,
		Linear:       h.linear,
		Threads:      h.threads,
		Refs:         h.refs,
		Now:          h.clock.Now,
		Sleep:        sleep,
		Log:          slog.New(slog.NewTextHandler(h.log, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFiveStageWorkItem(t *testing.T) {
	h := newHarness()
	n := h.notifier(t, nil)
	t.Cleanup(func() { _ = n.Close() })
	events := fiveStage(baseNotice())
	if len(events) != 8 {
		t.Fatalf("events %d", len(events))
	}
	var stages, milestones int
	for _, ev := range events {
		if ev.Kind == KindStageChanged {
			stages++
		}
		if milestone(ev.Kind) {
			milestones++
		}
	}
	if stages != 5 || milestones != 3 {
		t.Fatalf("stages %d milestones %d", stages, milestones)
	}
	for i, ev := range events {
		if i > 0 {
			h.clock.Advance(3 * time.Second)
		}
		if err := n.Notify(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	n.Settle()

	roots, statuses, replies, updates := classifySlack(h.slack.snapshot())
	if roots != 1 || statuses != 1 || replies != milestones || updates != len(events)-1 {
		t.Fatalf("slack roots %d status %d replies %d updates %d", roots, statuses, replies, updates)
	}
	var statusTS string
	for _, msg := range h.slack.snapshot() {
		if msg.channel != testChan {
			t.Fatalf("channel %s", msg.channel)
		}
		assertAttribution(t, msg.text, testLink)
		if msg.update {
			if msg.ts != statusTS {
				t.Fatalf("update ts %s, status %s", msg.ts, statusTS)
			}
			continue
		}
		if msg.thread == "" {
			continue
		}
		if statusTS == "" {
			statusTS = msg.ts
		}
	}

	_, repos, checks, notes := h.gh.snapshot()
	if len(repos) == 0 || repos[0] != "polds/factory" {
		t.Fatalf("repos %#v", repos)
	}
	perms, _, _, _ := h.gh.snapshot()
	if perms["checks"] != "write" || perms["issues"] != "write" || perms["metadata"] != "read" || perms["contents"] != "" {
		t.Fatalf("perms %#v", perms)
	}
	var created, edited int
	for _, check := range checks {
		if check.details != testLink || !strings.Contains(check.summary, testAgent) || !strings.Contains(check.summary, testModel) {
			t.Fatalf("check %#v", check)
		}
		if check.create {
			created++
			if check.sha != "abc123" || check.id != 1 {
				t.Fatalf("created check %#v", check)
			}
			continue
		}
		edited++
		if check.id != 1 {
			t.Fatalf("updated check %#v", check)
		}
	}
	if created != 1 || edited != len(events)-1 {
		t.Fatalf("checks created %d edited %d", created, edited)
	}
	if checks[len(checks)-1].conclusion != "success" || checks[len(checks)-1].status != "completed" {
		t.Fatalf("final check %#v", checks[len(checks)-1])
	}
	var root ghComment
	var mile int
	for _, note := range notes {
		for _, body := range note.bodies {
			assertAttribution(t, body, testLink)
		}
		if note.marker == rootMarker("wi_01") {
			root = note
			continue
		}
		mile++
		if len(note.bodies) != 1 {
			t.Fatalf("milestone edited %#v", note)
		}
	}
	if len(root.bodies) != len(events) || mile != milestones {
		t.Fatalf("root writes %d milestones %d", len(root.bodies), mile)
	}

	slackRow, err := h.threads.GetOutboundThread(context.Background(), "wi_01", ProviderSlack)
	if err != nil || slackRow.RootRef == "" || slackRow.StatusMsgRef == "" || slackRow.Channel != testChan {
		t.Fatalf("slack thread %#v %v", slackRow, err)
	}
	ghRow, err := h.threads.GetOutboundThread(context.Background(), "wi_01", ProviderGitHub)
	if err != nil || ghRow.RootRef != rootMarker("wi_01") || ghRow.StatusMsgRef != "1" || ghRow.Channel != "polds/factory#17" {
		t.Fatalf("github thread %#v %v", ghRow, err)
	}
	refs := h.refs.snapshot()
	wantRef := "slack:T0FACTORY/" + testChan + "/" + slackRow.RootRef
	if len(refs) != 1 || refs[0].ExternalRef != wantRef || refs[0].WorkItemID != "wi_01" || refs[0].ConversationID != "conv_1" {
		t.Fatalf("refs %#v", refs)
	}
	if strings.Contains(h.log.String(), "ghs_") || strings.Contains(h.log.String(), "xox") {
		t.Fatalf("token-like log: %s", h.log.String())
	}
}

func TestRestartReusesStoredThread(t *testing.T) {
	h := newHarness()
	first := h.notifier(t, nil)
	ev := baseNotice()
	ev.Kind = KindStageChanged
	ev.Stage = "triage"
	if err := first.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	first.Settle()
	_ = first.Close()
	before, err := h.threads.GetOutboundThread(context.Background(), "wi_01", ProviderSlack)
	if err != nil {
		t.Fatal(err)
	}

	h.clock.Advance(3 * time.Second)
	second := h.notifier(t, nil)
	t.Cleanup(func() { _ = second.Close() })
	ev.Stage = "planning"
	if err := second.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	second.Settle()

	roots, statuses, replies, updates := classifySlack(h.slack.snapshot())
	if roots != 1 || statuses != 1 || replies != 0 || updates != 1 {
		t.Fatalf("slack roots %d status %d replies %d updates %d", roots, statuses, replies, updates)
	}
	after, err := h.threads.GetOutboundThread(context.Background(), "wi_01", ProviderSlack)
	if err != nil || after.RootRef != before.RootRef || after.StatusMsgRef != before.StatusMsgRef {
		t.Fatalf("before %#v after %#v %v", before, after, err)
	}
	_, _, checks, notes := h.gh.snapshot()
	var created int
	for _, check := range checks {
		if check.create {
			created++
		}
	}
	if created != 1 || len(checks) != 2 {
		t.Fatalf("checks %#v", checks)
	}
	if len(notes) != 1 || notes[0].marker != rootMarker("wi_01") || len(notes[0].bodies) != 2 {
		t.Fatalf("notes %#v", notes)
	}
}

func TestCoalesceStatusEdits(t *testing.T) {
	h := newHarness()
	started := make(chan time.Duration, 1)
	release := make(chan struct{})
	n, err := New(Config{
		ExternalURL:  "https://factory.example",
		SlackChannel: testChan,
		Slack:        h.slack,
		Threads:      h.threads,
		Now:          h.clock.Now,
		Sleep: func(ctx context.Context, d time.Duration) error {
			if d < coalesceWindow {
				return nil
			}
			select {
			case started <- d:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-release:
				h.clock.Advance(d)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Log: slog.New(slog.NewTextHandler(h.log, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		_ = n.Close()
	})
	ev := baseNotice()
	ev.Kind = KindStageChanged
	ev.Stage = "triage"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	ev.Stage = "planning"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	ev.Stage = "building"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		if got != coalesceWindow {
			t.Fatalf("wait %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coalesce did not wait")
	}
	if _, _, _, updates := classifySlack(h.slack.snapshot()); updates != 0 {
		t.Fatalf("updated inside the window")
	}
	release <- struct{}{}
	select {
	case <-h.slack.updated:
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced edit was not sent")
	}
	n.Settle()
	_, _, _, updates := classifySlack(h.slack.snapshot())
	if updates != 1 {
		t.Fatalf("updates %d", updates)
	}
	var saw bool
	for _, msg := range h.slack.snapshot() {
		if msg.update && strings.Contains(msg.text, "Stage: building") {
			saw = true
		}
		if msg.update && strings.Contains(msg.text, "Stage: planning") {
			t.Fatalf("sent a superseded edit: %s", msg.text)
		}
	}
	if !saw {
		t.Fatal("latest stage was not sent")
	}
}

func TestFailureDoesNotFailTheRun(t *testing.T) {
	h := newHarness()
	const canary = "xoxb-CANARY-DO-NOT-LOG"
	h.slack.err = errors.New("slack down " + canary)
	var waits []time.Duration
	n := h.notifier(t, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})
	t.Cleanup(func() { _ = n.Close() })
	ev := baseNotice()
	ev.Kind = KindRunStarted
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if h.slack.attempts != maxAttempts {
		t.Fatalf("attempts %d", h.slack.attempts)
	}
	if len(waits) != maxAttempts-1 {
		t.Fatalf("waits %d %#v", len(waits), waits)
	}
	for i, got := range waits {
		if want := backoff(i); got != want {
			t.Fatalf("wait %d = %s, want %s", i, got, want)
		}
	}
	if strings.Contains(h.log.String(), canary) || !strings.Contains(h.log.String(), "[REDACTED]") {
		t.Fatalf("log %s", h.log.String())
	}
	roots, statuses, _, _ := classifySlack(h.slack.snapshot())
	if roots != 0 || statuses != 0 {
		t.Fatalf("posted roots %d status %d", roots, statuses)
	}
}

func TestCanceledNotifyStops(t *testing.T) {
	h := newHarness()
	h.slack.err = errors.New("slack down")
	ctx, cancel := context.WithCancel(context.Background())
	n := h.notifier(t, func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	})
	t.Cleanup(func() { _ = n.Close() })
	ev := baseNotice()
	ev.Kind = KindRunStarted
	err := n.Notify(ctx, ev)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if h.slack.attempts != 1 {
		t.Fatalf("attempts %d", h.slack.attempts)
	}
}

func TestConcurrentNotifyOneRoot(t *testing.T) {
	h := newHarness()
	n := h.notifier(t, func(context.Context, time.Duration) error { return nil })
	t.Cleanup(func() { _ = n.Close() })
	ev := baseNotice()
	ev.Kind = KindStageChanged
	ev.Stage = "triage"
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.Notify(context.Background(), ev); err != nil {
				t.Errorf("notify: %v", err)
			}
		}()
	}
	wg.Wait()
	n.Settle()
	roots, statuses, replies, _ := classifySlack(h.slack.snapshot())
	if roots != 1 || statuses != 1 || replies != 0 {
		t.Fatalf("slack roots %d status %d replies %d", roots, statuses, replies)
	}
	_, _, checks, notes := h.gh.snapshot()
	var created int
	for _, check := range checks {
		if check.create {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("check creates %d", created)
	}
	var rootWrites int
	for _, note := range notes {
		if note.marker == rootMarker("wi_01") {
			rootWrites++
		}
	}
	if rootWrites != 1 {
		t.Fatalf("root comments %d", rootWrites)
	}
}

func TestCheckRunWaitsForHeadSHA(t *testing.T) {
	h := newHarness()
	n := h.notifier(t, nil)
	t.Cleanup(func() { _ = n.Close() })
	ev := baseNotice()
	ev.Kind = KindStageChanged
	ev.Stage = "triage"
	ev.HeadSHA = ""
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	_, _, checks, notes := h.gh.snapshot()
	if len(checks) != 0 || len(notes) != 1 {
		t.Fatalf("checks %d notes %d", len(checks), len(notes))
	}
	h.clock.Advance(3 * time.Second)
	ev.HeadSHA = "def456"
	ev.Stage = "building"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	_, _, checks, _ = h.gh.snapshot()
	if len(checks) != 1 || !checks[0].create || checks[0].sha != "def456" {
		t.Fatalf("checks %#v", checks)
	}
	h.clock.Advance(3 * time.Second)
	ev.Kind = KindFailed
	ev.Text = "tests failed"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	_, _, checks, _ = h.gh.snapshot()
	last := checks[len(checks)-1]
	if last.create || last.conclusion != "failure" || last.status != "completed" || !strings.Contains(last.summary, "Failed") {
		t.Fatalf("last %#v", last)
	}
}

func TestLinearEditsOneComment(t *testing.T) {
	h := newHarness()
	h.slack = newFakeSlack()
	n, err := New(Config{
		ExternalURL: "https://factory.example",
		Linear:      h.linear,
		Threads:     h.threads,
		Now:         h.clock.Now,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		Log:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	ev := baseNotice()
	ev.IssueID = "lin_1"
	ev.Repo = ""
	ev.Number = 0
	ev.Kind = KindStageChanged
	ev.Stage = "triage"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Second)
	ev.Kind = KindQuestionAsked
	ev.Text = "which name?"
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	notes, updates := h.linear.snapshot()
	if len(notes) != 2 || updates != 1 {
		t.Fatalf("notes %#v updates %d", notes, updates)
	}
	if notes[0].issue != "lin_1" || notes[1].issue != "lin_1" || notes[0].id == notes[1].id {
		t.Fatalf("notes %#v", notes)
	}
	row, err := h.threads.GetOutboundThread(context.Background(), "wi_01", ProviderLinear)
	if err != nil || row.RootRef != notes[0].id || row.StatusMsgRef != notes[0].id || row.Channel != "lin_1" {
		t.Fatalf("thread %#v %v", row, err)
	}
	assertAttribution(t, notes[0].body, testLink)
	assertAttribution(t, notes[1].body, testLink)
	if !strings.Contains(notes[1].body, "Reply in this thread to answer.") {
		t.Fatalf("milestone %s", notes[1].body)
	}

	again, err := New(Config{
		ExternalURL: "https://factory.example",
		Linear:      h.linear,
		Threads:     h.threads,
		Now:         h.clock.Now,
		Sleep:       func(context.Context, time.Duration) error { return nil },
		Log:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Second)
	ev.Kind = KindFindings
	ev.Text = "two nits"
	if err := again.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	notes, updates = h.linear.snapshot()
	if len(notes) != 2 || updates != 2 {
		t.Fatalf("restart notes %d updates %d", len(notes), updates)
	}
	if !strings.Contains(notes[0].body, "Findings") || !strings.Contains(notes[0].body, "two nits") {
		t.Fatalf("status %s", notes[0].body)
	}
}

func TestNotifierGuards(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing store")
	}
	h := newHarness()
	n := h.notifier(t, nil)
	t.Cleanup(func() { _ = n.Close() })
	if err := n.Notify(context.Background(), Notice{}); err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(context.Background(), Notice{WorkItemID: "wi_01", Kind: "nope"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Notify(ctx, baseNotice()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if len(h.slack.snapshot()) != 0 {
		t.Fatal("posted")
	}
	rendered := fmt.Sprintf("%s %v %+v %#v", n, n, n, n)
	if strings.Contains(rendered, "xox") || strings.Contains(rendered, "ghs_") || !strings.Contains(rendered, "slack:true") {
		t.Fatalf("format %s", rendered)
	}
	if n.LogValue().Kind() == slog.KindAny {
		t.Fatal("log value walked the struct")
	}
}

func fiveStage(base Notice) []Notice {
	stages := []string{"triage", "planning", "building", "reviewing", "human_handoff"}
	var out []Notice
	for i, stage := range stages {
		ev := base
		ev.Kind = KindStageChanged
		ev.Stage = stage
		out = append(out, ev)
		switch i {
		case 1:
			q := base
			q.Kind = KindQuestionAsked
			q.Stage = stage
			q.Text = "what should an empty name do?"
			out = append(out, q)
		case 2:
			pr := base
			pr.Kind = KindPROpened
			pr.Stage = stage
			pr.PRURL = "https://github.com/polds/factory/pull/9"
			out = append(out, pr)
		}
	}
	done := base
	done.Kind = KindHandoff
	done.Stage = "human_handoff"
	out = append(out, done)
	return out
}

func classifySlack(msgs []slackMsg) (roots, statuses, replies, updates int) {
	var statusTS string
	for _, msg := range msgs {
		if msg.update {
			updates++
			continue
		}
		if msg.thread == "" {
			roots++
			continue
		}
		if statusTS == "" {
			statusTS = msg.ts
			statuses++
			continue
		}
		replies++
	}
	return roots, statuses, replies, updates
}
