// REDESIGN-001 RM-003 — global SSO config repository.
//
// Replaces the per-tenant AuthProviderRepository. The global_sso_config table
// has exactly one row per provider_id (a stable string like "google", "github",
// "okta_saml"). Operators configure it at deploy time via SQL or a seed
// migration; there is no REST admin API (the per-tenant admin surface was the
// Review §A1 gate flaw that motivated this redesign).
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GlobalSSOProvider is the Go representation of one global_sso_config row.
//
// Kind mirrors the auth_provider_type vocabulary so existing provider-type
// switch statements in the handler/service layers need minimal changes.
type GlobalSSOProvider struct {
	ProviderID  string // stable string id: 'google', 'github', 'okta_saml', etc.
	Kind        string // 'oauth_google' | 'oauth_github' | 'oauth_microsoft' | 'oauth_generic' | 'saml'
	DisplayName string
	Enabled     bool

	// OAuth fields (zero-value for SAML).
	OAuthClientID        string
	OAuthClientSecretEnc []byte // AES-256-GCM ciphertext; never returned over the wire
	OAuthIssuerURL       string // required only for Kind='oauth_generic'
	OAuthScopes          []string

	// SAML fields (zero-value for OAuth).
	SAMLMetadataURL string
	SAMLMetadataXML []byte // raw IdP metadata XML bytes

	AutoProvision bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AuthProviderType returns an AuthProviderType equivalent of the Kind field
// so callers can reuse the existing IsOAuth() / IsValid() helpers without
// change. The string values are identical to the old enum constants.
func (p *GlobalSSOProvider) AuthProviderType() AuthProviderType {
	return AuthProviderType(p.Kind)
}

// GlobalSSOConfigRepository performs all DB operations on global_sso_config.
type GlobalSSOConfigRepository struct {
	pool *pgxpool.Pool
}

// NewGlobalSSOConfigRepository builds the repository against the given pool.
func NewGlobalSSOConfigRepository(pool *pgxpool.Pool) *GlobalSSOConfigRepository {
	return &GlobalSSOConfigRepository{pool: pool}
}

// globalSSOColumns is the canonical SELECT column list. Centralised so every
// scan uses the same ordering.
const globalSSOColumns = `provider_id, kind, display_name, enabled,
	COALESCE(oauth_client_id, ''),
	oauth_client_secret_enc,
	COALESCE(oauth_issuer_url, ''),
	oauth_scopes,
	COALESCE(saml_metadata_url, ''),
	saml_metadata_xml,
	auto_provision,
	created_at, updated_at`

// Get returns the enabled provider with the given provider_id, or ErrNotFound.
// Disabled providers are treated as missing so the login flow surfaces a clean
// 404 rather than leaking configuration details.
func (r *GlobalSSOConfigRepository) Get(ctx context.Context, providerID string) (*GlobalSSOProvider, error) {
	const q = `SELECT ` + globalSSOColumns + `
		FROM global_sso_config
		WHERE provider_id = $1`

	row := r.pool.QueryRow(ctx, q, providerID)
	p, err := scanGlobalSSOProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get global sso provider %q: %w", providerID, err)
	}
	return p, nil
}

// List returns all global SSO providers. When enabledOnly is true, only rows
// with enabled=TRUE are returned (used by the public provider list endpoint).
func (r *GlobalSSOConfigRepository) List(ctx context.Context, enabledOnly bool) ([]*GlobalSSOProvider, error) {
	q := `SELECT ` + globalSSOColumns + ` FROM global_sso_config`
	if enabledOnly {
		q += ` WHERE enabled = TRUE`
	}
	q += ` ORDER BY created_at ASC`

	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list global sso providers: %w", err)
	}
	defer rows.Close()

	var out []*GlobalSSOProvider
	for rows.Next() {
		p, err := scanGlobalSSOProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan global sso provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Upsert inserts or updates a provider row. Used by seed scripts and
// operator tooling; there is no admin HTTP API.
func (r *GlobalSSOConfigRepository) Upsert(ctx context.Context, p *GlobalSSOProvider) (*GlobalSSOProvider, error) {
	const q = `
		INSERT INTO global_sso_config (
			provider_id, kind, display_name, enabled,
			oauth_client_id, oauth_client_secret_enc, oauth_issuer_url, oauth_scopes,
			saml_metadata_url, saml_metadata_xml,
			auto_provision, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (provider_id) DO UPDATE SET
			kind                    = EXCLUDED.kind,
			display_name            = EXCLUDED.display_name,
			enabled                 = EXCLUDED.enabled,
			oauth_client_id         = EXCLUDED.oauth_client_id,
			oauth_client_secret_enc = EXCLUDED.oauth_client_secret_enc,
			oauth_issuer_url        = EXCLUDED.oauth_issuer_url,
			oauth_scopes            = EXCLUDED.oauth_scopes,
			saml_metadata_url       = EXCLUDED.saml_metadata_url,
			saml_metadata_xml       = EXCLUDED.saml_metadata_xml,
			auto_provision          = EXCLUDED.auto_provision,
			updated_at              = now()
		RETURNING ` + globalSSOColumns

	row := r.pool.QueryRow(ctx, q,
		p.ProviderID,
		p.Kind,
		p.DisplayName,
		p.Enabled,
		nullIfEmpty(p.OAuthClientID),
		nullableBytes(p.OAuthClientSecretEnc),
		nullIfEmpty(p.OAuthIssuerURL),
		oauthScopes(p.OAuthScopes),
		nullIfEmpty(p.SAMLMetadataURL),
		nullableBytes(p.SAMLMetadataXML),
		p.AutoProvision,
	)
	out, err := scanGlobalSSOProvider(row)
	if err != nil {
		return nil, fmt.Errorf("upsert global sso provider %q: %w", p.ProviderID, err)
	}
	return out, nil
}

// Delete removes the provider with the given provider_id. Returns ErrNotFound
// if no matching row exists.
func (r *GlobalSSOConfigRepository) Delete(ctx context.Context, providerID string) error {
	const q = `DELETE FROM global_sso_config WHERE provider_id = $1`
	tag, err := r.pool.Exec(ctx, q, providerID)
	if err != nil {
		return fmt.Errorf("delete global sso provider %q: %w", providerID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── scan helper ──────────────────────────────────────────────────────────────

// scanGlobalSSOProvider reads one row using the canonical column list.
// rowScanner is the existing narrow interface (pgx.Row / pgx.Rows) defined
// in auth_providers.go.
func scanGlobalSSOProvider(s rowScanner) (*GlobalSSOProvider, error) {
	var (
		p           GlobalSSOProvider
		secretEnc   []byte
		metadataXML []byte
		scopes      []string
	)
	if err := s.Scan(
		&p.ProviderID,
		&p.Kind,
		&p.DisplayName,
		&p.Enabled,
		&p.OAuthClientID,
		&secretEnc,
		&p.OAuthIssuerURL,
		&scopes,
		&p.SAMLMetadataURL,
		&metadataXML,
		&p.AutoProvision,
		&p.CreatedAt,
		&p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.OAuthClientSecretEnc = secretEnc
	p.OAuthScopes = scopes
	p.SAMLMetadataXML = metadataXML
	return &p, nil
}
