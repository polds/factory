-- Run lifecycle rows on the factory stream.
-- Idempotent and forward-only. String literals in this file must not
-- contain a semicolon.
--
-- run_events.execution_id stays NOT NULL. Harness rows keep the per-run
-- seq, and a lifecycle row never takes one. run_lifecycle_events shares
-- run_events.gid's identity sequence and run_events_factory_seq, so
-- assign_run_event_factory_seq still hands out one cursor. Readers page
-- with factory_seq > cursor, the same call as before.
--
-- The trigger fires for every insert and every status update, including
-- writes that do not go through store methods. Setting status to the
-- value it already has writes nothing. payload_json is run_id, status,
-- and stage when stage is not empty. It stores no prompt and no token.
--
-- idle is a legal run status. No store method sets it. A later writer
-- that does is covered by the same trigger.
--
-- Applying this file does not rewrite run_events and does not backfill
-- lifecycle rows for runs that already exist.

ALTER TABLE runs
DROP CONSTRAINT IF EXISTS runs_status_check;

ALTER TABLE runs
ADD CONSTRAINT runs_status_check CHECK (
    status IN (
        'queued',
        'assigned',
        'running',
        'idle',
        'succeeded',
        'failed',
        'cancelled'
    )
);

CREATE TABLE IF NOT EXISTS run_lifecycle_events (
    gid bigint PRIMARY KEY DEFAULT nextval('run_events_gid_seq'),
    run_id text NOT NULL REFERENCES runs (id),
    ts timestamptz NOT NULL DEFAULT clock_timestamp(),
    -- noqa: disable=RF04
    type text NOT NULL CHECK (
        type IN ('run.created', 'run.status')
    ),
    -- noqa: enable=RF04
    payload_json jsonb NOT NULL,
    factory_seq bigint
);

CREATE UNIQUE INDEX IF NOT EXISTS run_lifecycle_events_factory_seq_idx
ON run_lifecycle_events (factory_seq);

CREATE INDEX IF NOT EXISTS run_lifecycle_events_run_idx
ON run_lifecycle_events (run_id, gid);

CREATE OR REPLACE FUNCTION emit_run_lifecycle_event()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    payload jsonb;
    event_type text;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'INSERT' THEN
        event_type := 'run.created';
    ELSE
        event_type := 'run.status';
    END IF;
    payload := jsonb_build_object(
        'run_id', NEW.id,
        'status', NEW.status
    );
    IF coalesce(NEW.stage, '') <> '' THEN
        payload := payload || jsonb_build_object('stage', NEW.stage);
    END IF;
    INSERT INTO run_lifecycle_events (run_id, type, payload_json)
    VALUES (NEW.id, event_type, payload);
    PERFORM pg_notify('run_events', NEW.id);
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS runs_lifecycle_event ON runs;

CREATE TRIGGER runs_lifecycle_event
AFTER INSERT OR UPDATE OF status ON runs
FOR EACH ROW
EXECUTE FUNCTION emit_run_lifecycle_event();

-- One assigner, both tables. nextval still runs once per committed row,
-- in gid order. A row that is still inserting is not visible, so it
-- receives a later factory_seq after it commits. The advisory lock is
-- the same key migration 0006 used.

CREATE OR REPLACE FUNCTION assign_run_event_factory_seq()
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    row_gid bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('factory-events', 3));
    PERFORM 1
    FROM run_events
    WHERE run_events.factory_seq IS NULL
    FOR UPDATE;
    PERFORM 1
    FROM run_lifecycle_events
    WHERE run_lifecycle_events.factory_seq IS NULL
    FOR UPDATE;
    FOR row_gid IN
        SELECT pending.gid
        FROM (
            SELECT run_events.gid
            FROM run_events
            WHERE run_events.factory_seq IS NULL
            UNION ALL
            SELECT run_lifecycle_events.gid
            FROM run_lifecycle_events
            WHERE run_lifecycle_events.factory_seq IS NULL
        ) AS pending
        ORDER BY pending.gid
    LOOP
        UPDATE run_events
        SET factory_seq = nextval('run_events_factory_seq')
        WHERE run_events.gid = row_gid
            AND run_events.factory_seq IS NULL;
        IF NOT FOUND THEN
            UPDATE run_lifecycle_events
            SET factory_seq = nextval('run_events_factory_seq')
            WHERE run_lifecycle_events.gid = row_gid
                AND run_lifecycle_events.factory_seq IS NULL;
        END IF;
    END LOOP;
END;
$$;
