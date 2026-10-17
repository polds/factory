-- One building run may be queued, assigned, or running per work item.
-- A terminal building run leaves the index, so a later revision or
-- follow-up can start another. The index is the guard across processes
-- and restarts. String literals in this file must not contain a semicolon.
--
-- A database that already has two in-flight building runs cannot build
-- the index. Keep the newest per work item. Mark every older one failed,
-- finish its open executions the same way, and drop its queue row.

UPDATE executions AS open_exec
SET
    state = 'failed',
    failure_reason = 'superseded_duplicate_building_run',
    ended_at = clock_timestamp()
FROM runs AS older
WHERE
    open_exec.run_id = older.id
    AND open_exec.state IN ('assigned', 'running')
    AND older.id IN (
        SELECT ranked.id
        FROM (
            SELECT
                runs.id,
                row_number() OVER (
                    PARTITION BY runs.work_item_id
                    ORDER BY runs.created_at DESC, runs.id DESC
                ) AS keep_rank
            FROM runs
            WHERE
                runs.stage = 'building'
                AND runs.status IN ('queued', 'assigned', 'running')
                AND runs.work_item_id IS NOT NULL
        ) AS ranked
        WHERE ranked.keep_rank > 1
    );

DELETE FROM run_queue
WHERE run_queue.run_id IN (
    SELECT ranked.id
    FROM (
        SELECT
            runs.id,
            row_number() OVER (
                PARTITION BY runs.work_item_id
                ORDER BY runs.created_at DESC, runs.id DESC
            ) AS keep_rank
        FROM runs
        WHERE
            runs.stage = 'building'
            AND runs.status IN ('queued', 'assigned', 'running')
            AND runs.work_item_id IS NOT NULL
    ) AS ranked
    WHERE ranked.keep_rank > 1
);

UPDATE runs
SET
    status = 'failed',
    failure_reason = 'superseded_duplicate_building_run',
    ended_at = clock_timestamp(),
    worker_id = NULL
WHERE runs.id IN (
    SELECT ranked.id
    FROM (
        SELECT
            runs.id,
            row_number() OVER (
                PARTITION BY runs.work_item_id
                ORDER BY runs.created_at DESC, runs.id DESC
            ) AS keep_rank
        FROM runs
        WHERE
            runs.stage = 'building'
            AND runs.status IN ('queued', 'assigned', 'running')
            AND runs.work_item_id IS NOT NULL
    ) AS ranked
    WHERE ranked.keep_rank > 1
);

CREATE UNIQUE INDEX IF NOT EXISTS runs_one_building_inflight
ON runs (work_item_id)
WHERE stage = 'building'
AND status IN ('queued', 'assigned', 'running')
AND work_item_id IS NOT NULL;
