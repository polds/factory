package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestRequireRowCallSiteParity(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	fac, agent := h.seed(t)
	run := h.createRun(t, fac, agent, "default", store.PriorityInteractive)
	if err := h.st.UpsertWorker(ctx, store.Worker{ID: "w1", Pool: "default", Status: "connected"}); err != nil {
		t.Fatal(err)
	}

	if err := h.st.RecordRejection(ctx, "", run.ID); err == nil || err.Error() != "store: worker and run are required" {
		t.Fatalf("record rejection empty: %v", err)
	}
	if err := h.st.RecordRejection(ctx, "missing", run.ID); !exactNotFound(err) {
		t.Fatalf("record rejection missing worker: %v", err)
	}
	if err := h.st.RecordRejection(ctx, "w1", "missing"); !exactNotFound(err) {
		t.Fatalf("record rejection missing run: %v", err)
	}
	if err := h.st.RecordRejection(ctx, "w1", run.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.st.CreateBenchmarkRun(ctx, "", "suite", "ada", 1); err == nil || err.Error() != "store: factory is required" {
		t.Fatalf("factory empty: %v", err)
	}
	if _, err := h.st.CreateBenchmarkRun(ctx, "missing", "suite", "ada", 1); !exactNotFound(err) {
		t.Fatalf("factory missing: %v", err)
	}
	bench, err := h.st.CreateBenchmarkRun(ctx, fac, "suite", "ada", 1)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.st.TrialCells(ctx, ""); err == nil || err.Error() != "store: benchmark run is required" {
		t.Fatalf("trial cells empty: %v", err)
	}
	if _, err := h.st.TrialCells(ctx, "missing"); !exactNotFound(err) {
		t.Fatalf("trial cells missing: %v", err)
	}
	cells, err := h.st.TrialCells(ctx, bench.ID)
	if err != nil || len(cells) != 0 {
		t.Fatalf("trial cells: %v len %d", err, len(cells))
	}

	if _, err := h.st.ListBenchmarkTrials(ctx, ""); err == nil || err.Error() != "store: benchmark run is required" {
		t.Fatalf("list trials empty: %v", err)
	}
	if _, err := h.st.ListBenchmarkTrials(ctx, "missing"); !exactNotFound(err) {
		t.Fatalf("list trials missing: %v", err)
	}
	trials, err := h.st.ListBenchmarkTrials(ctx, bench.ID)
	if err != nil || len(trials) != 0 {
		t.Fatalf("list trials: %v len %d", err, len(trials))
	}

	if _, err := h.st.RunScores(ctx, ""); err == nil || err.Error() != "store: run is required" {
		t.Fatalf("run scores empty: %v", err)
	}
	if _, err := h.st.RunScores(ctx, "missing"); !exactNotFound(err) {
		t.Fatalf("run scores missing: %v", err)
	}
	scores, err := h.st.RunScores(ctx, run.ID)
	if err != nil || len(scores) != 0 {
		t.Fatalf("run scores: %v len %d", err, len(scores))
	}

	if err := h.st.MarkSteerDelivered(ctx, ""); err == nil || err.Error() != "store: steer id is required" {
		t.Fatalf("mark steer empty: %v", err)
	}
	if err := h.st.MarkSteerDelivered(ctx, "missing"); !exactNotFound(err) {
		t.Fatalf("mark steer missing: %v", err)
	}
	steer, err := h.st.EnqueueSteer(ctx, run.ID, store.SteerInterrupt, "stop", "ada")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkSteerDelivered(ctx, steer.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkSteerDelivered(ctx, steer.ID); err != nil {
		t.Fatalf("second deliver: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	checks := []struct {
		op  string
		err error
	}{
		{"store: record rejection:", h.st.RecordRejection(cancelled, "w1", run.ID)},
		{"store: factory:", errOnly(h.st.CreateBenchmarkRun(cancelled, fac, "suite", "ada", 1))},
		{"store: trial cells:", errOnly(h.st.TrialCells(cancelled, bench.ID))},
		{"store: list trials:", errOnly(h.st.ListBenchmarkTrials(cancelled, bench.ID))},
		{"store: run scores:", errOnly(h.st.RunScores(cancelled, run.ID))},
		{"store: mark steer:", h.st.MarkSteerDelivered(cancelled, steer.ID)},
	}
	for _, check := range checks {
		if check.err == nil || !strings.HasPrefix(check.err.Error(), check.op) || !errors.Is(check.err, context.Canceled) {
			t.Fatalf("%s err %v", check.op, check.err)
		}
		if exactNotFound(check.err) {
			t.Fatalf("%s collapsed to ErrNotFound", check.op)
		}
	}
}

func errOnly(_ any, err error) error { return err }

// exactNotFound is the unwrapped sentinel requireRow returns for no rows.
func exactNotFound(err error) bool {
	return err == store.ErrNotFound //nolint:errorlint // identity, not a wrap
}
