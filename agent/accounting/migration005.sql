CREATE TABLE budget_jobs (
    run_id text PRIMARY KEY REFERENCES budget_inbox(run_id),
    attempt_id text REFERENCES budget_attempts(id),
    phase text NOT NULL CHECK (phase IN ('accepted','acquiring','admitted','effect_started','finished','legacy_unknown')),
    receipt bytea,
	dispatch_allowed boolean NOT NULL DEFAULT false,
    CHECK ((attempt_id IS NULL) = (receipt IS NULL)),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX budget_jobs_attempt ON budget_jobs(attempt_id);
INSERT INTO budget_jobs(run_id,phase)
SELECT run_id,CASE WHEN state='pending' THEN 'accepted' ELSE 'legacy_unknown' END FROM budget_inbox;
