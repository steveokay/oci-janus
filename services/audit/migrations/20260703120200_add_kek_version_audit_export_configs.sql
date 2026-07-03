-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted the
-- two secret columns (hmac_secret, bearer_token) on audit_export_configs. Both
-- columns rotate together in one transaction, so a single tracking column suffices.
ALTER TABLE audit_export_configs ADD COLUMN kek_version SMALLINT;

-- +goose Down
ALTER TABLE audit_export_configs DROP COLUMN kek_version;
