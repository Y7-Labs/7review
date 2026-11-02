CREATE TABLE budget_publication_operations (
    id text PRIMARY KEY,
    boundary_id text NOT NULL,
    project_id text NOT NULL,
    authorization_id text NOT NULL UNIQUE REFERENCES budget_operator_commands(id),
    command_id text NOT NULL,
    run_id text NOT NULL REFERENCES budget_jobs(run_id),
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    assessment_digest text NOT NULL,
    artifact_digest text NOT NULL,
    destination bytea NOT NULL,
    destination_digest text NOT NULL,
    payload bytea NOT NULL,
    payload_digest text NOT NULL,
    state text NOT NULL CHECK(state IN ('pending','sending','delivered','uncertain','failed','obsolete')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(boundary_id,project_id,command_id)
);
CREATE INDEX budget_publication_operations_state ON budget_publication_operations(boundary_id,project_id,state,created_at);
