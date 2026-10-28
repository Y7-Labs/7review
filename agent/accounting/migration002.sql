ALTER TABLE budget_actions ADD COLUMN IF NOT EXISTS integrity_incident boolean NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS budget_receipts (
    id text PRIMARY KEY,
    action_id text NOT NULL UNIQUE REFERENCES budget_actions(id),
    payload bytea NOT NULL,
    payload_digest text NOT NULL
);
CREATE TABLE IF NOT EXISTS budget_quarantine (
    id text PRIMARY KEY,
    receipt_id text NOT NULL,
    payload bytea NOT NULL,
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
UPDATE budget_schema SET version=2 WHERE singleton=true;
