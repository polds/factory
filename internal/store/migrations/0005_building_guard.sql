-- One building run may be queued, assigned, or running per work item.
-- A terminal building run leaves the index, so a later revision or
-- follow-up can start another. The index is the guard across processes
-- and restarts. String literals in this file must not contain a semicolon.

CREATE UNIQUE INDEX IF NOT EXISTS runs_one_building_inflight
ON runs (work_item_id)
WHERE stage = 'building'
AND status IN ('queued', 'assigned', 'running')
AND work_item_id IS NOT NULL;
