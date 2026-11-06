package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

func TestINVRetentionResultSurvivesWithoutWorkItem(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	ins, err := h.st.InsertInvocation(ctx, store.InsertInvocationParams{
		FactoryID:           factoryID,
		ProfileName:         "scan",
		SourceOccurrenceKey: "retention:1",
		CanonicalDigest:     "d1",
		DefinitionDigest:    "def",
		State:               invocation.StateAccepted,
		Intent:              "new",
		Priority:            "interactive",
		PrincipalJSON:       json.RawMessage(`{"kind":"api-key","actor":"k"}`),
	})
	if err != nil || !ins.Created {
		t.Fatalf("insert %+v err %v", ins, err)
	}
	if err := h.st.PutResult(ctx, store.ResultRow{
		InvocationID:   ins.Row.ID,
		FactoryID:      factoryID,
		Summary:        "ok",
		CoverageStatus: "full",
		FindingsCount:  0,
		ReportBody:     []byte("{}"),
		PublishedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	row, err := h.st.GetResult(ctx, factoryID, ins.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := h.st.CreateWorkItem(ctx, store.NewWorkItem{
		FactoryID: factoryID,
		Source:    store.SourceAPI,
		Title:     "retention",
		Stage:     store.StageTriage,
		Status:    store.WorkItemOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.LinkInvocationWorkItem(ctx, ins.Row.ID, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.raw.Exec(ctx, `DELETE FROM work_items WHERE id = $1`, item.ID); err != nil {
		t.Fatal(err)
	}
	row, err = h.st.GetResult(ctx, factoryID, ins.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.FindingsCount != 0 || row.Summary != "ok" {
		t.Fatalf("row %+v", row)
	}
}
