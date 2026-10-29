CREATE TABLE budget_inbox (
    id text PRIMARY KEY,
    boundary_id text NOT NULL,
    project_id text NOT NULL,
    run_id text NOT NULL UNIQUE,
    fingerprint text NOT NULL,
    request bytea NOT NULL,
    state text NOT NULL CHECK (state IN ('pending','running','completed','failed','expired','uncertain')),
    deadline timestamptz NOT NULL,
    claim_token text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX budget_inbox_pending ON budget_inbox(boundary_id, project_id, created_at) WHERE state='pending';
CREATE TABLE budget_inbox_conflicts (
    id text PRIMARY KEY,
    inbox_id text NOT NULL REFERENCES budget_inbox(id),
    fingerprint text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
