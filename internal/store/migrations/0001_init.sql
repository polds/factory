-- M2 system of record. Values of secrets and env vars are not columns.
-- Executions keep only run_token_sha256.

CREATE TABLE IF NOT EXISTS factories (
    id text PRIMARY KEY,
    -- noqa: disable=RF04
    name text NOT NULL UNIQUE,
    alias text NOT NULL DEFAULT '',
    -- noqa: enable=RF04
    schema_version text NOT NULL,
    definition_source text NOT NULL,
    production_ref text NOT NULL DEFAULT '',
    current_definition_id text
);

CREATE TABLE IF NOT EXISTS definitions (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    git_sha text NOT NULL DEFAULT '',
    validated_at timestamptz,
    applied_at timestamptz,
    status text NOT NULL CHECK (status IN ('applied', 'superseded')),
    spec_json jsonb NOT NULL
);

ALTER TABLE factories
ADD CONSTRAINT factories_current_definition_fk
FOREIGN KEY (current_definition_id) REFERENCES definitions (id);

CREATE TABLE IF NOT EXISTS runners (
    definition_id text NOT NULL REFERENCES definitions (id),
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    -- noqa: enable=RF04
    platform_json jsonb NOT NULL,
    instance_shape_json jsonb NOT NULL,
    setup_commands jsonb NOT NULL,
    PRIMARY KEY (definition_id, name)
);

CREATE TABLE IF NOT EXISTS agents (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    definition_id text NOT NULL REFERENCES definitions (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    -- noqa: enable=RF04
    agent_type text NOT NULL DEFAULT '',
    spec_json jsonb NOT NULL,
    runner_name text NOT NULL,
    UNIQUE (definition_id, name),
    FOREIGN KEY (definition_id, runner_name)
    REFERENCES runners (definition_id, name)
);

CREATE TABLE IF NOT EXISTS runs (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    agent_id text REFERENCES agents (id),
    status text NOT NULL CHECK (
        status IN (
            'queued',
            'assigned',
            'running',
            'succeeded',
            'failed',
            'cancelled'
        )
    ),
    priority int NOT NULL CHECK (priority IN (1, 2, 3)),
    pool text NOT NULL,
    prompt text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    repo text NOT NULL DEFAULT '',
    worker_id text,
    failure_reason text,
    exit_code int,
    cancel_requested boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    ended_at timestamptz
);

CREATE TABLE IF NOT EXISTS executions (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES runs (id),
    attempt int NOT NULL CHECK (attempt > 0),
    worker_id text NOT NULL,
    state text NOT NULL CHECK (
        state IN (
            'assigned',
            'running',
            'completed',
            'failed',
            'orphaned',
            'cancelled'
        )
    ),
    failure_reason text,
    exit_code int,
    failure_details_json jsonb,
    run_token_sha256 bytea NOT NULL,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ended_at timestamptz,
    UNIQUE (run_id, attempt)
);

CREATE TABLE IF NOT EXISTS run_events (
    run_id text NOT NULL REFERENCES runs (id),
    seq bigint NOT NULL,
    execution_id text NOT NULL REFERENCES executions (id),
    idx int NOT NULL,
    ts timestamptz NOT NULL,
    -- noqa: disable=RF04
    type text NOT NULL,
    -- noqa: enable=RF04
    payload_json jsonb NOT NULL,
    PRIMARY KEY (run_id, seq),
    UNIQUE (execution_id, idx)
);

CREATE TABLE IF NOT EXISTS workers (
    id text PRIMARY KEY,
    pool text NOT NULL,
    labels_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    platform text NOT NULL DEFAULT '',
    last_heartbeat_at timestamptz,
    max_concurrent int NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS run_queue (
    run_id text PRIMARY KEY REFERENCES runs (id),
    factory_id text NOT NULL REFERENCES factories (id),
    pool text NOT NULL,
    priority int NOT NULL,
    enqueued_at timestamptz NOT NULL,
    lease_until timestamptz,
    attempts int NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS run_queue_lease_idx
ON run_queue (pool, priority DESC, enqueued_at ASC, run_id ASC);
