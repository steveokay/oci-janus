-- +goose Up
-- +goose StatementBegin
--
-- REDESIGN-001 RM-003 — Global SSO config, replacing per-tenant auth_providers.
--
-- Self-hosters have ONE IdP per provider type. The platform-wide default lives
-- here; operators configure via SQL or a future admin-only env-driven seed path.
-- No REST API; the Review §A1 sso_admin gate flaw is closed by removing the
-- surface entirely.

CREATE TABLE IF NOT EXISTS global_sso_config (
    -- Stable string identifier chosen by the operator: 'google', 'github',
    -- 'microsoft', 'okta_saml', etc. Used as the {provider_id} path segment
    -- in /auth/oauth/{provider_id}/start and /auth/saml/{provider_id}/start.
    provider_id  TEXT PRIMARY KEY,

    kind         TEXT NOT NULL CHECK (kind IN (
                     'oauth_google','oauth_github','oauth_microsoft',
                     'oauth_generic','saml'
                 )),
    display_name TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,

    -- OAuth fields (NULL for SAML providers).
    oauth_client_id         TEXT,
    -- AES-256-GCM ciphertext. KEK is SSO_CREDENTIAL_KEY_HEX (unchanged from
    -- the per-tenant model). Plaintext is accepted only at seed time and never
    -- returned over the wire.
    oauth_client_secret_enc BYTEA,
    -- Used for generic OIDC discovery. Named providers (Google/GitHub/Microsoft)
    -- hard-code their endpoints in sso.go — this column is only relevant for
    -- 'oauth_generic'.
    oauth_issuer_url        TEXT,
    oauth_scopes            TEXT[] NOT NULL DEFAULT ARRAY['openid','email','profile'],

    -- SAML fields (NULL for OAuth providers).
    -- Stored as raw XML. URL-based metadata resolution is a future optimisation.
    saml_metadata_url       TEXT,
    saml_metadata_xml       BYTEA,

    -- Auto-provisioning (both OAuth and SAML).
    -- TRUE: an authenticated SSO user with no local row is created and granted
    --       default_role at org scope '*'.
    -- FALSE: login is refused for unknown users (admin must pre-create accounts).
    auto_provision BOOLEAN NOT NULL DEFAULT TRUE,
    -- default_role is not stored here — auto-provisioned users always receive
    -- 'reader' at the global scope; promotion to higher roles is an explicit
    -- admin action. Matches the principle-of-least-privilege baseline.

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE global_sso_config IS
    'Deployment-wide SSO providers. REDESIGN-001 RM-003 — replaced per-tenant auth_providers.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS global_sso_config;
-- +goose StatementEnd
