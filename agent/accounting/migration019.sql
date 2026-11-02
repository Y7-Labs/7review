ALTER TABLE budget_jobs ADD COLUMN publication_authorization_id text REFERENCES budget_operator_commands(id);
ALTER TABLE budget_jobs ADD CONSTRAINT budget_jobs_publication_authorization CHECK (
    publication_authorization_id IS NULL
    OR (terminal_reduction_id IS NOT NULL AND cancel_command_id IS NULL AND phase='finished' AND NOT dispatch_allowed)
);
