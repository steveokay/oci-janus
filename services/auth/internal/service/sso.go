// FE-API-034 — SSO provider configuration + OAuth callback business logic.
//
// This file lives in the service package because it crosses two repository
// boundaries (auth_providers and users) and because token issuance lives here
// already. The HTTP handler layer translates redirect-dance HTTP traffic into
// calls on these methods.
//
// SAML is intentionally not implemented in this iteration — the handler stubs
// SAML routes with 501 Not Implemented. See the DEFERRAL commit note.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── SSO sentinel errors ─────────────────────────────────────────────────────

// Sentinel errors returned by SSO service methods. Handlers map these to HTTP
// status codes; all error paths log server-side detail so an operator can
// debug a stuck redirect without leaking the cause to the user.
var (
	ErrProviderNotFound       = errors.New("auth provider not found or disabled")
	ErrInvalidProviderConfig  = errors.New("invalid provider configuration")
	ErrSessionNotFound        = errors.New("login session not found or expired")
	ErrAutoProvisionDisabled  = errors.New("auto-provision disabled and no matching user")
	ErrEmailNotVerified       = errors.New("email not verified by identity provider")
	ErrInvalidNextParam       = errors.New("invalid next parameter")
	ErrSAMLNotImplemented     = errors.New("SAML support is not implemented in this build")
)

// SSO is the FE-API-034 SSO service. It owns auth_providers + login_sessions
// repositories and reuses the existing Service for JWT issuance, user lookup,
// and role assignment.
//
// CredentialKey is the AES-256 key used to encrypt OAuth client_secret at
// rest. Must be exactly 32 bytes; constructor enforces this.
type SSO struct {
	auth          *Service
	providers     authProviderRepo
	sessions      loginSessionRepo
	credentialKey []byte
}

// authProviderRepo is the subset of AuthProviderRepository used by the SSO
// service. Mirrors the userRepo pattern so handler-package tests can swap in
// in-memory fakes.
type authProviderRepo interface {
	Create(ctx context.Context, p *repository.AuthProvider) (*repository.AuthProvider, error)
	GetByID(ctx context.Context, id uuid.UUID) (*repository.AuthProvider, error)
	ListByTenant(ctx context.Context, tenantID uuid.UUID, enabledOnly bool) ([]*repository.AuthProvider, error)
	Update(ctx context.Context, id uuid.UUID, req repository.UpdateAuthProviderRequest) (*repository.AuthProvider, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

// loginSessionRepo is the subset of LoginSessionRepository used by the SSO
// service.
type loginSessionRepo interface {
	Create(ctx context.Context, s *repository.LoginSession) error
	ConsumeByState(ctx context.Context, state string) (*repository.LoginSession, error)
	DeleteExpired(ctx context.Context) (int64, error)
}

// Compile-time interface check.
var _ authProviderRepo = (*repository.AuthProviderRepository)(nil)
var _ loginSessionRepo = (*repository.LoginSessionRepository)(nil)

// AuthProviderRepo is an exported alias so handler-package tests can build
// fakes without importing the repository package transitively.
type AuthProviderRepo = authProviderRepo

// LoginSessionRepo is an exported alias for handler-package tests.
type LoginSessionRepo = loginSessionRepo

// NewSSO constructs the SSO service. credentialKey must be exactly 32 bytes
// (AES-256). A nil key disables provider CRUD with a clear startup error so
// no plaintext secret can ever be persisted unencrypted.
func NewSSO(auth *Service, providers authProviderRepo, sessions loginSessionRepo, credentialKey []byte) (*SSO, error) {
	if auth == nil {
		return nil, errors.New("SSO: auth service is nil")
	}
	if len(credentialKey) != 32 {
		return nil, fmt.Errorf("SSO: credential key must be 32 bytes, got %d", len(credentialKey))
	}
	return &SSO{
		auth:          auth,
		providers:     providers,
		sessions:      sessions,
		credentialKey: credentialKey,
	}, nil
}

// AuthService returns the underlying *Service so HTTP handlers can issue
// tokens after a successful SSO callback. Read-only accessor — callers must
// not mutate the returned service.
func (s *SSO) AuthService() *Service { return s.auth }

// Providers returns the underlying provider repo so the admin CRUD handler
// can use it directly.
func (s *SSO) Providers() authProviderRepo { return s.providers }

// Sessions returns the underlying session repo (used by tests and the
// background cleanup goroutine).
func (s *SSO) Sessions() loginSessionRepo { return s.sessions }

// CredentialKey returns the AES-256 key used for client_secret encryption.
// Exported so the admin CRUD handler can re-encrypt on PATCH without needing
// to plumb the raw key through every layer.
func (s *SSO) CredentialKey() []byte { return s.credentialKey }

// ── Provider CRUD helpers ───────────────────────────────────────────────────

// CreateProviderInput is the validated input for CreateProvider. ClientSecret
// is the plaintext OAuth client_secret — encrypted here and never stored.
type CreateProviderInput struct {
	TenantID     uuid.UUID
	Type         repository.AuthProviderType
	DisplayName  string
	Enabled      bool

	OAuthClientID     string
	OAuthClientSecret string // plaintext; encrypted before storage
	OAuthIssuerURL    string
	OAuthScopes       []string

	SAMLIdpMetadataXML string
	SAMLEntityID       string
	SAMLAudience       string

	AutoProvision bool
	DefaultRole   string

	UpdatedBy *uuid.UUID
}

// CreateProvider validates the input, encrypts the plaintext client_secret,
// and persists the row. Returns the persisted record with the ciphertext
// stripped — callers must never expose ciphertext to the wire.
func (s *SSO) CreateProvider(ctx context.Context, in CreateProviderInput) (*repository.AuthProvider, error) {
	if err := validateProviderInput(in.Type, in.DisplayName, in.OAuthClientID, in.OAuthClientSecret,
		in.OAuthIssuerURL, in.SAMLIdpMetadataXML, in.DefaultRole); err != nil {
		return nil, err
	}

	var secretEnc []byte
	if in.Type.IsOAuth() && in.OAuthClientSecret != "" {
		ct, err := aes.Encrypt([]byte(in.OAuthClientSecret), s.credentialKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt client secret: %w", err)
		}
		secretEnc = ct
	}

	p := &repository.AuthProvider{
		TenantID:             in.TenantID,
		Type:                 in.Type,
		DisplayName:          in.DisplayName,
		Enabled:              in.Enabled,
		OAuthClientID:        in.OAuthClientID,
		OAuthClientSecretEnc: secretEnc,
		OAuthIssuerURL:       in.OAuthIssuerURL,
		OAuthScopes:          in.OAuthScopes,
		SAMLIdpMetadataXML:   in.SAMLIdpMetadataXML,
		SAMLEntityID:         in.SAMLEntityID,
		SAMLAudience:         in.SAMLAudience,
		AutoProvision:        in.AutoProvision,
		DefaultRole:          in.DefaultRole,
		UpdatedBy:            in.UpdatedBy,
	}
	return s.providers.Create(ctx, p)
}

// UpdateProviderInput is the partial-update input for UpdateProvider.
// ClientSecret is the plaintext replacement; nil pointer = leave unchanged.
type UpdateProviderInput struct {
	DisplayName        *string
	Enabled            *bool
	OAuthClientID      *string
	OAuthClientSecret  *string // plaintext; encrypted before storage
	OAuthIssuerURL     *string
	OAuthScopes        *[]string
	SAMLIdpMetadataXML *string
	SAMLEntityID       *string
	SAMLAudience       *string
	AutoProvision      *bool
	DefaultRole        *string
	UpdatedBy          *uuid.UUID
}

// UpdateProvider applies a partial update. When OAuthClientSecret is non-nil
// the plaintext is re-encrypted before storage; a non-nil pointer to an empty
// string clears the secret (sets the column to NULL).
func (s *SSO) UpdateProvider(ctx context.Context, id uuid.UUID, in UpdateProviderInput) (*repository.AuthProvider, error) {
	if in.DefaultRole != nil {
		if !validRoles[*in.DefaultRole] {
			return nil, fmt.Errorf("%w: invalid default_role", ErrInvalidProviderConfig)
		}
	}

	req := repository.UpdateAuthProviderRequest{
		DisplayName:        in.DisplayName,
		Enabled:            in.Enabled,
		OAuthClientID:      in.OAuthClientID,
		OAuthIssuerURL:     in.OAuthIssuerURL,
		OAuthScopes:        in.OAuthScopes,
		SAMLIdpMetadataXML: in.SAMLIdpMetadataXML,
		SAMLEntityID:       in.SAMLEntityID,
		SAMLAudience:       in.SAMLAudience,
		AutoProvision:      in.AutoProvision,
		DefaultRole:        in.DefaultRole,
		UpdatedBy:          in.UpdatedBy,
	}

	if in.OAuthClientSecret != nil {
		// Empty plaintext → empty ciphertext → repo stores NULL.
		var ct []byte
		if *in.OAuthClientSecret != "" {
			enc, err := aes.Encrypt([]byte(*in.OAuthClientSecret), s.credentialKey)
			if err != nil {
				return nil, fmt.Errorf("encrypt client secret: %w", err)
			}
			ct = enc
		}
		req.OAuthClientSecretEnc = &ct
	}

	return s.providers.Update(ctx, id, req)
}

// DeleteProvider removes a provider by ID. Users provisioned via this
// provider keep working (sso_provider_id is set to NULL by the FK rule).
func (s *SSO) DeleteProvider(ctx context.Context, id uuid.UUID) error {
	return s.providers.Delete(ctx, id)
}

// ListEnabledProviders returns the providers a tenant has enabled. Used by
// the public /api/v1/auth/providers list endpoint. Ciphertext is stripped
// before return so callers cannot expose it accidentally.
func (s *SSO) ListEnabledProviders(ctx context.Context, tenantID uuid.UUID) ([]*repository.AuthProvider, error) {
	ps, err := s.providers.ListByTenant(ctx, tenantID, true)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		p.OAuthClientSecretEnc = nil
	}
	return ps, nil
}

// ListAllProviders returns every provider (enabled + disabled) for the admin
// CRUD list endpoint. Ciphertext is stripped.
func (s *SSO) ListAllProviders(ctx context.Context, tenantID uuid.UUID) ([]*repository.AuthProvider, error) {
	ps, err := s.providers.ListByTenant(ctx, tenantID, false)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		p.OAuthClientSecretEnc = nil
	}
	return ps, nil
}

// GetProvider returns one provider by ID. Returns ErrProviderNotFound if
// the provider is missing OR disabled when forLogin=true. Ciphertext is
// returned only when forCallback=true (the callback needs the plaintext for
// the token exchange).
func (s *SSO) GetProvider(ctx context.Context, id uuid.UUID, forLogin, forCallback bool) (*repository.AuthProvider, error) {
	p, err := s.providers.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrProviderNotFound
		}
		return nil, err
	}
	if forLogin && !p.Enabled {
		return nil, ErrProviderNotFound
	}
	if !forCallback {
		p.OAuthClientSecretEnc = nil
	}
	return p, nil
}

// DecryptClientSecret decodes the persisted ciphertext and returns the
// plaintext OAuth client_secret. Only the callback path may call this; the
// plaintext must never escape the request scope.
func (s *SSO) DecryptClientSecret(p *repository.AuthProvider) (string, error) {
	if len(p.OAuthClientSecretEnc) == 0 {
		return "", nil
	}
	pt, err := aes.Decrypt(p.OAuthClientSecretEnc, s.credentialKey)
	if err != nil {
		return "", fmt.Errorf("decrypt client secret: %w", err)
	}
	return string(pt), nil
}

// ── OAuth flow helpers ──────────────────────────────────────────────────────

// loginSessionTTL is how long a /start row remains usable before /callback
// must consume it. 10 minutes is enough for slow IdP MFA dialogs without
// leaving stale CSRF tokens lying around for hours.
const loginSessionTTL = 10 * time.Minute

// StartLoginInput carries the validated inputs for StartLogin.
type StartLoginInput struct {
	ProviderID uuid.UUID
	TenantID   uuid.UUID
	NextURL    string // intra-app path; validated by SanitizeNextParam
}

// StartLoginResult carries the values the handler needs to build the
// authorization redirect.
type StartLoginResult struct {
	Provider      *repository.AuthProvider
	State         string
	PKCEVerifier  string
	PKCEChallenge string
}

// StartLogin generates a fresh state + PKCE pair, persists the login session,
// and returns the values the handler needs to compose the authorization URL.
// The handler owns the URL construction so this layer remains free of
// provider-specific URL knowledge.
func (s *SSO) StartLogin(ctx context.Context, in StartLoginInput) (*StartLoginResult, error) {
	p, err := s.GetProvider(ctx, in.ProviderID, true, false)
	if err != nil {
		return nil, err
	}
	if !p.Type.IsOAuth() {
		return nil, ErrSAMLNotImplemented
	}

	state, err := randomURLToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate pkce verifier: %w", err)
	}
	challenge := pkceS256(verifier)

	sess := &repository.LoginSession{
		State:        state,
		TenantID:     in.TenantID,
		ProviderID:   in.ProviderID,
		PKCEVerifier: verifier,
		RedirectURL:  in.NextURL,
		ExpiresAt:    time.Now().Add(loginSessionTTL),
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return nil, fmt.Errorf("persist login session: %w", err)
	}
	return &StartLoginResult{
		Provider:      p,
		State:         state,
		PKCEVerifier:  verifier,
		PKCEChallenge: challenge,
	}, nil
}

// CreateSAMLLoginSession mints a single-use RelayState token, persists it in
// auth_login_sessions alongside the AuthnRequest ID, and returns the
// generated state value. The OAuth flow uses StartLogin (which also builds
// PKCE); the SAML flow only needs the RelayState + the AuthnRequest ID +
// the redirect URL, so this method keeps that surface small.
//
// authnRequestID is the ID attribute crewjam/saml generated on the
// AuthnRequest; we persist it in the pkce_verifier column (unused for SAML
// otherwise) so callbackSAML can pass it to ParseResponse as the only
// permitted InResponseTo value. Stashing it here means we don't need a new
// migration to add a saml_request_id column for v1.
func (s *SSO) CreateSAMLLoginSession(ctx context.Context, tenantID, providerID uuid.UUID, authnRequestID, nextURL string) (string, error) {
	relayState, err := randomURLToken(32)
	if err != nil {
		return "", fmt.Errorf("generate relay state: %w", err)
	}
	sess := &repository.LoginSession{
		State:        relayState,
		TenantID:     tenantID,
		ProviderID:   providerID,
		PKCEVerifier: authnRequestID, // SAML reuses this column for the AuthnRequest ID
		RedirectURL:  nextURL,
		ExpiresAt:    time.Now().Add(loginSessionTTL),
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return "", fmt.Errorf("persist saml login session: %w", err)
	}
	return relayState, nil
}

// ConsumeLoginSession looks up the session by state and deletes it
// atomically. A second call with the same state returns ErrSessionNotFound
// (single-use replay defence).
func (s *SSO) ConsumeLoginSession(ctx context.Context, state string) (*repository.LoginSession, error) {
	sess, err := s.sessions.ConsumeByState(ctx, state)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	return sess, nil
}

// SSOIdentity is the normalised identity returned by an IdP userinfo call.
// EmailVerified MUST be true for auto-provisioning to succeed — handlers
// that proceed past EnsureSSOUser without checking this guard themselves
// risk silently provisioning unverified accounts (security regression).
type SSOIdentity struct {
	Email         string
	EmailVerified bool
	DisplayName   string
	Subject       string // IdP-side user id, for logging/audit
}

// isSyntheticSAEmail returns true for the synthetic email format used to
// populate the shadow user's email column for service accounts (spec §4.1).
// These emails are never valid IdP-asserted identities — any IdP returning
// this format is either misconfigured or adversarial. Rejecting them here,
// before any DB call, closes the window where a crafted IdP assertion could
// match a shadow-user row via GetByEmail.
//
// Format: "sa+" + UUID + "@internal.invalid" — mirroring the
// CreateAtomic helper in repository/service_account.go.
func isSyntheticSAEmail(email string) bool {
	return strings.HasPrefix(email, "sa+") && strings.HasSuffix(email, "@internal.invalid")
}

// EnsureSSOUser matches the identity to an existing local user OR provisions
// a new one. It enforces the verified-email gate, the auto-provision flag,
// and the default role grant. Returns the (possibly newly created) user and
// the role names to embed in the JWT.
//
// Concurrency: if a parallel SSO callback races to create the same user, the
// CreateSSOUser call returns ErrAlreadyExists and we re-query by email so
// the second caller still gets a valid user record.
func (s *SSO) EnsureSSOUser(ctx context.Context, p *repository.AuthProvider, ident SSOIdentity) (*repository.User, []string, error) {
	if ident.Email == "" {
		return nil, nil, fmt.Errorf("%w: idp returned empty email", ErrInvalidProviderConfig)
	}
	if !ident.EmailVerified {
		// Refuse unverified emails — an attacker who controls an IdP account
		// with an unverified attacker@victim-corp.com would otherwise be
		// auto-provisioned as the legitimate victim.
		return nil, nil, ErrEmailNotVerified
	}

	// Reject synthetic service-account emails before any DB call. An IdP
	// returning "sa+<uuid>@internal.invalid" is either misconfigured or
	// adversarial; these emails are machine-internal and must never be used
	// to authenticate a human SSO session (FE-API-048, Task 10).
	if isSyntheticSAEmail(ident.Email) {
		return nil, nil, fmt.Errorf("%w: email domain is reserved for internal service accounts", ErrInvalidProviderConfig)
	}

	user, err := s.auth.users.GetHumanByEmail(ctx, p.TenantID, ident.Email)
	switch {
	case err == nil:
		// Existing human user — accept the SSO login. Note that we do NOT
		// check whether they originally registered via SSO or password; either
		// path is a valid way to reach the same account. GetHumanByEmail
		// already excludes shadow users (kind='service_account'), so we cannot
		// accidentally bind an SA identity here.
		if !user.IsActive {
			return nil, nil, ErrAccountDisabled
		}
		_ = s.auth.users.TouchLastLogin(ctx, user.ID)
		roles := s.auth.loadRoleNames(ctx, user.ID, p.TenantID)
		return user, roles, nil
	case errors.Is(err, repository.ErrNotFound):
		if !p.AutoProvision {
			return nil, nil, ErrAutoProvisionDisabled
		}
	default:
		return nil, nil, fmt.Errorf("no human user with email %q: %w", ident.Email, err)
	}

	// Auto-provision path.
	username := DeriveSSOUsername(ident.Email)
	created, err := s.auth.users.CreateSSOUser(ctx, repository.CreateSSOUserRequest{
		TenantID:      p.TenantID,
		Username:      username,
		Email:         ident.Email,
		DisplayName:   ident.DisplayName,
		SSOProviderID: p.ID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			// Lost a race with a parallel callback — re-query by email and
			// return that row. GetHumanByEmail is used here intentionally: if
			// the race created a shadow-user row instead of a human row
			// (should never happen in normal operation) this call returns
			// ErrNotFound and we propagate it as an error rather than silently
			// authenticating as a non-human principal.
			created, err = s.auth.users.GetHumanByEmail(ctx, p.TenantID, ident.Email)
			if err != nil {
				return nil, nil, fmt.Errorf("no human user with email %q after race: %w", ident.Email, err)
			}
		} else {
			return nil, nil, fmt.Errorf("create sso user: %w", err)
		}
	}

	// Grant the default role at org scope "*" so the user can sign in but
	// has no implicit access to any org until an admin grants it. CLAUDE.md
	// §7 forbids the wildcard org from colliding with a real org name
	// (validateOrgName rejects "*"), so this is unambiguous.
	if err := s.auth.users.GrantRole(ctx, repository.RoleAssignment{
		TenantID:   p.TenantID,
		UserID:     created.ID,
		RoleName:   p.DefaultRole,
		ScopeType:  "org",
		ScopeValue: "*",
	}); err != nil {
		slog.WarnContext(ctx, "SSO auto-provision: GrantRole failed", "user_id", created.ID, "err", err)
	}

	roles := s.auth.loadRoleNames(ctx, created.ID, p.TenantID)
	return created, roles, nil
}

// IssueSSOToken issues a JWT for an SSO-authenticated user. Wraps the
// existing IssueToken path so the SSO callback does not duplicate JWT
// signing logic.
func (s *SSO) IssueSSOToken(ctx context.Context, user *repository.User, roles []string) (string, error) {
	return s.auth.IssueToken(ctx, user.ID.String(), user.TenantID.String(), nil, roles)
}

// ── Validation helpers ──────────────────────────────────────────────────────

// validRoles is the allowlist of default_role values. Mirrors the CHECK
// constraint on auth_providers.default_role so a BFF-level rejection beats
// a DB-level constraint-violation error.
var validRoles = map[string]bool{
	"reader": true,
	"writer": true,
	"admin":  true,
	"owner":  true,
}

// providerDisplayNameMax bounds the display_name field so a misconfigured
// admin cannot poison the login dropdown with a multi-megabyte string.
const providerDisplayNameMax = 128

// validateProviderInput enforces the per-type required fields. OAuth needs
// client_id and (for create) a non-empty client_secret; SAML needs the IdP
// metadata XML. Both types need a sane display_name and an allowed default
// role.
func validateProviderInput(t repository.AuthProviderType, displayName, clientID, clientSecret, issuerURL, samlMetadata, defaultRole string) error {
	if !t.IsValid() {
		return fmt.Errorf("%w: invalid type", ErrInvalidProviderConfig)
	}
	dn := strings.TrimSpace(displayName)
	if dn == "" || len(dn) > providerDisplayNameMax {
		return fmt.Errorf("%w: display_name must be 1..%d chars", ErrInvalidProviderConfig, providerDisplayNameMax)
	}
	if !validRoles[defaultRole] {
		return fmt.Errorf("%w: invalid default_role", ErrInvalidProviderConfig)
	}
	if t.IsOAuth() {
		if strings.TrimSpace(clientID) == "" {
			return fmt.Errorf("%w: oauth_client_id is required", ErrInvalidProviderConfig)
		}
		if strings.TrimSpace(clientSecret) == "" {
			return fmt.Errorf("%w: oauth_client_secret is required", ErrInvalidProviderConfig)
		}
		if t == repository.AuthProviderOAuthGeneric && strings.TrimSpace(issuerURL) == "" {
			return fmt.Errorf("%w: oauth_issuer_url is required for generic OIDC", ErrInvalidProviderConfig)
		}
	}
	if t == repository.AuthProviderSAML {
		if strings.TrimSpace(samlMetadata) == "" {
			return fmt.Errorf("%w: saml_idp_metadata_xml is required", ErrInvalidProviderConfig)
		}
	}
	return nil
}

// SanitizeNextParam validates and returns a safe intra-app redirect path or
// "" if no path was provided. Rejects anything that could become an open
// redirect: external schemes, protocol-relative URLs, and CRLF injection.
func SanitizeNextParam(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 256 {
		return "", ErrInvalidNextParam
	}
	// Reject CRLF (header injection guard) and any scheme/host component.
	if strings.ContainsAny(raw, "\r\n") {
		return "", ErrInvalidNextParam
	}
	// Must start with exactly one "/" and may not start with "//" (which
	// browsers treat as a protocol-relative URL → external redirect).
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", ErrInvalidNextParam
	}
	// Disallow embedded scheme separators — "http://" inside the path is a
	// red flag even after the prefix check.
	if strings.Contains(raw, "://") {
		return "", ErrInvalidNextParam
	}
	// Parse to a URL and ensure no host/scheme leaked through.
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalidNextParam
	}
	if u.Scheme != "" || u.Host != "" {
		return "", ErrInvalidNextParam
	}
	return raw, nil
}

// DeriveSSOUsername converts an IdP email to a username that fits the
// existing username regex (3..64 chars, [a-zA-Z0-9_-]). The local part is
// taken, non-conforming chars are replaced with '-', and a short hash suffix
// is appended to keep collisions unlikely without round-tripping the DB.
//
// Returned username is not unique by itself — CreateSSOUser may still hit
// ErrAlreadyExists if a real password user with the same local part exists,
// in which case the caller falls back to GetByEmail and reuses that row.
func DeriveSSOUsername(email string) string {
	local := email
	if at := strings.IndexByte(email, '@'); at >= 0 {
		local = email[:at]
	}
	// Replace any non-allowed character with '-'.
	b := make([]byte, 0, len(local))
	for i := 0; i < len(local); i++ {
		c := local[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	// Empty after sanitisation (e.g. all-Unicode local part) → fall back to
	// a deterministic-ish placeholder so DB constraints don't bounce us.
	if len(b) == 0 {
		b = []byte("sso-user")
	}
	// 3-char minimum on the username regex — pad if needed.
	for len(b) < 3 {
		b = append(b, '0')
	}
	// 64-char ceiling on the username regex — trim aggressively.
	if len(b) > 56 {
		b = b[:56]
	}
	// Append a 6-char hash suffix derived from the full email so two SSO
	// users with the same local part at different domains do not collide.
	suffix := emailSuffix(email)
	return string(b) + "-" + suffix
}

// emailSuffix returns a 6-char hash suffix used by DeriveSSOUsername.
func emailSuffix(email string) string {
	h := sha256.Sum256([]byte(strings.ToLower(email)))
	return base64.RawURLEncoding.EncodeToString(h[:5])[:6]
}

// ── PKCE helpers ────────────────────────────────────────────────────────────

// randomURLToken returns a base64url-encoded random string of approximately
// the given byte length. Used for both CSRF state and PKCE verifier.
func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceS256 computes the SHA-256 PKCE code_challenge for the given verifier.
// Per RFC 7636 §4.2 the challenge is base64url-encoded without padding.
func pkceS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
