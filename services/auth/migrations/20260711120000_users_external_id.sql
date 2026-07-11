-- +goose Up
ALTER TABLE users ADD COLUMN external_id TEXT;
ALTER TABLE users ADD COLUMN provisioned_via TEXT;
-- IdP-provisioned users have a stable externalId; the partial unique index lets
-- every non-SCIM user keep external_id NULL without colliding.
CREATE UNIQUE INDEX users_tenant_external_id_uniq
    ON users (tenant_id, external_id) WHERE external_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS users_tenant_external_id_uniq;
ALTER TABLE users DROP COLUMN IF EXISTS provisioned_via;
ALTER TABLE users DROP COLUMN IF EXISTS external_id;
