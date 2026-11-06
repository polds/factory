-- M8-I invocation receipts and durable results.

CREATE TABLE IF NOT EXISTS invocations (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id) ON DELETE CASCADE,
    work_item_id text REFERENCES work_items (id) ON DELETE SET NULL,
    profile_name text NOT NULL DEFAULT '',
    source_occurrence_key text NOT NULL,
    canonical_digest text NOT NULL,
    definition_revision text NOT NULL DEFAULT '',
    definition_digest text NOT NULL,
    policy_digest text NOT NULL DEFAULT '',
    profile_digest text NOT NULL DEFAULT '',
    principal jsonb NOT NULL,
    state text NOT NULL,
    outcome text NOT NULL DEFAULT '',
    counts_as_completed_scan boolean NOT NULL DEFAULT true,
    intent text NOT NULL,
    priority text NOT NULL,
    correlation jsonb,
    target_repository text NOT NULL DEFAULT '',
    target_ref text NOT NULL DEFAULT '',
    target_pull_request integer,
    pinned_head_sha text NOT NULL DEFAULT '',
    pinned_base_sha text NOT NULL DEFAULT '',
    neutral_stage boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invocations_factory_profile_occurrence_key
        UNIQUE (factory_id, profile_name, source_occurrence_key)
);

CREATE INDEX IF NOT EXISTS invocations_work_item_idx ON invocations (work_item_id);

CREATE TABLE IF NOT EXISTS invocation_results (
    invocation_id text PRIMARY KEY REFERENCES invocations (id) ON DELETE CASCADE,
    factory_id text NOT NULL REFERENCES factories (id) ON DELETE CASCADE,
    summary text NOT NULL DEFAULT '',
    coverage_status text NOT NULL DEFAULT '',
    findings_count integer NOT NULL DEFAULT 0,
    report_body bytea,
    artifact_paths jsonb NOT NULL DEFAULT '[]'::jsonb,
    published_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE work_items ADD COLUMN IF NOT EXISTS invocation_id text REFERENCES invocations (id) ON DELETE SET NULL;
