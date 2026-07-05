-- +goose Up
-- +goose StatementBegin

-- Tier-1 #1 (TOTP MFA). See docs/superpowers/specs/2026-07-05-mfa-totp-design.md.
-- MFA applies to local password (kind='human') accounts only; SSO users are
-- exempt (their IdP owns the second factor).

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS mfa_enabled            BOOLEAN  NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS mfa_secret_enc         BYTEA    NULL,
    ADD COLUMN IF NOT EXISTS mfa_secret_kek_version SMALLINT NULL,
    ADD COLUMN IF NOT EXISTS mfa_enrolled_at        TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS mfa_last_used_counter  BIGINT   NULL;

COMMENT ON COLUMN users.mfa_secret_enc IS
    'AES-256-GCM (libs/crypto/aes) encrypted TOTP shared secret under MFA_SECRET_KEY_HEX. Rotatable via rotate-kek (mfa_secret_kek_version).';
COMMENT ON COLUMN users.mfa_last_used_counter IS
    'TOTP time-step counter of the last accepted code; a code whose counter is not strictly greater is rejected (replay prevention).';

-- Single-use backup codes (argon2id hashed). 8 rows per enrolment.
CREATE TABLE IF NOT EXISTS user_mfa_backup_codes (
    id         UUID        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT        NOT NULL,
    used_at    TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_user_mfa_backup_codes_user
    ON user_mfa_backup_codes(user_id);

-- Admin "require MFA for all password accounts" toggle (per-tenant row;
-- deployment-wide in single mode). Read locally in Service.Login.
ALTER TABLE token_policies
    ADD COLUMN IF NOT EXISTS require_mfa BOOLEAN NOT NULL DEFAULT false;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE token_policies DROP COLUMN IF EXISTS require_mfa;
DROP INDEX IF EXISTS idx_user_mfa_backup_codes_user;
DROP TABLE IF EXISTS user_mfa_backup_codes;
ALTER TABLE users
    DROP COLUMN IF EXISTS mfa_last_used_counter,
    DROP COLUMN IF EXISTS mfa_enrolled_at,
    DROP COLUMN IF EXISTS mfa_secret_kek_version,
    DROP COLUMN IF EXISTS mfa_secret_enc,
    DROP COLUMN IF EXISTS mfa_enabled;

-- +goose StatementEnd
