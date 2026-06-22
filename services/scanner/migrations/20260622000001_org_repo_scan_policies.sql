-- +goose Up

-- FE-API-049 — org-default + per-repo scan policy tables.
--
-- Inheritance chain (resolved at scan-on-push by services/scanner):
--
--   per-repo override (this PR's repo_scan_policies)
--     → org default (this PR's org_scan_policies, only when enabled=true)
--     → tenant policy (FE-API-018 scan_policies)
--     → synthesized auto_scan_on_push=true (no rows anywhere)
--
-- We keep both tables narrow + structurally identical to scan_policies so
-- the validation helpers and the FE editor can be shared. Constraint
-- shapes match scan_policies row-for-row.

CREATE TABLE org_scan_policies (
    -- PK is org_id alone — an org belongs to exactly one tenant in the
    -- metadata service, so denormalising tenant_id here is a referential
    -- convenience (used by RLS-style queries that scope reads) rather
    -- than a uniqueness contributor.
    org_id               UUID PRIMARY KEY,
    tenant_id            UUID NOT NULL,
    auto_scan_on_push    BOOLEAN NOT NULL DEFAULT TRUE,
    block_on_severity    TEXT NOT NULL DEFAULT ''
        CHECK (block_on_severity IN ('','CRITICAL','HIGH','MEDIUM','LOW')),
    exempt_cves          TEXT[] NOT NULL DEFAULT '{}',
    scanner_plugin       TEXT NOT NULL DEFAULT 'trivy',
    scanner_version_pin  TEXT NOT NULL DEFAULT '',
    -- enabled flips the policy on/off without losing config. A disabled
    -- org default does NOT propagate to inheriting repos — the
    -- inheritance helper treats it as if no row existed (matches
    -- FE-API-039 semantics).
    enabled              BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- updated_by is the user_id of the last actor; never client-supplied
    -- — the BFF reads it from the JWT.
    updated_by           UUID
);

-- Index supports the tenant-wide listing used by future admin surfaces
-- (e.g. "show every org with a custom scan policy"). Tenant-scoped
-- reads in the inheritance helper hit the PK directly so this index is
-- a no-op for them.
CREATE INDEX idx_org_scan_policies_tenant
    ON org_scan_policies(tenant_id);

CREATE TABLE repo_scan_policies (
    repo_id              UUID PRIMARY KEY,
    tenant_id            UUID NOT NULL,
    -- org_id is denormalised onto the row so the inheritance helper can
    -- find the parent org without a metadata round-trip when GetEffective
    -- is called with only repo_id.
    org_id               UUID NOT NULL,
    auto_scan_on_push    BOOLEAN NOT NULL DEFAULT TRUE,
    block_on_severity    TEXT NOT NULL DEFAULT ''
        CHECK (block_on_severity IN ('','CRITICAL','HIGH','MEDIUM','LOW')),
    exempt_cves          TEXT[] NOT NULL DEFAULT '{}',
    scanner_plugin       TEXT NOT NULL DEFAULT 'trivy',
    scanner_version_pin  TEXT NOT NULL DEFAULT '',
    enabled              BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by           UUID
);

CREATE INDEX idx_repo_scan_policies_tenant
    ON repo_scan_policies(tenant_id);
CREATE INDEX idx_repo_scan_policies_org
    ON repo_scan_policies(org_id);

-- Note on existing FE-API-018 scan_policies rows: this migration does
-- NOT touch them. The tenant-level row stays as the bottom-of-chain
-- fallback for tenants that haven't opted into org/repo overrides yet.
-- A future migration could backfill org defaults from existing tenant
-- policies, but that's a separate ticket (FE-API-049 ships
-- backwards-compatible by design).

-- +goose Down

DROP INDEX IF EXISTS idx_repo_scan_policies_org;
DROP INDEX IF EXISTS idx_repo_scan_policies_tenant;
DROP TABLE IF EXISTS repo_scan_policies;
DROP INDEX IF EXISTS idx_org_scan_policies_tenant;
DROP TABLE IF EXISTS org_scan_policies;
