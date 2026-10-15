package outbound

import (
	"context"

	"github.com/polds/factory/internal/store"
)

// Provider names stored on outbound_threads. They match internal/store.
const (
	ProviderGitHub = store.ProviderGitHub
	ProviderSlack  = store.ProviderSlack
	ProviderLinear = store.ProviderLinear
)

// Thread is one outbound conversation for a work item and provider.
// One row exists per (WorkItemID, Provider).
type Thread struct {
	WorkItemID   string
	Provider     string
	Channel      string
	RootRef      string
	StatusMsgRef string
}

// Threads is the T17 outbound thread API. *store.Store implements it.
// GetOutboundThread returns store.ErrNotFound when the pair has no row.
type Threads interface {
	GetOutboundThread(ctx context.Context, workItemID, provider string) (store.OutboundThread, error)
	UpsertOutboundThread(ctx context.Context, t store.OutboundThread) (store.OutboundThread, error)
}

// Refs binds a provider thread to a work item. *store.Store implements it.
// A nil Refs skips the bind. T30 passes the store so a Slack root can
// continue GitHub-originated work.
type Refs interface {
	BindExternalRef(ctx context.Context, provider, externalRef, workItemID, conversationID string) (store.ExternalRef, bool, error)
}

func toStore(t Thread) store.OutboundThread {
	return store.OutboundThread{
		WorkItemID:   t.WorkItemID,
		Provider:     t.Provider,
		Channel:      t.Channel,
		RootRef:      t.RootRef,
		StatusMsgRef: t.StatusMsgRef,
	}
}

func fromStore(t store.OutboundThread) Thread {
	return Thread{
		WorkItemID:   t.WorkItemID,
		Provider:     t.Provider,
		Channel:      t.Channel,
		RootRef:      t.RootRef,
		StatusMsgRef: t.StatusMsgRef,
	}
}

var (
	_ Threads = (*store.Store)(nil)
	_ Refs    = (*store.Store)(nil)
)
