// FE-API-034 — auth_providers repository.
//
// Persists per-tenant SSO provider configuration. The handler/service layer
// owns plaintext client_secret handling; this repository only sees and
// stores the AES-256-GCM ciphertext.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthProviderType is the canonical set of provider kinds. Mirrors the
// auth_provider_type enum in migration 20260621000001_auth_providers.sql.
type AuthProviderType string

const (
	AuthProviderOAuthGoogle    AuthProviderType = "oauth_google"
	AuthProviderOAuthGitHub    AuthProviderType = "oauth_github"
	AuthProviderOAuthMicrosoft AuthProviderType = "oauth_microsoft"
	AuthProviderOAuthGeneric   AuthProviderType = "oauth_generic"
	AuthProviderSAML           AuthProviderType = "saml"
)

// IsOAuth reports whether the provider type is one of the OAuth/OIDC kinds.
func (t AuthProviderType) IsOAuth() bool {
	switch t {
	case AuthProviderOAuthGoogle, AuthProviderOAuthGitHub,
		AuthProviderOAuthMicrosoft, AuthProviderOAuthGeneric:
		return true
	}
	return false
}

// IsValid reports whether the value is one of the known enum members.
func (t AuthProviderType) IsValid() bool {
	return t.IsOAuth() || t == AuthProviderSAML
}

// AuthProvider is the database model for a configured SSO provider.
//
// OAuthClientSecretEnc is the AES-256-GCM ciphertext of the OAuth client
// secret. Plaintext is never persisted and never returned over the wire.
// SAMLIdpMetadataXML is the raw IdP metadata XML; an optional discovery layer
// (resolve metadata from a URL) is the responsibility of the service layer.
type AuthProvider struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Type        AuthProviderType
	DisplayName string
	Enabled     bool

	// OAuth — nil/empty for SAML providers.
	OAuthClientID         string
	OAuthClientSecretEnc  []byte
	OAuthIssuerURL        string
	OAuthScopes           []string

	// SAML — empty for OAuth providers.
	SAMLIdpMetadataXML string
	SAMLEntityID       string
	SAMLAudience       string

	// Auto-provisioning policy (both types).
	AutoProvision bool
	DefaultRole   string

	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy *uuid.UUID
}

// AuthProviderRepository performs all DB operations on the auth_providers table.
type AuthProviderRepository struct {
	pool *pgxpool.Pool
}

// NewAuthProviderRepository builds the repository against the given pool.
func NewAuthProviderRepository(pool *pgxpool.Pool) *AuthProviderRepository {
	return &AuthProviderRepository{pool: pool}
}

// providerColumns is the canonical SELECT column list. Centralised so every
// scanRow uses the same ordering and a future ALTER TABLE only touches one
// place.
const providerColumns = `id, tenant_id, type, display_name, enabled,
	COALESCE(oauth_client_id, ''),
	oauth_client_secret_enc,
	COALESCE(oauth_issuer_url, ''),
	oauth_scopes,
	COALESCE(saml_idp_metadata_xml, ''),
	COALESCE(saml_entity_id, ''),
	COALESCE(saml_audience, ''),
	auto_provision,
	default_role,
	created_at, updated_at, updated_by`

// Create inserts a new provider row and returns the persisted record.
// Returns ErrAlreadyExists on a unique-constraint violation (same canonical
// OAuth type already configured for the tenant).
func (r *AuthProviderRepository) Create(ctx context.Context, p *AuthProvider) (*AuthProvider, error) {
	const q = `
		INSERT INTO auth_providers (
			tenant_id, type, display_name, enabled,
			oauth_client_id, oauth_client_secret_enc, oauth_issuer_url, oauth_scopes,
			saml_idp_metadata_xml, saml_entity_id, saml_audience,
			auto_provision, default_role, updated_by
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING ` + providerColumns

	// Pointer-handling: COALESCE in SELECT converts NULL to "" but on INSERT we
	// want NULL for "no value" rather than the empty string, so optional TEXT
	// fields are passed via NULLIF in pgx parameter binding.
	row := r.pool.QueryRow(ctx, q,
		p.TenantID, string(p.Type), p.DisplayName, p.Enabled,
		nullIfEmpty(p.OAuthClientID),
		nullableBytes(p.OAuthClientSecretEnc),
		nullIfEmpty(p.OAuthIssuerURL),
		oauthScopes(p.OAuthScopes),
		nullIfEmpty(p.SAMLIdpMetadataXML),
		nullIfEmpty(p.SAMLEntityID),
		nullIfEmpty(p.SAMLAudience),
		p.AutoProvision, p.DefaultRole, p.UpdatedBy,
	)
	out, err := scanAuthProvider(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("create auth provider: %w", err)
	}
	return out, nil
}

// GetByID returns the provider with the given primary key, or ErrNotFound.
func (r *AuthProviderRepository) GetByID(ctx context.Context, id uuid.UUID) (*AuthProvider, error) {
	const q = `SELECT ` + providerColumns + ` FROM auth_providers WHERE id = $1`
	row := r.pool.QueryRow(ctx, q, id)
	p, err := scanAuthProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get auth provider: %w", err)
	}
	return p, nil
}

// ListByTenant returns all providers for the given tenant. enabledOnly=true
// filters to enabled=true rows — used by the public /api/v1/auth/providers
// list endpoint so disabled providers are never advertised.
func (r *AuthProviderRepository) ListByTenant(ctx context.Context, tenantID uuid.UUID, enabledOnly bool) ([]*AuthProvider, error) {
	q := `SELECT ` + providerColumns + ` FROM auth_providers WHERE tenant_id = $1`
	if enabledOnly {
		q += ` AND enabled = TRUE`
	}
	q += ` ORDER BY created_at ASC`

	rows, err := r.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list auth providers: %w", err)
	}
	defer rows.Close()

	var out []*AuthProvider
	for rows.Next() {
		p, err := scanAuthProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan auth provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateAuthProviderRequest carries partial-update inputs. Pointer fields are
// "set this value" when non-nil and "leave unchanged" when nil — matches the
// pattern used by UpdateProfileRequest above.
type UpdateAuthProviderRequest struct {
	DisplayName *string
	Enabled     *bool

	OAuthClientID        *string
	OAuthClientSecretEnc *[]byte // nil = leave; non-nil = replace (always re-encrypt before calling)
	OAuthIssuerURL       *string
	OAuthScopes          *[]string

	SAMLIdpMetadataXML *string
	SAMLEntityID       *string
	SAMLAudience       *string

	AutoProvision *bool
	DefaultRole   *string

	UpdatedBy *uuid.UUID
}

// Update applies the partial-update request and returns the refreshed row.
// Mirrors the explicit-CASE pattern used by UpdateProfile so a single
// statement leaves untouched fields exactly as-is.
func (r *AuthProviderRepository) Update(ctx context.Context, id uuid.UUID, req UpdateAuthProviderRequest) (*AuthProvider, error) {
	const q = `
		UPDATE auth_providers SET
		    display_name            = CASE WHEN $2::bool  THEN $3::text  ELSE display_name            END,
		    enabled                 = CASE WHEN $4::bool  THEN $5::bool  ELSE enabled                 END,
		    oauth_client_id         = CASE WHEN $6::bool  THEN $7::text  ELSE oauth_client_id         END,
		    oauth_client_secret_enc = CASE WHEN $8::bool  THEN $9::bytea ELSE oauth_client_secret_enc END,
		    oauth_issuer_url        = CASE WHEN $10::bool THEN $11::text ELSE oauth_issuer_url        END,
		    oauth_scopes            = CASE WHEN $12::bool THEN $13::text[] ELSE oauth_scopes          END,
		    saml_idp_metadata_xml   = CASE WHEN $14::bool THEN $15::text ELSE saml_idp_metadata_xml   END,
		    saml_entity_id          = CASE WHEN $16::bool THEN $17::text ELSE saml_entity_id          END,
		    saml_audience           = CASE WHEN $18::bool THEN $19::text ELSE saml_audience           END,
		    auto_provision          = CASE WHEN $20::bool THEN $21::bool ELSE auto_provision          END,
		    default_role            = CASE WHEN $22::bool THEN $23::text ELSE default_role            END,
		    updated_by              = COALESCE($24::uuid, updated_by),
		    updated_at              = NOW()
		WHERE id = $1
		RETURNING ` + providerColumns

	setName, name := stringSet(req.DisplayName)
	setEnabled, enabled := boolSet(req.Enabled)
	setClientID, clientID := stringSet(req.OAuthClientID)
	setSecret, secretEnc := bytesSet(req.OAuthClientSecretEnc)
	setIssuer, issuer := stringSet(req.OAuthIssuerURL)
	setScopes, scopes := scopesSet(req.OAuthScopes)
	setMetadata, metadata := stringSet(req.SAMLIdpMetadataXML)
	setEntity, entity := stringSet(req.SAMLEntityID)
	setAudience, audience := stringSet(req.SAMLAudience)
	setProvision, provision := boolSet(req.AutoProvision)
	setRole, role := stringSet(req.DefaultRole)

	row := r.pool.QueryRow(ctx, q, id,
		setName, name,
		setEnabled, enabled,
		setClientID, clientID,
		setSecret, secretEnc,
		setIssuer, issuer,
		setScopes, scopes,
		setMetadata, metadata,
		setEntity, entity,
		setAudience, audience,
		setProvision, provision,
		setRole, role,
		req.UpdatedBy,
	)
	p, err := scanAuthProvider(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("update auth provider: %w", err)
	}
	return p, nil
}

// Delete removes the provider with the given ID. Returns ErrNotFound if no
// matching row exists.
func (r *AuthProviderRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM auth_providers WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete auth provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// rowScanner is the narrow interface satisfied by both pgx.Row and pgx.Rows.
// Lets scanAuthProvider serve both QueryRow and Query iteration.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanAuthProvider reads one provider row using the canonical column list.
func scanAuthProvider(s rowScanner) (*AuthProvider, error) {
	var (
		p        AuthProvider
		typeStr  string
		scopes   []string
		secret   []byte
		updated  *uuid.UUID
	)
	if err := s.Scan(
		&p.ID, &p.TenantID, &typeStr, &p.DisplayName, &p.Enabled,
		&p.OAuthClientID,
		&secret,
		&p.OAuthIssuerURL,
		&scopes,
		&p.SAMLIdpMetadataXML,
		&p.SAMLEntityID,
		&p.SAMLAudience,
		&p.AutoProvision,
		&p.DefaultRole,
		&p.CreatedAt, &p.UpdatedAt, &updated,
	); err != nil {
		return nil, err
	}
	p.Type = AuthProviderType(typeStr)
	p.OAuthScopes = scopes
	p.OAuthClientSecretEnc = secret
	p.UpdatedBy = updated
	return &p, nil
}

// nullIfEmpty maps "" → nil so the optional TEXT column is stored as SQL NULL
// rather than an empty string (matches the COALESCE semantics in the SELECT).
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableBytes maps nil/0-length → nil so empty ciphertext is stored as NULL.
func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// oauthScopes returns a non-nil slice so the DEFAULT clause never fires for
// explicit empty input — callers that want the default must pass nil.
func oauthScopes(s []string) any {
	if s == nil {
		return nil // use column default
	}
	return s
}

// stringSet returns (setFlag, value) for the SQL CASE-WHEN pattern.
func stringSet(p *string) (bool, string) {
	if p == nil {
		return false, ""
	}
	return true, *p
}

// boolSet mirrors stringSet for *bool fields.
func boolSet(p *bool) (bool, bool) {
	if p == nil {
		return false, false
	}
	return true, *p
}

// bytesSet mirrors stringSet for *[]byte fields. A nil pointer means
// "leave unchanged"; a non-nil pointer to nil/empty bytes is normalised to
// nil so the column is cleared (NULL) rather than set to an empty bytea.
func bytesSet(p *[]byte) (bool, []byte) {
	if p == nil {
		return false, nil
	}
	if len(*p) == 0 {
		return true, nil
	}
	return true, *p
}

// scopesSet mirrors stringSet for *[]string fields. nil = leave unchanged;
// non-nil empty slice clears the column to NULL/empty array.
func scopesSet(p *[]string) (bool, []string) {
	if p == nil {
		return false, nil
	}
	return true, *p
}
