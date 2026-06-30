package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// Sentinel errors returned by Service methods. Handlers map these to HTTP/gRPC status codes.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAccountLocked      = errors.New("account locked")
	ErrAccountDisabled    = errors.New("account disabled")
	ErrTokenRevoked       = errors.New("token has been revoked")
	ErrKeyExpired         = errors.New("api key has expired")
)

const (
	tokenTTL        = 5 * time.Minute
	lockoutDuration = 15 * time.Minute
	maxFailedLogins = 5
	// rawSecretLen is the number of random bytes for API key secrets.
	rawSecretLen = 32
)

// Claims is the JWT payload for all tokens issued by registry-auth.
// It extends the standard RegisteredClaims with registry-specific fields.
type Claims struct {
	jwt.RegisteredClaims
	TenantID string             `json:"tenant_id"`
	Access   []RepositoryAccess `json:"access"`
	// Roles is the flat list of RBAC role names the user holds within the tenant
	// (e.g. ["admin"], ["writer","reader"]). Frontend gates admin-only UI on
	// presence of "owner" or "admin" here. Empty for tokens issued before RBAC
	// resolution (e.g. legacy refresh paths).
	Roles []string `json:"roles,omitempty"`
	// IsGlobalAdmin mirrors users.is_global_admin — the typed platform-admin
	// primitive that replaces the (admin, org, '*') legacy marker.
	// REDESIGN-001 Phase 5.1. Existing cached JWT-validation values without
	// this field decode as false, which is safe — they trigger a re-validation
	// on the next request so the flag is refreshed promptly.
	IsGlobalAdmin bool `json:"is_global_admin,omitempty"`
	// PrincipalKind mirrors users.kind ("human" or "service_account") for the
	// authenticated subject. Set at JWT issuance time and by the API-key
	// Bearer dispatch (FUT-006). Empty on legacy tokens issued before
	// REDESIGN-001 Phase 5.4 — admin gates treat empty as "human" for
	// backward compatibility (legacy logins did not embed the field). The
	// claim's authenticity is enforced by the same RS256 signature that
	// guards every other field, so downstream gates may trust it after a
	// successful ValidateToken.
	PrincipalKind string `json:"principal_kind,omitempty"`
}

// RepositoryAccess describes a scope granted within a single token.
type RepositoryAccess struct {
	Type    string   `json:"type"`    // always "repository"
	Name    string   `json:"name"`    // "org/repo"
	Actions []string `json:"actions"` // e.g. ["push","pull"]
}

// JWKSResponse is the JSON Web Key Set returned by the /.well-known/jwks.json endpoint.
type JWKSResponse struct {
	Keys []JWK `json:"keys"`
}

// JWK represents one RSA public key in JWKS format.
type JWK struct {
	Kty string `json:"kty"` // "RSA"
	Use string `json:"use"` // "sig"
	Kid string `json:"kid"`
	Alg string `json:"alg"` // "RS256"
	N   string `json:"n"`   // base64url-encoded modulus
	E   string `json:"e"`   // base64url-encoded exponent
}

// ValidatedKey is the enriched result returned by ValidateAPIKey. It provides
// a unified view of the authenticated principal regardless of whether the key
// was issued to a human user or a service account.
//
// PrincipalKind is always "human" or "service_account". ServiceAccountID is
// non-nil when PrincipalKind=="service_account". EffectiveScopes is the
// intersection of the key's stored scopes and the SA's AllowedScopes (for
// human keys it equals the key's scopes directly, since there is no SA-level
// allowlist to intersect).
type ValidatedKey struct {
	// UserID is the users.id for the authenticated principal. For SA-owned keys
	// this is the shadow user's ID so downstream JWT issuance treats SA callers
	// the same way as human callers.
	UserID uuid.UUID
	// TenantID is the tenant the key belongs to.
	TenantID uuid.UUID
	// Access is the OCI RepositoryAccess list derived from EffectiveScopes.
	// It is suitable for embedding directly in a JWT `access` claim.
	Access []RepositoryAccess
	// PrincipalKind is "human" or "service_account".
	PrincipalKind string
	// ServiceAccountID is set when PrincipalKind=="service_account".
	ServiceAccountID *uuid.UUID
	// EffectiveScopes is the final scope list after applying the SA allowlist
	// intersection (for SA keys) or using the key's own scopes (for human keys).
	EffectiveScopes []string
}

// Service is the core authentication business logic.
type Service struct {
	users           userRepo
	apiKeys         apiKeyRepo
	serviceAccounts saRepo
	audit           AuditEmitter
	redis           redisClient
	privKey         *rsa.PrivateKey
	pubKey          *rsa.PublicKey
	keyID           string
}

// New constructs a Service by parsing the base64-encoded PEM keys from config.
// sa and audit may be nil; if nil, the service-account branch of ValidateAPIKey
// returns codes.Unimplemented and cross-tenant audit emission is skipped.
func New(
	users *repository.UserRepository,
	apiKeys *repository.APIKeyRepository,
	sa *repository.ServiceAccountRepo,
	audit AuditEmitter,
	rdb *redis.Client,
	privKeyB64, pubKeyB64, keyID string,
) (*Service, error) {
	privKey, err := parsePrivateKey(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	pubKey, err := parsePublicKey(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT public key: %w", err)
	}
	var saR saRepo
	if sa != nil {
		saR = sa
	}
	return &Service{
		users:           users,
		apiKeys:         apiKeys,
		serviceAccounts: saR,
		audit:           audit,
		redis:           rdb,
		privKey:         privKey,
		pubKey:          pubKey,
		keyID:           keyID,
	}, nil
}

// IssueToken signs and returns a JWT for the given user with the requested access
// scopes, roles, and the global-admin flag. Roles is the flat list of RBAC role
// names held by the user in the tenant; it is embedded in the JWT so downstream
// services (and the frontend) can read user roles without an extra RPC.
// isGlobalAdmin mirrors users.is_global_admin (REDESIGN-001 Phase 5.1).
// principalKind mirrors users.kind ("human" or "service_account") and is
// embedded so downstream services can deny SA principals at admin gates
// without a separate lookup (REDESIGN-001 Phase 5.4). Empty principalKind
// is encoded as "human" so legacy callers and tests keep their default
// behaviour without coordinated update.
func (s *Service) IssueToken(ctx context.Context, userID, tenantID string, access []RepositoryAccess, roles []string, isGlobalAdmin bool, principalKind string) (string, error) {
	if principalKind == "" {
		principalKind = "human"
	}
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "registry-auth",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{"registry-core"},
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
		TenantID:      tenantID,
		Access:        access,
		Roles:         roles,
		IsGlobalAdmin: isGlobalAdmin,
		PrincipalKind: principalKind,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.keyID
	signed, err := tok.SignedString(s.privKey)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	// Track the JTI in the user's active-token set so ChangePassword can
	// revoke every live session in one shot. A Redis hiccup must not fail
	// token issuance — log and move on; the worst case is one stale session
	// surviving a password change, which the token's 5-minute TTL bounds.
	if err := s.recordIssuedJTI(ctx, userID, claims.ID); err != nil {
		slog.WarnContext(ctx, "auth: failed to record issued JTI", "user_id", userID, "error", err)
	}
	return signed, nil
}

// ValidateToken parses and validates the token string, then checks the Redis
// revocation list. Returns the parsed claims on success.
func (s *Service) ValidateToken(ctx context.Context, tokenStr string) (*Claims, error) {
	var claims Claims
	tok, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.pubKey, nil
	})
	if err != nil || !tok.Valid {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
	}

	// Check revocation list — the key TTL matches token lifetime so an expired
	// token's revocation entry is self-cleaning (no background cleanup needed).
	// See RevokeToken for the TTL derivation that makes this guarantee hold.
	revoked, err := s.isRevoked(ctx, claims.ID)
	if err != nil {
		return nil, fmt.Errorf("check revocation: %w", err)
	}
	if revoked {
		return nil, ErrTokenRevoked
	}

	// Check principal-level revocation (spec §5.5 / security HIGH H2 / Review §B).
	// T8's ServiceAccountService.SetDisabled writes "revoke:user:<shadow_user_id>"
	// with a 25-minute TTL when an SA is disabled. We check the same key here so
	// any outstanding JWT for the disabled principal is rejected immediately
	// without waiting for the token's natural expiry.
	//
	// Fail-CLOSED on Redis error: human users have no second-layer check (only SA
	// principals get a ValidateAPIKey DB lookup), so a Redis outage that prevented
	// revocation lookups would silently allow disabled human users to keep using
	// their JWT until natural expiry (up to JWT TTL = 5 minutes). Returning
	// codes.Unavailable forces clients to retry; momentary 503s are preferred over
	// principal-revocation bypass.
	val, err := s.redis.Get(ctx, "revoke:user:"+claims.Subject).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		slog.ErrorContext(ctx, "principal revocation check failed; failing closed",
			"err", err, "subject", claims.Subject)
		return nil, status.Error(codes.Unavailable, "principal revocation check unavailable")
	}
	if val != "" {
		return nil, status.Error(codes.Unauthenticated, "principal revoked")
	}

	return &claims, nil
}

// RevokeToken stores the token's JTI in Redis so ValidateToken rejects it.
//
// SEC-005: The Redis TTL must equal the token's remaining lifetime — not a fixed value.
// This way the revocation entry expires exactly when the JWT itself would expire,
// preventing both memory leaks (TTL too long) and revocation bypass (TTL too short).
// ValidateToken relies on this coupling to avoid a separate GC process for stale entries.
func (s *Service) RevokeToken(ctx context.Context, claims *Claims) error {
	// Compute remaining lifetime from the token's own expiry claim.
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		// Token already expired — no need to store a revocation entry; it is
		// already invalid and any existing Redis entry will have auto-expired.
		return nil
	}
	// Store the JTI with a TTL equal to the remaining token lifetime so the
	// entry disappears exactly when the token would naturally cease to be valid.
	return s.redis.Set(ctx, revokedKey(claims.ID), "1", ttl).Err()
}

// RefreshToken validates an existing, non-expired JWT and issues a replacement
// with a fresh JTI and a new 300-second lifetime. The original token's JTI is
// revoked in Redis so it cannot be replayed after the refresh succeeds.
//
// Only valid, non-expired tokens may be refreshed — callers that present an
// expired token receive ErrInvalidCredentials so the HTTP handler can return
// 401 without leaking why validation failed.
func (s *Service) RefreshToken(ctx context.Context, tokenStr string) (string, error) {
	// ValidateToken parses the JWT, verifies the RS256 signature, checks the
	// exp claim (rejects expired tokens), and consults the Redis revocation list.
	claims, err := s.ValidateToken(ctx, tokenStr)
	if err != nil {
		return "", ErrInvalidCredentials
	}

	// Issue a new token carrying the same subject, tenant, roles, and
	// global-admin flag, but with a fresh JTI and updated iat/exp. Access
	// scopes are not carried over for session tokens (login flow) because they
	// are issued without explicit scopes (nil); Docker-scoped tokens are
	// short-lived and are never refreshed this way.
	// IsGlobalAdmin is carried forward from the original claims — the DB value
	// may have changed between issue and refresh, but the TTL is 300s so the
	// staleness window is the same as for Roles. A flag revocation takes effect
	// on the next login, like role removals.
	// PrincipalKind is carried forward — refresh must not mint a new kind. A
	// service-account JWT refreshes to another service-account JWT.
	newToken, err := s.IssueToken(ctx, claims.Subject, claims.TenantID, claims.Access, claims.Roles, claims.IsGlobalAdmin, claims.PrincipalKind)
	if err != nil {
		return "", fmt.Errorf("issue refresh token: %w", err)
	}

	// Revoke the old JTI so the caller cannot use both the old and new token
	// simultaneously. A failure here is logged but does not prevent the refresh
	// from succeeding — the worst case is a brief window where both tokens are
	// valid, which is acceptable given the 300-second TTL.
	if revokeErr := s.RevokeToken(ctx, claims); revokeErr != nil {
		// Non-fatal: log the error but return the new token. The old token will
		// expire naturally within its remaining TTL (≤ 300s) even without an
		// explicit revocation entry in Redis.
		slog.WarnContext(ctx, "refresh: failed to revoke old token JTI",
			"jti", claims.ID,
			"error", revokeErr,
		)
	}

	return newToken, nil
}

// Login validates credentials, enforces account lockout, and issues a token.
// The user's RBAC role names and global-admin flag are loaded and embedded in
// the JWT so downstream services (and the frontend) can read them without an
// extra RPC.
func (s *Service) Login(ctx context.Context, tenantID uuid.UUID, username, password string) (string, error) {
	user, err := s.AuthenticateUser(ctx, tenantID, username, password)
	if err != nil {
		return "", err
	}
	// Load the user's role names from the role_assignments table; deduplicated
	// because a user can hold the same role across multiple scopes (e.g. admin
	// of two orgs). A failure here is non-fatal — log it and issue a token
	// without roles rather than blocking login on a transient DB error.
	roles := s.loadRoleNames(ctx, user.ID, user.TenantID)
	// user.IsGlobalAdmin comes from the scanned users.is_global_admin column
	// (migration 20260629000001). It is always fresh from the DB on the login
	// path so no separate lookup is needed.
	// user.Kind is forwarded as the JWT principal_kind claim — Login is the
	// human credential path (password), so in practice this is always
	// "human", but we forward the actual column to keep the contract tight.
	return s.IssueToken(ctx, user.ID.String(), user.TenantID.String(), nil, roles, user.IsGlobalAdmin, user.Kind)
}

// loadRoleNames returns the deduplicated, sorted role names the user holds in
// the tenant. Errors are logged and an empty slice is returned — the caller
// decides whether absence of roles is fatal.
func (s *Service) loadRoleNames(ctx context.Context, userID, tenantID uuid.UUID) []string {
	assignments, err := s.users.GetUserRoles(ctx, userID, tenantID)
	if err != nil {
		slog.WarnContext(ctx, "loadRoleNames: GetUserRoles failed", "user_id", userID, "error", err)
		return nil
	}
	seen := make(map[string]struct{}, len(assignments))
	roles := make([]string, 0, len(assignments))
	for _, a := range assignments {
		if _, ok := seen[a.RoleName]; ok {
			continue
		}
		seen[a.RoleName] = struct{}{}
		roles = append(roles, a.RoleName)
	}
	return roles
}

// CreateUser hashes the password and persists the user record.
//
// REM-018: displayName is non-optional on the public POST /api/v1/users path
// — the handler validates it before calling here. Internal callers that
// don't have a display_name yet (test fixtures, legacy provisioning paths)
// may pass empty string; validation is skipped in that case and the column
// stores NULL via NULLIF inside repository.Create.
func (s *Service) CreateUser(ctx context.Context, tenantID uuid.UUID, username, email, displayName, password string) (*repository.User, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if displayName != "" {
		if err := validateDisplayName(displayName); err != nil {
			return nil, err
		}
	}
	hash, err := argon2pkg.Hash(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return s.users.Create(ctx, repository.CreateUserRequest{
		TenantID:     tenantID,
		Username:     username,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
	})
}

// CreateAPIKey generates a random secret, stores its argon2id hash, and returns
// the raw secret. The raw secret is never stored and cannot be recovered later.
func (s *Service) CreateAPIKey(ctx context.Context, tenantID, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (key *repository.APIKey, rawSecret string, err error) {
	raw := make([]byte, rawSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate secret: %w", err)
	}
	rawSecret = hex.EncodeToString(raw) // 64-char lowercase hex

	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		return nil, "", fmt.Errorf("hash secret: %w", err)
	}

	// Default nil scopes to an explicit empty slice so pgx serialises it
	// as `'{}'` instead of SQL NULL. The api_keys.scopes column is
	// NOT NULL DEFAULT '{}'; a nil slice from the JSON body (the frontend
	// never sends `scopes` because the dialog has no UI for it yet) was
	// hitting the constraint with a generic "internal error" — the
	// handler logs the cause now (see http.go) so a recurrence surfaces.
	if scopes == nil {
		scopes = []string{}
	}
	key, err = s.apiKeys.Create(ctx, repository.CreateAPIKeyRequest{
		TenantID:  tenantID,
		UserID:    &userID, // human-owned key — ServiceAccountID stays nil
		Name:      name,
		KeyHash:   hash,
		KeyPrefix: rawSecret[:12], // first 12 chars for display, not a secret
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, "", err
	}
	return key, rawSecret, nil
}

// ValidateAPIKeyOpts carries the arguments for ValidateAPIKey. Using a struct
// keeps the public API stable when optional fields (such as RequestTenantID) are
// added without breaking existing call sites.
type ValidateAPIKeyOpts struct {
	// KeyID is the api_keys.id UUID.
	KeyID uuid.UUID
	// RawSecret is the plaintext secret returned at key-creation time.
	RawSecret string
	// RequestTenantID is the tenant inferred from the gateway-injected
	// X-Tenant-ID header. When non-nil, SA keys are cross-tenant-checked against
	// the SA's stored TenantID (spec §5.4, security finding H1). For human keys
	// or when the header is absent, this field is nil.
	RequestTenantID *uuid.UUID
}

// ValidateAPIKey looks up the key by ID, checks expiry, verifies the secret
// hash, and returns a ValidatedKey describing the authenticated principal.
//
// For service-account keys it additionally:
//   - rejects the request when the SA is disabled (spec §5.5);
//   - enforces a cross-tenant guard: if RequestTenantID is set and does not
//     match the SA's tenant, it emits a best-effort audit event and returns
//     codes.Unauthenticated (spec §5.4, security finding H1);
//   - intersects the key's scopes with the SA's AllowedScopes and rejects the
//     key when the intersection is empty (spec §5.4).
//
// For human keys the behaviour is unchanged from the pre-T9 path.
//
// The legacy positional-arg wrapper validateAPIKeyByID is intentionally not
// exposed; all call sites must use this method.
func (s *Service) ValidateAPIKey(ctx context.Context, opts ValidateAPIKeyOpts) (*ValidatedKey, error) {
	// 1. Key lookup and baseline validation (shared between human and SA paths).
	//    Always re-read the row from the DB — even on a cache HIT — so that
	//    is_active / expires_at flips propagate within one TTL window without
	//    relying solely on the explicit invalidation hooks. The DB row read is
	//    sub-millisecond; what we save with the cache is the ~50-100 ms
	//    Argon2id verify, not the row fetch.
	key, err := s.apiKeys.GetByID(ctx, opts.KeyID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	// 1a. Cache fast path (REDESIGN-001 Phase 6.7).
	//
	// If we have previously verified this exact (key_id, secret) pair within
	// the last apiKeyCacheTTL, skip the Argon2id step. The cache key embeds
	// sha256(secret) so a stolen key_id alone cannot surface a HIT.
	//
	// We still apply every row-state and SA-state check below before
	// returning — the cache only attests to the secret match. A HIT lets us
	// pass the expiry / is_active / SA-disabled gates without paying Argon2.
	//
	// On any cache failure (Redis down, malformed entry) we silently fall
	// through to the cold path — the cache is an optimisation, never a gate.
	if cached, ok := s.getCachedValidatedKey(ctx, opts.KeyID, opts.RawSecret); ok {
		vk, hitErr := s.applyKeyChecksFromCache(ctx, key, cached, opts.RequestTenantID)
		// applyKeyChecksFromCache returns (nil, nil) only when the cached
		// payload does not match the row's owner shape (extremely unlikely
		// but defensible). In that one case we fall through to the cold
		// path so the request still completes correctly.
		if hitErr != nil || vk != nil {
			return vk, hitErr
		}
	}

	// 2. Cold path: verify the secret FIRST (before expiry / is_active checks)
	// so an attacker who guesses or knows a key id cannot distinguish "wrong
	// secret" (fast) from "expired" (also fast) by timing. argon2 is
	// intentionally slow (~100ms); making every reject path pay that cost
	// neutralises the oracle. PENTEST-004 applies the same pattern to
	// AuthenticateUser.
	ok, err := argon2pkg.Verify(opts.RawSecret, key.KeyHash)
	if err != nil {
		return nil, fmt.Errorf("verify key: %w", err)
	}
	if !ok {
		// Never write a negative cache entry — every wrong-secret attempt
		// must continue to pay the full Argon2 cost so brute-force attackers
		// cannot mass-test cheaply (Phase 6.7 security invariant).
		return nil, ErrInvalidCredentials
	}

	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return nil, ErrKeyExpired
	}
	if !key.IsActive {
		// Revoked keys are treated the same as invalid credentials to avoid
		// distinguishing between "never existed" and "actively revoked".
		return nil, ErrInvalidCredentials
	}

	// 3. Branch on owner type. Each successful branch additionally writes the
	// result to the cache so the next call within apiKeyCacheTTL bypasses
	// Argon2. The write is best-effort: a Redis failure logs at WARN and
	// continues (the next call simply re-pays the Argon2 cost).
	switch {
	case key.ServiceAccountID != nil:
		// SA-owned key: load the SA, apply disable check, cross-tenant guard,
		// and scope intersection.
		vk, err := s.validateSAKey(ctx, key, opts.RequestTenantID)
		if err == nil && vk != nil {
			s.putCachedValidatedKey(ctx, opts.KeyID, opts.RawSecret, vk)
		}
		return vk, err

	case key.UserID != nil:
		// Human-owned key: fire-and-forget last_used update, return immediately.
		s.touchLastUsedAsync(key.ID)
		vk := &ValidatedKey{
			UserID:          *key.UserID,
			TenantID:        key.TenantID,
			Access:          mapScopesToAccess(key.Scopes),
			PrincipalKind:   "human",
			EffectiveScopes: key.Scopes,
		}
		s.putCachedValidatedKey(ctx, opts.KeyID, opts.RawSecret, vk)
		return vk, nil

	default:
		// Both owner columns are NULL — violates the DB CHECK constraint but
		// defend at the application layer as well.
		return nil, fmt.Errorf("api_key %s has null owner on both sides — database constraint violated", key.ID)
	}
}

// applyKeyChecksFromCache runs every row-state / SA-state gate that the cold
// path runs after a successful Argon2 verify, using a previously cached
// identity instead of re-deriving it from the DB. Returns a ValidatedKey
// when every gate passes, or an error matching the cold path's behaviour
// (ErrKeyExpired, ErrInvalidCredentials, ErrAccountDisabled, etc.).
//
// Returns (nil, nil) only when the cached payload's owner shape does not
// match the row (e.g. cache built for a human key but the row has since
// been altered to look SA-owned — extremely unlikely but defensible).
// The caller falls through to the cold path on a (nil, nil) return.
//
// This is the central "stale cache cannot outlive revocation" enforcement
// point: even though the secret-match has been proven by the cache HIT,
// every other gate runs from the fresh DB row.
func (s *Service) applyKeyChecksFromCache(ctx context.Context, key *repository.APIKey, cached *cachedValidatedKey, requestTenantID *uuid.UUID) (*ValidatedKey, error) {
	// Row-state gates — identical ordering to the cold path so behaviour is
	// observably the same regardless of whether the request hit the cache.
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return nil, ErrKeyExpired
	}
	if !key.IsActive {
		return nil, ErrInvalidCredentials
	}

	switch {
	case key.ServiceAccountID != nil:
		// SA branch: rerun the SA disable / cross-tenant / scope-intersection
		// checks from the live SA row. The cache supplied EffectiveScopes,
		// but we deliberately do not trust it — the SA's AllowedScopes may
		// have been narrowed since the cache was written, in which case the
		// effective set must shrink to match.
		if s.serviceAccounts == nil {
			return nil, fmt.Errorf("%w: service-account key support requires a ServiceAccountRepo", ErrInvalidCredentials)
		}
		sa, err := s.serviceAccounts.Get(ctx, *key.ServiceAccountID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrInvalidCredentials
			}
			return nil, fmt.Errorf("lookup service account: %w", err)
		}
		if sa.DisabledAt != nil {
			return nil, ErrAccountDisabled
		}
		if requestTenantID != nil && *requestTenantID != sa.TenantID {
			s.emitCrossTenantAttempt(ctx, sa, key, requestTenantID)
			return nil, ErrInvalidCredentials
		}
		eff := intersectScopes(key.Scopes, sa.AllowedScopes)
		if len(eff) == 0 {
			return nil, fmt.Errorf("%w: all key scopes removed from SA allowlist; rotate key", ErrInvalidCredentials)
		}
		s.touchLastUsedAsync(key.ID)
		return &ValidatedKey{
			UserID:           sa.ShadowUserID,
			TenantID:         sa.TenantID,
			Access:           mapScopesToAccess(eff),
			PrincipalKind:    "service_account",
			ServiceAccountID: &sa.ID,
			EffectiveScopes:  eff,
		}, nil

	case key.UserID != nil:
		// Human branch: row already passed expiry + active checks above; no
		// further state to consult. Assemble from the DB row (not the cache)
		// so a stale EffectiveScopes — for example if an operator narrowed
		// the key's scopes after the cache entry was written — cannot leak.
		_ = cached
		s.touchLastUsedAsync(key.ID)
		return &ValidatedKey{
			UserID:          *key.UserID,
			TenantID:        key.TenantID,
			Access:          mapScopesToAccess(key.Scopes),
			PrincipalKind:   "human",
			EffectiveScopes: key.Scopes,
		}, nil

	default:
		// Cached entry refers to a row that now has neither owner set —
		// fall through to the cold path so the regular DB-constraint
		// violation error message fires.
		return nil, nil
	}
}

// validateSAKey handles the service-account branch of ValidateAPIKey.
// It is extracted as a separate method to keep ValidateAPIKey readable.
func (s *Service) validateSAKey(ctx context.Context, key *repository.APIKey, requestTenantID *uuid.UUID) (*ValidatedKey, error) {
	// SA repo may be nil if the service was constructed without one (e.g. in
	// legacy test fixtures). Fail with Unimplemented rather than panic.
	if s.serviceAccounts == nil {
		return nil, fmt.Errorf("%w: service-account key support requires a ServiceAccountRepo", ErrInvalidCredentials)
	}

	sa, err := s.serviceAccounts.Get(ctx, *key.ServiceAccountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// SA deleted after key was issued — treat as invalid.
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup service account: %w", err)
	}

	// Spec §5.5: reject calls when the SA is disabled.
	if sa.DisabledAt != nil {
		return nil, ErrAccountDisabled
	}

	// Spec §5.4 / security finding H1: cross-tenant guard.
	// If the gateway injected a tenant header, verify it matches the SA's own
	// tenant before proceeding. Emit a best-effort audit event for forensics.
	if requestTenantID != nil && *requestTenantID != sa.TenantID {
		s.emitCrossTenantAttempt(ctx, sa, key, requestTenantID)
		return nil, ErrInvalidCredentials
	}

	// Scope intersection: the effective scopes are the key's scopes intersected
	// with the SA's current AllowedScopes. An empty intersection means the SA
	// admin has removed all of the key's scopes — reject and hint at remediation.
	eff := intersectScopes(key.Scopes, sa.AllowedScopes)
	if len(eff) == 0 {
		return nil, fmt.Errorf("%w: all key scopes removed from SA allowlist; rotate key", ErrInvalidCredentials)
	}

	// Fire-and-forget last_used writeback (same as human path).
	s.touchLastUsedAsync(key.ID)

	return &ValidatedKey{
		UserID:           sa.ShadowUserID,
		TenantID:         sa.TenantID,
		Access:           mapScopesToAccess(eff),
		PrincipalKind:    "service_account",
		ServiceAccountID: &sa.ID,
		EffectiveScopes:  eff,
	}, nil
}

// emitCrossTenantAttempt records a best-effort audit event for a cross-tenant
// API key use attempt. Errors are logged and swallowed so the auth response
// latency is unaffected by a slow or unavailable audit backend.
func (s *Service) emitCrossTenantAttempt(ctx context.Context, sa *repository.ServiceAccount, key *repository.APIKey, claimedTenant *uuid.UUID) {
	if s.audit == nil {
		return
	}
	ev := AuditEvent{
		Action:  "pentest.cross_tenant_attempt",
		ActorID: sa.ShadowUserID.String(),
		Fields: map[string]any{
			"service_account_id": sa.ID.String(),
			"key_id":             key.ID.String(),
			"claimed_tenant":     claimedTenant.String(),
			"actual_tenant":      sa.TenantID.String(),
		},
	}
	if err := s.audit.Emit(ctx, ev); err != nil {
		slog.WarnContext(ctx, "auth: cross-tenant audit emit failed",
			"sa_id", sa.ID,
			"err", err,
		)
	}
}

// touchLastUsedAsync fires a background goroutine to update last_used_at. It
// uses a detached context with a 5-second timeout so that a slow or unavailable
// database never delays the auth response. Errors are silently discarded.
func (s *Service) touchLastUsedAsync(keyID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.apiKeys.TouchLastUsed(ctx, keyID)
	}()
}

// intersectScopes returns the elements of a that are also in b. Order is
// preserved (follows a's order). The result is always a non-nil slice; it may
// be empty when there is no overlap.
func intersectScopes(a, b []string) []string {
	allowed := make(map[string]struct{}, len(b))
	for _, s := range b {
		allowed[s] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if _, ok := allowed[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// mapScopesToAccess converts a flat scope list to a single wildcard
// RepositoryAccess entry. This mirrors the scopesToProto helper in the handler
// layer and is a Sprint 1 simplification; full scope-to-resource mapping ships
// in a later task.
func mapScopesToAccess(scopes []string) []RepositoryAccess {
	if len(scopes) == 0 {
		return nil
	}
	return []RepositoryAccess{{
		Type:    "repository",
		Name:    "*",
		Actions: scopes,
	}}
}

// AuthenticateUser validates credentials and returns the authenticated user
// without issuing a token. It enforces account lockout so callers can then
// issue a custom-scoped token (e.g. the Docker token endpoint).
//
// PENTEST-004: when the username does not exist, we still run a dummy Argon2id
// verify against a constant hash so the response time is indistinguishable
// from the known-user wrong-password path. Without this, an attacker could
// enumerate valid usernames by measuring the ~100 ms gap that Argon2id verify
// adds — known users take much longer than unknown users.
func (s *Service) AuthenticateUser(ctx context.Context, tenantID uuid.UUID, username, password string) (*repository.User, error) {
	user, err := s.users.GetByUsername(ctx, tenantID, username)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Burn the same Argon2id work the happy path would. We discard
			// the verify result; the goal is purely timing equalization.
			_, _ = argon2pkg.Verify(password, dummyArgonHash())
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !user.IsActive {
		// PENTEST-005: AuthenticateUser still distinguishes these states via
		// typed errors so handlers can audit-log the cause server-side, but
		// HTTP handlers MUST collapse all of these to a single 401 response
		// to deny enumeration through status-code differences.
		return nil, ErrAccountDisabled
	}
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		return nil, ErrAccountLocked
	}

	ok, err := argon2pkg.Verify(password, user.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		count, ferr := s.users.RecordFailedLogin(ctx, user.ID)
		if ferr == nil && count >= maxFailedLogins {
			lockUntil := time.Now().Add(lockoutDuration)
			_ = s.users.LockUntil(ctx, user.ID, lockUntil)
		}
		return nil, ErrInvalidCredentials
	}

	_ = s.users.ResetFailedLogins(ctx, user.ID)
	return user, nil
}

// dummyArgonHash returns a constant Argon2id hash of a throwaway password.
// Verifying any user-supplied password against this hash takes the same
// wall-clock time as verifying against a real user's password hash. Lazily
// generated once per process to avoid paying Argon2's cost at package init.
var (
	dummyArgonHashOnce sync.Once
	dummyArgonHashVal  string
)

func dummyArgonHash() string {
	dummyArgonHashOnce.Do(func() {
		// Use a long, fixed string so the same hash parameters (m, t, p) as
		// real user hashes are exercised; argon2pkg.Hash applies the standard
		// parameters configured for this service.
		h, err := argon2pkg.Hash("dummy-password-for-timing-mitigation-aaaa")
		if err != nil {
			// Hash failure here is exceptional (would happen at every login
			// path) — emit a warning and fall back to an empty string. Verify
			// against "" returns quickly, which weakens the mitigation but
			// keeps the service running.
			slog.Warn("auth: failed to generate dummy argon2 hash; PENTEST-004 mitigation degraded", "error", err)
			return
		}
		dummyArgonHashVal = h
	})
	return dummyArgonHashVal
}

// GetUserByID returns the user record for the given ID.
func (s *Service) GetUserByID(ctx context.Context, id uuid.UUID) (*repository.User, error) {
	return s.users.GetByID(ctx, id)
}

// CountTenantUsers returns the number of users in the tenant (FE-API-028).
// Surfaced on the platform-admin tenant-detail card. Inactive accounts are
// included on purpose — operators want total headcount, not concurrent users.
func (s *Service) CountTenantUsers(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return s.users.CountByTenant(ctx, tenantID)
}

// LookupUsernames batch-resolves user_ids within a tenant to (username,
// display_name) tuples (REM-018-followup). Unknown ids are dropped from the
// returned slice — callers iterate by input set and treat absence as
// "render the UUID / system fallback".
func (s *Service) LookupUsernames(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]repository.UserSummary, error) {
	return s.users.LookupByIDs(ctx, tenantID, ids)
}

// GetUserRoles returns all role assignments for a user within a tenant.
func (s *Service) GetUserRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]repository.RoleAssignment, error) {
	return s.users.GetUserRoles(ctx, userID, tenantID)
}

// SetGlobalAdmin updates users.is_global_admin for the given user. Only callers
// that are themselves global admins may invoke this; the bootstrap CLI writes
// the flag directly via SQL on first run.
//
// Audits via rbac.role_granted / rbac.role_revoked routing keys with the
// synthetic role name "global_admin" so the audit catalogue (Phase 6.3)
// surfaces the change in /activity.
func (s *Service) SetGlobalAdmin(ctx context.Context, userID uuid.UUID, granted bool, actorID uuid.UUID) error {
	if err := s.users.SetGlobalAdmin(ctx, userID, granted); err != nil {
		return err
	}
	// Emit a best-effort audit event so the change appears in /activity.
	// A publish failure is logged but does not roll back the DB write — the
	// flag is already set; the audit gap is preferable to leaving the flag
	// in an inconsistent state.
	if s.audit != nil {
		action := "rbac.role_granted"
		if !granted {
			action = "rbac.role_revoked"
		}
		ev := AuditEvent{
			Action:  action,
			ActorID: actorID.String(),
			Fields: map[string]any{
				"role":    "global_admin",
				"user_id": userID.String(),
				"granted": granted,
			},
		}
		if err := s.audit.Emit(ctx, ev); err != nil {
			slog.WarnContext(ctx, "SetGlobalAdmin: audit emit failed",
				"user_id", userID,
				"granted", granted,
				"err", err,
			)
		}
	}
	return nil
}

// GrantRole creates a role assignment. The role is looked up by name.
func (s *Service) GrantRole(ctx context.Context, a repository.RoleAssignment) error {
	return s.users.GrantRole(ctx, a)
}

// RevokeRole deletes the role assignment with the given ID, scoped to the tenant.
func (s *Service) RevokeRole(ctx context.Context, assignmentID, tenantID uuid.UUID) error {
	return s.users.RevokeRole(ctx, assignmentID, tenantID)
}

// RevokeRoleScoped deletes the role assignment only when the scope matches the
// expected values (PENTEST-011). Empty expectedScopeType / expectedScopeValue
// disable the corresponding check, so passing both empty is equivalent to the
// plain RevokeRole call.
func (s *Service) RevokeRoleScoped(ctx context.Context, assignmentID, tenantID uuid.UUID, expectedScopeType, expectedScopeValue string) error {
	return s.users.RevokeRoleScoped(ctx, assignmentID, tenantID, expectedScopeType, expectedScopeValue)
}

// ListMembers returns the enriched membership list for the given tenant scope.
// Each Member carries the principal kind, display name, and — for service-account
// principals — the service_accounts.id so callers can link back to the SA.
func (s *Service) ListMembers(ctx context.Context, tenantID uuid.UUID, scopeType, scopeValue string) ([]repository.Member, error) {
	return s.users.ListMembers(ctx, tenantID, scopeType, scopeValue)
}

// ListAPIKeys returns all active API keys owned by the given user.
func (s *Service) ListAPIKeys(ctx context.Context, userID uuid.UUID) ([]*repository.APIKey, error) {
	return s.apiKeys.ListByUser(ctx, userID)
}

// DeleteAPIKey soft-deletes an API key owned by the given user.
//
// REDESIGN-001 Phase 6.7: after a successful soft-delete, the API-key
// validation cache for this keyID is invalidated so a CI bot still holding
// the secret cannot keep authenticating off a cached HIT for the rest of the
// TTL window. The HIT path also re-reads the row's is_active flag as a
// backstop, so a failure to invalidate cleanly is non-fatal — the worst
// case is up to apiKeyCacheTTL of staleness, bounded by the row check.
func (s *Service) DeleteAPIKey(ctx context.Context, keyID, userID uuid.UUID) error {
	if err := s.apiKeys.Delete(ctx, keyID, userID); err != nil {
		return err
	}
	s.InvalidateAPIKeyCache(ctx, keyID)
	return nil
}

// JWKS returns the public key set for JWT verification by other services.
func (s *Service) JWKS() JWKSResponse {
	return JWKSResponse{Keys: []JWK{rsaToJWK(s.pubKey, s.keyID)}}
}

// CheckIPRateLimit returns ErrRateLimited when the given IP has exceeded
// maxAuthFailures failed attempts in the past minute.
func (s *Service) CheckIPRateLimit(ctx context.Context, ip string) error {
	key := "rl:auth:fail:" + ip
	count, err := s.redis.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err // Redis failure — fail open (do not block legitimate users)
	}
	if count >= 10 {
		return ErrRateLimited
	}
	return nil
}

// ErrRateLimited is returned when per-IP auth failure rate limit is exceeded.
var ErrRateLimited = errors.New("too many failed authentication attempts")

// RecordAuthFailure increments the per-IP failure counter with a 60-second window.
func (s *Service) RecordAuthFailure(ctx context.Context, ip string) {
	key := "rl:auth:fail:" + ip
	pipe := s.redis.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 60*time.Second)
	_, _ = pipe.Exec(ctx)
}

// revokedKey returns the Redis key used to track JTI revocation.
func revokedKey(jti string) string { return "jwt:revoked:" + jti }

func (s *Service) isRevoked(ctx context.Context, jti string) (bool, error) {
	_, err := s.redis.Get(ctx, revokedKey(jti)).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	return err == nil, err
}

// rsaToJWK encodes an RSA public key as a JSON Web Key.
func rsaToJWK(pub *rsa.PublicKey, kid string) JWK {
	return JWK{
		Kty: "RSA",
		Use: "sig",
		Kid: kid,
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   encodeExponent(pub.E),
	}
}

// encodeExponent encodes the RSA public exponent as big-endian base64url with
// leading zero bytes stripped (per RFC 7517).
func encodeExponent(e int) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(e))
	i := 0
	for i < len(b)-1 && b[i] == 0 {
		i++
	}
	return base64.RawURLEncoding.EncodeToString(b[i:])
}

func parsePrivateKey(b64 string) (*rsa.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fall back to PKCS1 format
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}

func parsePublicKey(b64 string) (*rsa.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in public key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not RSA")
	}
	return rsaKey, nil
}
