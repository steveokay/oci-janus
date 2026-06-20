-- +goose Up

-- FE-API-007: tenants need a stable, DNS-safe slug used to build the wildcard
-- registry hostname `<slug>.<PLATFORM_BASE_DOMAIN>`. `name` already enforces
-- a DNS-safe shape via tenantNameRE in the handler, but slug is a separate
-- column so an admin can rename a tenant without losing the hostname.

ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS slug TEXT;

-- Backfill slug from name: lowercase, replace non-alphanumeric runs with `-`,
-- collapse repeated `-`, and trim leading/trailing `-`. The double regexp_replace
-- mirrors the Go-side normaliser in services/tenant/internal/repository.NormalizeSlug
-- so the SQL and application paths produce identical results.
UPDATE tenants
SET slug = trim(BOTH '-' FROM regexp_replace(
        regexp_replace(lower(name), '[^a-z0-9]+', '-', 'g'),
        '-+', '-', 'g'))
WHERE slug IS NULL OR slug = '';

-- Any tenants whose name normalises to an empty string (extreme edge case for
-- non-ASCII-only names that bypassed the older validator) fall back to the
-- raw tenant id so the column is never NULL after backfill.
UPDATE tenants
SET slug = id::text
WHERE slug IS NULL OR slug = '';

-- NOT NULL after backfill guarantees every existing row has a value before we
-- enforce the unique index.
ALTER TABLE tenants
    ALTER COLUMN slug SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_slug_unique ON tenants(slug);

-- +goose Down

DROP INDEX IF EXISTS idx_tenants_slug_unique;
ALTER TABLE tenants DROP COLUMN IF EXISTS slug;
