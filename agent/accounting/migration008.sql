ALTER TABLE budget_actions DROP CONSTRAINT budget_actions_state_check;
ALTER TABLE budget_actions ADD CONSTRAINT budget_actions_state_check
CHECK (state IN ('prepared','sending','uncertain','result_recorded'));
CREATE TABLE budget_action_leases (
    action_id text PRIMARY KEY REFERENCES budget_actions(id),
    worker_id text,
    fence bigint NOT NULL DEFAULT 0 CHECK (fence >= 0),
    lease_until timestamptz,
    job_token text,
    sending_fence bigint,
    CHECK ((worker_id IS NULL) = (lease_until IS NULL)),
    CHECK (sending_fence IS NULL OR sending_fence=fence)
);
