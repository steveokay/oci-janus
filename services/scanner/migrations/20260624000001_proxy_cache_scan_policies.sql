-- +goose Up

-- FUT-017 — per-upstream scan policy for the pull-through proxy cache.
--
-- When services/proxy successfully caches a manifest from an upstream
-- registry it publishes events.RoutingCachePopulated. The scanner
-- consumer (worker.HandleCachePopulated) looks up the row matching
-- (tenant_id, upstream_name); if present AND auto_scan=true, it
-- enqueues a scan against the cached manifest digest.
--
-- Keyed by (tenant_id, upstream_name) — not upstream_id — so a tenant
-- can configure "always scan whatever I pull through Docker Hub" once
-- and have it survive an upstream_id rotation (re-pairing the same
-- logical upstream to a new credential set). upstream_name is the
-- operator-chosen handle (e.g. "dockerhub", "ecr") that services/proxy
-- emits on the cache.populated payload.
--
-- The schema is intentionally narrower than the FE-API-018 / FE-API-049
-- scan policy tables: a proxy-cache policy only chooses (a) whether to
-- auto-scan and (b) the block-on severity. We do NOT carry exempt_cves
-- or scanner_version_pin here — proxy-cached images go through the same
-- worker pool as pushed images, so any exempt list / version pin
-- attached to the per-tenant policy still applies once the scan is
-- enqueued. Keeping the table narrow also keeps the FE editor for FUT-017
-- a single boolean + a select.

CREATE TABLE proxy_cache_scan_policies (
    tenant_id          UUID NOT NULL,
    -- upstream_name is the operator-chosen handle for the upstream
    -- (e.g. "dockerhub", "ghcr", "ecr"). Lowercased + dash-bounded by
    -- the BFF before reaching this column. Free-form TEXT so an
    -- operator can rename an upstream without a migration; the
    -- (tenant_id, upstream_name) composite key is the row identity.
    upstream_name      TEXT NOT NULL,
    -- auto_scan flips the consumer's enqueue gate. Defaults to FALSE so
    -- a freshly-created row is a no-op until the operator opts in —
    -- mirrors the explicit-opt-in posture of the FE-API-018 tenant
    -- policy default for newly-arriving tenants without scanning
    -- preferences.
    auto_scan          BOOLEAN NOT NULL DEFAULT FALSE,
    -- severity_threshold is the lowest severity that should trigger
    -- the same block-on-severity behaviour the per-tenant policy
    -- provides. Empty string means "never block, just record findings".
    -- "none" is accepted as an explicit synonym for "never block" so
    -- the FE radio button maps cleanly to a non-empty token.
    severity_threshold TEXT NOT NULL DEFAULT ''
        CHECK (severity_threshold IN ('','none','low','medium','high','critical')),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by         UUID,
    PRIMARY KEY (tenant_id, upstream_name)
);

-- Index supports the ListProxyCacheScanPolicies RPC (tenant-scoped
-- listing for the admin FE). The PK already covers point reads.
CREATE INDEX idx_proxy_cache_scan_policies_tenant
    ON proxy_cache_scan_policies(tenant_id, upstream_name);

-- +goose Down

DROP INDEX IF EXISTS idx_proxy_cache_scan_policies_tenant;
DROP TABLE IF EXISTS proxy_cache_scan_policies;
