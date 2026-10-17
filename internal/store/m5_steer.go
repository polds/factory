package store

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type steerClaim struct {
	mu     sync.Mutex
	tx     pgx.Tx
	ids    []string
	left   int
	done   map[string]bool
	closed bool
}

const steerColumns = `
    id, run_id, kind, text, created_at, delivered_at, actor`

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

// NextSteers returns undelivered steers for runID with id greater than after.
// after empty starts at the beginning. Each id is returned to one caller:
// the rows are locked FOR UPDATE SKIP LOCKED until MarkSteerDelivered.
// Close rolls the locks back, so a crash leaves the steers for the next poller.
func (s *Store) NextSteers(ctx context.Context, runID, after string) ([]Steer, error) {
	if runID == "" {
		return nil, fmt.Errorf("store: run is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: next steers: %w", err)
	}
	held := false
	defer func() {
		if !held {
			_ = tx.Rollback(ctx)
		}
	}()
	rows, err := tx.Query(ctx, `
		SELECT `+steerColumns+`
		FROM steer_requests
		WHERE run_id = $1
		  AND delivered_at IS NULL
		  AND ($2 = '' OR id > $2)
		ORDER BY id
		FOR UPDATE SKIP LOCKED`, runID, after)
	if err != nil {
		return nil, fmt.Errorf("store: next steers: %w", err)
	}
	out := []Steer{}
	var scanErr error
	for rows.Next() {
		var st Steer
		st, scanErr = scanSteer(rows)
		if scanErr != nil {
			break
		}
		out = append(out, st)
	}
	if scanErr == nil {
		scanErr = rows.Err()
	}
	rows.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("store: next steers: %w", scanErr)
	}
	if len(out) == 0 {
		return []Steer{}, nil
	}
	hold := &steerClaim{
		tx:   tx,
		ids:  make([]string, len(out)),
		left: len(out),
		done: map[string]bool{},
	}
	for i := range out {
		hold.ids[i] = out[i].ID
	}
	s.steerMu.Lock()
	if s.steerClaims == nil {
		s.steerClaims = map[string]*steerClaim{}
	}
	for _, id := range hold.ids {
		s.steerClaims[id] = hold
	}
	s.steerMu.Unlock()
	held = true
	return out, nil
}

// MarkSteerDelivered records that the harness accepted id.
// Repeating it is a no-op. A missing id returns ErrNotFound.
// When id was returned by NextSteers, the update commits on the claim
// that locked it, once every id in that batch is marked.
func (s *Store) MarkSteerDelivered(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("store: steer id is required")
	}
	if hold, ok := s.steerHold(id); ok {
		return s.markSteerClaim(ctx, hold, id)
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

func (s *Store) steerHold(id string) (*steerClaim, bool) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	hold, ok := s.steerClaims[id]
	if !ok {
		return nil, false
	}
	return hold, true
}

func (s *Store) markSteerClaim(ctx context.Context, hold *steerClaim, id string) error {
	hold.mu.Lock()
	err := s.markSteerLocked(ctx, hold, id)
	release := hold.closed
	hold.mu.Unlock()
	if release {
		s.dropSteerClaim(hold)
	}
	return err
}

func (s *Store) markSteerLocked(ctx context.Context, hold *steerClaim, id string) error {
	if hold.done[id] {
		return nil
	}
	if hold.closed {
		return fmt.Errorf("store: steer claim closed")
	}
	tag, err := hold.tx.Exec(ctx, `
		UPDATE steer_requests
		SET delivered_at = clock_timestamp()
		WHERE id = $1 AND delivered_at IS NULL`, id)
	if err != nil {
		hold.closed = true
		_ = hold.tx.Rollback(context.Background())
		return fmt.Errorf("store: mark steer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var one int
		err = hold.tx.QueryRow(ctx, `SELECT 1 FROM steer_requests WHERE id = $1`, id).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			if cerr := s.finishSteerLocked(ctx, hold, id); cerr != nil {
				return cerr
			}
			return ErrNotFound
		}
		if err != nil {
			hold.closed = true
			_ = hold.tx.Rollback(context.Background())
			return fmt.Errorf("store: mark steer: %w", err)
		}
	}
	return s.finishSteerLocked(ctx, hold, id)
}

func (s *Store) finishSteerLocked(ctx context.Context, hold *steerClaim, id string) error {
	hold.done[id] = true
	hold.left--
	if hold.left > 0 {
		return nil
	}
	if err := hold.tx.Commit(ctx); err != nil {
		hold.closed = true
		_ = hold.tx.Rollback(context.Background())
		return fmt.Errorf("store: commit steer claim: %w", err)
	}
	hold.closed = true
	return nil
}

func (s *Store) dropSteerClaim(hold *steerClaim) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	for _, id := range hold.ids {
		if s.steerClaims[id] == hold {
			delete(s.steerClaims, id)
		}
	}
}

func (s *Store) abortSteerClaims() {
	s.steerMu.Lock()
	seen := map[*steerClaim]struct{}{}
	holds := []*steerClaim{}
	for _, hold := range s.steerClaims {
		if _, ok := seen[hold]; ok {
			continue
		}
		seen[hold] = struct{}{}
		holds = append(holds, hold)
	}
	s.steerClaims = nil
	s.steerMu.Unlock()
	for _, hold := range holds {
		hold.mu.Lock()
		if !hold.closed {
			hold.closed = true
			_ = hold.tx.Rollback(context.Background())
		}
		hold.mu.Unlock()
	}
}

func isForeignKey(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
