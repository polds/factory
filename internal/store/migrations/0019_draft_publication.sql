-- One draft publication for an invocation. The same target issue cannot
-- be published twice. The row stores the SHA, ref, and pull request
-- identity. It has no token and no bundle bytes.

CREATE TABLE IF NOT EXISTS draft_publications (
    invocation_id text PRIMARY KEY
    REFERENCES invocations (id) ON DELETE CASCADE,
    target_repo text NOT NULL,
    issue_key text NOT NULL,
    state text NOT NULL,
    head_sha text NOT NULL,
    git_ref text NOT NULL,
    pr_number integer,
    pr_url text,
    draft boolean,
    detail text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT draft_publications_state_check
    CHECK (state IN ('pending', 'pushed', 'opened', 'failed')),
    CONSTRAINT draft_publications_target_issue
    UNIQUE (target_repo, issue_key)
);
