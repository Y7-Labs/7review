CREATE TABLE IF NOT EXISTS budget_heads (
    id text PRIMARY KEY,
    generation bigint NOT NULL CHECK (generation >= 0),
    current_attempt_id text REFERENCES budget_attempts(id)
);
ALTER TABLE budget_attempts ADD COLUMN IF NOT EXISTS tracking_id text REFERENCES budget_heads(id);
CREATE TABLE IF NOT EXISTS budget_intakes (
    id text PRIMARY KEY,
    fingerprint text NOT NULL,
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    receipt bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS budget_intake_conflicts (
    id text PRIMARY KEY,
    intake_id text NOT NULL,
    fingerprint text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
