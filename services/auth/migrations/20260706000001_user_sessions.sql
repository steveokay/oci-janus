-- +goose Up
-- user_sessions tracks interactive login sessions (password / MFA / SSO) so a
-- user can list and revoke them. The stable sid is embedded in the JWT and
-- survives the 300s-TTL JTI-rotating refresh (the JTI is not stable; the sid is).
CREATE TABLE user_sessions (
    sid            UUID PRIMARY KEY,
    user_id        UUID NOT NULL,
    tenant_id      UUID NOT NULL,
    device_label   TEXT NOT NULL,
    user_agent     TEXT NOT NULL,
    ip             INET NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ
);

-- Live-session lookups for the list + revoke-others paths.
CREATE INDEX idx_user_sessions_user_live
    ON user_sessions (user_id) WHERE revoked_at IS NULL;
-- Sweep by expiry.
CREATE INDEX idx_user_sessions_expires
    ON user_sessions (expires_at);

-- +goose Down
DROP TABLE user_sessions;
