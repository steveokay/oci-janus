-- +goose Up

-- Drop all per-tenant custom-domain tables. Per REDESIGN-001 Phase 2.1 / RM-001:
-- custom domains are removed end-to-end. Multi-mode deployments use wildcard
-- tenant subdomains (`tenant.slug.platformDomain`) which do not need DB-backed
-- lookups and are not affected by this migration.
--
-- The cascade order matters: domain_notifications references tenant_domains,
-- and tenant_domains references tenants. Explicit DROP order avoids relying
-- on RESTRICT vs CASCADE semantics.

DROP TABLE IF EXISTS domain_notifications;
DROP TABLE IF EXISTS tenant_domains;

-- +goose Down
-- See git history for the original schema. Restoring requires reverting the
-- proto + handler + repository deletions plus the original migration files
-- (20260611000001_domain_notification.sql + 20260620000002_add_domain_is_primary.sql).
-- The down migration here prevents `goose down` from erroring out; it does NOT
-- recreate the schema.
SELECT 1;
