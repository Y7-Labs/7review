CREATE TABLE budget_model_tool_observations (
    id text PRIMARY KEY,
    parent_reduction_id text NOT NULL UNIQUE REFERENCES budget_model_reductions(id),
    run_id text NOT NULL REFERENCES budget_jobs(run_id),
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    receipt bytea NOT NULL,
    receipt_digest text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE budget_tool_observation_scopes (
    observation_id text NOT NULL REFERENCES budget_model_tool_observations(id),
    scope_id text NOT NULL REFERENCES budget_scopes(id),
    origin_period text NOT NULL,
    PRIMARY KEY(observation_id,scope_id)
);
CREATE INDEX budget_tool_observation_scopes_scope ON budget_tool_observation_scopes(scope_id);
ALTER TABLE budget_jobs
    ADD COLUMN tool_observation_id text REFERENCES budget_model_tool_observations(id);
