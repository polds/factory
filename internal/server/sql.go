package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polds/factory/internal/store"
)

// claim leases the queue row and marks the run assigned.
// The returned prompt is the runs.prompt value committed in this same
// transaction, under the queue-row lock and the runs-row lock taken by
// the status update. A resume that already committed a new prompt is
// visible here. A resume that still needs the queue row waits until this
// transaction ends, in this process or another.
func (s *Server) claim(ctx context.Context, runID string, ttl time.Duration) (string, bool, error) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	// Held across the transaction so a session replace cannot publish
	// a new id, restamp, and then have this commit write the id whose
	// lock already died.
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if !s.sessionHeld || s.sessionID == "" {
		return "", false, nil
	}
	sessionID := s.sessionID
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `
		WITH picked AS (
		    SELECT run_id
		    FROM run_queue
		    WHERE run_id = $1
		      AND (lease_until IS NULL OR lease_until <= clock_timestamp())
		    FOR UPDATE SKIP LOCKED
		)
		UPDATE run_queue q
		SET lease_until = clock_timestamp() + ($2::bigint * interval '1 millisecond'),
		    owner_session = $3
		FROM picked
		WHERE q.run_id = picked.run_id
		RETURNING q.run_id`, runID, ttl.Milliseconds(), sessionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("server: claim: %w", err)
	}
	var prompt string
	err = tx.QueryRow(ctx, `
		UPDATE runs SET status = 'assigned'
		WHERE id = $1 AND status = 'queued'
		RETURNING prompt`, runID).Scan(&prompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("server: claim: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("server: claim: %w", err)
	}
	return prompt, true, nil
}

func (s *Server) releaseClaim(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if _, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'queued', worker_id = NULL
		WHERE id = $1 AND status = 'assigned'`, runID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE run_queue SET lease_until = NULL, owner_session = NULL WHERE run_id = $1`, runID)
	return err
}

func (s *Server) rewind(ctx context.Context, runID, execID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if _, err := s.db.Exec(ctx, `
		UPDATE executions
		SET state = 'orphaned', ended_at = clock_timestamp()
		WHERE id = $1 AND state IN ('assigned', 'running')`, execID); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'queued', worker_id = NULL
		WHERE id = $1 AND status IN ('assigned', 'running')`, runID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE run_queue SET lease_until = NULL, owner_session = NULL WHERE run_id = $1`, runID)
	return err
}

func (s *Server) prepareRequeue(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if _, err := s.db.Exec(ctx, `
		UPDATE executions
		SET state = 'orphaned', ended_at = clock_timestamp()
		WHERE run_id = $1 AND state IN ('assigned', 'running')`, runID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		UPDATE runs SET status = 'assigned', worker_id = NULL
		WHERE id = $1 AND status = 'running'`, runID)
	return err
}

func (s *Server) renewLease(ctx context.Context, runID string, ttl time.Duration) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	s.sessionMu.Lock()
	if !s.sessionHeld || s.sessionID == "" {
		s.sessionMu.Unlock()
		return fmt.Errorf("server: renew lease: session is not held")
	}
	sessionID := s.sessionID
	s.sessionMu.Unlock()
	tag, err := s.db.Exec(ctx, `
		UPDATE run_queue
		SET lease_until = clock_timestamp() + ($2::bigint * interval '1 millisecond')
		WHERE run_id = $1
		  AND lease_until IS NOT NULL
		  AND owner_session = $3`, runID, ttl.Milliseconds(), sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("server: renew lease: run %s is not leased by this server", runID)
	}
	return nil
}

func (s *Server) liveExecution(ctx context.Context, runID string) (string, error) {
	if id := s.execForRun(runID); id != "" {
		var state string
		err := s.db.QueryRow(ctx, `SELECT state FROM executions WHERE id = $1`, id).Scan(&state)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if state == store.ExecAssigned || state == store.ExecRunning {
			return id, nil
		}
	}
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM executions
		WHERE run_id = $1 AND state IN ('assigned', 'running')
		ORDER BY attempt DESC
		LIMIT 1`, runID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) executionForEvent(ctx context.Context, runID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `
		SELECT id FROM executions WHERE run_id = $1 ORDER BY attempt DESC LIMIT 1`, runID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id, err = store.NewID(store.PrefixExec)
	if err != nil {
		return "", err
	}
	// Not a run token. The column is NOT NULL and this execution never runs.
	var sum [32]byte
	tag, err := s.db.Exec(ctx, `
		INSERT INTO executions (id, run_id, attempt, worker_id, state, run_token_sha256, ended_at)
		VALUES ($1, $2, 1, '', 'cancelled', $3, clock_timestamp())
		ON CONFLICT (run_id, attempt) DO NOTHING`, id, runID, sum[:])
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return id, nil
	}
	err = s.db.QueryRow(ctx, `SELECT id FROM executions WHERE run_id = $1 AND attempt = 1`, runID).Scan(&id)
	return id, err
}

// forceExpire drops the lease so ExpireLeases requeues the run. The
// execution becomes orphaned. That state is not written onto the run as
// worker_lost: the run is queued again and has no failure class.
func (s *Server) forceExpire(ctx context.Context, runID string) error {
	s.leaseMu.Lock()
	if _, err := s.db.Exec(ctx, `
		UPDATE run_queue SET lease_until = '1970-01-02T00:00:00Z'
		WHERE run_id = $1`, runID); err != nil {
		s.leaseMu.Unlock()
		return err
	}
	s.leaseMu.Unlock()
	orphans, err := s.store.ExpireLeases(ctx, s.now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	for _, orphan := range orphans {
		if id, ok := s.byRun[orphan.RunID]; ok && id == orphan.ExecutionID {
			delete(s.byRun, orphan.RunID)
		}
		delete(s.live, orphan.ExecutionID)
	}
	s.mu.Unlock()
	return nil
}

func (s *Server) matchToken(ctx context.Context, runID string, sum []byte) (string, bool, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, run_token_sha256
		FROM executions
		WHERE run_id = $1
		ORDER BY attempt DESC`, runID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var matched string
	found := 0
	for rows.Next() {
		var id string
		var hash []byte
		if err := rows.Scan(&id, &hash); err != nil {
			return "", false, err
		}
		found++
		if subtle.ConstantTimeCompare(hash, sum) == 1 && matched == "" {
			matched = id
		}
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if found == 0 {
		var dummy [32]byte
		subtle.ConstantTimeCompare(dummy[:], sum)
		return "", false, nil
	}
	return matched, matched != "", nil
}

func (s *Server) isLeader(ctx context.Context) bool {
	s.leaderMu.Lock()
	defer s.leaderMu.Unlock()
	if s.leader == nil {
		return false
	}
	var ok bool
	if err := s.leader.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int4, $2::int4)`, leaderLockClass, leaderLockObj).Scan(&ok); err != nil {
		s.log.Error("leader lock", "err", err)
		return false
	}
	return ok
}

func newServerSessionID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "srv_" + hex.EncodeToString(buf[:]), nil
}

func serverSessionLockKey(sessionID string) string {
	return "factory-server:" + sessionID
}

func lockServerSession(ctx context.Context, conn *pgx.Conn, sessionID string) error {
	_, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, $2::bigint))`, serverSessionLockKey(sessionID), serverSessionLockSalt)
	return err
}

// ownerSessionHeld reports whether sessionID's process still holds its
// session advisory lock. An empty id is a lease written by a process
// that does not know this column. That lease stays owned until
// ExpireLeases. A successful probe takes the lock only long enough to
// learn that the owner is gone, then releases it on the same connection.
func (s *Server) ownerSessionHeld(ctx context.Context, sessionID string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	if sessionID == "" {
		return true, nil
	}
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return false, err
	}
	held, err := probeSessionLock(ctx, conn, sessionID)
	if err != nil {
		raw := conn.Hijack()
		_ = raw.Close(ctx)
		return false, err
	}
	conn.Release()
	return held, nil
}

func probeSessionLock(ctx context.Context, conn *pgxpool.Conn, sessionID string) (bool, error) {
	key := serverSessionLockKey(sessionID)
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, $2::bigint))`, key, serverSessionLockSalt).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return true, nil
	}
	var released bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, $2::bigint))`, key, serverSessionLockSalt).Scan(&released); err != nil {
		return false, err
	}
	if !released {
		return false, errors.New("server: owner session lock was not released")
	}
	return false, nil
}

// watchSession blocks on the session connection. A reset, an idle reaper,
// or a closed socket ends that read. The process then stops claiming under
// the dead id, locks a new id, and compare-and-sets tracked rows onto it.
// One sweep can win the gap. The next claim uses the id this process holds.
func (s *Server) watchSession() {
	defer s.wg.Done()
	for {
		if s.ctx.Err() != nil {
			return
		}
		conn, id, ok := s.currentSessionConn()
		if ok {
			_, err := conn.PgConn().ReceiveMessage(s.ctx)
			if s.ctx.Err() != nil {
				return
			}
			if err == nil {
				continue
			}
			if !s.retireSession(id, conn) {
				continue
			}
			_ = conn.Close(context.Background())
		}
		if err := s.acquireSession(s.ctx, id); err != nil {
			if s.ctx.Err() != nil {
				return
			}
			s.log.Error("session acquire", "err", err)
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (s *Server) currentSessionConn() (*pgx.Conn, string, bool) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if !s.sessionHeld || s.session == nil {
		return nil, s.sessionID, false
	}
	return s.session, s.sessionID, true
}

func (s *Server) retireSession(id string, conn *pgx.Conn) bool {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.session != conn || s.sessionID != id || !s.sessionHeld {
		return false
	}
	s.sessionHeld = false
	s.session = nil
	return true
}

func (s *Server) acquireSession(ctx context.Context, oldID string) error {
	if s.sessionCfg == nil {
		return errors.New("server: session config is missing")
	}
	newID, err := newServerSessionID()
	if err != nil {
		return err
	}
	conn, err := pgx.ConnectConfig(ctx, s.sessionCfg.Copy())
	if err != nil {
		return err
	}
	if err := lockServerSession(ctx, conn, newID); err != nil {
		_ = conn.Close(ctx)
		return err
	}
	s.sessionMu.Lock()
	if ctx.Err() != nil || s.ctx.Err() != nil || (s.sessionHeld && s.session != nil) {
		s.sessionMu.Unlock()
		_ = conn.Close(context.Background())
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	if oldID != "" && oldID != newID {
		if err := s.restampTracked(ctx, oldID, newID); err != nil {
			s.sessionMu.Unlock()
			_ = conn.Close(context.Background())
			return err
		}
	}
	s.session = conn
	s.sessionID = newID
	s.sessionHeld = true
	hook := s.afterSessionRecover
	s.sessionMu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

// restampTracked moves runs this process still tracks from the dead
// session id to the id whose lock it just acquired. A row that no longer
// has the old id was requeued or claimed elsewhere and stays there.
func (s *Server) restampTracked(ctx context.Context, oldID, newID string) error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.byRun))
	for runID := range s.byRun {
		ids = append(ids, runID)
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		UPDATE run_queue
		SET owner_session = $1
		WHERE owner_session = $2
		  AND run_id = ANY($3)`, newID, oldID, ids)
	return err
}

// dropSessionConn closes the socket under the session lock. The leader
// connection and the worker sockets stay up. The watch loop sees the
// read fail and opens a replacement.
func (s *Server) dropSessionConn() error {
	s.sessionMu.Lock()
	conn := s.session
	s.sessionMu.Unlock()
	if conn == nil {
		return errors.New("server: no session connection")
	}
	return conn.PgConn().Conn().Close()
}
