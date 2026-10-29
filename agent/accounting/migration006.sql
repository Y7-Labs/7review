CREATE TABLE budget_action_results (
    action_id text PRIMARY KEY REFERENCES budget_actions(id),
    run_id text NOT NULL REFERENCES budget_jobs(run_id),
    request_digest text NOT NULL,
    response_digest text NOT NULL,
    outcome bytea NOT NULL,
    status text NOT NULL CHECK (status='recorded_unvalidated'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX budget_action_results_job ON budget_action_results(run_id);
CREATE INDEX budget_actions_attempt ON budget_actions(attempt_id);
CREATE UNIQUE INDEX budget_jobs_dispatch_owner ON budget_jobs(attempt_id) WHERE dispatch_allowed;
