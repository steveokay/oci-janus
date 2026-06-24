-- +goose Up
-- +goose StatementBegin
-- FUT-017: per-upstream auto-sign-on-cache policy for the proxy pull-through
-- cache. When auto_sign=true and a key_id is set, services/signer will sign
-- newly-cached manifests with that key as it consumes cache.populated events
-- from services/proxy.
--
-- Scope is (tenant_id, upstream_name) — same scope shape used by other
-- per-upstream proxy policies (rate limits, scan policies). Operators
-- configure one row per (tenant, upstream) pair through the dashboard.
--
-- Default is disabled: a fresh upstream registration does NOT auto-sign
-- anything until the operator explicitly opts in. Empty key_id is also
-- treated as disabled by the consumer regardless of auto_sign, so an
-- operator that flips auto_sign on without picking a key just no-ops
-- until they choose one — no accidental signing with a wrong default key.
CREATE TABLE proxy_cache_sign_policies (
    tenant_id     UUID        NOT NULL,
    upstream_name TEXT        NOT NULL,
    auto_sign     BOOLEAN     NOT NULL DEFAULT FALSE,
    key_id        TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, upstream_name)
);

-- Index lets the ListProxyCacheSignPolicies RPC scan a single tenant
-- without a full table walk. The PK already covers point lookups by
-- (tenant_id, upstream_name) so we don't need an extra index for Get.
CREATE INDEX idx_proxy_cache_sign_policies_tenant
    ON proxy_cache_sign_policies (tenant_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_proxy_cache_sign_policies_tenant;
DROP TABLE IF EXISTS proxy_cache_sign_policies;
-- +goose StatementEnd
