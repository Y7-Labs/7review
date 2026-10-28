CREATE TABLE IF NOT EXISTS budget_schema (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    version integer NOT NULL
);
INSERT INTO budget_schema(singleton,version) VALUES(true,1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS budget_attempts (
    id text PRIMARY KEY,
    binding_digest text NOT NULL,
    binding bytea NOT NULL,
    active boolean NOT NULL DEFAULT true
);
CREATE TABLE IF NOT EXISTS budget_scopes (
    id text PRIMARY KEY,
    kind text NOT NULL CHECK(kind IN ('attempt','change','project')),
    authority_digest text NOT NULL
);
CREATE TABLE IF NOT EXISTS budget_actions (
    id text PRIMARY KEY,
    attempt_id text NOT NULL REFERENCES budget_attempts(id),
    request_digest text NOT NULL,
    intent_digest text NOT NULL,
    request bytea NOT NULL,
    quote bytea NOT NULL,
    upper_amount jsonb NOT NULL,
    state text NOT NULL CHECK(state IN ('sending','uncertain','result_recorded')),
    outcome bytea,
    settled boolean NOT NULL DEFAULT false,
    actual_amount jsonb,
    receipt_id text UNIQUE,
    CHECK(settled = (actual_amount IS NOT NULL)),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS budget_routes (
    id text PRIMARY KEY,
    qualification_digest text NOT NULL,
    qualification bytea NOT NULL
);
CREATE TABLE IF NOT EXISTS budget_holds (
    action_id text NOT NULL REFERENCES budget_actions(id),
    scope_id text NOT NULL REFERENCES budget_scopes(id),
    origin_period text NOT NULL,
    PRIMARY KEY(action_id,scope_id)
);
CREATE INDEX IF NOT EXISTS budget_holds_scope ON budget_holds(scope_id);
