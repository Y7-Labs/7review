CREATE TABLE budget_unused_releases (
    action_id text PRIMARY KEY REFERENCES budget_actions(id),
    cancel_command_id text NOT NULL REFERENCES budget_operator_commands(id),
    receipt bytea NOT NULL,
    receipt_digest text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE budget_unused_release_scopes (
    action_id text NOT NULL REFERENCES budget_unused_releases(action_id),
    scope_id text NOT NULL REFERENCES budget_scopes(id),
    origin_period text NOT NULL,
    PRIMARY KEY(action_id,scope_id)
);
CREATE INDEX budget_unused_release_scopes_scope ON budget_unused_release_scopes(scope_id);
ALTER TABLE budget_actions ADD COLUMN unused_release_id text REFERENCES budget_unused_releases(action_id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE budget_actions DROP CONSTRAINT budget_actions_state_check;
ALTER TABLE budget_actions ADD CONSTRAINT budget_actions_state_check
CHECK(state IN ('prepared','sending','uncertain','result_recorded','not_executed'));
ALTER TABLE budget_actions ADD CONSTRAINT budget_actions_unused_release CHECK (
    (state='not_executed')=(unused_release_id IS NOT NULL)
    AND (unused_release_id IS NULL OR (unused_release_id=id AND fenced AND NOT settled AND outcome IS NULL AND actual_amount IS NULL AND receipt_id IS NULL))
);
