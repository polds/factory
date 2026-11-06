// Package ledger prepares canonical invocation events before AppendLedger.
package ledger

import (
	"context"
	"fmt"

	"github.com/polds/factory/internal/definition"
	"github.com/polds/factory/internal/store"
)

// PrepareBody rejects a body over NativeBodyCap and redacts credentials.
func PrepareBody(body string) (string, error) {
	if len(body) > store.NativeBodyCap {
		return "", fmt.Errorf("%w: body exceeds %d bytes", store.ErrInputInvalid, store.NativeBodyCap)
	}
	return definition.RedactInline(body), nil
}

// Wakes reports whether a ledger kind records a wake intent.
// Mention, result, and completion do not wake. Comment and assign do.
func Wakes(kind string) bool {
	switch kind {
	case store.LedgerKindComment, store.LedgerKindAssign:
		return true
	case store.LedgerKindMention, store.LedgerKindResult, store.LedgerKindCompletion:
		return false
	default:
		return false
	}
}

// Append redacts the body, suppresses mention wakes, and commits through AppendLedger.
func Append(ctx context.Context, st *store.Store, invocationID string, entry store.LedgerEntry) (store.LedgerRecord, error) {
	body, err := PrepareBody(entry.Body)
	if err != nil {
		return store.LedgerRecord{}, err
	}
	entry.Body = body
	entry.Wake = entry.Wake && Wakes(entry.Kind)
	return st.AppendLedger(ctx, invocationID, entry)
}
