-- FE-API-048 Task 3 — make api_keys.user_id nullable; add service_account_id;
-- enforce exactly-one-owner CHECK; replace the old unique constraint with two
-- partial unique indexes so human keys and SA keys share names without conflict.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE api_keys ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE api_keys ADD COLUMN service_account_id UUID
    REFERENCES service_accounts(id) ON DELETE CASCADE;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_owner_exactly_one
    CHECK ((user_id IS NULL) <> (service_account_id IS NULL));

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_user_id_name_key;

CREATE UNIQUE INDEX api_keys_user_name_unique
    ON api_keys (user_id, name) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX api_keys_sa_name_unique
    ON api_keys (service_account_id, name) WHERE service_account_id IS NOT NULL;

CREATE INDEX idx_api_keys_sa
    ON api_keys (service_account_id) WHERE service_account_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM api_keys WHERE service_account_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot rollback: % api_keys rows are owned by service accounts; revoke them first',
            (SELECT count(*) FROM api_keys WHERE service_account_id IS NOT NULL);
    END IF;
END $$;

DROP INDEX IF EXISTS api_keys_sa_name_unique;
DROP INDEX IF EXISTS api_keys_user_name_unique;
DROP INDEX IF EXISTS idx_api_keys_sa;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_owner_exactly_one;
ALTER TABLE api_keys DROP COLUMN service_account_id;
ALTER TABLE api_keys ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_user_id_name_key UNIQUE (user_id, name);
-- +goose StatementEnd
