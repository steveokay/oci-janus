-- Dev seed for FE-API-048 — three service accounts under the dev tenant
-- 98dbe36b-ef28-4903-b25c-bff1b2921c9e so the new /api-keys/service-accounts
-- UI is non-empty on first boot.
--
-- Idempotent: ON CONFLICT DO NOTHING so re-running the seed is safe.
-- The hardcoded SA UUIDs make each run deterministic.
--
-- API keys are NOT seeded here. A hardcoded known-plaintext key in a dev seed
-- could accidentally ship to staging. Operators can issue keys from the UI or
-- via POST /api/v1/service-accounts/<id>/keys after the stack is up.
--
-- Load with:
--   make seed-dev
-- or manually:
--   docker exec -i docker-compose-postgres-1 \
--     psql -U registry -d registry_auth < infra/dev-seed/service_accounts.sql

DO $$
DECLARE
    dev_tenant      UUID := '98dbe36b-ef28-4903-b25c-bff1b2921c9e';
    -- Look up the dev admin inserted by 20260610000001_seed_dev_tenant.sql.
    -- If the migration has not run yet (fresh volume, race), dev_admin stays
    -- NULL; sa1/sa2 then insert with created_by NULL (same as sa3).
    dev_admin       UUID := (
        SELECT id FROM users
        WHERE tenant_id = dev_tenant
          AND email = 'admin@dev.local'
        LIMIT 1
    );
    sa1_id          UUID := '11111111-aaaa-bbbb-cccc-000000000001';
    sa2_id          UUID := '11111111-aaaa-bbbb-cccc-000000000002';
    sa3_id          UUID := '11111111-aaaa-bbbb-cccc-000000000003';
    sa1_shadow      UUID;
    sa2_shadow      UUID;
    sa3_shadow      UUID;
BEGIN
    -- ── SA 1: "ci-prod" — active deploy bot ──────────────────────────────────
    -- shadow user: kind='service_account', email sentinel follows the
    -- sa+<uuid>@internal.invalid convention established in the service layer.
    INSERT INTO users (tenant_id, username, email, password_hash, kind)
    VALUES (
        dev_tenant,
        'sa-ci-prod',
        'sa+' || sa1_id || '@internal.invalid',
        '',          -- service accounts never authenticate with a password
        'service_account'
    )
    ON CONFLICT (tenant_id, email) DO NOTHING
    RETURNING id INTO sa1_shadow;

    -- If ON CONFLICT skipped the insert, RETURNING does not fire and
    -- sa1_shadow stays NULL. Re-query to recover the existing row id.
    IF sa1_shadow IS NULL THEN
        SELECT id INTO sa1_shadow
        FROM users
        WHERE tenant_id = dev_tenant
          AND email = 'sa+' || sa1_id || '@internal.invalid'
        LIMIT 1;
    END IF;

    INSERT INTO service_accounts (
        id, tenant_id, shadow_user_id, name, description, allowed_scopes, created_by
    )
    VALUES (
        sa1_id,
        dev_tenant,
        sa1_shadow,
        'ci-prod',
        'GitHub Actions deploy bot for myapp',
        ARRAY['pull', 'push'],
        dev_admin       -- NULL-safe: if admin row missing, falls back to NULL
    )
    ON CONFLICT DO NOTHING;

    -- ── SA 2: "old-bot" — disabled ───────────────────────────────────────────
    INSERT INTO users (tenant_id, username, email, password_hash, kind)
    VALUES (
        dev_tenant,
        'sa-old-bot',
        'sa+' || sa2_id || '@internal.invalid',
        '',
        'service_account'
    )
    ON CONFLICT (tenant_id, email) DO NOTHING
    RETURNING id INTO sa2_shadow;

    IF sa2_shadow IS NULL THEN
        SELECT id INTO sa2_shadow
        FROM users
        WHERE tenant_id = dev_tenant
          AND email = 'sa+' || sa2_id || '@internal.invalid'
        LIMIT 1;
    END IF;

    INSERT INTO service_accounts (
        id, tenant_id, shadow_user_id, name, description, allowed_scopes,
        created_by, disabled_at
    )
    VALUES (
        sa2_id,
        dev_tenant,
        sa2_shadow,
        'old-bot',
        '',
        ARRAY['pull'],
        dev_admin,
        now() - interval '7 days'   -- disabled a week ago
    )
    ON CONFLICT DO NOTHING;

    -- ── SA 3: "orphaned-creator-sa" — created_by NULL ────────────────────────
    -- Exercises the UI's audit-snapshot fallback when the creator user has
    -- been deleted (created_by is NULL from the start in this seed).
    INSERT INTO users (tenant_id, username, email, password_hash, kind)
    VALUES (
        dev_tenant,
        'sa-orphaned-creator',
        'sa+' || sa3_id || '@internal.invalid',
        '',
        'service_account'
    )
    ON CONFLICT (tenant_id, email) DO NOTHING
    RETURNING id INTO sa3_shadow;

    IF sa3_shadow IS NULL THEN
        SELECT id INTO sa3_shadow
        FROM users
        WHERE tenant_id = dev_tenant
          AND email = 'sa+' || sa3_id || '@internal.invalid'
        LIMIT 1;
    END IF;

    INSERT INTO service_accounts (
        id, tenant_id, shadow_user_id, name, description, allowed_scopes, created_by
    )
    VALUES (
        sa3_id,
        dev_tenant,
        sa3_shadow,
        'orphaned-creator-sa',
        '',
        ARRAY['pull'],
        NULL            -- intentionally NULL — creator already gone
    )
    ON CONFLICT DO NOTHING;

END $$;
