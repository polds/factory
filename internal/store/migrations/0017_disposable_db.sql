-- One row per evidence run. The DSN and the password are not columns.

CREATE TABLE IF NOT EXISTS disposable_databases (
    run_id text PRIMARY KEY,
    database_name text NOT NULL UNIQUE,
    role_name text NOT NULL UNIQUE,
    state text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT disposable_databases_state_check
    CHECK (state IN ('creating', 'ready', 'dropping', 'dropped'))
);
