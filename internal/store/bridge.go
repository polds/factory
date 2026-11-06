package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// BridgeNotice is one native report for the outbound owner.
// Summary is report text, never a credential.
type BridgeNotice struct {
	IdempotencyKey string
	Origin         string
	Binding        string
	TaskRef        string
	Summary        string
}

// BridgePolicy is the process-local send switch. The queue is not.
type BridgePolicy struct {
	Enabled    bool
	Authorized bool
	Outage     bool
	Owner      string
}

// BridgeOutcome is the durable enqueue result.
type BridgeOutcome struct {
	Queued     bool
	Sent       bool
	Suppressed string
	SendCount  int
	Binding    string
}

// CommitBridge inserts one notice in its own transaction.
func (s *Store) CommitBridge(ctx context.Context, n BridgeNotice, pol BridgePolicy) (BridgeOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BridgeOutcome{}, fmt.Errorf("store: bridge: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := CommitBridgeTx(ctx, tx, n, pol)
	if err != nil {
		return BridgeOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BridgeOutcome{}, fmt.Errorf("store: bridge: %w", err)
	}
	return out, nil
}

// CommitBridgeTx inserts one notice on tx. The caller commits.
// Echo, replay, and the send bit are decided in this transaction.
func CommitBridgeTx(ctx context.Context, tx pgx.Tx, n BridgeNotice, pol BridgePolicy) (BridgeOutcome, error) {
	return commitBridge(ctx, tx, n, pol)
}

// BridgeSendCount is the number of notices this database has sent.
func (s *Store) BridgeSendCount(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM bridge_outbox WHERE sent`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: bridge count: %w", err)
	}
	return n, nil
}

type bridgeDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func commitBridge(ctx context.Context, db bridgeDB, n BridgeNotice, pol BridgePolicy) (BridgeOutcome, error) {
	count, err := bridgeSendCount(ctx, db)
	if err != nil {
		return BridgeOutcome{}, err
	}
	echo, err := bridgeEcho(ctx, db, n, pol.Owner)
	if err != nil {
		return BridgeOutcome{}, err
	}
	if echo {
		return BridgeOutcome{Suppressed: "echo", SendCount: count}, nil
	}
	if n.IdempotencyKey == "" {
		return BridgeOutcome{Queued: true, SendCount: count}, nil
	}
	sent := pol.Enabled && pol.Authorized && !pol.Outage
	tag, err := db.Exec(ctx, `
		INSERT INTO bridge_outbox (
		    idempotency_key, notice_origin, notice_binding, task_ref, notice_summary, sent)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		n.IdempotencyKey, n.Origin, n.Binding, n.TaskRef, n.Summary, sent,
	)
	if err != nil {
		return BridgeOutcome{}, fmt.Errorf("store: bridge insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return BridgeOutcome{Suppressed: "replay", SendCount: count}, nil
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO bridge_bindings (binding_key) VALUES ($1)
		ON CONFLICT (binding_key) DO NOTHING`, n.IdempotencyKey); err != nil {
		return BridgeOutcome{}, fmt.Errorf("store: bridge binding: %w", err)
	}
	if sent {
		count++
	}
	return BridgeOutcome{
		Queued:    true,
		Sent:      sent,
		SendCount: count,
		Binding:   n.IdempotencyKey,
	}, nil
}

func bridgeEcho(ctx context.Context, db bridgeDB, n BridgeNotice, owner string) (bool, error) {
	if owner != "" && n.Origin == owner {
		return true, nil
	}
	if n.Binding == "" {
		return false, nil
	}
	var found bool
	if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bridge_bindings WHERE binding_key = $1)`, n.Binding).Scan(&found); err != nil {
		return false, fmt.Errorf("store: bridge binding: %w", err)
	}
	return found, nil
}

func bridgeSendCount(ctx context.Context, db bridgeDB) (int, error) {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM bridge_outbox WHERE sent`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: bridge count: %w", err)
	}
	return n, nil
}
