package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/polds/factory/internal/store"
)

func TestPRFactIdempotent(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	firstItem, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.SourceIssue,
		Title:     "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondItem, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.SourceAPI,
		Title:     "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	laterOpen := opened.Add(24 * time.Hour)
	merged := opened.Add(72 * time.Hour)
	lines := 10
	replaced := 40

	got, err := h.st.UpsertPRFact(ctx, store.PRFact{
		FactoryID:  factoryID,
		Repo:       "acme/app",
		Number:     4,
		WorkItemID: firstItem.ID,
		OpenedAt:   &opened,
	})
	if err != nil || got.WorkItemID != firstItem.ID || got.OpenedAt == nil || !got.OpenedAt.Equal(opened) {
		t.Fatalf("insert %+v %v", got, err)
	}
	if got.HumanPushSeen == nil || *got.HumanPushSeen || got.ChangedLines != nil {
		t.Fatalf("defaults %+v", got)
	}

	human := true
	got, err = h.st.UpsertPRFact(ctx, store.PRFact{
		FactoryID:     factoryID,
		Repo:          "acme/app",
		Number:        4,
		WorkItemID:    secondItem.ID,
		OpenedAt:      &laterOpen,
		MergedAt:      &merged,
		HumanPushSeen: &human,
		ChangedLines:  &lines,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkItemID != firstItem.ID || !got.OpenedAt.Equal(opened) || got.MergedAt == nil || !got.MergedAt.Equal(merged) {
		t.Fatalf("fill %+v", got)
	}
	if got.HumanPushSeen == nil || !*got.HumanPushSeen || got.ChangedLines == nil || *got.ChangedLines != 10 {
		t.Fatalf("flags %+v", got)
	}

	quiet := false
	got, err = h.st.UpsertPRFact(ctx, store.PRFact{
		FactoryID:     factoryID,
		Repo:          "acme/app",
		Number:        4,
		HumanPushSeen: &quiet,
		ChangedLines:  &replaced,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.HumanPushSeen == nil || !*got.HumanPushSeen || got.ChangedLines == nil || *got.ChangedLines != 40 || got.MergedAt == nil {
		t.Fatalf("stick %+v", got)
	}
	if n := countRows(t, h, `SELECT count(*) FROM pr_facts WHERE factory_id = $1`, factoryID); n != 1 {
		t.Fatalf("rows %d", n)
	}
}
