-- +goose Up

CREATE TABLE tenants (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT        NOT NULL UNIQUE,
    plan       TEXT        NOT NULL DEFAULT 'standard',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tenant_policies (
    tenant_id              UUID    PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    scan_on_push           BOOLEAN NOT NULL DEFAULT true,
    block_on_severity      TEXT    NOT NULL DEFAULT 'CRITICAL',
    allow_unscanned        BOOLEAN NOT NULL DEFAULT false,
    proxy_cache_enabled    BOOLEAN NOT NULL DEFAULT true,
    signing_required       BOOLEAN NOT NULL DEFAULT false,
    exempt_repositories    TEXT[]  NOT NULL DEFAULT '{}',
    storage_quota_bytes    BIGINT  NOT NULL DEFAULT 107374182400  -- 100 GB default
);

CREATE TABLE tenant_domains (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    domain              TEXT        NOT NULL UNIQUE,
    verification_token  TEXT        NOT NULL,
    verified            BOOLEAN     NOT NULL DEFAULT false,
    registered_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at         TIMESTAMPTZ
);

CREATE INDEX idx_tenant_domains_tenant ON tenant_domains(tenant_id);
CREATE INDEX idx_tenant_domains_unverified ON tenant_domains(verified, registered_at)
    WHERE verified = false;

-- +goose Down

DROP TABLE IF EXISTS tenant_domains;
DROP TABLE IF EXISTS tenant_policies;
DROP TABLE IF EXISTS tenants;
