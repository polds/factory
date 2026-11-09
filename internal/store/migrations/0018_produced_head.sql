-- Produced head for one native task. The bundle bytes are an artifact object,
-- not a column and not a role file.

CREATE TABLE IF NOT EXISTS native_produced_heads (
    task_id text PRIMARY KEY,
    run_id text NOT NULL,
    execution_id text NOT NULL,
    head_sha text NOT NULL,
    bundle_sha256 text NOT NULL,
    state text NOT NULL,
    CONSTRAINT native_produced_heads_state_check
    CHECK (state IN ('recording', 'ready'))
);
