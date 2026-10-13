package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ApplyDefinition upserts the factory and replaces its current definition in
// one transaction. A runner reference that is not part of d fails the foreign
// key and rolls back, so the previous definition and its agents stay current.
func (s *Store) ApplyDefinition(ctx context.Context, factoryName string, d AppliedDefinition) (string, error) {
	if factoryName == "" {
		return "", fmt.Errorf("store: factory name is required")
	}
	if d.SchemaVersion == "" {
		return "", fmt.Errorf("store: schema version is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("store: apply definition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 2))`, "definition:"+factoryName); err != nil {
		return "", fmt.Errorf("store: apply definition: %w", err)
	}
	factoryID, err := upsertFactory(ctx, tx, factoryName, d)
	if err != nil {
		return "", err
	}
	defID, err := NewID(PrefixDefinition)
	if err != nil {
		return "", err
	}
	spec := jsonOr(d.SpecJSON, `{}`)
	if _, err := tx.Exec(ctx, `
		INSERT INTO definitions (id, factory_id, git_sha, validated_at, applied_at, status, spec_json)
		VALUES ($1, $2, $3, clock_timestamp(), clock_timestamp(), 'applied', $4::jsonb)`,
		defID, factoryID, d.GitSHA, []byte(spec),
	); err != nil {
		return "", fmt.Errorf("store: apply definition: %w", err)
	}
	for _, runner := range d.Runners {
		if runner.Name == "" {
			return "", fmt.Errorf("store: runner name is required")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO runners (definition_id, factory_id, name, platform_json, instance_shape_json, setup_commands)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6::jsonb)`,
			defID, factoryID, runner.Name,
			[]byte(jsonOr(runner.PlatformJSON, `{}`)),
			[]byte(jsonOr(runner.InstanceShapeJSON, `{}`)),
			[]byte(jsonOr(runner.SetupCommands, `[]`)),
		); err != nil {
			return "", fmt.Errorf("store: runner %s: %w", runner.Name, err)
		}
	}
	for _, agent := range d.Agents {
		if agent.Name == "" {
			return "", fmt.Errorf("store: agent name is required")
		}
		agentID, err := NewID(PrefixAgent)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO agents (id, factory_id, definition_id, name, agent_type, spec_json, runner_name)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)`,
			agentID, factoryID, defID, agent.Name, agent.AgentType, []byte(jsonOr(agent.SpecJSON, `{}`)), agent.Runner,
		); err != nil {
			return "", fmt.Errorf("store: agent %s: %w", agent.Name, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE definitions
		SET status = 'superseded'
		WHERE factory_id = $1 AND id <> $2 AND status = 'applied'`,
		factoryID, defID,
	); err != nil {
		return "", fmt.Errorf("store: apply definition: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE factories
		SET current_definition_id = $2, production_ref = $2
		WHERE id = $1`, factoryID, defID); err != nil {
		return "", fmt.Errorf("store: apply definition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("store: apply definition: %w", err)
	}
	return defID, nil
}

func upsertFactory(ctx context.Context, tx pgx.Tx, name string, d AppliedDefinition) (string, error) {
	source := d.DefinitionSource
	if source == "" {
		source = "local"
	}
	var factoryID string
	err := tx.QueryRow(ctx, `SELECT id FROM factories WHERE name = $1 FOR UPDATE`, name).Scan(&factoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		factoryID, err = NewID(PrefixFactory)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO factories (id, name, alias, schema_version, definition_source, production_ref)
			VALUES ($1, $2, $3, $4, $5, '')`,
			factoryID, name, d.Alias, d.SchemaVersion, source,
		); err != nil {
			return "", fmt.Errorf("store: factory: %w", err)
		}
		return factoryID, nil
	}
	if err != nil {
		return "", fmt.Errorf("store: factory: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE factories
		SET alias = $2, schema_version = $3, definition_source = $4
		WHERE id = $1`,
		factoryID, d.Alias, d.SchemaVersion, source,
	); err != nil {
		return "", fmt.Errorf("store: factory: %w", err)
	}
	return factoryID, nil
}

func jsonOr(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(fallback)
	}
	return raw
}

// GetFactory loads a factory by id or unique name.
func (s *Store) GetFactory(ctx context.Context, nameOrID string) (Factory, error) {
	var fac Factory
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, alias, schema_version, definition_source, production_ref,
		       COALESCE(current_definition_id, '')
		FROM factories
		WHERE id = $1 OR name = $1
		ORDER BY CASE WHEN id = $1 THEN 0 ELSE 1 END
		LIMIT 1`, nameOrID).Scan(
		&fac.ID, &fac.Name, &fac.Alias, &fac.SchemaVersion, &fac.DefinitionSource,
		&fac.ProductionRef, &fac.CurrentDefinitionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Factory{}, ErrNotFound
	}
	if err != nil {
		return Factory{}, fmt.Errorf("store: get factory: %w", err)
	}
	return fac, nil
}

// ListFactories returns every factory ordered by name.
func (s *Store) ListFactories(ctx context.Context) ([]Factory, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, alias, schema_version, definition_source, production_ref,
		       COALESCE(current_definition_id, '')
		FROM factories
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list factories: %w", err)
	}
	defer rows.Close()
	out := []Factory{}
	for rows.Next() {
		var fac Factory
		if err := rows.Scan(
			&fac.ID, &fac.Name, &fac.Alias, &fac.SchemaVersion, &fac.DefinitionSource,
			&fac.ProductionRef, &fac.CurrentDefinitionID,
		); err != nil {
			return nil, fmt.Errorf("store: list factories: %w", err)
		}
		out = append(out, fac)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list factories: %w", err)
	}
	return out, nil
}

// ListAgents returns agents of the factory's current definition.
func (s *Store) ListAgents(ctx context.Context, factoryID string) ([]Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.factory_id, a.definition_id, a.name, a.agent_type, a.runner_name, a.spec_json
		FROM agents a
		JOIN factories f ON f.id = a.factory_id AND f.current_definition_id = a.definition_id
		WHERE f.id = $1
		ORDER BY a.name`, factoryID)
	if err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var agent Agent
		var spec []byte
		if err := rows.Scan(&agent.ID, &agent.FactoryID, &agent.DefinitionID, &agent.Name, &agent.AgentType, &agent.Runner, &spec); err != nil {
			return nil, fmt.Errorf("store: list agents: %w", err)
		}
		agent.SpecJSON = json.RawMessage(spec)
		out = append(out, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list agents: %w", err)
	}
	return out, nil
}
