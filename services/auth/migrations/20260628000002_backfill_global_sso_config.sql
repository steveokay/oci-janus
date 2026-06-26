-- +goose Up
-- +goose StatementBegin
--
-- REDESIGN-001 RM-003 — Backfill global_sso_config from auth_providers.
--
-- Strategy: DISTINCT ON (type) — the first provider row per type (ordered by
-- created_at ASC) wins. Operators with multiple tenants who configured the same
-- provider type differently should review the result before removing auth_providers
-- in the next migration.
--
-- The auth_providers.type enum value is used directly as provider_id
-- (e.g. 'oauth_google'). Operators may UPDATE provider_id post-migration if
-- they prefer short labels like 'google', but the string form of the enum is
-- a safe default that matches the existing URL pattern.
--
-- saml_idp_metadata_xml is TEXT in auth_providers but BYTEA in global_sso_config;
-- cast is safe because UTF-8 XML is valid UTF-8 bytes.

INSERT INTO global_sso_config (
    provider_id, kind, display_name, enabled,
    oauth_client_id, oauth_client_secret_enc, oauth_issuer_url, oauth_scopes,
    saml_metadata_xml,
    auto_provision, created_at, updated_at
)
SELECT DISTINCT ON (type)
    type::TEXT                                    AS provider_id,
    type::TEXT                                    AS kind,
    display_name,
    enabled,
    NULLIF(oauth_client_id, '')                   AS oauth_client_id,
    oauth_client_secret_enc,
    NULLIF(oauth_issuer_url, '')                  AS oauth_issuer_url,
    oauth_scopes,
    CASE WHEN saml_idp_metadata_xml IS NOT NULL AND saml_idp_metadata_xml != ''
         THEN saml_idp_metadata_xml::BYTEA
         ELSE NULL
    END                                           AS saml_metadata_xml,
    auto_provision,
    created_at,
    updated_at
FROM auth_providers
WHERE enabled = TRUE
ORDER BY type, created_at ASC
ON CONFLICT (provider_id) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Revert: clear any rows we inserted (provider_ids match the enum values).
DELETE FROM global_sso_config
WHERE provider_id IN (
    'oauth_google','oauth_github','oauth_microsoft','oauth_generic','saml'
);
-- +goose StatementEnd
