-- M6 measure and improve. Idempotent and forward-only.
-- String literals in this file must not contain a semicolon.
--
-- runs.purpose defaults to work, so rows that already exist read work
-- without an UPDATE. The lifecycle trigger watches status only, and this
-- file does not set status, so applying it adds no run_lifecycle_events
-- row. purpose cannot change after insert. The trigger raises when the
-- new value differs. A same-value write is allowed. The check values
-- are api.PurposeWork, PurposeScorer, PurposeBenchmark, and
-- PurposeImprovement.
--
-- Scorer, batch, benchmark, and pr_facts columns are the M6 contract.
-- The partial unique index is guard rail 4. Its name is
-- improvement_batches_one_inflight. CreateBatch maps that constraint
-- to api.ErrBatchInFlight.
--
-- work_items.source is unchanged. Measurement runs are runs, not a new
-- work item source.

ALTER TABLE runs
ADD COLUMN IF NOT EXISTS purpose text NOT NULL DEFAULT 'work';

ALTER TABLE runs
DROP CONSTRAINT IF EXISTS runs_purpose_check;

ALTER TABLE runs
ADD CONSTRAINT runs_purpose_check CHECK (
    purpose IN ('work', 'scorer', 'benchmark', 'improvement')
);

CREATE OR REPLACE FUNCTION reject_run_purpose_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.purpose IS DISTINCT FROM OLD.purpose THEN
        RAISE EXCEPTION 'runs.purpose is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS runs_purpose_immutable ON runs;

CREATE TRIGGER runs_purpose_immutable
BEFORE UPDATE OF purpose ON runs
FOR EACH ROW
EXECUTE FUNCTION reject_run_purpose_change();

CREATE INDEX IF NOT EXISTS runs_purpose_idx
ON runs (factory_id, purpose);

CREATE TABLE IF NOT EXISTS scorer_results (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    run_id text NOT NULL REFERENCES runs (id),
    scorer_name text NOT NULL,
    judge_run_id text REFERENCES runs (id),
    -- noqa: disable=RF04
    label text NOT NULL DEFAULT '',
    -- noqa: enable=RF04
    score double precision NOT NULL DEFAULT 0,
    passed boolean NOT NULL DEFAULT FALSE,
    reasoning text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (
        status IN ('ok', 'scorer_invalid', 'pending')
    ),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    reviewed_at timestamptz,
    reviewed_by_batch_id text,
    UNIQUE (run_id, scorer_name)
);

CREATE INDEX IF NOT EXISTS scorer_results_factory_scorer_idx
ON scorer_results (factory_id, scorer_name, created_at);

CREATE INDEX IF NOT EXISTS scorer_results_unreviewed_idx
ON scorer_results (factory_id, created_at)
WHERE reviewed_at IS NULL AND passed = FALSE;

CREATE TABLE IF NOT EXISTS improvement_batches (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    agent_name text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'queued',
            'running',
            'pr_open',
            'merged',
            'closed',
            'failed'
        )
    ),
    improvement_run_id text REFERENCES runs (id),
    pr_url text NOT NULL DEFAULT '',
    pr_number int,
    definition_repo text NOT NULL DEFAULT '',
    rejection_note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    CHECK (pr_number IS NULL OR pr_number > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS improvement_batches_one_inflight
ON improvement_batches (factory_id, agent_name)
WHERE status IN ('queued', 'running', 'pr_open');

CREATE INDEX IF NOT EXISTS improvement_batches_factory_idx
ON improvement_batches (factory_id, created_at DESC, id DESC);

ALTER TABLE scorer_results
DROP CONSTRAINT IF EXISTS scorer_results_reviewed_by_batch_fk;

ALTER TABLE scorer_results
ADD CONSTRAINT scorer_results_reviewed_by_batch_fk
FOREIGN KEY (reviewed_by_batch_id) REFERENCES improvement_batches (id);

CREATE TABLE IF NOT EXISTS improvement_batch_failures (
    batch_id text NOT NULL REFERENCES improvement_batches (id),
    scorer_result_id text NOT NULL REFERENCES scorer_results (id),
    PRIMARY KEY (batch_id, scorer_result_id)
);

CREATE TABLE IF NOT EXISTS benchmark_runs (
    id text PRIMARY KEY,
    factory_id text NOT NULL REFERENCES factories (id),
    suite text NOT NULL,
    requested_by text NOT NULL DEFAULT '',
    repetitions int NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS benchmark_runs_factory_suite_idx
ON benchmark_runs (factory_id, suite, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS benchmark_trials (
    id text PRIMARY KEY,
    benchmark_run_id text NOT NULL REFERENCES benchmark_runs (id),
    -- noqa: disable=RF04
    configuration text NOT NULL,
    task text NOT NULL,
    -- noqa: enable=RF04
    repetition int NOT NULL,
    run_id text NOT NULL REFERENCES runs (id),
    outcome text NOT NULL CHECK (
        outcome IN ('pass', 'fail', 'errored', 'pending')
    ),
    cost_cents bigint NOT NULL DEFAULT 0,
    duration_ms bigint NOT NULL DEFAULT 0,
    correctness_result_id text REFERENCES scorer_results (id)
);

CREATE INDEX IF NOT EXISTS benchmark_trials_cell_idx
ON benchmark_trials (benchmark_run_id, configuration, task);

CREATE TABLE IF NOT EXISTS pr_facts (
    factory_id text NOT NULL REFERENCES factories (id),
    repo text NOT NULL,
    number int NOT NULL CHECK (number > 0),
    work_item_id text REFERENCES work_items (id),
    opened_at timestamptz,
    merged_at timestamptz,
    closed_at timestamptz,
    first_review_at timestamptz,
    human_push_seen boolean NOT NULL DEFAULT FALSE,
    is_improvement boolean NOT NULL DEFAULT FALSE,
    changed_lines int,
    PRIMARY KEY (factory_id, repo, number),
    CHECK (changed_lines IS NULL OR changed_lines >= 0)
);

CREATE INDEX IF NOT EXISTS pr_facts_opened_idx
ON pr_facts (factory_id, opened_at);

CREATE INDEX IF NOT EXISTS pr_facts_merged_idx
ON pr_facts (factory_id, merged_at);
