-- Factory scope for publish, budgets, scoped API keys, and frozen run
-- documents. The evidence-name cleanup deletes only ledger rows whose
-- database and role both match the retired prefix. It drops no other name.
-- DROP DATABASE cannot run inside this migration transaction; the store
-- drops those databases and roles immediately before this file is applied.

ALTER TABLE draft_publications ADD COLUMN IF NOT EXISTS factory_id text;

UPDATE draft_publications AS d
SET factory_id = i.factory_id
FROM invocations AS i
WHERE d.invocation_id = i.id
  AND d.factory_id IS NULL;

ALTER TABLE draft_publications ALTER COLUMN factory_id SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'draft_publications_factory_fk'
    ) THEN
        ALTER TABLE draft_publications
            ADD CONSTRAINT draft_publications_factory_fk
            FOREIGN KEY (factory_id) REFERENCES factories (id);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS factory_budgets (
    factory_id text NOT NULL REFERENCES factories (id) ON DELETE CASCADE,
    scope_key text NOT NULL DEFAULT '',
    ceiling_cents bigint NOT NULL,
    spent_cents bigint NOT NULL DEFAULT 0,
    reserved_cents bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (factory_id, scope_key)
);

CREATE TABLE IF NOT EXISTS factory_api_keys (
    factory_id text PRIMARY KEY REFERENCES factories (id) ON DELETE CASCADE,
    key_sha256 bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE invocations ADD COLUMN IF NOT EXISTS frozen_json jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Ledger rows only. A name that does not match is left in place, including
-- a current evidence database and any other role.
DELETE FROM disposable_databases
WHERE database_name ~ '^dreamd_[0-9a-f]{32}$'
  AND role_name ~ '^dreamd_[0-9a-f]{32}$';
