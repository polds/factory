// Package inbox is the native inbox entry point. Read does not acknowledge.
package inbox

import (
	"context"

	"github.com/polds/factory/internal/store"
)

// Read projects one assignee inbox. It does not ack, including a long-poll.
func Read(ctx context.Context, st *store.Store, q store.InboxQuery) (store.InboxPage, error) {
	return st.ReadInbox(ctx, q)
}

// Apply records a durable apply once for the message.
func Apply(ctx context.Context, st *store.Store, messageID, taskID, principal string) (bool, error) {
	return st.ApplyMessage(ctx, messageID, taskID, principal)
}

// Ack acknowledges one delivery after Apply. A duplicate ack does not apply again.
func Ack(ctx context.Context, st *store.Store, deliveryID, principal, taskID string) (store.AckResult, error) {
	return st.AckDelivery(ctx, deliveryID, principal, taskID)
}

// Claim takes the inbox fence or returns claim_fenced with the current holder.
func Claim(ctx context.Context, st *store.Store, taskID, holder string) (store.InboxClaim, error) {
	return st.ClaimInbox(ctx, taskID, holder)
}

// Redeliver creates a new delivery id for the same message and does not ack.
func Redeliver(ctx context.Context, st *store.Store, messageID string) (store.InboxDelivery, error) {
	return st.Redeliver(ctx, messageID)
}
