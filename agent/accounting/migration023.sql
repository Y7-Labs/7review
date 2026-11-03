ALTER TABLE budget_publication_operations
    ADD COLUMN reconcile_after timestamptz;

UPDATE budget_publication_operations
SET reconcile_after=clock_timestamp()
WHERE state='uncertain';

ALTER TABLE budget_publication_operations
    ADD CONSTRAINT budget_publication_operation_reconciliation CHECK (
        (state='uncertain' AND reconcile_after IS NOT NULL)
        OR (state<>'uncertain' AND reconcile_after IS NULL)
    );
