-- +goose Up
-- +goose StatementBegin
--
-- Tenant-level storage quota. Until this migration the only quota field was
-- per-repository (repositories.storage_quota), which forced platform admins to
-- SQL their way to a different number for every huge customer. With a tenant-
-- level cap the management API can set one number per tenant.
--
-- Default: 100 GiB. Existing tenants get this default — bump it explicitly per
-- tenant via UpdateTenantQuota when they need more.
--
ALTER TABLE tenants
    ADD COLUMN IF NOT EXISTS storage_quota BIGINT NOT NULL DEFAULT 107374182400;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tenants DROP COLUMN IF EXISTS storage_quota;
-- +goose StatementEnd
