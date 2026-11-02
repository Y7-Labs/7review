ALTER TABLE budget_attempts ADD COLUMN superseded_by_attempt_id text REFERENCES budget_attempts(id);
ALTER TABLE budget_attempts ADD COLUMN supersession_intake_id text REFERENCES budget_intakes(id);
ALTER TABLE budget_attempts ADD CONSTRAINT budget_attempts_supersession CHECK (
    (superseded_by_attempt_id IS NULL)=(supersession_intake_id IS NULL)
    AND (superseded_by_attempt_id IS NULL OR superseded_by_attempt_id<>id)
);

ALTER TABLE budget_unused_releases ALTER COLUMN cancel_command_id DROP NOT NULL;
ALTER TABLE budget_unused_releases ADD COLUMN cause text NOT NULL DEFAULT 'cancelled';
ALTER TABLE budget_unused_releases ADD COLUMN superseding_attempt_id text REFERENCES budget_attempts(id);
ALTER TABLE budget_unused_releases ADD COLUMN supersession_intake_id text REFERENCES budget_intakes(id);
ALTER TABLE budget_unused_releases ADD CONSTRAINT budget_unused_release_authority CHECK (
    (cause='cancelled' AND cancel_command_id IS NOT NULL AND superseding_attempt_id IS NULL AND supersession_intake_id IS NULL)
    OR
    (cause='superseded' AND cancel_command_id IS NULL AND superseding_attempt_id IS NOT NULL AND supersession_intake_id IS NOT NULL)
);

ALTER TABLE budget_inbox DROP CONSTRAINT budget_inbox_state_check;
ALTER TABLE budget_inbox ADD CONSTRAINT budget_inbox_state_check
CHECK(state IN ('pending','running','completed','failed','expired','uncertain','cancelled','superseded'));
