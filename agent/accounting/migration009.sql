ALTER TABLE budget_actions ADD COLUMN fenced boolean NOT NULL DEFAULT false;
ALTER TABLE budget_jobs ADD COLUMN recovery_action_id text REFERENCES budget_actions(id);
UPDATE budget_actions a SET fenced=true
WHERE a.state='prepared' OR EXISTS (
    SELECT 1 FROM budget_action_leases l WHERE l.action_id=a.id
);
