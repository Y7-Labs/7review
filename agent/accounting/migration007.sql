CREATE TABLE budget_aggregates (
    run_id text PRIMARY KEY REFERENCES budget_jobs(run_id),
    checkpoint bytea NOT NULL,
    checkpoint_digest text NOT NULL,
    initial_event jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- Never reconstruct old accepted state from newer SCM data during migration.
UPDATE budget_jobs SET phase='legacy_unknown',dispatch_allowed=false
WHERE phase<>'finished';
