package ledger

import (
	"strings"
	"testing"

	"github.com/polds/factory/internal/store"
)

func TestPrepareBodyCapAndRedaction(t *testing.T) {
	got, err := PrepareBody("token Bearer supersecretvalue end")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "supersecretvalue") || !strings.Contains(got, "Bearer [REDACTED]") {
		t.Fatalf("redacted body %q", got)
	}
	if _, err := PrepareBody(strings.Repeat("a", store.NativeBodyCap)); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareBody(strings.Repeat("a", store.NativeBodyCap+1)); err == nil {
		t.Fatal("accepted 8193 bytes")
	}
}

func TestWakesMentionDoesNotWake(t *testing.T) {
	if !Wakes(store.LedgerKindComment) || !Wakes(store.LedgerKindAssign) {
		t.Fatal("comment and assign wake")
	}
	if Wakes(store.LedgerKindMention) || Wakes(store.LedgerKindResult) || Wakes(store.LedgerKindCompletion) {
		t.Fatal("mention, result, and completion must not wake")
	}
}
