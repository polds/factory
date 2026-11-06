package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/polds/factory/internal/invocation"
	"github.com/polds/factory/internal/store"
)

func TestInsertInvocationReplayAndConflict(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	factoryID, _ := h.seed(t)
	params := store.InsertInvocationParams{
		FactoryID:           factoryID,
		ProfileName:         "scan",
		SourceOccurrenceKey: "human:review:1",
		CanonicalDigest:     "digest-a",
		DefinitionDigest:    "def-a",
		State:               invocation.StateAccepted,
		Intent:              "new",
		Priority:            "interactive",
		PrincipalJSON:       json.RawMessage(`{"kind":"api-key","actor":"api-key"}`),
	}
	first, err := h.st.InsertInvocation(ctx, params)
	if err != nil || !first.Created {
		t.Fatalf("first insert %+v err %v", first, err)
	}
	replay, err := h.st.InsertInvocation(ctx, params)
	if err != nil || replay.Created || replay.Conflict {
		t.Fatalf("replay %+v err %v", replay, err)
	}
	if replay.Row.ID != first.Row.ID {
		t.Fatal("replay id mismatch")
	}
	params.CanonicalDigest = "digest-b"
	conflict, err := h.st.InsertInvocation(ctx, params)
	if err != nil || !conflict.Conflict {
		t.Fatalf("conflict %+v err %v", conflict, err)
	}
}
