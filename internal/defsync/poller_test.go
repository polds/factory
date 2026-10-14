package defsync

// These tests replace newTicker and must not call t.Parallel.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/polds/factory/internal/definition"
)

type pollResult struct {
	sha     string
	changed bool
	err     error
}

func TestPollerSyncsOnStartAndWake(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "def")
	c := &fakeCloner{resps: []cloneResp{{
		sha:   "sha-poll",
		files: map[string]string{"factory.yaml": validFactory("alpha")},
	}}}
	wake := make(chan struct{}, 1)
	results := make(chan pollResult, 4)
	p := &Poller{
		Interval: time.Hour,
		Source:   Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"},
		Dest:     dest,
		Cloner:   c,
		Wake:     chanWake(wake),
		Handler: func(sha string, changed bool, _ []definition.Diagnostic, err error) {
			results <- pollResult{sha: sha, changed: changed, err: err}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	first := waitResult(t, results)
	if first.err != nil || !first.changed || first.sha != "sha-poll" {
		t.Fatalf("first=%+v", first)
	}
	wake <- struct{}{}
	second := waitResult(t, results)
	if second.err != nil || second.changed || second.sha != "sha-poll" {
		t.Fatalf("wake=%+v", second)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("alpha") {
		t.Fatalf("dest:\n%s", got)
	}
}

func TestPollerSyncsOnInterval(t *testing.T) {
	ticks := make(chan time.Time, 1)
	prev := newTicker
	newTicker = func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() {}
	}
	t.Cleanup(func() { newTicker = prev })

	dest := filepath.Join(t.TempDir(), "def")
	c := &fakeCloner{resps: []cloneResp{
		{sha: "sha-1", files: map[string]string{"factory.yaml": validFactory("alpha")}},
		{sha: "sha-2", files: map[string]string{"factory.yaml": validFactory("beta")}},
	}}
	results := make(chan pollResult, 4)
	p := &Poller{
		Interval: time.Hour,
		Source:   Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"},
		Dest:     dest,
		Cloner:   c,
		Handler: func(sha string, changed bool, _ []definition.Diagnostic, err error) {
			results <- pollResult{sha: sha, changed: changed, err: err}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	if got := waitResult(t, results); got.sha != "sha-1" || !got.changed {
		t.Fatalf("start=%+v", got)
	}
	ticks <- time.Now()
	if got := waitResult(t, results); got.sha != "sha-2" || !got.changed || got.err != nil {
		t.Fatalf("tick=%+v", got)
	}
	if got := readFile(t, filepath.Join(dest, "factory.yaml")); got != validFactory("beta") {
		t.Fatalf("dest:\n%s", got)
	}
}

func TestPollerKeepsPollingAfterCloneError(t *testing.T) {
	ticks := make(chan time.Time, 1)
	prev := newTicker
	newTicker = func(time.Duration) (<-chan time.Time, func()) {
		return ticks, func() {}
	}
	t.Cleanup(func() { newTicker = prev })

	dest := filepath.Join(t.TempDir(), "def")
	boom := errors.New("transport down")
	c := &fakeCloner{resps: []cloneResp{
		{err: boom},
		{sha: "sha-2", files: map[string]string{"factory.yaml": validFactory("alpha")}},
	}}
	results := make(chan pollResult, 4)
	p := &Poller{
		Interval: time.Hour,
		Source:   Source{URL: "https://example.test/defs.git", Ref: "main", ProductionRef: "main"},
		Dest:     dest,
		Cloner:   c,
		Handler: func(sha string, changed bool, _ []definition.Diagnostic, err error) {
			results <- pollResult{sha: sha, changed: changed, err: err}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	first := waitResult(t, results)
	if !errors.Is(first.err, boom) || first.changed {
		t.Fatalf("first=%+v", first)
	}
	ticks <- time.Now()
	second := waitResult(t, results)
	if second.err != nil || !second.changed || second.sha != "sha-2" {
		t.Fatalf("second=%+v", second)
	}
}

func TestPollerStopsOnCancel(t *testing.T) {
	prev := newTicker
	newTicker = func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}
	t.Cleanup(func() { newTicker = prev })

	p := &Poller{
		Interval: time.Hour,
		Source:   Source{URL: "https://example.test/defs.git", Ref: "main"},
		Dest:     filepath.Join(t.TempDir(), "def"),
		Cloner:   &fakeCloner{resps: []cloneResp{{sha: "sha-1", files: map[string]string{"factory.yaml": validFactory("alpha")}}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestPollerRequiresInterval(t *testing.T) {
	p := &Poller{
		Dest:   filepath.Join(t.TempDir(), "def"),
		Cloner: &fakeCloner{},
		Source: Source{URL: "https://example.test/defs.git", Ref: "main"},
	}
	if err := p.Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func waitResult(t *testing.T, results <-chan pollResult) pollResult {
	t.Helper()
	select {
	case got := <-results:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sync")
	}
	return pollResult{}
}

type chanWake chan struct{}

func (c chanWake) Wake() <-chan struct{} { return c }
