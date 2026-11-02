CREATE TABLE budget_model_reductions (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES budget_jobs(run_id),
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    group_digest text NOT NULL,
    input_digest text NOT NULL,
    artifact_digest text NOT NULL,
    artifact bytea NOT NULL,
    state text NOT NULL CHECK (state IN ('validated','needs_tools','needs_findings_repair','needs_coverage_repair','blocked')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(run_id,group_digest)
);
CREATE TABLE budget_model_reduction_inputs (
    action_id text PRIMARY KEY REFERENCES budget_action_results(action_id),
    reduction_id text NOT NULL REFERENCES budget_model_reductions(id)
);
CREATE INDEX budget_model_reduction_inputs_artifact ON budget_model_reduction_inputs(reduction_id);
