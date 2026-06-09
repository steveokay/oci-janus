-- +goose Up
CREATE TABLE tenants (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE organizations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, name)
);

CREATE TABLE repositories (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,
    is_public       BOOLEAN NOT NULL DEFAULT false,
    storage_quota   BIGINT NOT NULL DEFAULT 10737418240,
    storage_used    BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, name)
);

CREATE TABLE manifests (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id     UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tenant_id   UUID NOT NULL,
    digest      TEXT NOT NULL,
    media_type  TEXT NOT NULL,
    raw_json    BYTEA NOT NULL,
    size_bytes  BIGINT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(repo_id, digest)
);

CREATE TABLE tags (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id         UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(repo_id, name)
);

CREATE TABLE blobs (
    digest      TEXT PRIMARY KEY,
    size_bytes  BIGINT NOT NULL,
    storage_key TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE blob_links (
    repo_id     UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    blob_digest TEXT NOT NULL REFERENCES blobs(digest),
    PRIMARY KEY (repo_id, blob_digest)
);

CREATE TABLE scan_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    manifest_digest TEXT NOT NULL,
    repo_id         UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    scanner_name    TEXT NOT NULL,
    scanner_version TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending','running','complete','failed')),
    severity_counts JSONB NOT NULL DEFAULT '{}',
    findings        JSONB NOT NULL DEFAULT '[]',
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_tags_repo_id ON tags(repo_id);
CREATE INDEX idx_manifests_repo_id ON manifests(repo_id);
CREATE INDEX idx_blob_links_digest ON blob_links(blob_digest);
CREATE INDEX idx_scan_results_manifest ON scan_results(manifest_digest);
CREATE INDEX idx_scan_results_tenant ON scan_results(tenant_id);
CREATE INDEX idx_repositories_tenant ON repositories(tenant_id);
CREATE INDEX idx_manifests_tenant ON manifests(tenant_id);

-- +goose Down
DROP TABLE IF EXISTS scan_results;
DROP TABLE IF EXISTS blob_links;
DROP TABLE IF EXISTS blobs;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS manifests;
DROP TABLE IF EXISTS repositories;
DROP TABLE IF EXISTS organizations;
DROP TABLE IF EXISTS tenants;
