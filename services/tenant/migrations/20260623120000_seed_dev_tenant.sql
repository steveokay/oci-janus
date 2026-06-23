-- +goose Up
-- +goose StatementBegin

-- Dev tenant — the UUID matches the seed in
-- services/auth/migrations/20260610000001_seed_dev_tenant.sql so a
-- fresh checkout boots a working dev stack end-to-end (admin login →
-- workspace settings → register custom domain) without manual SQL.
--
-- Without this row, any tenant-service write that has a FK on
-- tenants(id) — `tenant_domains`, `tenant_policies` — fails with a
-- 23503 foreign-key violation and the BFF surfaces a generic "couldn't
-- register, check the BFF logs" toast. That was the actual failure
-- mode the operator hit on 2026-06-23.
--
-- ON CONFLICT (id) DO NOTHING so re-running the migration on an
-- already-seeded DB is a safe no-op. The same UUID is used by every
-- service that needs the dev tenant (auth, audit, metadata, etc.) —
-- treat changing it as a coordinated cross-service migration.

INSERT INTO tenants (id, name, plan, slug, created_at)
VALUES (
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e',
    'Dev',
    'free',
    'dev',
    now()
)
ON CONFLICT (id) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Symmetrical delete. Cascades cleanly via ON DELETE CASCADE on the
-- tenant_domains + tenant_policies FKs, so a goose-down sweeps the
-- whole dev fixture including any test-registered domains.

DELETE FROM tenants WHERE id = '98dbe36b-ef28-4903-b25c-bff1b2921c9e';

-- +goose StatementEnd
