-- M9-T native task, per-invocation ledger, inbox projection, and wake intent.
-- parent_task_id is not parent_run_id. Stage predecessor links stay on runs.
-- linear_issue is not a column. task_ref is the Factory-side native task id.

CREATE TABLE IF NOT EXISTS invocation_ledgers (
    invocation_id text PRIMARY KEY
    REFERENCES invocations (id) ON DELETE CASCADE,
    ledger_seq bigint NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS native_tasks (
    id text PRIMARY KEY,
    factory_id text NOT NULL
    REFERENCES factories (id) ON DELETE CASCADE,
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    parent_task_id text
    REFERENCES native_tasks (id),
    assignee text NOT NULL,
    blocker_ids text[] NOT NULL DEFAULT '{}',
    activation_id text,
    execution_id text,
    task_status text NOT NULL,
    budget_scope text NOT NULL DEFAULT '',
    task_ref text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT native_tasks_task_ref_is_id CHECK (task_ref = id)
);

CREATE INDEX IF NOT EXISTS native_tasks_factory_idx
ON native_tasks (factory_id, invocation_id);

CREATE TABLE IF NOT EXISTS invocation_events (
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    ledger_seq bigint NOT NULL,
    message_id text NOT NULL UNIQUE,
    kind text NOT NULL,
    task_id text
    REFERENCES native_tasks (id),
    body text NOT NULL DEFAULT '',
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (invocation_id, ledger_seq),
    CONSTRAINT invocation_events_seq_positive CHECK (ledger_seq > 0)
);

CREATE TABLE IF NOT EXISTS invocation_ledger_state (
    invocation_id text NOT NULL,
    ledger_seq bigint NOT NULL,
    task_id text,
    task_status text NOT NULL DEFAULT '',
    assignee text NOT NULL DEFAULT '',
    PRIMARY KEY (invocation_id, ledger_seq),
    FOREIGN KEY (invocation_id, ledger_seq)
    REFERENCES invocation_events (invocation_id, ledger_seq)
    ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS invocation_outbox (
    invocation_id text NOT NULL,
    ledger_seq bigint NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (invocation_id, ledger_seq),
    FOREIGN KEY (invocation_id, ledger_seq)
    REFERENCES invocation_events (invocation_id, ledger_seq)
    ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS native_deliveries (
    id text PRIMARY KEY,
    message_id text NOT NULL
    REFERENCES invocation_events (message_id) ON DELETE CASCADE,
    invocation_id text NOT NULL,
    ledger_seq bigint NOT NULL,
    task_id text NOT NULL
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    assignee text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (invocation_id, ledger_seq)
    REFERENCES invocation_events (invocation_id, ledger_seq)
    ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS native_deliveries_assignee_idx
ON native_deliveries (assignee, task_id);

CREATE TABLE IF NOT EXISTS native_acks (
    delivery_id text NOT NULL
    REFERENCES native_deliveries (id) ON DELETE CASCADE,
    principal text NOT NULL,
    task_id text NOT NULL,
    acked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (delivery_id, principal, task_id)
);

CREATE TABLE IF NOT EXISTS native_applies (
    message_id text PRIMARY KEY
    REFERENCES invocation_events (message_id) ON DELETE CASCADE,
    task_id text NOT NULL,
    principal text NOT NULL,
    apply_count bigint NOT NULL DEFAULT 1,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS native_wake_intents (
    task_id text PRIMARY KEY
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    assignee text NOT NULL,
    reason text NOT NULL,
    pending boolean NOT NULL DEFAULT true,
    coalesce_count bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS native_inbox_claims (
    task_id text PRIMARY KEY
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    holder text NOT NULL,
    fence bigint NOT NULL,
    claimed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT native_inbox_claims_fence_positive CHECK (fence > 0)
);
