package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// FactoryBudget is one factory or repository ceiling that outlives an invocation.
type FactoryBudget struct {
	FactoryID     string
	ScopeKey      string
	CeilingCents  int64
	SpentCents    int64
	ReservedCents int64
}

// FrozenRun is the price, publish narrowing, and evidence paths copied at open.
type FrozenRun struct {
	RatesJSON json.RawMessage
	IssueKey  string
	Title     string
	Body      string
	RefPrefix string
	Package   string
	Mutation  string
	Sentinel  string
	Break     string
}

type frozenDocument struct {
	Rates    json.RawMessage `json:"rates,omitempty"`
	Publish  *frozenPublish  `json:"publish,omitempty"`
	Evidence *frozenEvidence `json:"evidence,omitempty"`
}

type frozenPublish struct {
	IssueKey  string `json:"issueKey,omitempty"`
	Title     string `json:"title,omitempty"`
	Body      string `json:"body,omitempty"`
	RefPrefix string `json:"refPrefix,omitempty"`
}

type frozenEvidence struct {
	Package  string `json:"package,omitempty"`
	Mutation string `json:"mutation,omitempty"`
	Sentinel string `json:"sentinel,omitempty"`
	Break    string `json:"break,omitempty"`
}

// UpsertFactoryBudget inserts or updates a ceiling. Spent and reserved cents
// are left in place when the row already exists.
func (s *Store) UpsertFactoryBudget(ctx context.Context, factoryID, scopeKey string, ceiling int64) error {
	if factoryID == "" || ceiling < 0 {
		return fmt.Errorf("%w: factory budget", ErrInputInvalid)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO factory_budgets (factory_id, scope_key, ceiling_cents)
		VALUES ($1, $2, $3)
		ON CONFLICT (factory_id, scope_key)
		DO UPDATE SET ceiling_cents = EXCLUDED.ceiling_cents`,
		factoryID, scopeKey, ceiling); err != nil {
		return fmt.Errorf("store: factory budget: %w", err)
	}
	return nil
}

// FactoryBudget returns one ceiling row.
func (s *Store) FactoryBudget(ctx context.Context, factoryID, scopeKey string) (FactoryBudget, error) {
	var row FactoryBudget
	err := s.pool.QueryRow(ctx, `
		SELECT factory_id, scope_key, ceiling_cents, spent_cents, reserved_cents
		FROM factory_budgets
		WHERE factory_id = $1 AND scope_key = $2`, factoryID, scopeKey).Scan(
		&row.FactoryID, &row.ScopeKey, &row.CeilingCents, &row.SpentCents, &row.ReservedCents,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return FactoryBudget{}, ErrNotFound
	}
	if err != nil {
		return FactoryBudget{}, fmt.Errorf("store: factory budget: %w", err)
	}
	return row, nil
}

// CountFactories is the number of factory rows.
func (s *Store) CountFactories(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM factories`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count factories: %w", err)
	}
	return n, nil
}

// PutFrozenRun stores the invocation copy of rates, publish narrowing, and evidence.
func (s *Store) PutFrozenRun(ctx context.Context, invocationID string, doc FrozenRun) error {
	if invocationID == "" {
		return fmt.Errorf("%w: frozen run", ErrInputInvalid)
	}
	body, err := json.Marshal(frozenDocument{
		Rates: doc.RatesJSON,
		Publish: &frozenPublish{
			IssueKey: doc.IssueKey, Title: doc.Title, Body: doc.Body, RefPrefix: doc.RefPrefix,
		},
		Evidence: &frozenEvidence{
			Package: doc.Package, Mutation: doc.Mutation, Sentinel: doc.Sentinel, Break: doc.Break,
		},
	})
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE invocations SET frozen_json = $2::jsonb WHERE id = $1`, invocationID, body)
	if err != nil {
		return fmt.Errorf("store: frozen run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FrozenRun reads the invocation copy. A missing document is an empty value.
func (s *Store) FrozenRun(ctx context.Context, invocationID string) (FrozenRun, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT frozen_json FROM invocations WHERE id = $1`, invocationID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return FrozenRun{}, ErrNotFound
	}
	if err != nil {
		return FrozenRun{}, fmt.Errorf("store: frozen run: %w", err)
	}
	if len(raw) == 0 {
		return FrozenRun{}, nil
	}
	var doc frozenDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return FrozenRun{}, fmt.Errorf("store: frozen run: %w", err)
	}
	out := FrozenRun{RatesJSON: doc.Rates}
	if doc.Publish != nil {
		out.IssueKey = doc.Publish.IssueKey
		out.Title = doc.Publish.Title
		out.Body = doc.Publish.Body
		out.RefPrefix = doc.Publish.RefPrefix
	}
	if doc.Evidence != nil {
		out.Package = doc.Evidence.Package
		out.Mutation = doc.Evidence.Mutation
		out.Sentinel = doc.Evidence.Sentinel
		out.Break = doc.Evidence.Break
	}
	return out, nil
}

// EnsureFactoryKey inserts a factory-scoped API key when the factory has none.
// The plaintext is returned only when this call creates the row.
func (s *Store) EnsureFactoryKey(ctx context.Context, factoryID string) (string, bool, error) {
	if factoryID == "" {
		return "", false, fmt.Errorf("%w: factory key", ErrInputInvalid)
	}
	var existing []byte
	err := s.pool.QueryRow(ctx, `SELECT key_sha256 FROM factory_api_keys WHERE factory_id = $1`, factoryID).Scan(&existing)
	if err == nil {
		return "", false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("store: factory key: %w", err)
	}
	plain, sum, err := newFactoryKey()
	if err != nil {
		return "", false, err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO factory_api_keys (factory_id, key_sha256)
		VALUES ($1, $2)
		ON CONFLICT (factory_id) DO NOTHING`, factoryID, sum)
	if err != nil {
		return "", false, fmt.Errorf("store: factory key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return plain, true, nil
	}
	return "", false, nil
}

// PutFactoryKey stores the hash of token for factoryID, replacing a previous key.
func (s *Store) PutFactoryKey(ctx context.Context, factoryID, token string) error {
	if factoryID == "" || token == "" {
		return fmt.Errorf("%w: factory key", ErrInputInvalid)
	}
	sum := sha256.Sum256([]byte(token))
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO factory_api_keys (factory_id, key_sha256)
		VALUES ($1, $2)
		ON CONFLICT (factory_id) DO UPDATE SET key_sha256 = EXCLUDED.key_sha256`,
		factoryID, sum[:]); err != nil {
		return fmt.Errorf("store: factory key: %w", err)
	}
	return nil
}

// FactoryByKeyHash returns the factory whose scoped key hashes to sum.
func (s *Store) FactoryByKeyHash(ctx context.Context, sum []byte) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT factory_id FROM factory_api_keys WHERE key_sha256 = $1`, sum).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: factory key: %w", err)
	}
	return id, nil
}

func newFactoryKey() (string, []byte, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	plain := "fk_" + hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(plain))
	return plain, sum[:], nil
}

// adjustFactoryBudgetsTx moves reserved and spent cents on the factory cap
// and, when the invocation names that repo, the repo cap. No row means the
// factory has no cap. enforce refuses a reserve that would pass a ceiling.
func adjustFactoryBudgetsTx(ctx context.Context, tx pgx.Tx, invocationID string, reserveDelta, spentDelta int64, enforce bool) error {
	if invocationID == "" || (reserveDelta == 0 && spentDelta == 0) {
		return nil
	}
	var factoryID, target string
	err := tx.QueryRow(ctx, `
		SELECT factory_id, target_repository FROM invocations WHERE id = $1`, invocationID).Scan(&factoryID, &target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT scope_key, ceiling_cents, spent_cents, reserved_cents
		FROM factory_budgets
		WHERE factory_id = $1 AND (scope_key = '' OR scope_key = $2)
		ORDER BY scope_key
		FOR UPDATE`, factoryID, target)
	if err != nil {
		return err
	}
	type held struct {
		key            string
		ceiling, spent int64
		reserved       int64
	}
	var locked []held
	for rows.Next() {
		var row held
		if err := rows.Scan(&row.key, &row.ceiling, &row.spent, &row.reserved); err != nil {
			rows.Close()
			return err
		}
		locked = append(locked, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, row := range locked {
		nextReserved := row.reserved + reserveDelta
		nextSpent := row.spent + spentDelta
		if nextReserved < 0 || nextSpent < 0 {
			return fmt.Errorf("%w: factory budget", ErrInputInvalid)
		}
		if enforce && nextSpent+nextReserved > row.ceiling {
			return ErrBudgetExceeded
		}
	}
	for _, row := range locked {
		if _, err := tx.Exec(ctx, `
			UPDATE factory_budgets
			SET reserved_cents = reserved_cents + $3,
			    spent_cents = spent_cents + $4
			WHERE factory_id = $1 AND scope_key = $2`,
			factoryID, row.key, reserveDelta, spentDelta); err != nil {
			return err
		}
	}
	return nil
}

// DefinitionSpec reads the current definition spec for a factory id or name.
func (s *Store) DefinitionSpec(ctx context.Context, nameOrID string) (json.RawMessage, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		SELECT d.spec_json
		FROM definitions d
		JOIN factories f ON f.current_definition_id = d.id
		WHERE f.id = $1 OR f.name = $1
		ORDER BY CASE WHEN f.id = $1 THEN 0 ELSE 1 END
		LIMIT 1`, nameOrID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: definition spec: %w", err)
	}
	return json.RawMessage(raw), nil
}
