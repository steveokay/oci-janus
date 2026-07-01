-- +goose Up
-- FUT-001 — federated workload identity. Trust relationship between a
-- workspace service account and an external OIDC IdP (GitHub Actions /
-- GitLab CI / Buildkite / any OIDC issuer in the OIDC_ALLOWED_ISSUERS env
-- allowlist). On a successful POST /auth/token/workload, the trust's
-- service_account_id receives a short-lived RS256 JWT.
--
-- The (tenant_id, issuer_url, subject_pattern) tuple is unique — a single
-- IdP subject can be claimed by exactly one SA per tenant, so a misconfigured
-- federation cannot silently map the same CI runner to two different SAs.
--
-- service_account_id has ON DELETE CASCADE so deleting an SA atomically
-- removes its trust rows — preventing orphaned trusts that would 5xx at
-- exchange time when the SA lookup misses.

CREATE TABLE oidc_trust_configs (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                UUID        NOT NULL,
    service_account_id       UUID        NOT NULL REFERENCES service_accounts(id) ON DELETE CASCADE,
    display_name             TEXT        NOT NULL,
    issuer_url               TEXT        NOT NULL,
    audience                 TEXT        NOT NULL,
    subject_pattern          TEXT        NOT NULL,
    jwks_cache_ttl_seconds   INTEGER     NOT NULL DEFAULT 3600,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at             TIMESTAMPTZ,
    CONSTRAINT oidc_trust_unique_subject UNIQUE (tenant_id, issuer_url, subject_pattern)
);

-- idx_oidc_trust_tenant supports the per-tenant ListOIDCTrusts query (the
-- common admin-list path) plus the per-tenant tenant_id filter in CRUD.
CREATE INDEX idx_oidc_trust_tenant ON oidc_trust_configs (tenant_id);
-- idx_oidc_trust_sa supports the ON DELETE CASCADE FK from service_accounts
-- so deleting an SA is O(matching trusts) rather than O(table).
CREATE INDEX idx_oidc_trust_sa     ON oidc_trust_configs (service_account_id);
-- idx_oidc_trust_issuer supports the ListByIssuer query on the exchange hot
-- path — given an OIDC token's iss claim, find every trust that could match
-- without scanning the whole table.
CREATE INDEX idx_oidc_trust_issuer ON oidc_trust_configs (issuer_url);

-- +goose Down
DROP TABLE IF EXISTS oidc_trust_configs;
