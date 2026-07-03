-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted
-- webhook_endpoints.secret_enc (stored as hex TEXT, not BYTEA).
ALTER TABLE webhook_endpoints ADD COLUMN kek_version SMALLINT;

-- +goose Down
ALTER TABLE webhook_endpoints DROP COLUMN kek_version;
