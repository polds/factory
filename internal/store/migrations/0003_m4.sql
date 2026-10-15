-- M4 ingress, schedules, leases, and durable questions.
-- secret_ref is a credential name. url_token_hash is a SHA-256 digest.
-- Neither column holds a secret value. The migrator splits on semicolons
-- outside quotes and dollar-quoted bodies, so this file may contain a
-- function. String literals must not contain a semicolon.

CREATE TABLE IF NOT EXISTS deliveries (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    provider text NOT NULL CHECK (
        provider IN ('github', 'slack', 'linear', 'webhook', 'schedule')
    ),
    -- noqa: enable=RF04
    webhook_uid text NOT NULL DEFAULT '',
    delivery_id text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    headers jsonb NOT NULL DEFAULT '{}'::jsonb,
    body bytea NOT NULL,
    truncated boolean NOT NULL DEFAULT FALSE,
    status text NOT NULL DEFAULT 'received' CHECK (
        status IN ('received', 'matched', 'unmatched', 'ignored', 'failed')
    ),
    matched_automations jsonb NOT NULL DEFAULT '[]'::jsonb,
    expires_at timestamptz NOT NULL,
    UNIQUE (provider, webhook_uid, delivery_id)
);

CREATE INDEX IF NOT EXISTS deliveries_factory_status_idx
ON deliveries (factory_id, status, received_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS deliveries_expires_idx
ON deliveries (expires_at);

-- No secret material. external_id is the provider's installation id.
CREATE TABLE IF NOT EXISTS integration_installations (
    -- noqa: disable=RF04
    provider text NOT NULL CHECK (
        provider IN ('github', 'slack', 'linear', 'webhook', 'schedule')
    ),
    -- noqa: enable=RF04
    external_id text NOT NULL,
    factory_id text NOT NULL REFERENCES factories (id),
    PRIMARY KEY (provider, external_id)
);

-- auth_mode and signature_scheme are immutable after insert.
CREATE TABLE IF NOT EXISTS webhook_registrations (
    -- noqa: disable=RF04
    uid text PRIMARY KEY,
    -- noqa: enable=RF04
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    -- noqa: enable=RF04
    auth_mode text NOT NULL CHECK (
        auth_mode IN ('token', 'url_token', 'signature')
    ),
    signature_scheme text NOT NULL DEFAULT '',
    secret_ref text NOT NULL DEFAULT '',
    url_token_hash bytea,
    delivery_id_header text NOT NULL DEFAULT '' CHECK (
        char_length(delivery_id_header) <= 64
    ),
    enabled boolean NOT NULL DEFAULT TRUE,
    UNIQUE (factory_id, name),
    CHECK (
        url_token_hash IS NULL OR octet_length(url_token_hash) = 32
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS webhook_registrations_url_hash_idx
ON webhook_registrations (url_token_hash)
WHERE url_token_hash IS NOT NULL;

CREATE OR REPLACE FUNCTION webhook_registrations_immutable()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'webhook auth_mode and signature_scheme are immutable';
END;
$$;

DROP TRIGGER IF EXISTS webhook_registrations_immutable
ON webhook_registrations;

CREATE TRIGGER webhook_registrations_immutable
BEFORE UPDATE OF auth_mode, signature_scheme
ON webhook_registrations
FOR EACH ROW
WHEN (
    new.auth_mode IS DISTINCT FROM old.auth_mode
    OR new.signature_scheme IS DISTINCT FROM old.signature_scheme
)
EXECUTE FUNCTION webhook_registrations_immutable();

CREATE TABLE IF NOT EXISTS schedules (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    automation text NOT NULL,
    -- noqa: disable=RF04
    name text NOT NULL DEFAULT '',
    -- noqa: enable=RF04
    cron text NOT NULL,
    last_fired_at timestamptz,
    next_fire_at timestamptz,
    paused boolean NOT NULL DEFAULT FALSE,
    UNIQUE (factory_id, automation, name)
);

CREATE INDEX IF NOT EXISTS schedules_due_idx
ON schedules (next_fire_at, id)
WHERE paused = FALSE AND next_fire_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS leader_leases (
    -- noqa: disable=RF04
    name text PRIMARY KEY,
    -- noqa: enable=RF04
    holder text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS external_refs (
    -- noqa: disable=RF04
    provider text NOT NULL,
    -- noqa: enable=RF04
    external_ref text NOT NULL,
    work_item_id text NOT NULL REFERENCES work_items (id),
    conversation_id text NOT NULL,
    PRIMARY KEY (provider, external_ref)
);

CREATE INDEX IF NOT EXISTS external_refs_work_item_idx
ON external_refs (work_item_id);

CREATE TABLE IF NOT EXISTS outbound_threads (
    work_item_id text NOT NULL REFERENCES work_items (id),
    -- noqa: disable=RF04
    provider text NOT NULL,
    -- noqa: enable=RF04
    channel text NOT NULL DEFAULT '',
    root_ref text NOT NULL DEFAULT '',
    status_msg_ref text NOT NULL DEFAULT '',
    PRIMARY KEY (work_item_id, provider)
);

CREATE TABLE IF NOT EXISTS input_requests (
    id text PRIMARY KEY,
    work_item_id text NOT NULL REFERENCES work_items (id),
    run_id text NOT NULL REFERENCES runs (id),
    execution_id text NOT NULL DEFAULT '',
    question text NOT NULL,
    answer text NOT NULL DEFAULT '',
    answered_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    answered_at timestamptz
);

CREATE INDEX IF NOT EXISTS input_requests_work_item_idx
ON input_requests (work_item_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX IF NOT EXISTS input_requests_one_pending
ON input_requests (work_item_id)
WHERE answered_at IS NULL;

ALTER TABLE work_items
ADD COLUMN IF NOT EXISTS suspended_run_id text NOT NULL DEFAULT '';

ALTER TABLE work_items
ADD COLUMN IF NOT EXISTS suspended_execution_id text NOT NULL DEFAULT '';

ALTER TABLE work_items
ADD COLUMN IF NOT EXISTS suspended_at timestamptz;

CREATE INDEX IF NOT EXISTS work_items_suspended_run_idx
ON work_items (suspended_run_id)
WHERE suspended_run_id <> '';
