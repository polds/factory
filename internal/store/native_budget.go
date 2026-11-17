package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polds/factory/internal/coordination/budget"
)

type scopeAccount struct {
	ID        string
	Kind      string
	Ceiling   int64
	Spent     int64
	Reserved  int64
	Paused    bool
	Missing   bool
	Episode   int
	Alerted   bool
	SharedKey bool
}

// ReserveAncestors holds cents on every ancestor scope.
func (s *Store) ReserveAncestors(ctx context.Context, in ReserveInput) (Reservation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reservation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := reserveTx(ctx, tx, in)
	if err != nil {
		return Reservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, err
	}
	return out, nil
}

// reserveTx locks scope rows in scope_id order.
// partialOnFailure is false: a ceiling miss rolls the whole reservation back.
func reserveTx(ctx context.Context, tx pgx.Tx, in ReserveInput) (Reservation, error) {
	if in.Cents < 0 || in.IdempotencyKey == "" || len(in.ScopeIDs) == 0 {
		return Reservation{}, fmt.Errorf("%w: reservation", ErrInputInvalid)
	}
	if err := lockKey(ctx, tx, "reserve:"+in.InvocationID+":"+in.IdempotencyKey); err != nil {
		return Reservation{}, err
	}
	var existingID string
	var existingCents int64
	var existingState string
	err := tx.QueryRow(ctx, `
		SELECT id, cents, state FROM budget_reservations
		WHERE invocation_id = $1 AND idempotency_key = $2`,
		in.InvocationID, in.IdempotencyKey).Scan(&existingID, &existingCents, &existingState)
	if err == nil {
		return Reservation{ID: existingID, InvocationID: in.InvocationID, Cents: existingCents, State: existingState, ScopeIDs: in.ScopeIDs, Replay: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, err
	}
	if err := ensureScopesTx(ctx, tx, in); err != nil {
		return Reservation{}, err
	}
	accounts, err := lockScopesTx(ctx, tx, in.ScopeIDs)
	if err != nil {
		return Reservation{}, err
	}
	bound, err := overdueReservedTx(ctx, tx, in.InvocationID, time.Now())
	if err != nil {
		return Reservation{}, err
	}
	if bound > 0 {
		if err := markMissingTx(ctx, tx, in.ScopeIDs); err != nil {
			return Reservation{}, err
		}
		return Reservation{}, &BudgetDenied{Bound: bound}
	}
	for _, account := range accounts {
		if account.Paused || account.Missing || account.Spent+account.Reserved+in.Cents > account.Ceiling {
			return Reservation{}, ErrBudgetExceeded
		}
	}
	if err := adjustFactoryBudgetsTx(ctx, tx, in.InvocationID, in.Cents, 0, true); err != nil {
		return Reservation{}, err
	}
	id, err := NewID(PrefixNativeReservation)
	if err != nil {
		return Reservation{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO budget_reservations (
		    id, invocation_id, idempotency_key, task_id, cents, state, usage_deadline)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, 'reserved', $6)`,
		id, in.InvocationID, in.IdempotencyKey, in.TaskID, in.Cents, in.UsageDeadline); err != nil {
		return Reservation{}, fmt.Errorf("store: reservation: %w", err)
	}
	for _, account := range accounts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_reservation_lines (reservation_id, scope_id, cents)
			VALUES ($1, $2, $3)`, id, account.ID, in.Cents); err != nil {
			return Reservation{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE budget_scopes
			SET reserved_cents = reserved_cents + $2
			WHERE scope_id = $1`, account.ID, in.Cents); err != nil {
			return Reservation{}, err
		}
	}
	return Reservation{ID: id, InvocationID: in.InvocationID, Cents: in.Cents, State: "reserved", ScopeIDs: in.ScopeIDs}, nil
}

// SettleUsage counts integer cents once per call_id, attempt_id, including a failed attempt.
// A late report above the reservation is quarantined and the scope pauses.
func (s *Store) SettleUsage(ctx context.Context, in SettleInput) (Settlement, error) {
	if in.CallID == "" || in.AttemptID == "" || in.Cents < 0 {
		return Settlement{}, fmt.Errorf("%w: usage", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Settlement{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := settleTx(ctx, tx, in)
	if err != nil {
		return Settlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Settlement{}, err
	}
	return out, nil
}

func settleTx(ctx context.Context, tx pgx.Tx, in SettleInput) (Settlement, error) {
	var dupCents int64
	err := tx.QueryRow(ctx, `
		INSERT INTO budget_charges (call_id, attempt_id, reservation_id, cents, failed)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5)
		ON CONFLICT (call_id, attempt_id) DO NOTHING
		RETURNING cents`, in.CallID, in.AttemptID, in.ReservationID, in.Cents, in.Failed).Scan(&dupCents)
	if errors.Is(err, pgx.ErrNoRows) {
		var counted int64
		if err := tx.QueryRow(ctx, `
			SELECT cents FROM budget_charges WHERE call_id = $1 AND attempt_id = $2`,
			in.CallID, in.AttemptID).Scan(&counted); err != nil {
			return Settlement{}, err
		}
		return Settlement{CallID: in.CallID, AttemptID: in.AttemptID, CountedCents: counted, Duplicate: true}, nil
	}
	if err != nil {
		return Settlement{}, fmt.Errorf("store: usage: %w", err)
	}
	if in.ReservationID == "" {
		return Settlement{CallID: in.CallID, AttemptID: in.AttemptID, CountedCents: in.Cents}, nil
	}
	var reserved int64
	var state, invocationID string
	if err := tx.QueryRow(ctx, `
		SELECT cents, state, invocation_id FROM budget_reservations WHERE id = $1 FOR UPDATE`, in.ReservationID).Scan(&reserved, &state, &invocationID); err != nil {
		return Settlement{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT scope_id, cents FROM budget_reservation_lines
		WHERE reservation_id = $1
		ORDER BY scope_id`, in.ReservationID)
	if err != nil {
		return Settlement{}, err
	}
	type line struct {
		id    string
		cents int64
	}
	var lines []line
	for rows.Next() {
		var ln line
		if err := rows.Scan(&ln.id, &ln.cents); err != nil {
			rows.Close()
			return Settlement{}, err
		}
		lines = append(lines, ln)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Settlement{}, err
	}
	ids := make([]string, len(lines))
	for i, ln := range lines {
		ids[i] = ln.id
	}
	if _, err := lockScopesTx(ctx, tx, ids); err != nil {
		return Settlement{}, err
	}
	// A new attempt_id is a new charge. Same-pair dedupe already returned.
	// The reservation hold was released by the first settlement, so this
	// attempt spends against the ceiling and does not release reserved again.
	if state != "reserved" {
		for _, ln := range lines {
			if _, err := creditScopeTx(ctx, tx, ln.id, in.Cents); err != nil {
				return Settlement{}, err
			}
		}
		return settleCounted(ctx, tx, invocationID, in)
	}
	counted := in.Cents
	quarantined := int64(0)
	if counted > reserved {
		quarantined = counted - reserved
		counted = reserved
	}
	for _, ln := range lines {
		release := ln.cents
		if _, err := tx.Exec(ctx, `
			UPDATE budget_scopes
			SET reserved_cents = reserved_cents - $2
			WHERE scope_id = $1 AND reserved_cents >= $2`, ln.id, release); err != nil {
			return Settlement{}, err
		}
		if counted > 0 {
			if _, err := creditScopeTx(ctx, tx, ln.id, counted); err != nil {
				return Settlement{}, err
			}
		}
		if quarantined > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE budget_scopes SET paused = true WHERE scope_id = $1`, ln.id); err != nil {
				return Settlement{}, err
			}
			qid, err := NewID(PrefixNativeReservation)
			if err != nil {
				return Settlement{}, err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO budget_quarantine (
				    id, call_id, attempt_id, scope_id, reported_cents, reservation_cents, excess_cents)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				qid, in.CallID, in.AttemptID, ln.id, in.Cents, reserved, quarantined); err != nil {
				return Settlement{}, err
			}
		}
	}
	if err := finishSettle(
		ctx, tx, invocationID, in, reserved, counted, quarantined,
	); err != nil {
		return Settlement{}, err
	}
	nextState := "settled"
	if counted < reserved {
		nextState = "refunded"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE budget_reservations SET state = $2 WHERE id = $1`, in.ReservationID, nextState); err != nil {
		return Settlement{}, err
	}
	if quarantined == 0 {
		if err := clearMissingIfCurrentTx(ctx, tx, invocationID, ids); err != nil {
			return Settlement{}, err
		}
	}
	out := Settlement{
		CallID: in.CallID, AttemptID: in.AttemptID, CountedCents: counted,
		QuarantinedCents: quarantined, Paused: quarantined > 0,
	}
	if quarantined > 0 {
		out.Code = "usage_late_over_reservation"
	}
	return out, nil
}

// RefundUsage subtracts cents once and can open a new alert episode.
func (s *Store) RefundUsage(ctx context.Context, in RefundInput) (Settlement, error) {
	if in.Cents <= 0 || in.CallID == "" || in.AttemptID == "" {
		return Settlement{}, fmt.Errorf("%w: refund", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Settlement{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		INSERT INTO budget_charges (call_id, attempt_id, cents, failed, quarantined_cents)
		VALUES ($1, $2, $3, false, 0)
		ON CONFLICT (call_id, attempt_id) DO NOTHING
		RETURNING cents`, in.CallID, in.AttemptID, -in.Cents).Scan(new(int64))
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return Settlement{}, err
		}
		return Settlement{CallID: in.CallID, AttemptID: in.AttemptID, Duplicate: true}, nil
	}
	if err != nil {
		return Settlement{}, err
	}
	sort.Strings(in.ScopeIDs)
	if _, err := lockScopesTx(ctx, tx, in.ScopeIDs); err != nil {
		return Settlement{}, err
	}
	for _, id := range in.ScopeIDs {
		if _, err := creditScopeTx(ctx, tx, id, -in.Cents); err != nil {
			return Settlement{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Settlement{}, err
	}
	return Settlement{CallID: in.CallID, AttemptID: in.AttemptID, CountedCents: -in.Cents}, nil
}

// ApplyBudgetDelta moves spent on one scope and emits the 80 percent alert.
func (s *Store) ApplyBudgetDelta(ctx context.Context, scopeID, callID, attemptID string, delta int64) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var inserted int64
	err = tx.QueryRow(ctx, `
		INSERT INTO budget_charges (call_id, attempt_id, cents, failed)
		VALUES ($1, $2, $3, false)
		ON CONFLICT (call_id, attempt_id) DO NOTHING
		RETURNING cents`, callID, attemptID, delta).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		var alerts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM budget_alerts WHERE scope_id = $1`, scopeID).Scan(&alerts); err != nil {
			return 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return alerts, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err := lockScopesTx(ctx, tx, []string{scopeID}); err != nil {
		return 0, err
	}
	if _, err := creditScopeTx(ctx, tx, scopeID, delta); err != nil {
		return 0, err
	}
	var alerts int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM budget_alerts WHERE scope_id = $1`, scopeID).Scan(&alerts); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return alerts, nil
}

// Admit refuses new spend after a usage deadline or a pause.
// The overshoot bound is the outstanding reservation. guaranteesCeiling is
// false for a shared key. A usage deadline that has passed admits nothing new.
func (s *Store) Admit(ctx context.Context, in AdmitInput) (AdmitResult, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AdmitResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	accounts, err := lockScopesTx(ctx, tx, in.ScopeIDs)
	if err != nil {
		return AdmitResult{}, err
	}
	bound, err := overdueReservedTx(ctx, tx, in.InvocationID, in.Now)
	if err != nil {
		return AdmitResult{}, err
	}
	shared := in.SharedKey
	for _, account := range accounts {
		if account.SharedKey {
			shared = true
		}
		if account.Paused || account.Missing {
			bound = account.Reserved
			if err := tx.Commit(ctx); err != nil {
				return AdmitResult{}, err
			}
			return AdmitResult{OvershootBoundCents: bound, GuaranteesCeiling: budget.GuaranteesCeiling(shared, false)}, &BudgetDenied{Bound: bound}
		}
	}
	if bound > 0 {
		if err := markMissingTx(ctx, tx, in.ScopeIDs); err != nil {
			return AdmitResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return AdmitResult{}, err
		}
		return AdmitResult{OvershootBoundCents: bound, GuaranteesCeiling: budget.GuaranteesCeiling(shared, false)}, &BudgetDenied{Bound: bound}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE budget_scopes
		SET guarantees_ceiling = $2
		WHERE invocation_id = $1`, in.InvocationID, budget.GuaranteesCeiling(shared, false)); err != nil {
		return AdmitResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdmitResult{}, err
	}
	return AdmitResult{Admitted: true, GuaranteesCeiling: budget.GuaranteesCeiling(shared, false)}, nil
}

// AuthorizeCeiling changes one ceiling and writes the audit row before commit.
// The audit spends nothing.
func (s *Store) AuthorizeCeiling(ctx context.Context, in CeilingInput) (CeilingAudit, error) {
	if in.Principal != CeilingPrincipal {
		return CeilingAudit{}, ErrPrincipalRejected
	}
	if in.NewCeiling < 0 {
		return CeilingAudit{}, fmt.Errorf("%w: ceiling", ErrInputInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CeilingAudit{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	accounts, err := lockScopesTx(ctx, tx, []string{in.ScopeID})
	if err != nil {
		return CeilingAudit{}, err
	}
	if len(accounts) != 1 {
		return CeilingAudit{}, ErrNotFound
	}
	account := accounts[0]
	if in.NewCeiling < account.Spent+account.Reserved {
		return CeilingAudit{}, ErrBudgetExceeded
	}
	id, err := NewID(PrefixNativeReservation)
	if err != nil {
		return CeilingAudit{}, err
	}
	audit := CeilingAudit{
		ID: id, ScopeID: in.ScopeID, Principal: in.Principal,
		PriorCeiling: account.Ceiling, NewCeiling: in.NewCeiling, SpentCents: 0,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO budget_overrides (
		    id, scope_id, principal, prior_ceiling, new_ceiling, spent_cents)
		VALUES ($1, $2, $3, $4, $5, 0)`,
		audit.ID, audit.ScopeID, audit.Principal, audit.PriorCeiling, audit.NewCeiling); err != nil {
		return CeilingAudit{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE budget_scopes SET ceiling_cents = $2 WHERE scope_id = $1`, in.ScopeID, in.NewCeiling); err != nil {
		return CeilingAudit{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CeilingAudit{}, err
	}
	return audit, nil
}

// RollupSpent reads each scope's spent once. It does not add those totals together.
func (s *Store) RollupSpent(ctx context.Context, invocationID string) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, spent_cents FROM budget_scopes WHERE invocation_id = $1`, invocationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var spent int64
		if err := rows.Scan(&kind, &spent); err != nil {
			return nil, err
		}
		out[kind] += spent
	}
	return out, rows.Err()
}

// OvershootBeyondCeiling reads the account and returns the contract bound.
func (s *Store) OvershootBeyondCeiling(ctx context.Context, scopeID string) (int64, error) {
	accounts, err := s.readScope(ctx, scopeID)
	if err != nil {
		return 0, err
	}
	return budget.OvershootBeyondCeiling(accounts.Spent, accounts.Reserved, accounts.Ceiling), nil
}

func (s *Store) readScope(ctx context.Context, scopeID string) (scopeAccount, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return scopeAccount{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	accounts, err := lockScopesTx(ctx, tx, []string{scopeID})
	if err != nil {
		return scopeAccount{}, err
	}
	if len(accounts) != 1 {
		return scopeAccount{}, ErrNotFound
	}
	return accounts[0], nil
}

func ensureScopesTx(ctx context.Context, tx pgx.Tx, in ReserveInput) error {
	sort.Strings(in.ScopeIDs)
	for _, id := range in.ScopeIDs {
		kind := scopeKind(id)
		if kind == "" {
			return fmt.Errorf("%w: scope %s", ErrInputInvalid, id)
		}
		guarantees := budget.GuaranteesCeiling(in.SharedKey, false)
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_scopes (
			    scope_id, invocation_id, kind, ceiling_cents, shared_key, guarantees_ceiling)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (scope_id) DO NOTHING`,
			id, in.InvocationID, kind, in.CeilingCents, in.SharedKey, guarantees); err != nil {
			return fmt.Errorf("store: budget scope: %w", err)
		}
	}
	return nil
}

// lockScopesTx locks scope rows in scope_id order so two reservations cannot both pass one ceiling.
func lockScopesTx(ctx context.Context, tx pgx.Tx, ids []string) ([]scopeAccount, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: scopes", ErrInputInvalid)
	}
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	out := make([]scopeAccount, 0, len(sorted))
	for _, id := range sorted {
		var account scopeAccount
		err := tx.QueryRow(ctx, `
			SELECT scope_id, kind, ceiling_cents, spent_cents, reserved_cents,
			       paused, missing_usage, alert_episode, alerted_in_episode, shared_key
			FROM budget_scopes
			WHERE scope_id = $1
			FOR UPDATE`, id).Scan(
			&account.ID, &account.Kind, &account.Ceiling, &account.Spent, &account.Reserved,
			&account.Paused, &account.Missing, &account.Episode, &account.Alerted, &account.SharedKey,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: scope %s", ErrNotFound, id)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, nil
}

func creditScopeTx(ctx context.Context, tx pgx.Tx, scopeID string, delta int64) (bool, error) {
	var ceiling, spent, reserved int64
	var episode int
	var alerted bool
	err := tx.QueryRow(ctx, `
		SELECT ceiling_cents, spent_cents, reserved_cents, alert_episode, alerted_in_episode
		FROM budget_scopes WHERE scope_id = $1 FOR UPDATE`, scopeID).Scan(&ceiling, &spent, &reserved, &episode, &alerted)
	if err != nil {
		return false, err
	}
	nextSpent, ep, emitted := budget.Step(ceiling, spent, delta, budget.Episode{Episode: episode, Alerted: alerted})
	if nextSpent+reserved > ceiling {
		return false, ErrBudgetExceeded
	}
	if _, err := tx.Exec(ctx, `
		UPDATE budget_scopes
		SET spent_cents = $2, alert_episode = $3, alerted_in_episode = $4
		WHERE scope_id = $1`, scopeID, nextSpent, ep.Episode, ep.Alerted); err != nil {
		return false, err
	}
	if emitted {
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_alerts (scope_id, episode, threshold_percent)
			VALUES ($1, $2, $3)
			ON CONFLICT (scope_id, episode) DO NOTHING`,
			scopeID, ep.Episode, budget.ThresholdPercent); err != nil {
			return false, err
		}
	}
	return emitted, nil
}

func overdueReservedTx(ctx context.Context, tx pgx.Tx, invocationID string, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, nil
	}
	var bound int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(sum(cents), 0)
		FROM budget_reservations
		WHERE invocation_id = $1
		  AND state = 'reserved'
		  AND usage_deadline IS NOT NULL
		  AND usage_deadline < $2`, invocationID, now).Scan(&bound)
	return bound, err
}

func clearMissingIfCurrentTx(ctx context.Context, tx pgx.Tx, invocationID string, ids []string) error {
	bound, err := overdueReservedTx(ctx, tx, invocationID, time.Now())
	if err != nil || bound > 0 {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			UPDATE budget_scopes SET missing_usage = false WHERE scope_id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

func markMissingTx(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			UPDATE budget_scopes SET missing_usage = true WHERE scope_id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

func scopeKind(id string) string {
	switch {
	case len(id) >= 8 && id[:8] == "factory:":
		return "factory"
	case len(id) >= 11 && id[:11] == "invocation:":
		return "invocation"
	case len(id) >= 5 && id[:5] == "lead:":
		return "lead"
	case len(id) >= 6 && id[:6] == "agent:":
		return "agent"
	default:
		return ""
	}
}

func settleCounted(ctx context.Context, tx pgx.Tx, invocationID string, in SettleInput) (Settlement, error) {
	if err := adjustFactoryBudgetsTx(ctx, tx, invocationID, 0, in.Cents, true); err != nil {
		return Settlement{}, err
	}
	return Settlement{CallID: in.CallID, AttemptID: in.AttemptID, CountedCents: in.Cents}, nil
}

func finishSettle(ctx context.Context, tx pgx.Tx, invocationID string, in SettleInput, reserved, counted, quarantined int64) error {
	if _, err := tx.Exec(ctx, `
		UPDATE budget_charges SET quarantined_cents = $3 WHERE call_id = $1 AND attempt_id = $2`,
		in.CallID, in.AttemptID, quarantined); err != nil {
		return err
	}
	return adjustFactoryBudgetsTx(ctx, tx, invocationID, -reserved, counted, false)
}
