-- +goose Up
-- +goose StatementBegin
--
-- FE-API-034 — SSO provider configuration (OAuth + SAML).
--
-- Tables:
--   * auth_providers       — per-tenant SSO config (Google/GitHub/Microsoft/generic OIDC + SAML).
--   * auth_login_sessions  — short-lived CSRF/PKCE state for the redirect dance.
--
-- The users table is extended with sso_provider_id so SSO-provisioned accounts
-- can be distinguished from password accounts. Existing rows stay NULL —
-- password auth keeps working unchanged.

CREATE TYPE auth_provider_type AS ENUM (
    'oauth_google',
    'oauth_github',
    'oauth_microsoft',
    'oauth_generic',
    'saml'
);

CREATE TABLE IF NOT EXISTS auth_providers (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- tenant_id is intentionally NOT a foreign key here. The auth service runs
    -- against its own database and does not own the tenants table (which lives
    -- in registry-tenant). The tenant ID is validated at the application layer.
    tenant_id               UUID NOT NULL,
    type                    auth_provider_type NOT NULL,
    display_name            TEXT NOT NULL,
    enabled                 BOOLEAN NOT NULL DEFAULT TRUE,

    -- OAuth fields (NULL for SAML providers).
    oauth_client_id         TEXT,
    -- AES-256-GCM ciphertext of the OAuth client_secret. The plaintext is
    -- accepted on the admin POST/PATCH and never stored or returned again.
    oauth_client_secret_enc BYTEA,
    -- Used for generic OIDC discovery (Google/GitHub/Microsoft hardcode their endpoints).
    oauth_issuer_url        TEXT,
    oauth_scopes            TEXT[] NOT NULL DEFAULT ARRAY['openid','email','profile'],

    -- SAML fields (NULL for OAuth providers).
    -- Stored as raw XML for now; URL-based metadata can be resolved at write time.
    saml_idp_metadata_xml   TEXT,
    saml_entity_id          TEXT,
    saml_audience           TEXT,

    -- Auto-provisioning (applies to both OAuth and SAML).
    -- When TRUE, an authenticated SSO user with no matching local row is
    -- created with default_role granted at org scope '*'. When FALSE, login is
    -- refused for unknown users (admin must pre-create accounts).
    auto_provision          BOOLEAN NOT NULL DEFAULT TRUE,
    default_role            TEXT NOT NULL DEFAULT 'reader'
        CHECK (default_role IN ('reader','writer','admin','owner')),

    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Admin user who last touched this row. NULL for the initial seed path.
    updated_by              UUID
);

CREATE INDEX IF NOT EXISTS idx_auth_providers_tenant_enabled
    ON auth_providers (tenant_id, enabled);

-- A tenant may have at most one provider per canonical OAuth provider type.
-- (oauth_generic and saml are excluded so a tenant can wire multiple generic
-- OIDC providers or SAML IdPs, distinguished by display_name.)
CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_providers_one_per_canonical_type
    ON auth_providers (tenant_id, type)
    WHERE type IN ('oauth_google','oauth_github','oauth_microsoft');

-- Short-lived state for the OAuth/SAML redirect dance. Rows expire 10 minutes
-- after creation and are deleted on first use (single-use replay defence).
CREATE TABLE IF NOT EXISTS auth_login_sessions (
    -- CSRF state token, also the primary key. 32 bytes of crypto/rand
    -- base64url-encoded → 43 chars.
    state          TEXT PRIMARY KEY,
    tenant_id      UUID NOT NULL,
    provider_id    UUID NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
    -- PKCE verifier (OAuth) or SAML RelayState. NULL when unused.
    pkce_verifier  TEXT,
    -- Intra-app redirect path applied after a successful login. Validated by
    -- the handler to disallow open redirects (no scheme, no leading "//").
    redirect_url   TEXT NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index used by the periodic cleanup sweep to drop expired rows quickly.
CREATE INDEX IF NOT EXISTS idx_auth_login_sessions_expires
    ON auth_login_sessions (expires_at);

-- Tag SSO-provisioned users so a future admin UI can distinguish them from
-- password users. NULL = password user. ON DELETE SET NULL so deleting a
-- provider does not orphan or delete the user row.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS sso_provider_id UUID NULL
        REFERENCES auth_providers(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_users_sso_provider
    ON users (sso_provider_id)
    WHERE sso_provider_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN IF EXISTS sso_provider_id;
DROP TABLE IF EXISTS auth_login_sessions;
DROP TABLE IF EXISTS auth_providers;
DROP TYPE IF EXISTS auth_provider_type;
-- +goose StatementEnd
