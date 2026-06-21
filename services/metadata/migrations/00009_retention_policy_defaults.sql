-- +goose Up
-- FE-API-039: per-org default retention policy.
--
-- This is the inheritance fallback for FE-API-037's per-repo retention CRUD.
-- When no per-repo row exists in retention_policies, the BFF (and the executor
-- once FE-API-040 ships) drops down to the org default stored here. If the org
-- default is disabled (enabled = FALSE) the fallback is treated as "no policy"
-- so a disabled default does NOT silently start propagating to every repo.
--
-- The shape mirrors retention_policies one-for-one (rules JSONB, protected
-- patterns TEXT[], preview_until window, updated_by) so the upsert + dry-run
-- code paths can share validation + preview-window reset semantics. The
-- retention_rule_kind enum from 00008 is reused — DO NOT recreate it here.
--
-- preview_until is owned by the application layer (see retention_org.go in
-- the repository package). It mirrors the per-repo semantics: set when a
-- default is freshly enabled or rules change materially, cleared on disable.

CREATE TABLE retention_policy_defaults (
  org_id                   UUID PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  -- tenant_id is denormalised from organizations for the same reason it is on
  -- retention_policies: the executor (FE-API-040) sweeps by tenant and a
  -- denormalised column avoids a JOIN per row.
  tenant_id                UUID NOT NULL,
  enabled                  BOOLEAN NOT NULL DEFAULT FALSE,
  -- Same {kind, value} shape as retention_policies.rules. The handler
  -- validates against the retention_rule_kind enum + per-kind value caps.
  rules                    JSONB NOT NULL DEFAULT '[]',
  -- Same default as retention_policies: semver + "latest" / "stable" so a
  -- fresh default doesn't immediately evict release tags from repos it
  -- inherits down to.
  protected_tag_patterns   TEXT[] NOT NULL DEFAULT ARRAY['latest','stable','^v?\d+(\.\d+){0,2}$'],
  -- Dry-run window — same 24h semantics as the per-repo column.
  preview_until            TIMESTAMPTZ,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- Caller's auth.users.id UUID from the JWT. Nullable for system writes
  -- (the bootstrap default-creation flow once FE-API-040 lands).
  updated_by               UUID
);

-- Tenant-scoped lookups for the executor + admin UI. The org_id PK already
-- covers per-org lookups; this index speeds up "all defaults for tenant X"
-- which the FE-API-040 sweep will need.
CREATE INDEX idx_retention_policy_defaults_tenant ON retention_policy_defaults(tenant_id);

-- +goose Down
DROP TABLE IF EXISTS retention_policy_defaults;
