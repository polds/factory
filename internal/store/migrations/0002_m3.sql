-- M3 work items, approvals, run linkage, and definition resources.
-- Secret values are not columns. The migrator splits on semicolons, so
-- string literals in this file must not contain one.

CREATE TABLE IF NOT EXISTS work_items (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    source text NOT NULL CHECK (
        source IN (
            'issue',
            'manual',
            'api',
            'webhook',
            'chat',
            'schedule',
            'mcp'
        )
    ),
    -- noqa: enable=RF04
    source_ref text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    -- noqa: disable=RF04
    stage text NOT NULL CHECK (
        stage IN (
            'triage',
            'planning',
            'building',
            'reviewing',
            'human_handoff'
        )
    ),
    -- noqa: enable=RF04
    status text NOT NULL CHECK (
        status IN (
            'open',
            'awaiting_approval',
            'awaiting_input',
            'complete',
            'cancelled'
        )
    ),
    created_by text NOT NULL DEFAULT '',
    conversation_id text NOT NULL DEFAULT '',
    revision_count int NOT NULL DEFAULT 0 CHECK (revision_count >= 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS work_items_factory_idx
ON work_items (factory_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS approvals (
    id text PRIMARY KEY,
    work_item_id text NOT NULL REFERENCES work_items (id),
    gate text NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    decided_at timestamptz,
    decided_by text NOT NULL DEFAULT '',
    decision text NOT NULL DEFAULT '' CHECK (
        decision IN ('', 'approve', 'reject')
    ),
    note text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS approvals_work_item_idx
ON approvals (work_item_id, requested_at);

-- One open request per gate. A decided row drops out of this index.
CREATE UNIQUE INDEX IF NOT EXISTS approvals_one_pending_gate
ON approvals (work_item_id, gate)
WHERE decision = '';

ALTER TABLE runs
ADD COLUMN work_item_id text REFERENCES work_items (id);

ALTER TABLE runs
ADD COLUMN parent_run_id text REFERENCES runs (id);

ALTER TABLE runs
ADD COLUMN conversation_id text NOT NULL DEFAULT '';

ALTER TABLE runs
ADD COLUMN stage text NOT NULL DEFAULT '';

ALTER TABLE runs
ADD COLUMN model_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS runs_work_item_idx
ON runs (work_item_id)
WHERE work_item_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS runs_parent_idx
ON runs (parent_run_id)
WHERE parent_run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS routers (
    id text PRIMARY KEY,
    definition_id text NOT NULL REFERENCES definitions (id),
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    type text NOT NULL,
    -- noqa: enable=RF04
    default_model text NOT NULL DEFAULT '',
    routing_json jsonb NOT NULL,
    UNIQUE (definition_id, name)
);

CREATE TABLE IF NOT EXISTS skills (
    id text PRIMARY KEY,
    definition_id text NOT NULL REFERENCES definitions (id),
    factory_id text NOT NULL REFERENCES factories (id),
    agent_id text REFERENCES agents (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    path text NOT NULL,
    -- noqa: enable=RF04
    content_digest text NOT NULL,
    UNIQUE (definition_id, path)
);

CREATE TABLE IF NOT EXISTS automations (
    id text PRIMARY KEY,
    definition_id text NOT NULL REFERENCES definitions (id),
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    -- noqa: enable=RF04
    enabled boolean NOT NULL DEFAULT TRUE,
    triggers_json jsonb NOT NULL,
    prompt text NOT NULL DEFAULT '',
    overrides_json jsonb NOT NULL,
    UNIQUE (definition_id, name)
);

CREATE TABLE IF NOT EXISTS scorers (
    id text PRIMARY KEY,
    definition_id text NOT NULL REFERENCES definitions (id),
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    model text NOT NULL DEFAULT '',
    -- noqa: enable=RF04
    labels_json jsonb NOT NULL,
    passing_score numeric NOT NULL,
    sampling_rate int NOT NULL CHECK (sampling_rate >= 0),
    self_improve boolean NOT NULL DEFAULT FALSE,
    UNIQUE (definition_id, name)
);

-- spec_json may name a credential. The credential value is not stored.
CREATE TABLE IF NOT EXISTS webhooks (
    id text PRIMARY KEY,
    definition_id text NOT NULL REFERENCES definitions (id),
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    -- noqa: enable=RF04
    auth_mode text NOT NULL,
    signature_scheme text NOT NULL DEFAULT '',
    delivery_id_header text NOT NULL DEFAULT '',
    spec_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (definition_id, name)
);
