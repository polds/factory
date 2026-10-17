-- M5 API gaps. Idempotent and forward-only.
-- String literals in this file must not contain a semicolon.
--
-- normalized_json stores the dispatcher event list after the raw
-- key is removed. NULL means the row was never dispatched, or it
-- predates this column. An empty array means normalization
-- produced no events. A nullable column with no default is
-- metadata-only on PostgreSQL 11 and newer. This file does not
-- rewrite deliveries and does not read objects from later
-- migrations.
--
-- deliveries_webhook_received_idx serves per-webhook history,
-- ordered by received_at descending and then id descending.

ALTER TABLE deliveries
ADD COLUMN IF NOT EXISTS normalized_json jsonb;

CREATE INDEX IF NOT EXISTS deliveries_webhook_received_idx
ON deliveries (
    factory_id,
    webhook_uid,
    received_at DESC,
    id DESC
);
