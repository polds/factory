-- M10-A bridge outbox. Idempotency keys and bindings survive a process restart.
-- A second instance inserts ON CONFLICT and does not send the same key again.

CREATE TABLE IF NOT EXISTS bridge_outbox (
    idempotency_key text PRIMARY KEY,
    notice_origin text NOT NULL DEFAULT '',
    notice_binding text NOT NULL DEFAULT '',
    task_ref text NOT NULL DEFAULT '',
    notice_summary text NOT NULL DEFAULT '',
    sent boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS bridge_bindings (
    binding_key text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);
