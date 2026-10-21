-- Per-trial definition SHA. A benchmark run resolves each configuration's
-- definitionRef once. The SHA is stored on every trial so a restart, or
-- the other factoryd instance, assigns that same tree.
--
-- definition_sha is 40 hex characters when resolution succeeded, and empty
-- only for rows written before this column existed.

ALTER TABLE benchmark_trials
ADD COLUMN IF NOT EXISTS definition_sha text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS benchmark_trials_run_idx
ON benchmark_trials (run_id);
