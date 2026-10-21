package store_test

import (
	"context"
	"testing"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

func TestBenchmarkTrialCells(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	bench, err := h.st.CreateBenchmarkRun(ctx, factoryID, "suite", "pat", 3)
	if err != nil || bench.Repetitions != 3 || bench.Suite != "suite" {
		t.Fatalf("run %+v %v", bench, err)
	}
	if _, err := h.st.CreateBenchmarkRun(ctx, factoryID, "suite", "pat", 0); err == nil {
		t.Fatal("zero repetitions accepted")
	}
	latest, err := h.st.LatestBenchmarkRun(ctx, factoryID, "suite")
	if err != nil || latest.ID != bench.ID {
		t.Fatalf("latest %+v %v", latest, err)
	}

	runs := make([]store.Run, 4)
	for i := range runs {
		runs[i] = h.createRun(t, factoryID, agentID, "trial", store.PriorityBatch)
	}
	record := func(run store.Run, outcome string, cost, duration int64) {
		t.Helper()
		if _, err := h.st.RecordTrial(ctx, store.BenchmarkTrial{
			BenchmarkRunID: bench.ID,
			Configuration:  "base",
			Task:           "one",
			Repetition:     1,
			RunID:          run.ID,
			Outcome:        outcome,
			CostCents:      cost,
			DurationMS:     duration,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(runs[0], api.TrialPass, 10, 100)
	record(runs[1], api.TrialFail, 30, 300)
	record(runs[2], api.TrialErrored, 999, 999)
	record(runs[3], api.TrialPending, 999, 999)
	other := h.createRun(t, factoryID, agentID, "trial-other", store.PriorityBatch)
	if _, err := h.st.RecordTrial(ctx, store.BenchmarkTrial{
		BenchmarkRunID: bench.ID,
		Configuration:  "alt",
		Task:           "two",
		Repetition:     1,
		RunID:          other.ID,
		Outcome:        api.TrialPass,
		CostCents:      7,
		DurationMS:     70,
	}); err != nil {
		t.Fatal(err)
	}

	cells, err := h.st.TrialCells(ctx, bench.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 {
		t.Fatalf("cells %+v", cells)
	}
	if cells[0].Configuration != "alt" || cells[0].N != 1 || cells[0].Pass != 1 || cells[0].Errored != 0 || cells[0].MedianCostCents != 7 {
		t.Fatalf("alt %+v", cells[0])
	}
	got := cells[1]
	if got.Configuration != "base" || got.Task != "one" || got.N != 2 || got.Pass != 1 || got.Errored != 1 || got.MedianCostCents != 20 || got.MedianDurationMS != 200 {
		t.Fatalf("base %+v", got)
	}
}
