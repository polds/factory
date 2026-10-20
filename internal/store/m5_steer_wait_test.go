package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

func TestReleaseSteerClaimsLetsNextPollTakeTheRow(t *testing.T) {
	h := newHarness(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	factoryID, agentID := h.seed(t)
	run := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	other := h.createRun(t, factoryID, agentID, "default", store.PriorityBatch)
	st, err := h.st.EnqueueSteer(ctx, run.ID, store.SteerFollowup, "keep going", "ada")
	if err != nil {
		t.Fatal(err)
	}
	kept, err := h.st.EnqueueSteer(ctx, other.ID, store.SteerFollowup, "leave me", "ada")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claimed, err := h.st.NextSteers(ctx, run.ID, "", now)
	if err != nil || len(claimed) != 1 || claimed[0].ID != st.ID {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	if _, err := h.st.NextSteers(ctx, other.ID, "", now); err != nil {
		t.Fatal(err)
	}
	hidden, err := h.st.NextSteers(ctx, run.ID, "", now)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("lease did not hide %+v %v", hidden, err)
	}
	if err := h.st.ReleaseSteerClaims(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	again, err := h.st.NextSteers(ctx, run.ID, "", now)
	if err != nil || len(again) != 1 || again[0].ID != st.ID || again[0].Text != "keep going" {
		t.Fatalf("reclaim %+v %v", again, err)
	}
	if err := h.st.MarkSteerDelivered(ctx, again[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.ReleaseSteerClaims(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	gone, err := h.st.NextSteers(ctx, run.ID, "", now.Add(time.Second))
	if err != nil || len(gone) != 0 {
		t.Fatalf("delivered row came back %+v %v", gone, err)
	}
	still, err := h.st.NextSteers(ctx, other.ID, "", now)
	if err != nil || len(still) != 0 {
		t.Fatalf("other run's lease was cleared %+v %v", still, err)
	}
	if err := h.st.ReleaseSteerClaims(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	back, err := h.st.NextSteers(ctx, other.ID, "", now)
	if err != nil || len(back) != 1 || back[0].ID != kept.ID {
		t.Fatalf("other reclaim %+v %v", back, err)
	}
	if err := h.st.ReleaseSteerClaims(ctx, ""); err == nil {
		t.Fatal("empty run was accepted")
	}
}
