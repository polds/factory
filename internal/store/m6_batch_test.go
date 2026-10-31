package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/polds/factory/internal/api"
	"github.com/polds/factory/internal/store"
)

func TestCreateBatchTwoInstance(t *testing.T) {
	h := newHarness(t, true)
	factoryID, agentID := h.seed(t)
	other := openSchemaStore(t, h)
	run := h.createRun(t, factoryID, agentID, "batch-race", store.PriorityBatch)
	failure := mustScore(t, h, factoryID, run.ID, "quality", "bad", false, api.ScorerStatusOK)

	stores := []*store.Store{h.st, other}
	errs := make([]error, len(stores))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, st := range stores {
		wg.Add(1)
		go func(i int, st *store.Store) {
			defer wg.Done()
			<-start
			_, errs[i] = st.CreateBatch(context.Background(), store.NewImprovementBatch{
				FactoryID:  factoryID,
				Agent:      "implementer",
				FailureIDs: []string{failure.ID},
			})
		}(i, st)
	}
	close(start)
	wg.Wait()

	var created, inflight int
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, api.ErrBatchInFlight):
			inflight++
		default:
			t.Fatalf("create batch: %v", err)
		}
	}
	if created != 1 || inflight != 1 {
		t.Fatalf("created %d inflight %d errs %v", created, inflight, errs)
	}
	if n := countRows(t, h, `SELECT count(*) FROM improvement_batches WHERE factory_id = $1`, factoryID); n != 1 {
		t.Fatalf("batches %d", n)
	}
	if n := countRows(t, h, `SELECT count(*) FROM improvement_batch_failures`); n != 1 {
		t.Fatalf("links %d", n)
	}
}

func TestMarkBatchReviewedRestart(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "batch-review", store.PriorityBatch)
	failure := mustScore(t, h, factoryID, run.ID, "quality", "bad", false, api.ScorerStatusOK)
	batch, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID:  factoryID,
		Agent:      "implementer",
		FailureIDs: []string{failure.ID},
	})
	if err != nil || batch.Status != api.BatchQueued || len(batch.Failures) != 1 {
		t.Fatalf("batch %+v %v", batch, err)
	}
	if err := h.st.MarkBatchReviewed(ctx, batch.ID, api.BatchClosed, "not this one"); err != nil {
		t.Fatal(err)
	}

	reopened := openSchemaStore(t, h)
	seen, err := reopened.RunScores(ctx, run.ID)
	if err != nil || len(seen) != 1 || seen[0].ReviewedAt == nil || seen[0].ReviewedByBatchID != batch.ID {
		t.Fatalf("reopened scores %+v %v", seen, err)
	}
	batches, err := reopened.ImprovementBatches(ctx, factoryID)
	if err != nil || len(batches) != 1 || batches[0].Status != api.BatchClosed || batches[0].RejectionNote != "not this one" || batches[0].ClosedAt == nil {
		t.Fatalf("reopened batches %+v %v", batches, err)
	}
	reviewedAt := *seen[0].ReviewedAt
	closedAt := *batches[0].ClosedAt

	if err := reopened.MarkBatchReviewed(ctx, batch.ID, api.BatchClosed, "again"); err != nil {
		t.Fatal(err)
	}
	again, err := reopened.RunScores(ctx, run.ID)
	if err != nil || again[0].ReviewedAt == nil || !again[0].ReviewedAt.Equal(reviewedAt) {
		t.Fatalf("second review %+v %v", again, err)
	}
	closed, err := reopened.ImprovementBatches(ctx, factoryID)
	if err != nil || closed[0].Status != api.BatchClosed || closed[0].RejectionNote != "not this one" || closed[0].ClosedAt == nil || !closed[0].ClosedAt.Equal(closedAt) {
		t.Fatalf("second batch %+v %v", closed, err)
	}
	if n := countRows(t, h, `SELECT count(*) FROM scorer_results WHERE reviewed_by_batch_id = $1`, batch.ID); n != 1 {
		t.Fatalf("reviewed rows %d", n)
	}
}

func TestBatchInFlightReleases(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "batch-release", store.PriorityBatch)
	firstFail := mustScore(t, h, factoryID, run.ID, "quality", "bad", false, api.ScorerStatusOK)
	other := h.createRun(t, factoryID, agentID, "batch-release-2", store.PriorityBatch)
	secondFail := mustScore(t, h, factoryID, other.ID, "quality", "worse", false, api.ScorerStatusOK)

	batch, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID: factoryID, Agent: "implementer", FailureIDs: []string{firstFail.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID: factoryID, Agent: "implementer", FailureIDs: []string{secondFail.ID},
	}); !errors.Is(err, api.ErrBatchInFlight) {
		t.Fatalf("second while queued: %v", err)
	}
	if err := h.st.SetBatchStatus(ctx, batch.ID, api.BatchFailed, "gave up"); err != nil {
		t.Fatal(err)
	}
	still, err := h.st.RunScores(ctx, run.ID)
	if err != nil || still[0].ReviewedAt != nil {
		t.Fatalf("failed batch reviewed %+v %v", still, err)
	}
	next, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID: factoryID, Agent: "implementer", FailureIDs: []string{secondFail.ID},
	})
	if err != nil {
		t.Fatalf("after failed: %v", err)
	}
	if err := h.st.MarkBatchReviewed(ctx, next.ID, api.BatchMerged, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID: factoryID, Agent: "implementer", FailureIDs: []string{firstFail.ID},
	}); err != nil {
		t.Fatalf("after merged: %v", err)
	}
}

func TestBatchRunAndPR(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	scored := h.createRun(t, factoryID, agentID, "batch-pr-score", store.PriorityBatch)
	failure := mustScore(t, h, factoryID, scored.ID, "quality", "bad", false, api.ScorerStatusOK)
	batch, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID: factoryID, Agent: "editor", FailureIDs: []string{failure.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	improve := h.createRun(t, factoryID, agentID, "batch-pr-run", store.PriorityBatch)
	if err := h.st.SetBatchRun(ctx, batch.ID, improve.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetBatchRun(ctx, batch.ID, improve.ID); err != nil {
		t.Fatalf("same run: %v", err)
	}
	other := h.createRun(t, factoryID, agentID, "batch-pr-other", store.PriorityBatch)
	if err := h.st.SetBatchRun(ctx, batch.ID, other.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("other run: %v", err)
	}
	if err := h.st.SetBatchPR(ctx, batch.ID, "https://github.com/acme/app/pull/9", 9, "acme/definitions"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetBatchPR(ctx, batch.ID, "https://github.com/acme/app/pull/9", 9, ""); err != nil {
		t.Fatalf("same pr: %v", err)
	}
	if err := h.st.SetBatchPR(ctx, batch.ID, "https://github.com/acme/app/pull/10", 10, ""); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("other pr: %v", err)
	}
	got, err := h.st.ImprovementBatches(ctx, factoryID)
	if err != nil || len(got) != 1 || got[0].Status != api.BatchPROpen || got[0].PRNumber != 9 || got[0].DefinitionRepo != "acme/definitions" || got[0].ImprovementRunID != improve.ID {
		t.Fatalf("batch %+v %v", got, err)
	}
}

func TestBatchFailuresCarryReasoningAndAt(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "batch-reasoning", store.PriorityBatch)
	const reasoning = "the skill omitted the required convention"
	row, err := h.st.UpsertScorerResult(ctx, store.ScorerResult{
		FactoryID:  factoryID,
		RunID:      run.ID,
		ScorerName: "cite-sources",
		Label:      "missed",
		Passed:     false,
		Reasoning:  reasoning,
		Status:     api.ScorerStatusOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.CreatedAt.IsZero() {
		t.Fatal("scorer result has no time")
	}
	batch, err := h.st.CreateBatch(ctx, store.NewImprovementBatch{
		FactoryID:  factoryID,
		Agent:      "implementer",
		FailureIDs: []string{row.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Failures) != 1 || batch.Failures[0].Reasoning != reasoning || batch.Failures[0].At.IsZero() || !batch.Failures[0].At.Equal(row.CreatedAt) {
		t.Fatalf("create reasoning %q at %s", batch.Failures[0].Reasoning, batch.Failures[0].At)
	}
	listed, err := h.st.ImprovementBatches(ctx, factoryID)
	if err != nil || len(listed) != 1 || len(listed[0].Failures) != 1 {
		t.Fatalf("list %+v %v", listed, err)
	}
	got := listed[0].Failures[0]
	if got.Reasoning != reasoning || got.At.IsZero() || !got.At.Equal(row.CreatedAt) {
		t.Fatalf("listed reasoning %q at %s", got.Reasoning, got.At)
	}
}
