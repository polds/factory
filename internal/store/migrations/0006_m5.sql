-- M5 console read models. Idempotent and forward-only.
-- String literals in this file must not contain a semicolon.
--
-- run_events.gid is the factory-wide exclusive cursor. Adding a
-- GENERATED ALWAYS AS IDENTITY column rewrites run_events: PostgreSQL
-- takes ACCESS EXCLUSIVE and writes every existing row, because an
-- identity default is volatile. That cost is acceptable on a dev-sized
-- table. A large table needs a later migration that adds the column
-- nullable, backfills in batches, then attaches the identity. Do not
-- copy this ALTER onto a production-sized table.
--
-- worker_rejections keeps seven days. RecordRejection deletes older
-- rows. There is no separate janitor: a pool that stops rejecting
-- keeps stale rows until the next rejection.
--
-- The CASE expression below is store.FailureBackfillCASE. The Go
-- constant is a verbatim copy. It reads run_status, failure_reason,
-- exit_code, harness_status, exec_state, and details, and it returns
-- class/code or NULL.

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS failure_class text;

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS failure_code text;

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS assigned_at timestamptz;

ALTER TABLE workers
ADD COLUMN IF NOT EXISTS backend text;

ALTER TABLE workers
ADD COLUMN IF NOT EXISTS version text;

ALTER TABLE workers
ADD COLUMN IF NOT EXISTS reconnects int NOT NULL DEFAULT 0;

ALTER TABLE workers
ADD COLUMN IF NOT EXISTS in_flight int NOT NULL DEFAULT 0;

ALTER TABLE workers
ADD COLUMN IF NOT EXISTS connected_at timestamptz;

ALTER TABLE workers
DROP CONSTRAINT IF EXISTS workers_reconnects_check;

ALTER TABLE workers
ADD CONSTRAINT workers_reconnects_check CHECK (reconnects >= 0);

ALTER TABLE workers
DROP CONSTRAINT IF EXISTS workers_in_flight_check;

ALTER TABLE workers
ADD CONSTRAINT workers_in_flight_check CHECK (in_flight >= 0);

CREATE TABLE IF NOT EXISTS worker_rejections (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    worker_id text NOT NULL REFERENCES workers (id),
    run_id text NOT NULL REFERENCES runs (id),
    -- noqa: disable=RF04
    at timestamptz NOT NULL DEFAULT clock_timestamp()
    -- noqa: enable=RF04
);

CREATE INDEX IF NOT EXISTS worker_rejections_worker_at_idx
ON worker_rejections (worker_id, at);

CREATE INDEX IF NOT EXISTS worker_rejections_at_idx
ON worker_rejections (at);

CREATE TABLE IF NOT EXISTS steer_requests (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES runs (id),
    kind text NOT NULL CHECK (
        kind IN ('interrupt', 'followup')
    ),
    -- noqa: disable=RF04
    text text NOT NULL DEFAULT '',
    -- noqa: enable=RF04
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz,
    actor text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS steer_requests_pending_idx
ON steer_requests (run_id, id)
WHERE delivered_at IS NULL;

CREATE TABLE IF NOT EXISTS automation_overrides (
    factory_id text NOT NULL REFERENCES factories (id),
    -- noqa: disable=RF04
    name text NOT NULL,
    -- noqa: enable=RF04
    paused boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (factory_id, name)
);

ALTER TABLE run_events
ADD COLUMN IF NOT EXISTS gid bigint GENERATED ALWAYS AS IDENTITY;

CREATE UNIQUE INDEX IF NOT EXISTS run_events_gid_idx
ON run_events (gid);

-- Backfill failed and cancelled runs once. A second apply leaves any
-- class already stored, including a class SetRunClass wrote later.
UPDATE runs
SET
    failure_class = split_part(classified.class_code, '/', 1),
    failure_code = split_part(classified.class_code, '/', 2)
FROM (
    SELECT
        shaped.id,
        -- noqa: disable=RF03
        CASE
            WHEN harness_status = 'failed_by_agent' OR exit_code = 10
                THEN 'agent/agent_reported'
            WHEN exit_code = 20 OR failure_reason = 'config_invalid'
                THEN 'configuration/config_invalid'
            WHEN exit_code = 21 OR failure_reason = 'model_unavailable'
                THEN 'infrastructure/model_unavailable'
            WHEN
                exit_code = 30
                OR failure_reason = 'environment_setup_failed'
                THEN 'infrastructure/environment_setup_failed'
            WHEN
                details #>> '{kubernetes,container,termination_reason}'
                = 'OOMKilled'
                OR details #>> '{kubernetes,pod,reason}' = 'OOMKilled'
                OR jsonb_path_exists(
                    details,
                    (
                        '$.kubernetes.events[*].reason'
                        || ' ? (@ == "OOMKilled")'
                    )::jsonpath
                )
                THEN 'infrastructure/oom_killed'
            WHEN
                details #>> '{kubernetes,pod,reason}' = 'Evicted'
                OR details #>> '{kubernetes,container,termination_reason}'
                = 'Evicted'
                OR jsonb_path_exists(
                    details,
                    (
                        '$.kubernetes.events[*].reason'
                        || ' ? (@ == "Evicted")'
                    )::jsonpath
                )
                THEN 'infrastructure/evicted'
            WHEN exit_code >= 128
                THEN 'infrastructure/container_killed'
            WHEN failure_reason = 'container_exit'
                THEN 'infrastructure/container_exit'
            WHEN
                exec_state = 'orphaned'
                OR failure_reason IN (
                    'orphaned',
                    'lease_expired',
                    'lease_expiry',
                    'worker_gone',
                    'worker_lost'
                )
                THEN 'infrastructure/worker_lost'
            WHEN failure_reason = 'superseded_duplicate_building_run'
                THEN 'cancelled/superseded'
            WHEN run_status = 'cancelled'
                THEN 'cancelled/cancelled'
            WHEN failure_reason <> ''
                THEN 'infrastructure/unknown'
        END AS class_code
        -- noqa: enable=RF03
    FROM (
        SELECT
            runs.id,
            runs.status AS run_status,
            runs.exit_code,
            latest.failure_details_json AS details,
            coalesce(runs.failure_reason, '') AS failure_reason,
            coalesce(latest.state, '') AS exec_state,
            coalesce(term.harness_status, '') AS harness_status
        FROM runs
        LEFT JOIN LATERAL (
            SELECT
                executions.state,
                executions.failure_details_json
            FROM executions
            WHERE executions.run_id = runs.id
            ORDER BY executions.attempt DESC
            LIMIT 1
        ) AS latest ON TRUE
        LEFT JOIN LATERAL (
            SELECT run_events.payload_json ->> 'status' AS harness_status
            FROM run_events
            WHERE
                run_events.run_id = runs.id
                AND run_events.type IN ('terminal', 'run.terminal')
            ORDER BY run_events.seq DESC
            LIMIT 1
        ) AS term ON TRUE
        WHERE
            runs.status IN ('failed', 'cancelled')
            AND runs.failure_class IS NULL
    ) AS shaped
) AS classified
WHERE
    runs.id = classified.id
    AND classified.class_code IS NOT NULL;
