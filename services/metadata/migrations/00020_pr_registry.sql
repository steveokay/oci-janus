-- +goose Up

-- FUT-023 Phase 1 — ephemeral PR-scoped registries.
--
-- Two tables:
--
--   pr_registry_config — one row per tenant. Holds the per-tenant
--   enable flag plus the KEK-sealed webhook secret used to verify
--   incoming provider (GitHub/GitLab/...) PR webhooks, and an optional
--   promote target org that a merged PR's images are copied into.
--
--   pr_namespaces — one row per PR namespace. Tracks the lifecycle of
--   the ephemeral org created for a given PR so a merge/close can tear
--   it down and (later) a promote can copy its images elsewhere.
--
-- Tenant-isolation posture matches every other table in this schema —
-- app-layer WHERE clauses in services/metadata; RLS is documented in
-- CLAUDE.md §9 but not applied per-table today (Phase 7 pending).

-- One row per tenant. tenant_id is the PK so a tenant can only ever
-- hold a single PR-registry config; an upsert on tenant_id toggles it.
CREATE TABLE pr_registry_config (
    tenant_id           UUID PRIMARY KEY,
    -- Master switch. FALSE (default) means the PR-registry feature is
    -- off for this tenant and incoming PR webhooks are ignored.
    enabled             BOOLEAN     NOT NULL DEFAULT FALSE,
    -- AES-256-GCM sealed under PR_REGISTRY_KEY_HEX; NULL = unset. The
    -- plaintext webhook secret is never stored — only the sealed blob.
    webhook_secret_enc  BYTEA,
    -- KEK version stamped on the sealed secret so a future key rotation
    -- can identify which key sealed a given row (matches the FUT-019
    -- kek_version convention).
    kek_version         SMALLINT    NOT NULL DEFAULT 1,
    -- Org that a merged PR's images are promoted into. NULL means a
    -- merge simply tears the PR namespace down with no promotion.
    promote_target_org  TEXT,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- JWT-derived user id of the operator who last changed the config,
    -- or NULL for CLI/bot-driven writes. Nullable rather than an FK so a
    -- later user deletion doesn't cascade the config row away.
    updated_by          UUID
);

-- One row per PR namespace. The UNIQUE constraint enforces exactly one
-- live-or-torn-down lifecycle row per (tenant, provider, source repo,
-- PR number) so re-processing a webhook is idempotent.
CREATE TABLE pr_namespaces (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL,
    -- org_id is ON DELETE SET NULL (NOT cascade) so this lifecycle row
    -- survives as a torn-down record after the ephemeral org is deleted.
    org_id        UUID        REFERENCES organizations(id) ON DELETE SET NULL,
    -- Source-control provider (e.g. 'github', 'gitlab').
    provider      TEXT        NOT NULL,
    -- Upstream repo the PR was opened against (e.g. 'owner/name').
    source_repo   TEXT        NOT NULL,
    pr_number     INTEGER     NOT NULL,
    -- Name of the ephemeral registry org created for this PR.
    org_name      TEXT        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active','torn_down')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    torn_down_at  TIMESTAMPTZ,
    UNIQUE (tenant_id, provider, source_repo, pr_number)
);

-- The "active PR namespaces for this tenant" list query filters by
-- (tenant_id, status); a dedicated index keeps that read off a seq scan
-- as the torn-down rows accumulate over the life of the tenant.
CREATE INDEX idx_pr_namespaces_tenant_status
    ON pr_namespaces (tenant_id, status);

-- +goose Down

DROP INDEX IF EXISTS idx_pr_namespaces_tenant_status;
DROP TABLE IF EXISTS pr_namespaces;
DROP TABLE IF EXISTS pr_registry_config;
