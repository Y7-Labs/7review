CREATE TABLE budget_model_tool_children (
    parent_reduction_id text PRIMARY KEY REFERENCES budget_model_reductions(id),
    parent_digest text NOT NULL,
    observation_id text NOT NULL UNIQUE REFERENCES budget_model_tool_observations(id),
    observation_digest text NOT NULL,
    child_action_id text NOT NULL UNIQUE REFERENCES budget_actions(id),
    builder_version integer NOT NULL CHECK (builder_version=1),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
