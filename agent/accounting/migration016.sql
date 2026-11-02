CREATE TABLE budget_operator_commands (
    id text PRIMARY KEY,
    boundary_id text NOT NULL,
    project_id text NOT NULL,
    command_id text NOT NULL,
    actor_id text NOT NULL,
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    run_id text NOT NULL REFERENCES budget_jobs(run_id),
    command bytea NOT NULL,
    command_digest text NOT NULL,
    receipt bytea NOT NULL,
    receipt_digest text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(boundary_id,project_id,command_id)
);
ALTER TABLE budget_jobs ADD COLUMN control_version bigint NOT NULL DEFAULT 1 CHECK(control_version>0);
ALTER TABLE budget_jobs ADD COLUMN cancel_command_id text REFERENCES budget_operator_commands(id);
ALTER TABLE budget_jobs ADD CONSTRAINT budget_jobs_cancel_disposition CHECK (
    cancel_command_id IS NULL OR (phase='finished' AND NOT dispatch_allowed AND terminal_reduction_id IS NULL)
);
ALTER TABLE budget_inbox DROP CONSTRAINT budget_inbox_state_check;
ALTER TABLE budget_inbox ADD CONSTRAINT budget_inbox_state_check
CHECK(state IN ('pending','running','completed','failed','expired','uncertain','cancelled'));
