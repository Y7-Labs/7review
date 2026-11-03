DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM budget_publication_operations WHERE state='delivered') THEN
        RAISE EXCEPTION 'schema v21 delivered publication has no durable receipt';
    END IF;
END $$;

ALTER TABLE budget_publication_operations
    ADD COLUMN delivery_receipt bytea,
    ADD COLUMN delivery_receipt_digest text,
    ADD COLUMN completed_at timestamptz,
    DROP CONSTRAINT budget_publication_operation_sending,
    ADD CONSTRAINT budget_publication_operation_sending CHECK (
        (state='pending' AND sending_at IS NULL)
        OR state='obsolete'
        OR (state IN ('sending','delivered','uncertain','failed') AND sending_at IS NOT NULL AND worker_id IS NOT NULL AND lease_until IS NOT NULL AND fence>0)
    ),
    ADD CONSTRAINT budget_publication_operation_completion CHECK (
        (state='delivered' AND delivery_receipt IS NOT NULL AND delivery_receipt_digest IS NOT NULL AND completed_at IS NOT NULL)
        OR (state<>'delivered' AND delivery_receipt IS NULL AND delivery_receipt_digest IS NULL AND completed_at IS NULL)
    );
