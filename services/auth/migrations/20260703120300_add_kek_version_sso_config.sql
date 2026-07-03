-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted
-- oauth_client_secret_enc. global_sso_config is current; auth_providers is the
-- legacy table (added conditionally — not every deployment has it).
ALTER TABLE global_sso_config ADD COLUMN kek_version SMALLINT;

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.auth_providers') IS NOT NULL THEN
        ALTER TABLE auth_providers ADD COLUMN IF NOT EXISTS kek_version SMALLINT;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE global_sso_config DROP COLUMN kek_version;

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.auth_providers') IS NOT NULL THEN
        ALTER TABLE auth_providers DROP COLUMN IF EXISTS kek_version;
    END IF;
END
$$;
-- +goose StatementEnd
