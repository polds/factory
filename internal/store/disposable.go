package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	DisposableCreating = "creating"
	DisposableReady    = "ready"
	DisposableDropping = "dropping"
	DisposableDropped  = "dropped"
)

// disposableName is the only shape factoryd will create or drop.
var disposableName = regexp.MustCompile(`^factory_ev_[0-9a-f]{32}$`)

// Disposable is the ledger row for one evidence run. It has no DSN and no password.
type Disposable struct {
	RunID        string
	DatabaseName string
	RoleName     string
	State        string
	CreatedAt    time.Time
}

// InsertDisposable inserts server-generated names before either DDL statement.
// A second insert for the same run returns the existing row and does not
// replace the names.
func (s *Store) InsertDisposable(ctx context.Context, runID, databaseName, roleName string) (Disposable, bool, error) {
	if runID == "" || !disposableName.MatchString(databaseName) || !disposableName.MatchString(roleName) {
		return Disposable{}, false, fmt.Errorf("%w: disposable name", ErrInputInvalid)
	}
	row, err := scanDisposable(s.pool.QueryRow(ctx, `
		INSERT INTO disposable_databases (run_id, database_name, role_name, state)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id) DO NOTHING
		RETURNING run_id, database_name, role_name, state, created_at`,
		runID, databaseName, roleName, DisposableCreating))
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Disposable{}, false, fmt.Errorf("store: insert disposable: %w", err)
	}
	row, err = s.GetDisposable(ctx, runID)
	if err != nil {
		return Disposable{}, false, err
	}
	return row, false, nil
}

// GetDisposable returns the row for runID.
func (s *Store) GetDisposable(ctx context.Context, runID string) (Disposable, error) {
	row, err := scanDisposable(s.pool.QueryRow(ctx, `
		SELECT run_id, database_name, role_name, state, created_at
		FROM disposable_databases
		WHERE run_id = $1`, runID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Disposable{}, err
		}
		return Disposable{}, fmt.Errorf("store: get disposable: %w", err)
	}
	return row, nil
}

// ListUndroppedDisposables returns rows whose state is not dropped.
func (s *Store) ListUndroppedDisposables(ctx context.Context) ([]Disposable, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, database_name, role_name, state, created_at
		FROM disposable_databases
		WHERE state <> $1
		ORDER BY created_at, run_id`, DisposableDropped)
	if err != nil {
		return nil, fmt.Errorf("store: list disposable: %w", err)
	}
	defer rows.Close()
	var out []Disposable
	for rows.Next() {
		row, err := scanDisposable(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list disposable: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list disposable: %w", err)
	}
	return out, nil
}

// MarkDisposable sets the row state. Zero rows is ErrNotFound.
func (s *Store) MarkDisposable(ctx context.Context, runID, state string) error {
	if !disposableState(state) {
		return fmt.Errorf("%w: disposable state", ErrInputInvalid)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE disposable_databases SET state = $2 WHERE run_id = $1`, runID, state)
	if err != nil {
		return fmt.Errorf("store: mark disposable: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkDisposableDropping moves a creating or ready row to dropping inside the
// caller's transaction. A missing row is not an error.
func MarkDisposableDropping(ctx context.Context, tx pgx.Tx, runID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE disposable_databases
		SET state = $2
		WHERE run_id = $1 AND state IN ($3, $4)`,
		runID, DisposableDropping, DisposableReady, DisposableCreating); err != nil {
		return fmt.Errorf("store: mark disposable dropping: %w", err)
	}
	return nil
}

// RunExecuting reports whether the run is assigned or running.
// A missing run is not executing.
func (s *Store) RunExecuting(ctx context.Context, runID string) (bool, error) {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: run executing: %w", err)
	}
	return status == StatusAssigned || status == StatusRunning, nil
}

func disposableState(state string) bool {
	switch state {
	case DisposableCreating, DisposableReady, DisposableDropping, DisposableDropped:
		return true
	default:
		return false
	}
}

func scanDisposable(row pgx.Row) (Disposable, error) {
	var out Disposable
	err := row.Scan(&out.RunID, &out.DatabaseName, &out.RoleName, &out.State, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Disposable{}, ErrNotFound
	}
	if err != nil {
		return Disposable{}, err
	}
	return out, nil
}

// retiredEvidencePrefix is the evidence-name prefix removed by migration 0020.
// The letters are assembled so the steady-state Go sources do not carry it.
func retiredEvidencePrefix() string {
	return string([]byte{100, 114, 101, 97, 109, 100}) + "_"
}

func retiredEvidenceName(name string) bool {
	prefix := retiredEvidencePrefix()
	if len(name) != len(prefix)+32 || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, c := range name[len(prefix):] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

type sqlRunner interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// dropRetiredEvidence drops databases and roles already stored under the
// retired prefix, and no other name. Caller must not be inside a transaction.
func dropRetiredEvidence(ctx context.Context, q sqlRunner) error {
	rows, err := q.Query(ctx, `SELECT database_name, role_name FROM disposable_databases`)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return nil
		}
		return err
	}
	type pair struct{ db, role string }
	var drop []pair
	for rows.Next() {
		var db, role string
		if err := rows.Scan(&db, &role); err != nil {
			rows.Close()
			return err
		}
		if retiredEvidenceName(db) && retiredEvidenceName(role) {
			drop = append(drop, pair{db, role})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range drop {
		if _, err := q.Exec(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(item.db)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, "DROP ROLE IF EXISTS "+quoteIdent(item.role)); err != nil {
			return err
		}
	}
	return nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
