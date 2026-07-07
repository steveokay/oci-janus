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
	"github.com/steveokay/oci-janus/libs/observability/metrics"
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

// AccountLockedError is a locked-account failure that carries the unlock time
// so the /login handler can tell the user when to retry. It unwraps to
// ErrAccountLocked, so existing `errors.Is(err, ErrAccountLocked)` checks (e.g.
// the audit-log classifier and the /token handler's generic-401 path) keep
// working unchanged; only callers that want the timestamp use errors.As.
type AccountLockedError struct {
	// Until is when the lockout expires (users.locked_until).
	Until time.Time
}

func (e *AccountLockedError) Error() string { return ErrAccountLocked.Error() }

// Unwrap lets errors.Is(err, ErrAccountLocked) match this typed error.
func (e *AccountLockedError) Unwrap() error { return ErrAccountLocked }

const (
	tokenTTL        = 5 * time.Minute
	lockoutDuration = 15 * time.Minute
	maxFailedLogins = 5
	// rawSecretLen is the number of random bytes for API key secrets.
	rawSecretLen = 32
)

// MFA typed-token constants. Challenge/setup tokens carry a non-empty Typ and
// a dedicated audience so they can never be accepted where an access token is
// expected (ValidateToken rejects any token with a non-empty Typ).
const (
	tokenTypeMFAChallenge = "mfa_challenge"
	tokenTypeMFASetup     = "mfa_setup"
	mfaChallengeTTL       = 5 * time.Minute
	mfaSetupTTL           = 15 * time.Minute
	audienceMFAChallenge  = "registry-auth-mfa"
	audienceMFASetup      = "registry-auth-mfa-setup"
	// maxMFAChallengeAttempts caps OTP/backup-code submissions per challenge
	// token (keyed on its jti) so a single stateless 5-minute challenge cannot be
	// replayed for unbounded guessing (SEC-079). The per-account lockout is the
	// hard backstop; this is defence-in-depth bound to the token itself.
	maxMFAChallengeAttempts = 5
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
	// Source identifies how the token was issued: "" (default = password
	// login or API-key dispatch) or "workload_oidc" (FUT-001 federated
	// workload identity). Downstream audit + analytics can group sessions
	// by origin without parsing the trust_id. Empty for legacy tokens.
	Source string `json:"source,omitempty"`
	// TrustID is the oidc_trust_configs.id that minted this token (FUT-001).
	// Empty unless Source == "workload_oidc". Lets operators trace a
	// running CI job back to the trust config that approved it.
	TrustID string `json:"trust_id,omitempty"`
	// Typ discriminates non-access tokens. Empty = normal access token.
	// mfa_challenge / mfa_setup tokens are refused by ValidateToken.
	Typ string `json:"typ,omitempty"`
	// Amr records the authentication methods that produced this token
	// (["pwd"], ["pwd","otp"], ["sso"]). Recorded for audit + future step-up;
	// RefreshToken copies it verbatim and never upgrades it.
	Amr []string `json:"amr,omitempty"`
	// Sid is the stable session id (user_sessions.sid) for interactive logins.
	// Unlike the JTI, it is preserved verbatim across RefreshToken so a session
	// can be listed and revoked (revoke:sid gate in ValidateToken) even though
	// the JTI rotates every 300s. Empty for non-session tokens (OCI /v2 Docker
	// tokens, workload OIDC, API-key dispatch).
	Sid string `json:"sid,omitempty"`
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
	// keys is the multi-key signing + validation ring (Phase 6.5). The ring
	// holds every RS256 key the service knows about; signing uses the kid
	// nominated at construction time, validation uses the kid carried in the
	// JWT header (falling back to a try-every-key sweep for legacy tokens
	// minted before the kid header was stamped).
	//
	// Replaces the pre-Phase-6.5 single-key trio (privKey/pubKey/keyID). The
	// migration is internal-only; callers of New / NewWithFakes are
	// unchanged for the single-key path (the helpers wrap the single key
	// into a 1-element ring transparently).
	keys *keyRing
	// tokenPolicy is the FUT-003 policy repo used by CreateAPIKey to enforce
	// max_ttl_days + stamp rotation_due_at on new keys. May be nil in test
	// fixtures — when nil, CreateAPIKey behaves as if no policy is set (the
	// grandfathering / legacy behaviour).
	tokenPolicy tokenPolicyReader
	// lastUsed is the FUT-003 Redis-debounced last_used_at updater. When
	// nil (legacy fakes), ValidateAPIKey falls back to the pre-FUT-003
	// touchLastUsedAsync path so old tests keep passing.
	lastUsed *lastUsedUpdater
	// mfaKEK is the 32-byte AES-256 key-encryption key used to encrypt TOTP
	// secrets at rest (users.mfa_secret_enc). Wired at startup from the
	// decoded MFA_SECRET_KEY_HEX via SetMFAKEK. Never logged.
	mfaKEK []byte
	// sessions is the user_sessions repository backing the active-session list
	// (sid lifecycle: issue → list → revoke). Wired via SetSessionRepo. When
	// nil, sessions are disabled and issueSessionToken mints plain (no-sid)
	// tokens so every login/MFA/SSO test that does not wire a session repo
	// keeps passing.
	sessions sessionRepo
	// sessionActive is the Redis-debounced user_sessions.last_active_at updater
	// fired from the ValidateToken hot path. When nil (legacy fakes / dev stacks
	// without the wiring), ValidateToken skips the last_active bump entirely —
	// last_active is telemetry, so its absence never blocks validation.
	sessionActive *sessionActiveUpdater
	// mfaIssuer is the otpauth:// issuer label embedded in enrolment URIs — the
	// name an authenticator app shows next to the account (e.g. "oci-janus").
	// Defaulted to "oci-janus" in every constructor.
	mfaIssuer string
	// mfaKEKVersion is the KEK generation stamped on freshly-encrypted MFA
	// secrets (users.mfa_secret_kek_version). Defaulted to defaultMFAKEKVersion
	// in every constructor; server startup overrides it from
	// MFA_SECRET_KEK_VERSION via SetMFAKEKVersion so new enrolments track a
	// rotated KEK.
	mfaKEKVersion int16
	// nowFn returns the current wall clock. Overridable so enrolment/login MFA
	// tests can pin the TOTP time step deterministically. nil ⇒ time.Now
	// (resolved in the now() helper).
	nowFn func() time.Time
}

// defaultMFAIssuer is the otpauth:// issuer label used when a constructor does
// not override it. Kept as a single constant so every constructor agrees.
const defaultMFAIssuer = "oci-janus"

// tokenPolicyReader is the narrow interface Service uses to consult the
// workspace token policy on CreateAPIKey. Small so tests can supply a fake
// without a real DB pool.
type tokenPolicyReader interface {
	GetOrDefault(ctx context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error)
}

// New constructs a Service by parsing the base64-encoded PEM keys from config.
// sa and audit may be nil; if nil, the service-account branch of ValidateAPIKey
// returns codes.Unimplemented and cross-tenant audit emission is skipped.
//
// This single-key constructor builds a 1-element key ring for backwards
// compatibility (Phase 6.5). To use the multi-key ring, build a *keyRing
// directly via loadKeyRingFromDir and pass it to NewWithKeyRing.
func New(
	users *repository.UserRepository,
	apiKeys *repository.APIKeyRepository,
	sa *repository.ServiceAccountRepo,
	audit AuditEmitter,
	rdb *redis.Client,
	privKeyB64, pubKeyB64, keyID string,
) (*Service, error) {
	ring, err := singleKeyRingFromB64(privKeyB64, pubKeyB64, keyID)
	if err != nil {
		return nil, err
	}
	var saR saRepo
	if sa != nil {
		saR = sa
	}
	s := &Service{
		users:           users,
		apiKeys:         apiKeys,
		serviceAccounts: saR,
		audit:           audit,
		redis:           rdb,
		keys:            ring,
		mfaIssuer:       defaultMFAIssuer,
		mfaKEKVersion:   defaultMFAKEKVersion,
	}
	// Auto-wire the FUT-003 debounced last_used_at updater when the caller
	// passed a non-nil api-key repo (production path). Tests that construct
	// via NewWithFakes drive the updater through the setter.
	if apiKeys != nil {
		s.lastUsed = newLastUsedUpdater(rdb, apiKeys, slog.Default())
	}
	return s, nil
}

// NewWithKeyRing constructs a Service from an already-built multi-key ring
// (Phase 6.5). Server startup uses this path when JWT_KEY_RING_PATH is set,
// loading every PEM in the directory into the ring before calling here.
//
// The single-key wrappers (New, NewWithFakes) remain the entry point for
// callers that only know about one key — they internally wrap their PEMs
// into a 1-element ring and call newKeyRing, so the in-memory shape is the
// same as the multi-key path.
func NewWithKeyRing(
	users *repository.UserRepository,
	apiKeys *repository.APIKeyRepository,
	sa *repository.ServiceAccountRepo,
	audit AuditEmitter,
	rdb *redis.Client,
	ring *keyRing,
) (*Service, error) {
	if ring == nil {
		return nil, errors.New("auth: key ring is required")
	}
	var saR saRepo
	if sa != nil {
		saR = sa
	}
	s := &Service{
		users:           users,
		apiKeys:         apiKeys,
		serviceAccounts: saR,
		audit:           audit,
		redis:           rdb,
		keys:            ring,
		mfaIssuer:       defaultMFAIssuer,
		mfaKEKVersion:   defaultMFAKEKVersion,
	}
	if apiKeys != nil {
		s.lastUsed = newLastUsedUpdater(rdb, apiKeys, slog.Default())
	}
	return s, nil
}

// singleKeyRingFromB64 wraps a single base64-encoded PEM key pair into a
// 1-element keyRing. Used by both New and NewWithFakes so the single-key
// path stays a thin shim over the multi-key ring without forcing callers to
// learn the new type.
func singleKeyRingFromB64(privKeyB64, pubKeyB64, keyID string) (*keyRing, error) {
	if keyID == "" {
		return nil, errors.New("auth: keyID is required for single-key ring")
	}
	priv, err := parsePrivateKey(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	pub, err := parsePublicKey(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT public key: %w", err)
	}
	ring, err := newKeyRing([]signingKey{{
		kid:        keyID,
		privateKey: priv,
		publicKey:  pub,
	}}, keyID)
	if err != nil {
		// Should not happen for a single well-formed entry; surface anyway
		// so any future invariant break is loud.
		return nil, fmt.Errorf("build single-key ring: %w", err)
	}
	return ring, nil
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
// amr records the authentication methods that produced the token (e.g.
// ["pwd"], ["pwd","otp"], ["sso"]). It is embedded verbatim in the Amr claim
// for audit + future step-up decisions; RefreshToken forwards it unchanged.
// sid is the stable session id (user_sessions.sid) for interactive logins; it
// is embedded in the Sid claim and preserved verbatim across RefreshToken so a
// session survives JTI rotation. Pass "" for non-session tokens (OCI Docker
// tokens, workload OIDC, API-key dispatch).
func (s *Service) IssueToken(ctx context.Context, userID, tenantID string, access []RepositoryAccess, roles []string, isGlobalAdmin bool, principalKind string, amr []string, sid string) (string, error) {
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
		Amr:           amr,
		// Sid ties this token to a listable/revocable session row; preserved
		// across refresh even as the JTI rotates. Empty for non-session tokens.
		Sid: sid,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	// Phase 6.5 — stamp the kid header so validators can pick the right
	// public key from the ring without trying every one in turn. The kid
	// itself is non-sensitive (it appears in the JWKS document) so it is
	// safe to embed in the JWT header.
	signingKID, signingPriv := s.keys.signer()
	tok.Header["kid"] = signingKID
	signed, err := tok.SignedString(signingPriv)
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

// IssueWorkloadToken signs a 15-minute RS256 registry JWT for a FUT-001
// workload identity exchange. Sets source="workload_oidc" and trust_id to
// the matched trust so the audit trail and downstream services can
// distinguish workload-minted tokens from password-login tokens.
//
// The TTL is intentionally longer than tokenTTL (5 min) so a long-running
// CI job (build + push + scan) doesn't need to re-exchange on every step.
// 15 minutes is the GitHub Actions recommendation for OIDC-derived
// credentials. The JTI is registered in the same active-token set as
// password-login tokens so a SA disable revokes it immediately.
func (s *Service) IssueWorkloadToken(ctx context.Context, userID, tenantID, trustID string, access []RepositoryAccess) (string, error) {
	now := time.Now()
	const workloadTokenTTL = 15 * time.Minute
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "registry-auth",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{"registry-core"},
			ExpiresAt: jwt.NewNumericDate(now.Add(workloadTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
		TenantID:      tenantID,
		Access:        access,
		Roles:         nil,
		IsGlobalAdmin: false,
		PrincipalKind: "service_account",
		Source:        "workload_oidc",
		TrustID:       trustID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signingKID, signingPriv := s.keys.signer()
	tok.Header["kid"] = signingKID
	signed, err := tok.SignedString(signingPriv)
	if err != nil {
		return "", fmt.Errorf("sign workload token: %w", err)
	}
	if err := s.recordIssuedJTI(ctx, userID, claims.ID); err != nil {
		slog.WarnContext(ctx, "auth: failed to record workload JTI", "user_id", userID, "error", err)
	}
	return signed, nil
}

// WorkloadTokenLifetimeSeconds is the TTL in seconds returned in the
// OAuth-style ExchangeWorkloadToken response (expires_in). Centralised so
// the handler and the service agree on the value.
const WorkloadTokenLifetimeSeconds = 15 * 60

// ValidateToken parses and validates the token string, then checks the Redis
// revocation list. Returns the parsed claims on success.
//
// Phase 6.5 — multi-key validation:
//  1. If the JWT carries a `kid` header AND that kid is present in the ring,
//     verify with that key only. This is the fast, common path.
//  2. If the kid is missing or unknown, fall back to trying every key in the
//     ring. Tokens minted before the kid header was stamped (pre-Phase-6.5)
//     land on this path; a slog.Warn surfaces the fallback so operators can
//     spot stale token issuers during a rotation window. Once every issuer
//     stamps a kid, the warn is the operator's signal that someone is still
//     issuing legacy tokens and they can disable the fallback path (future
//     work).
//
// The fallback is bounded by the ring size (1–N keys, typically 2 during a
// rotation window) so the worst-case is N signature verifies — cheap
// compared to the Redis revocation check that follows.
func (s *Service) ValidateToken(ctx context.Context, tokenStr string) (*Claims, error) {
	// parseAndVerify does the signature + expiry + kid-ring work and returns
	// the validated claims before any revocation checks run.
	claims, err := s.parseAndVerify(tokenStr)
	if err != nil {
		return nil, err
	}
	// A challenge/setup token must never be accepted as an access token.
	// These tokens carry a non-empty Typ + a dedicated audience and are only
	// spendable via ValidateMFAToken; refusing them here means RefreshToken
	// (which calls ValidateToken) rejects them too.
	if claims.Typ != "" {
		return nil, ErrInvalidCredentials
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

	// Session revocation (revoke:sid): a listed session can be killed even though
	// the JTI rotates on every refresh. Fail-CLOSED on a Redis error, exactly
	// like the principal (revoke:user) check above — a Redis outage must not let
	// a revoked session keep validating.
	if claims.Sid != "" {
		sv, serr := s.redis.Get(ctx, sessionRevokeKey(claims.Sid)).Result()
		if serr != nil && !errors.Is(serr, redis.Nil) {
			slog.ErrorContext(ctx, "session revocation check failed; failing closed", "err", serr)
			return nil, status.Error(codes.Unavailable, "session revocation check unavailable")
		}
		if sv != "" {
			return nil, status.Error(codes.Unauthenticated, "session revoked")
		}
	}

	// Debounced last_active telemetry: bump user_sessions.last_active_at at most
	// once per session per minute. Fire-and-forget on a background context so a
	// client disconnect doesn't cancel the write, and never on the request's
	// critical path. A parse failure or unwired updater is a silent no-op.
	if claims.Sid != "" && s.sessionActive != nil {
		if sid, perr := uuid.Parse(claims.Sid); perr == nil {
			s.sessionActive.Touch(context.Background(), sid)
		}
	}

	return claims, nil
}

// parseAndVerify parses tokenStr, verifies its RS256 signature against the key
// ring, and enforces the standard registered claims (expiry). It returns the
// validated claims BEFORE any revocation checks — callers layer their own
// revocation / typ gates on top. Extracted from ValidateToken so the MFA
// typed-token validator can reuse the exact same signature-verification path
// without paying for the access-token revocation lookups.
//
// Phase 6.5 kid-ring behaviour (fast kid-targeted verify + full-ring fallback)
// is preserved verbatim from the original inline ValidateToken body.
func (s *Service) parseAndVerify(tokenStr string) (*Claims, error) {
	var claims Claims
	tok, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		// Look up by kid if the JWT supplied one.
		kid, _ := t.Header["kid"].(string)
		if kid != "" {
			if pub := s.keys.find(kid); pub != nil {
				return pub, nil
			}
			// Kid present but not in ring — signal jwt to try the
			// fallback path. We return the first key's public half so
			// jwt.ParseWithClaims has something to verify against; if it
			// fails (which is likely — the token was signed with a kid we
			// don't know), we re-try below against every other key.
			//
			// Returning an error here would short-circuit the whole call
			// before we get to try the rest of the ring, which is the
			// opposite of what we want.
			return s.keys.all()[0].publicKey, nil
		}
		// No kid in header — same fallback signal. Return the first key
		// and let the post-parse retry loop catch the mismatch.
		return s.keys.all()[0].publicKey, nil
	})
	if err != nil || !tok.Valid {
		// First-pass verify failed. Try every key in the ring in turn —
		// either the issuer used a kid we did not know about (rotation
		// race) or the JWT was minted before kids were stamped at all.
		//
		// The fallback re-uses the same Claims pointer so a successful
		// retry populates the same struct.
		recovered, recovErr := s.validateWithFallback(tokenStr, &claims)
		if recovErr != nil {
			// Preserve the original error in the wrap so the operator
			// log surface is unchanged from the pre-Phase-6.5 behaviour.
			return nil, fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
		}
		tok = recovered
		// A fallback success means either (a) a legacy token without a
		// kid, or (b) a token whose kid did not match any ring entry.
		// Both are operator-visible signals during a rotation. SEC-048
		// follow-up: bump `registry_auth_jwt_kid_fallback_total` with
		// the reason label so operators can alert on sustained-high
		// fallback rates without scraping logs.
		hdrKid, _ := tok.Header["kid"].(string)
		reason := "missing_kid"
		if hdrKid != "" {
			reason = "unknown_kid"
		}
		metrics.AuthJWTKidFallbackTotal.WithLabelValues(reason).Inc()
		// parseAndVerify has no ctx (it is a pure verify helper); the
		// fallback metric is the durable operator signal, so we increment
		// it here and drop the per-call slog line that ValidateToken used
		// to emit. Sustained fallback is visible via the metric label.
	}
	return &claims, nil
}

// issueTypedToken mints a short-lived RS256 token carrying a non-access `typ`
// and a dedicated audience, so it can never be accepted where an access token
// is expected. Used for the two-step-login challenge and forced-enrolment setup
// tokens. It deliberately carries no roles/access.
func (s *Service) issueTypedToken(userID, tenantID, typ, audience string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "registry-auth",
			Subject:   userID,
			Audience:  jwt.ClaimStrings{audience},
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
		TenantID: tenantID,
		Typ:      typ,
		// The password check has already succeeded by the time these tokens are
		// minted, so the sole authentication method so far is "pwd". The otp
		// factor is not added until the challenge is spent successfully.
		Amr: []string{"pwd"},
	}
	// Mirror IssueToken's signing: sign with the ring's current signer and stamp
	// the kid header so validators can pick the right public key without trying
	// every one in turn. The kid is non-sensitive (it appears in the JWKS doc).
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signingKID, signingPriv := s.keys.signer()
	tok.Header["kid"] = signingKID
	signed, err := tok.SignedString(signingPriv)
	if err != nil {
		return "", fmt.Errorf("sign %s token: %w", typ, err)
	}
	// Typed tokens are stateless and short-lived — no JTI is recorded in the
	// active-token set (they are validated via ValidateMFAToken, which does not
	// consult the revocation list).
	return signed, nil
}

// IssueMFAChallengeToken mints the 5-minute token returned after a correct
// password when MFA is enabled; it is spent at POST /login/mfa.
func (s *Service) IssueMFAChallengeToken(ctx context.Context, userID, tenantID string) (string, error) {
	return s.issueTypedToken(userID, tenantID, tokenTypeMFAChallenge, audienceMFAChallenge, mfaChallengeTTL)
}

// IssueMFASetupToken mints the 15-minute token returned when the require-MFA
// policy is on and the user is un-enrolled; it authorizes only enroll/verify.
func (s *Service) IssueMFASetupToken(ctx context.Context, userID, tenantID string) (string, error) {
	return s.issueTypedToken(userID, tenantID, tokenTypeMFASetup, audienceMFASetup, mfaSetupTTL)
}

// ValidateMFAToken parses + signature-verifies a token and requires its `typ`
// to equal wantTyp. It does NOT run the access-token revocation checks (these
// short-lived tokens are stateless). Returns the claims on success.
func (s *Service) ValidateMFAToken(ctx context.Context, tokenStr, wantTyp string) (*Claims, error) {
	claims, err := s.parseAndVerify(tokenStr)
	if err != nil {
		return nil, err
	}
	if claims.Typ != wantTyp {
		return nil, ErrInvalidCredentials
	}
	return claims, nil
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
	// Amr is copied verbatim — refresh preserves the original authentication
	// methods and never upgrades them (a refreshed token is not a fresh login).
	// Sid is carried forward so the session survives JTI rotation — the whole
	// point of the stable session id is that revoke:sid keeps working across
	// refreshes even though the JTI changes every 300s.
	newToken, err := s.IssueToken(ctx, claims.Subject, claims.TenantID, claims.Access, claims.Roles, claims.IsGlobalAdmin, claims.PrincipalKind, claims.Amr, claims.Sid)
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

// LoginResult is the outcome of a password login. Exactly one of the three
// states is set: an access Token, an MFA challenge (MFARequired +
// ChallengeToken), or a forced-enrolment setup (MFASetupRequired + SetupToken).
// The handler branches on these flags to shape the HTTP response.
type LoginResult struct {
	// Token is the full RS256 access token (amr=["pwd"]). Set only when MFA is
	// not required for this user.
	Token string
	// MFARequired is true when the user has MFA enabled and must complete the
	// second step at POST /api/v1/login/mfa. ChallengeToken carries the
	// short-lived typ=mfa_challenge token to spend there.
	MFARequired    bool
	ChallengeToken string
	// MFASetupRequired is true when the workspace policy forces MFA and this
	// human password user is not yet enrolled. SetupToken carries the
	// short-lived typ=mfa_setup token that authorizes enroll/verify.
	MFASetupRequired bool
	SetupToken       string
}

// Login validates credentials, enforces account lockout, and returns a
// LoginResult describing the next step. The three outcomes are:
//
//   - MFA enabled → an mfa_challenge token (two-step login); the caller spends
//     it at POST /login/mfa with an OTP or backup code.
//   - MFA required by policy but the user is un-enrolled → an mfa_setup token
//     (forced enrolment); the caller enrols before receiving an access token.
//   - Otherwise → a full access token, with the user's RBAC role names and
//     global-admin flag embedded so downstream services (and the frontend) can
//     read them without an extra RPC.
//
// meta carries the client IP + User-Agent captured at the HTTP edge; it is
// threaded into the no-MFA branch so a successful password login creates a
// listable/revocable session row (the active-session-list feature). The
// MFA-required and setup-required branches create no session — they return
// short-lived challenge/setup tokens, not access tokens.
func (s *Service) Login(ctx context.Context, tenantID uuid.UUID, username, password string, meta SessionMeta) (LoginResult, error) {
	user, err := s.AuthenticateUser(ctx, tenantID, username, password)
	if err != nil {
		return LoginResult{}, err
	}

	// MFA-enabled users never get an access token straight from Login: they
	// must spend a challenge token at POST /login/mfa (amr becomes ["pwd","otp"]
	// only after the OTP/backup code is verified there).
	mfaState, err := s.users.GetMFAState(ctx, user.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if mfaState.Enabled {
		ct, terr := s.IssueMFAChallengeToken(ctx, user.ID.String(), user.TenantID.String())
		if terr != nil {
			return LoginResult{}, terr
		}
		return LoginResult{MFARequired: true, ChallengeToken: ct}, nil
	}

	// Forced enrolment: the workspace token policy requires MFA and this human
	// password user has no factor yet. Hand back an mfa_setup token so the FE
	// can drive enrolment before the user holds an access token. The policy repo
	// is optional (nil in some test fixtures) — when unwired we skip the gate,
	// matching the pre-MFA behaviour. Service-account principals never reach this
	// branch on the password path (GetHumanByUsername filters them), but we still
	// guard on Kind=="human" so the policy can never force a non-human enrolment.
	if s.tokenPolicy != nil {
		policy, perr := s.tokenPolicy.GetOrDefault(ctx, user.TenantID)
		if perr == nil && policy.RequireMFA && user.Kind == "human" {
			stk, terr := s.IssueMFASetupToken(ctx, user.ID.String(), user.TenantID.String())
			if terr != nil {
				return LoginResult{}, terr
			}
			return LoginResult{MFASetupRequired: true, SetupToken: stk}, nil
		}
	}

	// No MFA in play — issue the full access token.
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
	// Login is the password credential path, so the authentication method is
	// always "pwd" (["pwd"] amr).
	// issueSessionToken mints a sid, persists the user_sessions row stamped with
	// the captured client meta, and embeds the sid in the JWT so the login can be
	// listed and revoked. When no session repo is wired it degrades to a plain
	// (no-sid) token — keeping existing login unit tests green.
	tok, terr := s.issueSessionToken(ctx, user.ID, user.TenantID, roles, user.IsGlobalAdmin, user.Kind, []string{"pwd"}, meta)
	if terr != nil {
		return LoginResult{}, terr
	}
	return LoginResult{Token: tok}, nil
}

// VerifyLoginMFA completes the two-step login: it validates the mfa_challenge
// token minted by Login, verifies the submitted OTP or single-use backup code,
// and — on success — mints the full access token with amr=["pwd","otp"]. A
// wrong code feeds the SAME account-lockout counter that AuthenticateUser uses,
// so brute-forcing the second factor is bounded by the existing lockout policy.
// OTP codes and backup codes are never logged (CLAUDE.md §10).
// meta carries the client IP + User-Agent captured at the HTTP edge; it is
// forwarded to IssueMFACompletedToken so a completed second-factor login
// creates a listable/revocable session row.
func (s *Service) VerifyLoginMFA(ctx context.Context, challengeToken, code string, meta SessionMeta) (string, error) {
	// The challenge token is only spendable here: ValidateMFAToken enforces
	// typ==mfa_challenge, so a normal access token (or a setup token) is refused.
	claims, err := s.ValidateMFAToken(ctx, challengeToken, tokenTypeMFAChallenge)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	// SEC-079: enforce the account lockout at the OTP step too. A wrong OTP feeds
	// the same counter below, but without this pre-check a locked account could
	// still be probed by minting fresh challenge tokens. Reject a locked account
	// before spending any work on the code.
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		return "", ErrAccountLocked
	}
	// SEC-079: cap submissions per challenge token (its jti) so one stateless
	// 5-minute challenge cannot be replayed for unbounded guesses within its
	// window. Fail closed on a Redis error, matching the human-auth posture.
	within, aerr := s.recordMFAChallengeAttempt(ctx, claims.ID)
	if aerr != nil {
		return "", aerr
	}
	if !within {
		return "", ErrInvalidCredentials
	}
	ok, cerr := s.ConsumeMFACode(ctx, userID, code)
	if cerr != nil {
		return "", cerr
	}
	if !ok {
		// Wrong / replayed code — feed the existing lockout counter exactly the
		// way AuthenticateUser does (record + lock at the threshold) so the OTP
		// step cannot be brute-forced past the account-lockout policy.
		if count, ferr := s.users.RecordFailedLogin(ctx, userID); ferr == nil && count >= maxFailedLogins {
			_ = s.users.LockUntil(ctx, userID, time.Now().Add(lockoutDuration))
		}
		return "", ErrInvalidCredentials
	}
	// Success clears the failed-login counter + any lock, mirroring the password
	// path in AuthenticateUser so a subsequent login starts from a clean slate.
	_ = s.users.ResetFailedLogins(ctx, userID)
	tenantID, _ := uuid.Parse(claims.TenantID)
	// Roles + is_global_admin are resolved from the DB by the shared issuer; the
	// challenge token deliberately carries no roles/access. The MFA login path is
	// human-only (GetHumanByUsername gates AuthenticateUser). meta is forwarded so
	// the completed login mints a session row.
	return s.IssueMFACompletedToken(ctx, userID, tenantID, meta)
}

// recordMFAChallengeAttempt atomically counts submissions made against a single
// challenge token (keyed on its jti) and reports whether we are still within
// maxMFAChallengeAttempts. The counter is seeded with the challenge TTL on the
// first attempt so it self-expires with the token and never accumulates. This
// bounds OTP guessing on a stateless challenge that would otherwise be reusable
// for its full 5-minute lifetime (SEC-079); the per-account lockout remains the
// authoritative backstop. Returns an error (fail-closed) on a Redis failure,
// consistent with the human-auth Redis posture elsewhere in this service.
func (s *Service) recordMFAChallengeAttempt(ctx context.Context, jti string) (bool, error) {
	key := "mfa:challenge_attempts:" + jti
	n, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("mfa challenge attempt counter: %w", err)
	}
	if n == 1 {
		// First attempt seeds the TTL so the counter cannot outlive the token.
		_ = s.redis.Expire(ctx, key, mfaChallengeTTL).Err()
	}
	return n <= maxMFAChallengeAttempts, nil
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
//
// FUT-003 policy consultation:
//
//   - If a workspace token policy is wired AND has MaxTTLDays set, the
//     caller-requested expires_at is rejected when it exceeds now() + max.
//     Existing keys are NOT re-validated against a stricter policy —
//     grandfathering keeps operators from locking themselves out of their
//     own workspace on a tightening (Task 6 load-bearing invariant).
//
//   - If a workspace token policy has RotationIntervalDays set, the new key
//     gets rotation_due_at = now() + interval so FUT-004's lapse surface has
//     a deadline to enforce. Missed on legacy paths (policy not wired) which
//     leaves rotation_due_at NULL — treated as "no rotation required".
//
// The token-policy repo is optional; when nil (test fixtures without a
// policy wiring) we skip enforcement and behave exactly like the pre-FUT-003
// path. Grandfathering is enforced structurally: this function only ever
// consults the policy for the CURRENT create call.
func (s *Service) CreateAPIKey(ctx context.Context, tenantID, userID uuid.UUID, name string, scopes []string, expiresAt *time.Time) (key *repository.APIKey, rawSecret string, err error) {
	// FUT-003: enforce max_ttl_days BEFORE generating a secret so a rejected
	// call doesn't waste an Argon2 round.
	var rotationDueAt *time.Time
	if s.tokenPolicy != nil {
		policy, perr := s.tokenPolicy.GetOrDefault(ctx, tenantID)
		if perr != nil {
			return nil, "", fmt.Errorf("load token policy: %w", perr)
		}
		// SEC-064 (2026-07-01): the initial impl guarded on `expiresAt != nil`,
		// which silently skipped enforcement when the caller omitted expiry.
		// Result: any caller trivially bypassed a `max_ttl_days=30` policy by
		// leaving the expiry field blank — the key persisted with expires_at
		// NULL and validated forever. Now we treat nil as "caller didn't
		// specify; clamp to the policy cap." A caller wanting the full cap
		// worth of TTL doesn't have to compute it themselves.
		if policy.MaxTTLDays != nil {
			maxAllowed := time.Now().Add(time.Duration(*policy.MaxTTLDays) * 24 * time.Hour)
			if expiresAt == nil {
				clamped := maxAllowed
				expiresAt = &clamped
			} else if expiresAt.After(maxAllowed) {
				return nil, "", status.Errorf(codes.InvalidArgument,
					"requested TTL exceeds workspace max (%d days)", *policy.MaxTTLDays)
			}
		}
		if policy.RotationIntervalDays != nil {
			due := time.Now().Add(time.Duration(*policy.RotationIntervalDays) * 24 * time.Hour)
			rotationDueAt = &due
		}
	}

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
	// If policy required a rotation deadline, stamp it now. A failure here
	// does NOT roll back the create — the key is usable, we just log the
	// gap so operators can detect it. FUT-004's lapse UI will still surface
	// the key as "no deadline known" without an inbound alert path.
	if rotationDueAt != nil {
		if setErr := s.apiKeys.SetRotationDueAt(ctx, key.ID, rotationDueAt); setErr != nil {
			slog.WarnContext(ctx, "CreateAPIKey: SetRotationDueAt failed; key created without deadline",
				"key_id", key.ID, "err", setErr)
		} else {
			key.RotationDueAt = rotationDueAt
		}
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
		s.touchLastUsed(key.ID)
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
		s.touchLastUsed(key.ID)
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
		s.touchLastUsed(key.ID)
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
	s.touchLastUsed(key.ID)

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

// touchLastUsed dispatches a last_used_at bump through the FUT-003
// Redis-debounced updater. Fire-and-forget: returns immediately, the
// actual write (if the debounce window is open) happens on a goroutine
// owned by the updater.
//
// When the updater is not wired (legacy fakes constructed without an
// api-key repo), we fall through to the pre-FUT-003 goroutine-based
// TouchLastUsed so those tests still see a bumped timestamp — the
// debounce is a performance optimisation, not a correctness gate.
func (s *Service) touchLastUsed(keyID uuid.UUID) {
	if s.lastUsed != nil {
		s.lastUsed.Touch(context.Background(), keyID)
		return
	}
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
	// SEC-075: resolve via the kind-guarded lookup so a service-account shadow
	// row (kind='service_account', synthetic sa-<hex> username) can never be
	// authenticated by password. The kind='human' guard is the primary control
	// — we no longer rely on argon2.Verify rejecting an empty password_hash as
	// the sole barrier. A matched-but-non-human row is indistinguishable from
	// "no such user" here, so we return the same invalid-credentials shape and
	// do not leak that a shadow row exists.
	user, err := s.users.GetHumanByUsername(ctx, tenantID, username)
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
		// Carry the unlock time so the /login handler can surface a
		// "try again in N minutes" message. Unwraps to ErrAccountLocked, so
		// every existing errors.Is check is unaffected.
		return nil, &AccountLockedError{Until: *user.LockedUntil}
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

// ResolveUserEmails batch-resolves user_ids within a tenant to (id, email)
// tuples (FUT-019 Phase 3). Users with no email are dropped by the repo, so the
// returned slice may be shorter than ids. Mirrors LookupUsernames — the handler
// owns dedupe/cap; this layer just forwards to the repository.
func (s *Service) ResolveUserEmails(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]repository.EmailLookup, error) {
	return s.users.ResolveEmails(ctx, tenantID, ids)
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
//
// Phase 6.5 — every key in the ring is enumerated so external validators can
// verify tokens minted by any kid currently in rotation. Order matches the
// ring's internal order (deterministic, sorted by kid when loaded from disk
// via loadKeyRingFromDir).
func (s *Service) JWKS() JWKSResponse {
	all := s.keys.all()
	out := make([]JWK, 0, len(all))
	for _, k := range all {
		out = append(out, rsaToJWK(k.publicKey, k.kid))
	}
	return JWKSResponse{Keys: out}
}

// validateWithFallback retries JWT verification against every key in the
// ring. It is called only when the first-pass kid-targeted verify failed,
// which happens for:
//  1. JWTs minted before the kid header was stamped (pre-Phase-6.5);
//  2. JWTs whose kid is not in the current ring (e.g. an old key file was
//     deleted while a token issued under it is still within its TTL — the
//     operator should have waited for the TTL to drain first, but a
//     well-behaved validator gracefully rejects in that case anyway, since
//     the verify will fail against every other key in the ring).
//
// Returns the parsed token on success, or an error if NO key validates. The
// caller logs a slog.Warn on success so operators see fallback hits during
// rotation windows.
func (s *Service) validateWithFallback(tokenStr string, claims *Claims) (*jwt.Token, error) {
	all := s.keys.all()
	var lastErr error
	for _, k := range all {
		// Reset claims on each attempt — jwt.ParseWithClaims may have left
		// partially-populated fields from the previous (failed) attempt.
		*claims = Claims{}
		key := k.publicKey
		tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return key, nil
		})
		if err == nil && tok.Valid {
			return tok, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no key in ring validated the token")
	}
	return nil, lastErr
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
	return parsePrivateKeyPEM(raw)
}

// parsePrivateKeyPEM parses a raw PEM-encoded RSA private key (PKCS8 first,
// PKCS1 fallback). Extracted so the keyring-from-disk loader can reuse the
// same parsing logic as the legacy base64-wrapped path without going through
// an unnecessary base64 round-trip. Never logs the key material.
func parsePrivateKeyPEM(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("no PEM block found in private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fall back to PKCS1 format.
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
