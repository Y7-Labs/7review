CREATE TABLE budget_model_batches (
    id text PRIMARY KEY,
    run_id text NOT NULL UNIQUE REFERENCES budget_jobs(run_id),
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    group_digest text NOT NULL,
    manifest_digest text NOT NULL,
    manifest bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE budget_jobs
    ADD COLUMN model_batch_id text REFERENCES budget_model_batches(id);
