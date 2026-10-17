package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SteerLease is how long a NextSteers claim hides a row from other readers.
// The claim is a committed row update, not a held transaction.
const SteerLease = 15 * time.Second

const steerColumns = `
    id, run_id, kind, text, created_at, delivered_at, actor`

func newSteerOwner() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("store: steer owner: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func scanSteer(row pgx.Row) (Steer, error) {
	var st Steer
	err := row.Scan(&st.ID, &st.RunID, &st.Kind, &st.Text, &st.CreatedAt, &st.DeliveredAt, &st.Actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return Steer{}, ErrNotFound
	}
	if err != nil {
		return Steer{}, err
	}
	return st, nil
}

// EnqueueSteer queues an interrupt or follow-up for a run.
// The harness receives it from NextSteers. actor is a display name, not a credential.
func (s *Store) EnqueueSteer(ctx context.Context, runID, kind, text, actor string) (Steer, error) {
	switch kind {
	case SteerInterrupt, SteerFollowup:
	default:
		return Steer{}, fmt.Errorf("store: steer kind %q is not valid", kind)
	}
	if runID == "" {
		return Steer{}, fmt.Errorf("store: run is required")
	}
	id, err := NewID(PrefixSteer)
	if err != nil {
		return Steer{}, err
	}
	row, err := scanSteer(s.pool.QueryRow(ctx, `
		INSERT INTO steer_requests (id, run_id, kind, text, actor)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+steerColumns, id, runID, kind, text, actor))
	if err != nil {
		if isForeignKey(err) {
			return Steer{}, ErrNotFound
		}
		return Steer{}, fmt.Errorf("store: enqueue steer: %w", err)
	}
	return row, nil
}

// NextSteers claims undelivered steers for runID with id greater than after
// and commits before it returns. after empty starts at the beginning.
// now must be set. A row stays hidden until claim_expires_at, then any store
// can claim it again. MarkSteerDelivered does not need this store's claim.
func (s *Store) NextSteers(ctx context.Context, runID, after string, now time.Time) ([]Steer, error) {
	if runID == "" {
		return nil, fmt.Errorf("store: run is required")
	}
	if err := requireNow(now); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		WITH picked AS (
		    SELECT id
		    FROM steer_requests
		    WHERE run_id = $1
		      AND delivered_at IS NULL
		      AND (claim_expires_at IS NULL OR claim_expires_at <= $5)
		      AND ($2 = '' OR id > $2)
		    ORDER BY id
		    FOR UPDATE SKIP LOCKED
		),
		claimed AS (
		    UPDATE steer_requests
		    SET claimed_by = $3,
		        claim_expires_at = $4
		    FROM picked
		    WHERE steer_requests.id = picked.id
		    RETURNING
		        steer_requests.id,
		        steer_requests.run_id,
		        steer_requests.kind,
		        steer_requests.text,
		        steer_requests.created_at,
		        steer_requests.delivered_at,
		        steer_requests.actor
		)
		SELECT id, run_id, kind, text, created_at, delivered_at, actor
		FROM claimed
		ORDER BY id`, runID, after, s.steerOwner, now.Add(SteerLease), now)
	if err != nil {
		return nil, fmt.Errorf("store: next steers: %w", err)
	}
	defer rows.Close()
	out := []Steer{}
	for rows.Next() {
		st, err := scanSteer(rows)
		if err != nil {
			return nil, fmt.Errorf("store: next steers: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: next steers: %w", err)
	}
	return out, nil
}

// MarkSteerDelivered records that the harness accepted id.
// Any store can mark a steer, including one it did not claim.
// Repeating it is a no-op. A missing id returns ErrNotFound.
func (s *Store) MarkSteerDelivered(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("store: steer id is required")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE steer_requests
		SET delivered_at = clock_timestamp()
		WHERE id = $1 AND delivered_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("store: mark steer: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var one int
	err = s.pool.QueryRow(ctx, `SELECT 1 FROM steer_requests WHERE id = $1`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: mark steer: %w", err)
	}
	return nil
}

func isForeignKey(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
