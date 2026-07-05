// REDESIGN-001 RM-003 — SSO service, global-config edition.
//
// Per-tenant auth_providers (and the admin CRUD surface that had the Review §A1
// gate flaw) are replaced by the global_sso_config table. The SSO service now
// looks up providers by a stable string provider_id (e.g. "google", "okta_saml")
// rather than a per-tenant UUID.
//
// What changed vs. FE-API-034:
//   - authProviderRepo → globalSSOConfigRepo (string-keyed)
//   - CreateProvider / UpdateProvider / DeleteProvider / ListAllProviders removed
//   - StartLoginInput.ProviderID: uuid.UUID → string
//   - LoginSession.ProviderID: uuid.UUID → string
//   - EnsureSSOUser: accepts *repository.GlobalSSOProvider instead of *repository.AuthProvider
//   - TenantID no longer threaded through the session; resolved at callback time
//     from users.tenant_id or AUTH_DEFAULT_TENANT_ID for new provisioned users.
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
	ErrProviderNotFound      = errors.New("auth provider not found or disabled")
	ErrInvalidProviderConfig = errors.New("invalid provider configuration")
	ErrSessionNotFound       = errors.New("login session not found or expired")
	ErrAutoProvisionDisabled = errors.New("auto-provision disabled and no matching user")
	ErrEmailNotVerified      = errors.New("email not verified by identity provider")
	ErrInvalidNextParam      = errors.New("invalid next parameter")
	ErrSAMLNotImplemented    = errors.New("SAML support is not implemented in this build")
)

// globalSSOConfigRepo is the subset of GlobalSSOConfigRepository used by the
// SSO service. In-memory fakes in handler tests implement this interface.
type globalSSOConfigRepo interface {
	Get(ctx context.Context, providerID string) (*repository.GlobalSSOProvider, error)
	List(ctx context.Context, enabledOnly bool) ([]*repository.GlobalSSOProvider, error)
}

// loginSessionRepo is the subset of LoginSessionRepository used by the SSO
// service.
type loginSessionRepo interface {
	Create(ctx context.Context, s *repository.LoginSession) error
	ConsumeByState(ctx context.Context, state string) (*repository.LoginSession, error)
	DeleteExpired(ctx context.Context) (int64, error)
}

// Compile-time interface checks.
var _ globalSSOConfigRepo = (*repository.GlobalSSOConfigRepository)(nil)
var _ loginSessionRepo = (*repository.LoginSessionRepository)(nil)

// GlobalSSOConfigRepo is an exported alias so handler-package tests can build
// fakes without importing the repository package transitively.
type GlobalSSOConfigRepo = globalSSOConfigRepo

// LoginSessionRepo is an exported alias for handler-package tests.
type LoginSessionRepo = loginSessionRepo

// SSO is the REDESIGN-001 RM-003 SSO service. It owns the global_sso_config
// and login_sessions repositories and reuses the existing Service for JWT
// issuance, user lookup, and role assignment.
//
// CredentialKey is the AES-256 key used to decrypt OAuth client_secret_enc
// at callback time. Must be exactly 32 bytes; constructor enforces this.
type SSO struct {
	auth          *Service
	providers     globalSSOConfigRepo
	sessions      loginSessionRepo
	credentialKey []byte
	// defaultTenantID is used when auto-provisioning a new SSO user and no
	// existing user row can be matched by email. Comes from AUTH_DEFAULT_TENANT_ID.
	defaultTenantID uuid.UUID
}

// NewSSO constructs the SSO service. credentialKey must be exactly 32 bytes
// (AES-256). A nil key disables SSO with a clear startup error so no plaintext
// secret can ever be persisted unencrypted.
func NewSSO(auth *Service, providers globalSSOConfigRepo, sessions loginSessionRepo, credentialKey []byte) (*SSO, error) {
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

// WithDefaultTenantID sets the fallback tenant used when auto-provisioning a
// new SSO user. Called from server.go when AUTH_DEFAULT_TENANT_ID is set.
func (s *SSO) WithDefaultTenantID(id uuid.UUID) *SSO {
	s.defaultTenantID = id
	return s
}

// AuthService returns the underlying *Service so HTTP handlers can issue
// tokens after a successful SSO callback.
func (s *SSO) AuthService() *Service { return s.auth }

// Sessions returns the underlying session repo (used by the background
// cleanup goroutine).
func (s *SSO) Sessions() loginSessionRepo { return s.sessions }

// CredentialKey returns the AES-256 key used for client_secret_enc decryption.
func (s *SSO) CredentialKey() []byte { return s.credentialKey }

// ── Provider lookup ─────────────────────────────────────────────────────────

// LookupProvider returns the global SSO config for a given providerID.
// Returns (nil, ErrProviderNotFound) when the provider is unknown or disabled
// — the caller treats this as "this SSO option is not available" and surfaces
// a clean 404.
//
// REDESIGN-001 RM-003: collapsed from per-tenant per-uuid to global per-string-id.
func (s *SSO) LookupProvider(ctx context.Context, providerID string) (*repository.GlobalSSOProvider, error) {
	p, err := s.providers.Get(ctx, providerID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrProviderNotFound
		}
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrProviderNotFound
	}
	return p, nil
}

// LookupProviderWithSecret returns the provider together with the decrypted
// OAuth client_secret. Only the OAuth callback path may call this; the
// plaintext must never escape the request scope.
func (s *SSO) LookupProviderWithSecret(ctx context.Context, providerID string) (*repository.GlobalSSOProvider, string, error) {
	p, err := s.LookupProvider(ctx, providerID)
	if err != nil {
		return nil, "", err
	}
	secret, err := s.DecryptClientSecret(p)
	if err != nil {
		return nil, "", err
	}
	return p, secret, nil
}

// ListEnabledProviders returns the globally-enabled providers. Used by the
// public /api/v1/auth/providers list endpoint. Ciphertext is stripped before
// return so callers cannot expose it accidentally.
func (s *SSO) ListEnabledProviders(ctx context.Context) ([]*repository.GlobalSSOProvider, error) {
	ps, err := s.providers.List(ctx, true)
	if err != nil {
		return nil, err
	}
	// Strip ciphertext at the service boundary — defence in depth.
	for _, p := range ps {
		p.OAuthClientSecretEnc = nil
	}
	return ps, nil
}

// DecryptClientSecret decodes the persisted ciphertext and returns the
// plaintext OAuth client_secret. Only the callback path may call this; the
// plaintext must never escape the request scope.
func (s *SSO) DecryptClientSecret(p *repository.GlobalSSOProvider) (string, error) {
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
	ProviderID string // stable string id from global_sso_config
	NextURL    string // intra-app path; validated by SanitizeNextParam
}

// StartLoginResult carries the values the handler needs to build the
// authorization redirect.
type StartLoginResult struct {
	Provider      *repository.GlobalSSOProvider
	State         string
	PKCEVerifier  string
	PKCEChallenge string
}

// StartLogin generates a fresh state + PKCE pair, persists the login session,
// and returns the values the handler needs to compose the authorization URL.
// The handler owns the URL construction so this layer remains free of
// provider-specific URL knowledge.
func (s *SSO) StartLogin(ctx context.Context, in StartLoginInput) (*StartLoginResult, error) {
	p, err := s.LookupProvider(ctx, in.ProviderID)
	if err != nil {
		return nil, err
	}
	if !p.AuthProviderType().IsOAuth() {
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

	// RM-004: TenantID is no longer stored in the session.
	sess := &repository.LoginSession{
		State:        state,
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
// auth_login_sessions alongside the AuthnRequest ID, and returns the generated
// state value.
//
// authnRequestID is the ID attribute crewjam/saml generated on the
// AuthnRequest; we persist it in the pkce_verifier column so callbackSAML can
// pass it to ParseResponse as the only permitted InResponseTo value.
//
// RM-004: TenantID is no longer stored in the session.
func (s *SSO) CreateSAMLLoginSession(ctx context.Context, providerID string, authnRequestID, nextURL string) (string, error) {
	relayState, err := randomURLToken(32)
	if err != nil {
		return "", fmt.Errorf("generate relay state: %w", err)
	}
	sess := &repository.LoginSession{
		State:        relayState,
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

// ErrSSOSubjectMismatch is returned when the IdP-asserted (provider, subject)
// composite identity disagrees with the persisted binding on a user row that
// was matched via email. The caller surfaces this as an operator-actionable
// 401 — the user's IT/admin needs to either link the new SSO identity to the
// existing account or clear the stale binding so the row can rebind.
//
// Why fail closed rather than silently rebind: if an employee leaves and the
// corporate email is later reassigned to a new hire, allowing the new hire's
// first SSO login to overwrite the persisted subject would silently bind them
// to the prior employee's account (history, RBAC grants, audit trail). The
// only correct response is to refuse and force a human decision.
var ErrSSOSubjectMismatch = errors.New("sso subject does not match the persisted binding for this email")

// EnsureSSOUser matches the identity to an existing local user OR provisions
// a new one. It enforces the verified-email gate, the auto-provision flag,
// and the default role grant. Returns the (possibly newly created) user and
// the role names to embed in the JWT.
//
// Lookup order (REDESIGN-001 Phase 5.5):
//  1. (sso_provider_id, sso_subject) composite lookup — the IdP's stable
//     subject identifier is the authoritative binding.
//  2. Email fallback for accounts that pre-date the subject migration. If the
//     row has NULL sso_subject the subject is back-filled and login proceeds.
//     If the row has a non-NULL sso_subject that disagrees with the IdP's
//     assertion, the login is REJECTED to defend against email-recycle
//     account takeover.
//  3. Auto-provision when neither lookup matches and the provider allows it.
//
// Concurrency: if a parallel SSO callback races to create the same user, the
// CreateSSOUser call returns ErrAlreadyExists and we re-query by subject
// (then by email) so the second caller still gets a valid user record.
//
// REDESIGN-001 RM-003: accepts *repository.GlobalSSOProvider instead of
// *repository.AuthProvider. TenantID for new auto-provisioned users falls
// back to s.defaultTenantID when no existing user row can be found by email.
func (s *SSO) EnsureSSOUser(ctx context.Context, p *repository.GlobalSSOProvider, ident SSOIdentity, tenantID uuid.UUID) (*repository.User, []string, error) {
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

	// Resolve the tenant to search in. The caller supplies tenantID when it
	// can be determined from context (e.g. a custom domain request); it falls
	// back to defaultTenantID for single-tenant deployments.
	resolvedTenantID := tenantID
	if resolvedTenantID == uuid.Nil {
		resolvedTenantID = s.defaultTenantID
	}
	if resolvedTenantID == uuid.Nil {
		return nil, nil, fmt.Errorf("cannot resolve tenant for SSO callback: set AUTH_DEFAULT_TENANT_ID")
	}

	// Step 1: try the subject-keyed lookup first. A successful match here is
	// the strongest signal we have that this is the same human who logged in
	// before — the (provider, subject) pair is immune to email recycling
	// because the IdP minted the subject when the remote account was created
	// and will not re-assign it. SEC-040 — lookup is now tenant-scoped so a
	// shared IdP in multi-mode cannot leak cross-tenant.
	if ident.Subject != "" {
		bySubject, err := s.auth.users.GetUserBySSOSubject(ctx, resolvedTenantID, p.ProviderID, ident.Subject)
		switch {
		case err == nil:
			if !bySubject.IsActive {
				return nil, nil, ErrAccountDisabled
			}
			_ = s.auth.users.TouchLastLogin(ctx, bySubject.ID)
			roles := s.auth.loadRoleNames(ctx, bySubject.ID, resolvedTenantID)
			return bySubject, roles, nil
		case errors.Is(err, repository.ErrNotFound):
			// Fall through to email-based lookup / auto-provision.
		default:
			return nil, nil, fmt.Errorf("lookup user by sso subject: %w", err)
		}
	}

	user, err := s.auth.users.GetHumanByEmail(ctx, resolvedTenantID, ident.Email)
	switch {
	case err == nil:
		// Existing human user matched by email. Before accepting the login,
		// reconcile the persisted SSO subject with the IdP's assertion so we
		// detect (and refuse) a recycled-email takeover attempt.
		//
		// Three cases:
		//   a. user.SSOSubject == "" (pre-migration row): back-fill the
		//      subject and continue. After this point all future logins for
		//      this account will hit the Step 1 fast path.
		//   b. user.SSOSubject == ident.Subject: identity confirmed, accept.
		//   c. user.SSOSubject != ident.Subject AND ident.Subject != "":
		//      REJECT. The email matches but the IdP says this is a
		//      different remote account — almost certainly an email recycle.
		switch {
		case ident.Subject == "":
			// IdP failed to supply a subject. We cannot reconcile, so behave
			// as the legacy code did and accept the match — but log so an
			// operator can spot a misconfigured IdP integration.
			slog.WarnContext(ctx, "SSO callback missing subject; accepting email-only match",
				"provider_id", p.ProviderID, "user_id", user.ID)
		case user.SSOSubject == "":
			// Back-fill the subject for this pre-migration row.
			if err := s.auth.users.SetSSOSubject(ctx, user.ID, ident.Subject); err != nil {
				return nil, nil, fmt.Errorf("backfill sso subject: %w", err)
			}
			user.SSOSubject = ident.Subject
		case user.SSOSubject != ident.Subject:
			// SEC-042 — refuse with a generic, non-enumerating message. The
			// previous wording echoed the email back, which gave an attacker
			// who controls an IdP they can spin up freely (Auth0 trial,
			// self-hosted Keycloak) a probe for which addresses are
			// registered. The email is still emitted to the server-side log
			// so operators can debug genuine link issues.
			slog.WarnContext(ctx, "SSO subject mismatch — refusing login",
				"provider_id", p.ProviderID, "user_id", user.ID, "email", ident.Email)
			return nil, nil, fmt.Errorf("%w: this SSO identity is not linked to a registered account — contact your admin to link it",
				ErrSSOSubjectMismatch)
		}
		if !user.IsActive {
			return nil, nil, ErrAccountDisabled
		}
		_ = s.auth.users.TouchLastLogin(ctx, user.ID)
		roles := s.auth.loadRoleNames(ctx, user.ID, resolvedTenantID)
		return user, roles, nil
	case errors.Is(err, repository.ErrNotFound):
		if !p.AutoProvision {
			return nil, nil, ErrAutoProvisionDisabled
		}
	default:
		// SEC-042 — keep the email out of the error chain. The DB error
		// type is still propagated; the handler logs `provider_id` + the
		// wrapped err at error level so operators can correlate, but the
		// email itself only appears at the IdP-side audit log (out of
		// our scope) — there is intentionally no server-side log of the
		// email at this branch since a DB outage shouldn't be a vector
		// for enumerating which addresses are registered.
		return nil, nil, fmt.Errorf("lookup human user by email: %w", err)
	}

	// Auto-provision path. Persist the IdP subject at creation so the next
	// login for this user matches via Step 1 and never falls back to email.
	username := DeriveSSOUsername(ident.Email)
	created, err := s.auth.users.CreateSSOUser(ctx, repository.CreateSSOUserRequest{
		TenantID:      resolvedTenantID,
		Username:      username,
		Email:         ident.Email,
		DisplayName:   ident.DisplayName,
		SSOProviderID: p.ProviderID, // stable string id (e.g. "google")
		SSOSubject:    ident.Subject,
	})
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			// Lost a race with a parallel callback — re-query by subject
			// first (it's the authoritative key, tenant-scoped per SEC-040),
			// then by email. Clear the outer err on a successful recovery so
			// the role-grant / returning-user logic below runs against
			// `created`.
			created = nil
			if ident.Subject != "" {
				if bySubject, lookupErr := s.auth.users.GetUserBySSOSubject(ctx, resolvedTenantID, p.ProviderID, ident.Subject); lookupErr == nil {
					created = bySubject
				}
			}
			if created == nil {
				byEmail, emailErr := s.auth.users.GetHumanByEmail(ctx, resolvedTenantID, ident.Email)
				if emailErr != nil {
					// SEC-042 — strip the email from the wrapped error so it
					// never reaches the caller (this surface bubbles up to
					// SSO callback HTTP responses).
					return nil, nil, fmt.Errorf("no human user after race: %w", emailErr)
				}
				// SEC-041 — race-recovery via email is the one path where the
				// recovered row is NOT guaranteed to carry the requested
				// subject. The race-winning concurrent callback may have set
				// `byEmail.SSOSubject` to a different value; accepting the
				// row blind would mean handing this caller a session for an
				// identity the IdP did not assert in this request. Re-check
				// and refuse with the same generic message as the
				// well-trodden subject-mismatch path so the rejection shape
				// is identical and non-enumerating.
				//
				// Guard intentionally does NOT require `byEmail.SSOSubject !=
				// ""` — fail-closed if the recovered row is in an unexpected
				// shape (e.g. a partially-written race-winning row). Tightened
				// per the security-agent's pre-PR review.
				if ident.Subject != "" && byEmail.SSOSubject != ident.Subject {
					slog.WarnContext(ctx, "SSO race recovery: subject mismatch on email-recovered row — refusing login",
						"provider_id", p.ProviderID, "user_id", byEmail.ID, "email", ident.Email,
						"row_subject_empty", byEmail.SSOSubject == "")
					return nil, nil, fmt.Errorf("%w: this SSO identity is not linked to a registered account — contact your admin to link it",
						ErrSSOSubjectMismatch)
				}
				created = byEmail
			}
		} else {
			return nil, nil, fmt.Errorf("create sso user: %w", err)
		}
	}

	// Grant the reader role at org scope "*" so the user can sign in but has
	// no implicit access to any org until an admin grants it. CLAUDE.md §7
	// forbids the wildcard org from colliding with a real org name
	// (validateOrgName rejects "*"), so this is unambiguous.
	if err := s.auth.users.GrantRole(ctx, repository.RoleAssignment{
		TenantID:   resolvedTenantID,
		UserID:     created.ID,
		RoleName:   "reader",
		ScopeType:  "org",
		ScopeValue: "*",
	}); err != nil {
		slog.WarnContext(ctx, "SSO auto-provision: GrantRole failed", "user_id", created.ID, "err", err)
	}

	roles := s.auth.loadRoleNames(ctx, created.ID, resolvedTenantID)
	return created, roles, nil
}

// IssueSSOToken issues a JWT for an SSO-authenticated user. Wraps the
// existing issueSessionToken path so the SSO callback does not duplicate JWT
// signing logic. The user's is_global_admin flag is included in the JWT
// (REDESIGN-001 Phase 5.1).
//
// meta carries the client IP + User-Agent captured at the SSO callback edge so
// a successful federated login creates a listable/revocable session row. When
// no session repo is wired the underlying issueSessionToken degrades to a plain
// (no-sid) token.
func (s *SSO) IssueSSOToken(ctx context.Context, user *repository.User, roles []string, meta SessionMeta) (string, error) {
	// SSO callbacks always provision human users — service-account principals
	// are minted server-side and never appear in the SSO flow. user.Kind is
	// forwarded verbatim so the contract stays correct if that ever changes.
	// amr is ["sso"] — the authentication method was a federated SSO/SAML
	// assertion rather than a local password.
	return s.auth.issueSessionToken(ctx, user.ID, user.TenantID, roles, user.IsGlobalAdmin, user.Kind, []string{"sso"}, meta)
}

// ── Validation helpers ──────────────────────────────────────────────────────

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
