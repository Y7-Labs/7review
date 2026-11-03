ALTER TABLE budget_publication_operations
    ADD COLUMN worker_id text,
    ADD COLUMN fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0),
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN sending_at timestamptz,
    ADD CONSTRAINT budget_publication_operation_lease CHECK (
        (worker_id IS NULL AND lease_until IS NULL)
        OR (worker_id IS NOT NULL AND lease_until IS NOT NULL AND fence>0)
    ),
    ADD CONSTRAINT budget_publication_operation_sending CHECK (
        (state='pending' AND sending_at IS NULL)
        OR state='obsolete'
        OR (state IN ('sending','delivered','uncertain','failed') AND sending_at IS NOT NULL AND worker_id IS NOT NULL AND lease_until IS NOT NULL AND fence>0)
    );
