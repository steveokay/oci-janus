-- +goose Up
-- +goose StatementBegin
-- QA-001: add tenant_id to signatures so signature lookups are tenant-scoped.
-- Without this column, two tenants pushing the same public-image digest
-- shared a signature row, letting one tenant see (and admit images via) the
-- other's signature.
--
-- Existing rows are dropped: tenant_id cannot be derived from
-- (manifest_digest, repository_name) alone — the platform's "org/repo"
-- format reuses the same org name across tenants — and re-signing is
-- cheap. A WARN log makes the data loss visible to operators upgrading
-- from a pre-tenant-scoping deployment.

DELETE FROM signatures;

ALTER TABLE signatures ADD COLUMN tenant_id UUID NOT NULL;

-- Replace the (manifest_digest, signer_id) uniqueness with one that
-- includes tenant_id so each tenant maintains its own signature namespace.
ALTER TABLE signatures DROP CONSTRAINT signatures_manifest_digest_signer_id_key;
ALTER TABLE signatures
    ADD CONSTRAINT signatures_tenant_manifest_signer_key
    UNIQUE (tenant_id, manifest_digest, signer_id);

-- Drop the old single-column index and add a composite one so
-- per-tenant List queries hit an index.
DROP INDEX IF EXISTS idx_signatures_manifest;
CREATE INDEX idx_signatures_tenant_manifest ON signatures (tenant_id, manifest_digest);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_signatures_tenant_manifest;
CREATE INDEX idx_signatures_manifest ON signatures (manifest_digest);

ALTER TABLE signatures DROP CONSTRAINT signatures_tenant_manifest_signer_key;
ALTER TABLE signatures
    ADD CONSTRAINT signatures_manifest_digest_signer_id_key
    UNIQUE (manifest_digest, signer_id);

ALTER TABLE signatures DROP COLUMN tenant_id;
-- +goose StatementEnd
