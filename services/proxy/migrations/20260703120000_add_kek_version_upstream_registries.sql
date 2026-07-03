-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted a row.
-- Nullable — NULL means "never rotated" (original bootstrap key). The rotate-kek
-- subcommand stamps this on every re-encrypted row; trial-decryption remains the
-- authoritative verify.
ALTER TABLE upstream_registries ADD COLUMN kek_version SMALLINT;

-- +goose Down
ALTER TABLE upstream_registries DROP COLUMN kek_version;
