-- Intake slots and delivery effects for the automation engine.
-- note is the text AppendContext stores. Context reads kind = context
-- in seq order. The same bytes are appended to work_items.body so a
-- stage prompt keeps the original fence and the notes.
-- open_pr_url is the open pull request FollowUp reads. Empty means none.
-- String literals in this file must not contain a semicolon.

CREATE TABLE IF NOT EXISTS intake_slots (
    work_item_id text NOT NULL REFERENCES work_items (id),
    automation text NOT NULL,
    delivery_id text NOT NULL,
    run_id text NOT NULL DEFAULT '',
    PRIMARY KEY (work_item_id, automation)
);

CREATE TABLE IF NOT EXISTS delivery_effects (
    delivery_id text PRIMARY KEY,
    work_item_id text NOT NULL REFERENCES work_items (id),
    kind text NOT NULL CHECK (
        kind IN ('follow_up', 'context')
    ),
    run_id text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    seq bigint GENERATED ALWAYS AS IDENTITY
);

CREATE INDEX IF NOT EXISTS delivery_effects_context_idx
ON delivery_effects (work_item_id, seq)
WHERE kind = 'context';

-- The engine stores the provider name on work_items.source.
ALTER TABLE work_items DROP CONSTRAINT IF EXISTS work_items_source_check;

ALTER TABLE work_items ADD CONSTRAINT work_items_source_check CHECK (
    source IN (
        'issue',
        'manual',
        'api',
        'webhook',
        'chat',
        'schedule',
        'mcp',
        'github',
        'slack',
        'linear',
        'factory'
    )
);

ALTER TABLE work_items
ADD COLUMN IF NOT EXISTS open_pr_url text NOT NULL DEFAULT '';
