-- M9-N native policy, activation, join, and hierarchical budget accounts.
-- parent_task_id stays the delegation edge. parent_run_id is not that edge.
-- linear_issue is still not a column.

ALTER TABLE native_tasks
ADD COLUMN task_depth int NOT NULL DEFAULT 0,
ADD COLUMN stage_owner boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS native_policies (
    invocation_id text PRIMARY KEY
    REFERENCES invocations (id) ON DELETE CASCADE,
    factory_id text NOT NULL
    REFERENCES factories (id) ON DELETE CASCADE,
    digest text NOT NULL,
    native_hierarchy boolean NOT NULL,
    max_depth int NOT NULL,
    absolute_max_depth int NOT NULL,
    allow_ancestor_shortcut boolean NOT NULL,
    delegation text NOT NULL,
    stage_owner text NOT NULL,
    personas jsonb NOT NULL,
    ceiling_cents bigint NOT NULL,
    capacity_slots int NOT NULL,
    max_live int NOT NULL,
    shared_key boolean NOT NULL DEFAULT false,
    agent_id text NOT NULL DEFAULT '',
    work_item_id text NOT NULL DEFAULT '',
    -- noqa: disable=RF04
    stage text NOT NULL DEFAULT '',
    -- noqa: enable=RF04
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT native_policies_ceiling_nonneg CHECK (ceiling_cents >= 0),
    CONSTRAINT native_policies_slots_positive CHECK (capacity_slots > 0),
    CONSTRAINT native_policies_max_live_positive CHECK (max_live > 0)
);

CREATE TABLE IF NOT EXISTS native_capacity (
    invocation_id text PRIMARY KEY
    REFERENCES invocations (id) ON DELETE CASCADE,
    slots int NOT NULL,
    held int NOT NULL DEFAULT 0,
    CONSTRAINT native_capacity_slots_positive CHECK (slots > 0),
    CONSTRAINT native_capacity_held_nonneg CHECK (held >= 0),
    CONSTRAINT native_capacity_held_le_slots CHECK (held <= slots)
);

CREATE TABLE IF NOT EXISTS native_activations (
    id text PRIMARY KEY,
    task_id text NOT NULL
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    run_id text NOT NULL
    REFERENCES runs (id),
    execution_id text NOT NULL,
    fence bigint NOT NULL,
    state text NOT NULL,
    stage_owner boolean NOT NULL DEFAULT false,
    capacity_held boolean NOT NULL DEFAULT false,
    prior_run_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT native_activations_fence_positive CHECK (fence > 0),
    CONSTRAINT native_activations_task_fence UNIQUE (task_id, fence),
    CONSTRAINT native_activations_execution UNIQUE (execution_id)
);

CREATE INDEX IF NOT EXISTS native_activations_run_idx
ON native_activations (run_id);

CREATE TABLE IF NOT EXISTS native_delegations (
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    idempotency_key text NOT NULL,
    parent_task_id text NOT NULL
    REFERENCES native_tasks (id),
    child_task_id text NOT NULL
    REFERENCES native_tasks (id),
    reservation_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (invocation_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS native_joins (
    parent_task_id text NOT NULL
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    child_task_id text NOT NULL
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    required boolean NOT NULL DEFAULT true,
    -- noqa: disable=RF04
    policy text NOT NULL,
    -- noqa: enable=RF04
    PRIMARY KEY (parent_task_id, child_task_id),
    CONSTRAINT native_joins_policy CHECK (
        policy IN ('fail_parent', 'block_parent')
    )
);

CREATE TABLE IF NOT EXISTS native_subtree_gates (
    task_id text PRIMARY KEY
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    state text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT native_subtree_gates_state CHECK (
        state IN ('cancelled', 'paused')
    )
);

CREATE TABLE IF NOT EXISTS native_board_resumes (
    id text PRIMARY KEY,
    task_id text NOT NULL
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    principal text NOT NULL,
    prior_run_id text NOT NULL,
    new_run_id text NOT NULL,
    prior_status text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS native_terminal_evidence (
    run_id text PRIMARY KEY
    REFERENCES runs (id),
    task_id text NOT NULL,
    status text NOT NULL,
    execution_id text NOT NULL,
    ended_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS native_input (
    task_id text PRIMARY KEY
    REFERENCES native_tasks (id) ON DELETE CASCADE,
    delivery text NOT NULL,
    negotiated_steer boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT native_input_delivery CHECK (delivery IN ('pending', 'steer'))
);

CREATE TABLE IF NOT EXISTS budget_scopes (
    scope_id text PRIMARY KEY,
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    kind text NOT NULL,
    ceiling_cents bigint NOT NULL,
    spent_cents bigint NOT NULL DEFAULT 0,
    reserved_cents bigint NOT NULL DEFAULT 0,
    paused boolean NOT NULL DEFAULT false,
    missing_usage boolean NOT NULL DEFAULT false,
    alert_episode int NOT NULL DEFAULT 1,
    alerted_in_episode boolean NOT NULL DEFAULT false,
    shared_key boolean NOT NULL DEFAULT false,
    guarantees_ceiling boolean NOT NULL DEFAULT false,
    CONSTRAINT budget_scopes_kind CHECK (
        kind IN ('factory', 'invocation', 'lead', 'agent')
    ),
    CONSTRAINT budget_scopes_nonneg CHECK (
        spent_cents >= 0 AND reserved_cents >= 0 AND ceiling_cents >= 0
    ),
    CONSTRAINT budget_scopes_within_ceiling CHECK (
        spent_cents + reserved_cents <= ceiling_cents
    )
);

CREATE TABLE IF NOT EXISTS budget_reservations (
    id text PRIMARY KEY,
    invocation_id text NOT NULL
    REFERENCES invocations (id) ON DELETE CASCADE,
    idempotency_key text NOT NULL,
    task_id text,
    cents bigint NOT NULL,
    state text NOT NULL,
    usage_deadline timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT budget_reservations_cents_nonneg CHECK (cents >= 0),
    CONSTRAINT budget_reservations_state CHECK (
        state IN ('reserved', 'settled', 'refunded')
    ),
    CONSTRAINT budget_reservations_key UNIQUE (invocation_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS budget_reservation_lines (
    reservation_id text NOT NULL
    REFERENCES budget_reservations (id) ON DELETE CASCADE,
    scope_id text NOT NULL
    REFERENCES budget_scopes (scope_id),
    cents bigint NOT NULL,
    PRIMARY KEY (reservation_id, scope_id),
    CONSTRAINT budget_reservation_lines_cents_nonneg CHECK (cents >= 0)
);

CREATE TABLE IF NOT EXISTS budget_charges (
    call_id text NOT NULL,
    attempt_id text NOT NULL,
    reservation_id text,
    cents bigint NOT NULL,
    failed boolean NOT NULL DEFAULT false,
    quarantined_cents bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (call_id, attempt_id)
);

CREATE TABLE IF NOT EXISTS budget_alerts (
    scope_id text NOT NULL
    REFERENCES budget_scopes (scope_id),
    episode int NOT NULL,
    threshold_percent int NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope_id, episode)
);

CREATE TABLE IF NOT EXISTS budget_overrides (
    id text PRIMARY KEY,
    scope_id text NOT NULL
    REFERENCES budget_scopes (scope_id),
    principal text NOT NULL,
    prior_ceiling bigint NOT NULL,
    new_ceiling bigint NOT NULL,
    spent_cents bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT budget_overrides_spend_zero CHECK (spent_cents = 0)
);

CREATE TABLE IF NOT EXISTS budget_quarantine (
    id text PRIMARY KEY,
    call_id text NOT NULL,
    attempt_id text NOT NULL,
    scope_id text NOT NULL,
    reported_cents bigint NOT NULL,
    reservation_cents bigint NOT NULL,
    excess_cents bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
