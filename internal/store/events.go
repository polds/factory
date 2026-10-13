package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppendEvents stores evs starting at index startIdx. A row that already
// exists for (execution_id, idx) is not inserted again. The returned seqs
// align with evs and include the seq of a duplicate. New rows use the next
// per-run seq, so a retried overlapping batch does not create gaps or duplicates.
func (s *Store) AppendEvents(ctx context.Context, runID, execID string, startIdx int, evs []json.RawMessage) ([]int64, error) {
	if startIdx < 0 {
		return nil, fmt.Errorf("store: event index must be >= 0")
	}
	if len(evs) == 0 {
		return []int64{}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: append events: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, "events:"+runID); err != nil {
		return nil, fmt.Errorf("store: append events: %w", err)
	}
	var owner string
	err = tx.QueryRow(ctx, `SELECT run_id FROM executions WHERE id = $1`, execID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: append events: %w", err)
	}
	if owner != runID {
		return nil, fmt.Errorf("store: execution %s belongs to run %s", execID, owner)
	}

	seqs := make([]int64, len(evs))
	for i, raw := range evs {
		meta, err := eventMeta(raw)
		if err != nil {
			return nil, err
		}
		idx := startIdx + i
		seq, err := insertEvent(ctx, tx, runID, execID, idx, meta, raw)
		if err != nil {
			return nil, err
		}
		seqs[i] = seq
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify('run_events', $1)`, runID); err != nil {
		return nil, fmt.Errorf("store: append events: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: append events: %w", err)
	}
	return seqs, nil
}

type parsedEvent struct {
	Type string
	TS   time.Time
}

func eventMeta(raw json.RawMessage) (parsedEvent, error) {
	if !json.Valid(raw) {
		return parsedEvent{}, fmt.Errorf("store: event is not json")
	}
	var meta struct {
		Type string `json:"type"`
		TS   string `json:"ts"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return parsedEvent{}, fmt.Errorf("store: event: %w", err)
	}
	if meta.Type == "" {
		return parsedEvent{}, fmt.Errorf("store: event type is required")
	}
	ts := time.Now().UTC()
	if meta.TS != "" {
		parsed, err := time.Parse(time.RFC3339Nano, meta.TS)
		if err != nil {
			return parsedEvent{}, fmt.Errorf("store: event ts: %w", err)
		}
		ts = parsed
	}
	return parsedEvent{Type: meta.Type, TS: ts}, nil
}

func insertEvent(ctx context.Context, tx pgx.Tx, runID, execID string, idx int, meta parsedEvent, raw json.RawMessage) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, `
		SELECT seq FROM run_events WHERE execution_id = $1 AND idx = $2`, execID, idx).Scan(&seq)
	if err == nil {
		return seq, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("store: append event %d: %w", idx, err)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO run_events (run_id, seq, execution_id, idx, ts, type, payload_json)
		VALUES (
		    $1,
		    (SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = $1),
		    $2, $3, $4, $5, $6::jsonb
		)
		RETURNING seq`,
		runID, execID, idx, meta.TS, meta.Type, []byte(raw)).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("store: append event %d: %w", idx, err)
	}
	return seq, nil
}

// EventsFrom returns events for runID with seq greater than fromSeq, in seq order.
func (s *Store) EventsFrom(ctx context.Context, runID string, fromSeq int64, limit int) ([]Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store: event limit must be positive")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, seq, execution_id, idx, ts, type, payload_json
		FROM run_events
		WHERE run_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT $3`, runID, fromSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: events from: %w", err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var ev Event
		var payload []byte
		if err := rows.Scan(&ev.RunID, &ev.Seq, &ev.ExecutionID, &ev.Idx, &ev.TS, &ev.Type, &payload); err != nil {
			return nil, fmt.Errorf("store: events from: %w", err)
		}
		ev.Payload = json.RawMessage(payload)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: events from: %w", err)
	}
	return out, nil
}

// Listen subscribes to channel until ctx is cancelled. Payload strings are
// the run id notified by CreateRun (run_queue) or AppendEvents (run_events).
// LISTEN is registered before Listen returns.
func (s *Store) Listen(ctx context.Context, channel string) (<-chan string, error) {
	listenSQL, unlistenSQL, err := listenStatements(channel)
	if err != nil {
		return nil, err
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: listen: %w", err)
	}
	if _, err := conn.Exec(ctx, listenSQL); err != nil {
		conn.Release()
		return nil, fmt.Errorf("store: listen: %w", err)
	}
	out := make(chan string, 16)
	go func() {
		defer close(out)
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := conn.Exec(unlockCtx, unlistenSQL); err != nil {
				// LISTEN would otherwise survive Release and steal later notifications.
				_ = conn.Conn().Close(context.Background())
			}
			conn.Release()
		}()
		for {
			notification, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				return
			}
			if notification == nil || notification.Channel != channel {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case out <- notification.Payload:
			}
		}
	}()
	return out, nil
}

func listenStatements(channel string) (listenSQL, unlistenSQL string, err error) {
	switch channel {
	case "run_queue":
		return "LISTEN run_queue", "UNLISTEN run_queue", nil
	case "run_events":
		return "LISTEN run_events", "UNLISTEN run_events", nil
	default:
		return "", "", fmt.Errorf("store: unknown listen channel %q", channel)
	}
}
