-- +goose Up

-- FE-API-007: when a tenant has multiple verified custom domains we need a
-- deterministic "this one is the registry hostname" pick. is_primary marks the
-- winner; the partial unique index enforces at most one primary per tenant.

ALTER TABLE tenant_domains
    ADD COLUMN IF NOT EXISTS is_primary BOOLEAN NOT NULL DEFAULT FALSE;

-- Backfill: for tenants with at least one verified domain, mark the oldest
-- verified row as primary. ORDER BY (verified_at, registered_at, id) gives a
-- stable pick even when verified_at is NULL on older rows.
WITH first_verified AS (
    SELECT DISTINCT ON (tenant_id) id
    FROM tenant_domains
    WHERE verified = TRUE
    ORDER BY tenant_id,
             verified_at NULLS LAST,
             registered_at,
             id
)
UPDATE tenant_domains td
SET is_primary = TRUE
FROM first_verified fv
WHERE td.id = fv.id;

-- Partial unique index: at most one is_primary=TRUE row per tenant. Postgres
-- treats FALSE values as outside the index, so unverified or non-primary
-- domains don't collide.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_domains_one_primary
    ON tenant_domains(tenant_id)
    WHERE is_primary;

-- +goose Down

DROP INDEX IF EXISTS idx_tenant_domains_one_primary;
ALTER TABLE tenant_domains DROP COLUMN IF EXISTS is_primary;
