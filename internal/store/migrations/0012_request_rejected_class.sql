-- Reclassify request_rejected rows that 0006 stored under another class.
-- Migrate records a filename and skips it, so editing 0006 does not reach
-- a database that already applied that file. This UPDATE is not limited
-- to failure_class IS NULL.
--
-- Exit 10 and harness status failed_by_agent stay on the agent row.
-- A row already stored as configuration is left alone.
-- String literals in this file must not contain a semicolon.

UPDATE runs
SET
    failure_class = 'configuration',
    failure_code = 'config_invalid'
WHERE runs.id IN (
    SELECT runs.id
    FROM runs
    LEFT JOIN LATERAL (
        SELECT executions.failure_details_json AS details
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
        (
            coalesce(runs.failure_reason, '') = 'request_rejected'
            OR coalesce(term.harness_status, '') = 'request_rejected'
        )
        AND coalesce(term.harness_status, '') <> 'failed_by_agent'
        AND failure_effective_exit(
            runs.exit_code,
            latest.details
        ) IS DISTINCT FROM 10
        AND coalesce(runs.failure_class, '') <> 'configuration'
);
