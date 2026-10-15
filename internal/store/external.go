package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func scanExternalRef(row pgx.Row) (ExternalRef, error) {
	var ref ExternalRef
	err := row.Scan(&ref.Provider, &ref.ExternalRef, &ref.WorkItemID, &ref.ConversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExternalRef{}, ErrNotFound
	}
	if err != nil {
		return ExternalRef{}, err
	}
	return ref, nil
}

// BindExternalRef inserts the binding or returns the row that won the race.
// created is true only when this call inserted the row. A later caller with
// a different work item gets the first row and created=false.
func (s *Store) BindExternalRef(ctx context.Context, provider, externalRef, workItemID, conversationID string) (ExternalRef, bool, error) {
	if err := validProvider(provider); err != nil {
		return ExternalRef{}, false, err
	}
	if externalRef == "" || workItemID == "" || conversationID == "" {
		return ExternalRef{}, false, fmt.Errorf("store: external ref, work item, and conversation are required")
	}
	row, err := scanExternalRef(s.pool.QueryRow(ctx, `
		INSERT INTO external_refs (provider, external_ref, work_item_id, conversation_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider, external_ref) DO NOTHING
		RETURNING provider, external_ref, work_item_id, conversation_id`,
		provider, externalRef, workItemID, conversationID,
	))
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ExternalRef{}, false, fmt.Errorf("store: bind external ref: %w", err)
	}
	existing, err := s.GetExternalRef(ctx, provider, externalRef)
	if err != nil {
		return ExternalRef{}, false, fmt.Errorf("store: bind external ref: %w", err)
	}
	return existing, false, nil
}

// GetExternalRef returns the binding or ErrNotFound.
func (s *Store) GetExternalRef(ctx context.Context, provider, externalRef string) (ExternalRef, error) {
	row, err := scanExternalRef(s.pool.QueryRow(ctx, `
		SELECT provider, external_ref, work_item_id, conversation_id
		FROM external_refs
		WHERE provider = $1 AND external_ref = $2`, provider, externalRef))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ExternalRef{}, err
		}
		return ExternalRef{}, fmt.Errorf("store: get external ref: %w", err)
	}
	return row, nil
}

// UpsertOutboundThread inserts or replaces the thread for one work item and provider.
func (s *Store) UpsertOutboundThread(ctx context.Context, t OutboundThread) (OutboundThread, error) {
	if t.WorkItemID == "" {
		return OutboundThread{}, fmt.Errorf("store: work item is required")
	}
	if err := validProvider(t.Provider); err != nil {
		return OutboundThread{}, err
	}
	row, err := scanOutbound(s.pool.QueryRow(ctx, `
		INSERT INTO outbound_threads (
		    work_item_id, provider, channel, root_ref, status_msg_ref)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (work_item_id, provider) DO UPDATE
		SET channel = EXCLUDED.channel,
		    root_ref = EXCLUDED.root_ref,
		    status_msg_ref = EXCLUDED.status_msg_ref
		RETURNING work_item_id, provider, channel, root_ref, status_msg_ref`,
		t.WorkItemID, t.Provider, t.Channel, t.RootRef, t.StatusMsgRef,
	))
	if err != nil {
		return OutboundThread{}, fmt.Errorf("store: upsert outbound thread: %w", err)
	}
	return row, nil
}

// GetOutboundThread returns the thread or ErrNotFound.
func (s *Store) GetOutboundThread(ctx context.Context, workItemID, provider string) (OutboundThread, error) {
	row, err := scanOutbound(s.pool.QueryRow(ctx, `
		SELECT work_item_id, provider, channel, root_ref, status_msg_ref
		FROM outbound_threads
		WHERE work_item_id = $1 AND provider = $2`, workItemID, provider))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return OutboundThread{}, err
		}
		return OutboundThread{}, fmt.Errorf("store: get outbound thread: %w", err)
	}
	return row, nil
}

func scanOutbound(row pgx.Row) (OutboundThread, error) {
	var t OutboundThread
	err := row.Scan(&t.WorkItemID, &t.Provider, &t.Channel, &t.RootRef, &t.StatusMsgRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return OutboundThread{}, ErrNotFound
	}
	if err != nil {
		return OutboundThread{}, err
	}
	return t, nil
}
