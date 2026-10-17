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
-- class/code or NULL. Precedence matches failure.Classify:
-- failure_effective_exit uses exit_code, else normalized_exit_code,
-- else 128+signal. failure_snapshot_has matches the kubernetes reason
-- fields Classify reads. worker_lost is only orphaned, lease_expired,
-- and worker_gone.

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS failure_class text;

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS failure_code text;

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS assigned_at timestamptz;

CREATE INDEX IF NOT EXISTS runs_failure_class_idx
ON runs (failure_class);

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
    actor text NOT NULL DEFAULT '',
    claimed_by text NOT NULL DEFAULT '',
    claim_expires_at timestamptz
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
-- failure_effective_exit and failure_snapshot_has exist so the CASE
-- can follow Classify without repeating the snapshot walk.

CREATE OR REPLACE FUNCTION failure_effective_exit(
    exit_code int,
    details jsonb
)
RETURNS int
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT coalesce(
        exit_code,
        CASE
            WHEN
                details #>> '{kubernetes,container,normalized_exit_code}'
                ~ '^-?[0-9]+$'
                THEN (
                    details
                    #>> '{kubernetes,container,normalized_exit_code}'
                )::int
        END,
        CASE
            WHEN
                details #>> '{kubernetes,container,signal}' ~ '^[0-9]+$'
                AND (
                    details #>> '{kubernetes,container,signal}'
                )::int > 0
                THEN
                    128
                    + (details #>> '{kubernetes,container,signal}')::int
        END
    );
$$;

CREATE OR REPLACE FUNCTION failure_snapshot_has(
    details jsonb,
    token text
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT
        coalesce(
            details #>> '{kubernetes,container,termination_reason}',
            ''
        ) = token
        OR coalesce(
            details #>> '{kubernetes,container,waiting_reason}',
            ''
        ) = token
        OR coalesce(details #>> '{kubernetes,pod,reason}', '') = token
        OR (
            jsonb_typeof(details #> '{kubernetes,pod,conditions}')
            = 'array'
            AND EXISTS (
                SELECT 1
                FROM
                    jsonb_array_elements(
                        details #> '{kubernetes,pod,conditions}'
                    ) AS cond
                WHERE cond ->> 'reason' = token
            )
        )
        OR (
            jsonb_typeof(details #> '{kubernetes,job,conditions}')
            = 'array'
            AND EXISTS (
                SELECT 1
                FROM
                    jsonb_array_elements(
                        details #> '{kubernetes,job,conditions}'
                    ) AS cond
                WHERE cond ->> 'reason' = token
            )
        )
        OR (
            jsonb_typeof(details #> '{kubernetes,events}') = 'array'
            AND EXISTS (
                SELECT 1
                FROM
                    jsonb_array_elements(
                        details #> '{kubernetes,events}'
                    ) AS event
                WHERE event ->> 'reason' = token
            )
        );
$$;

UPDATE runs
SET
    failure_class = split_part(classified.class_code, '/', 1),
    failure_code = split_part(classified.class_code, '/', 2)
FROM (
    SELECT
        shaped.id,
        -- noqa: disable=RF03
        CASE
            WHEN
                harness_status = 'failed_by_agent'
                OR failure_effective_exit(exit_code, details) = 10
                THEN 'agent/agent_reported'
            WHEN
                failure_effective_exit(exit_code, details) = 20
                OR failure_reason = 'config_invalid'
                OR harness_status = 'config_invalid'
                THEN 'configuration/config_invalid'
            WHEN
                failure_effective_exit(exit_code, details) = 21
                OR failure_reason = 'model_unavailable'
                OR harness_status = 'model_unavailable'
                THEN 'infrastructure/model_unavailable'
            WHEN
                failure_effective_exit(exit_code, details) = 30
                OR failure_reason = 'environment_setup_failed'
                OR harness_status = 'environment_setup_failed'
                THEN 'infrastructure/environment_setup_failed'
            WHEN
                failure_snapshot_has(details, 'OOMKilled')
                OR failure_snapshot_has(details, 'OOMKilling')
                OR failure_reason = 'container_oom'
                THEN 'infrastructure/oom_killed'
            WHEN
                failure_snapshot_has(details, 'Evicted')
                OR failure_reason = 'evicted'
                THEN 'infrastructure/evicted'
            WHEN failure_effective_exit(exit_code, details) >= 128
                THEN 'infrastructure/container_killed'
            WHEN
                failure_reason = 'container_exit'
                OR (
                    failure_effective_exit(exit_code, details) > 0
                    AND failure_effective_exit(exit_code, details) < 128
                    AND failure_reason = ''
                )
                THEN 'infrastructure/container_exit'
            WHEN
                exec_state IN (
                    'orphaned', 'lease_expired', 'worker_gone'
                )
                OR failure_reason IN (
                    'orphaned', 'lease_expired', 'worker_gone'
                )
                OR run_status IN (
                    'orphaned', 'lease_expired', 'worker_gone'
                )
                THEN 'infrastructure/worker_lost'
            WHEN failure_reason = 'superseded_duplicate_building_run'
                THEN 'cancelled/superseded'
            WHEN run_status = 'cancelled'
                THEN 'cancelled/cancelled'
            WHEN failure_reason <> '' OR run_status = 'failed'
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
