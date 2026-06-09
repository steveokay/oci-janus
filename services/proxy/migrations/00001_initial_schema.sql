-- +goose Up

CREATE TABLE upstream_registries (
    upstream_id  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL,
    name         TEXT        NOT NULL,
    url          TEXT        NOT NULL,
    auth_type    TEXT        NOT NULL DEFAULT 'none' CHECK (auth_type IN ('none', 'basic', 'token')),
    username     TEXT        NOT NULL DEFAULT '',
    password_enc BYTEA,
    ttl_seconds  BIGINT      NOT NULL DEFAULT 3600,
    enabled      BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, name)
);

CREATE INDEX idx_upstream_registries_tenant ON upstream_registries(tenant_id);

-- Stores cached manifests fetched from upstream registries.
-- The TTL is enforced by comparing fetched_at against the upstream's ttl_seconds.
CREATE TABLE proxy_manifests (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    upstream_id UUID        NOT NULL REFERENCES upstream_registries(upstream_id) ON DELETE CASCADE,
    image       TEXT        NOT NULL,
    reference   TEXT        NOT NULL,
    digest      TEXT        NOT NULL,
    media_type  TEXT        NOT NULL,
    body        BYTEA       NOT NULL,
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, upstream_id, image, reference)
);

CREATE INDEX idx_proxy_manifests_upstream ON proxy_manifests(upstream_id);
CREATE INDEX idx_proxy_manifests_lookup   ON proxy_manifests(tenant_id, upstream_id, image, reference);

-- +goose Down

DROP TABLE proxy_manifests;
DROP TABLE upstream_registries;
