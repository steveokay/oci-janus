-- +goose Up
-- FE-API-037: per-repo retention policy CRUD.
--
-- The retention work spans several tickets (FE-API-037..043). This migration is
-- the foundation: one row per repository carrying the rules + protected-tag
-- patterns. Subsequent tickets layer on:
--   FE-API-038 — dry-run preview (uses preview_until below)
--   FE-API-039 — org-default fallback when no row exists
--   FE-API-040 — executor that consumes these rows
--   FE-API-041 — retention.* RabbitMQ events
--   FE-API-043 — max_idle_days enforcement (already in the enum here)
--
-- Decision: the enum lists max_idle_days from day one so we never have to write
-- a second ALTER TYPE migration when FE-API-043 ships. The handler-side
-- validation continues to allow it through, but the executor (FE-API-040) is
-- responsible for actually honouring it; until then a max_idle_days rule is a
-- no-op (still persisted, still echoed back to the UI).

-- Rule kinds enum. Forward-compat: max_idle_days lands later (FE-API-043);
-- adding it now in the enum is fine — UpsertRepoRetentionPolicy accepts
-- max_idle_days at the API level but the executor doesn't honor it yet.
CREATE TYPE retention_rule_kind AS ENUM (
  'max_age_days',        -- delete manifests pushed more than N days ago
  'max_count',           -- keep newest N manifests, evict oldest first
  'max_size_bytes',      -- cap repo total at N bytes; evict oldest first
  'dangling_grace_days', -- delete manifests dangling (no tag) for N days
  'max_idle_days'        -- delete manifests not pulled in N days (FE-API-043)
);

CREATE TABLE retention_policies (
  repo_id                  UUID PRIMARY KEY REFERENCES repositories(id) ON DELETE CASCADE,
  -- tenant_id is denormalised from the repositories row so the executor
  -- (FE-API-040) can sweep all enabled policies for a tenant with a single
  -- indexed scan rather than joining against repositories on every run.
  tenant_id                UUID NOT NULL,
  enabled                  BOOLEAN NOT NULL DEFAULT FALSE,
  -- rules is an array of {kind, value} objects matching retention_rule_kind.
  -- JSONB (not text[] of composite types) keeps the API shape isomorphic with
  -- the gRPC RetentionRule message and avoids a per-rule join table.
  rules                    JSONB NOT NULL DEFAULT '[]',
  -- protected_tag_patterns is a list of Go-flavoured regex strings; tags whose
  -- names match any pattern are excluded from deletion regardless of rule.
  -- Default covers the common operator expectations (semver + "latest" /
  -- "stable") so a fresh policy doesn't immediately evict release tags.
  protected_tag_patterns   TEXT[] NOT NULL DEFAULT ARRAY['latest','stable','^v?\d+(\.\d+){0,2}$'],
  -- preview_until is the dry-run window introduced by FE-API-038. When set,
  -- the executor (FE-API-040) calculates what would be deleted but skips the
  -- actual delete until NOW() > preview_until. UpsertRepoRetentionPolicy
  -- writes this column when a previously-disabled policy is enabled or when
  -- the rules change materially; the API never lets the caller set it
  -- directly.
  preview_until            TIMESTAMPTZ,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- updated_by carries the user_id (auth.users.id UUID) from the JWT for the
  -- caller of the last Upsert. Nullable for the bootstrap case where the
  -- executor itself patches a policy (future).
  updated_by               UUID
);

-- Tenant-scoped lookups for the executor + admin UI.
CREATE INDEX idx_retention_policies_tenant ON retention_policies(tenant_id);
-- Partial index — the executor only sweeps enabled policies, so a partial
-- index keeps the scan fast even at high repo counts.
CREATE INDEX idx_retention_policies_enabled ON retention_policies(tenant_id, enabled) WHERE enabled = TRUE;

-- +goose Down
DROP TABLE IF EXISTS retention_policies;
DROP TYPE IF EXISTS retention_rule_kind;
