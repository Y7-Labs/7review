CREATE TABLE budget_finding_dispositions (
    id text PRIMARY KEY REFERENCES budget_operator_commands(id),
    boundary_id text NOT NULL,
    project_id text NOT NULL,
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    finding_id text NOT NULL,
    finding_digest text NOT NULL,
    decision text NOT NULL CHECK(decision IN ('accepted','rejected','disputed')),
    created_at timestamptz NOT NULL,
    UNIQUE(boundary_id,project_id,attempt_id,finding_id,id)
);

CREATE TABLE budget_finding_disposition_heads (
    boundary_id text NOT NULL,
    project_id text NOT NULL,
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    finding_id text NOT NULL,
    disposition_id text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(boundary_id,project_id,attempt_id,finding_id),
    FOREIGN KEY(boundary_id,project_id,attempt_id,finding_id,disposition_id)
        REFERENCES budget_finding_dispositions(boundary_id,project_id,attempt_id,finding_id,id)
);
