package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// userRepo is the subset of *repository.UserRepository methods used by Service.
// Defining it here as an interface lets tests supply hand-written fakes without
// needing a real PostgreSQL pool (CLAUDE.md §18 — no real network calls in unit tests).
type userRepo interface {
	Create(ctx context.Context, req repository.CreateUserRequest) (*repository.User, error)
	GetByUsername(ctx context.Context, tenantID uuid.UUID, username string) (*repository.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*repository.User, error)
	RecordFailedLogin(ctx context.Context, id uuid.UUID) (int, error)
	LockUntil(ctx context.Context, id uuid.UUID, until time.Time) error
	ResetFailedLogins(ctx context.Context, id uuid.UUID) error
	// Profile / password mutations — used by /users/me endpoints (FE-API-011/012/013).
	UpdateProfile(ctx context.Context, id uuid.UUID, req repository.UpdateProfileRequest) (*repository.User, error)
	UpdatePasswordHash(ctx context.Context, id uuid.UUID, newHash string) error
	// RBAC methods — used by the GRPC handler's role management endpoints.
	GetUserRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]repository.RoleAssignment, error)
	GrantRole(ctx context.Context, a repository.RoleAssignment) error
	RevokeRole(ctx context.Context, assignmentID, tenantID uuid.UUID) error
	RevokeRoleScoped(ctx context.Context, assignmentID, tenantID uuid.UUID, expectedScopeType, expectedScopeValue string) error
	ListMembers(ctx context.Context, tenantID uuid.UUID, scopeType, scopeValue string) ([]repository.Member, error)
	// CountByTenant returns the user count for a tenant (FE-API-028).
	CountByTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
	// LookupByIDs batch-resolves users to (id, username, display_name)
	// tuples within a tenant (REM-018-followup). Used by services/management
	// to enrich the activity / notifications feed.
	LookupByIDs(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]repository.UserSummary, error)
	// FUT-012 Phase A — tenant-user lifecycle methods.
	ListTenantUsers(ctx context.Context, tenantID uuid.UUID, opts repository.ListTenantUsersOpts) ([]repository.TenantUserSummary, string, int32, error)
	CreateInvitedUser(ctx context.Context, req repository.CreateInvitedUserRequest) (*repository.User, error)
	SetUserStatus(ctx context.Context, tenantID, userID uuid.UUID, status string) error
	DisableAPIKeysForUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error)
	// SSO methods (FE-API-034). GetByEmail is used to match an IdP-asserted
	// email to an existing user; CreateSSOUser provisions a new account when
	// auto_provision=true; TouchLastLogin records the SSO login time for
	// existing users.
	GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*repository.User, error)
	// GetHumanByEmail is the kind-guarded variant of GetByEmail (FE-API-048,
	// Task 10). It returns ErrNotFound for service-account synthetic emails
	// (sa+N@internal.invalid) so they cannot match on the SSO login path.
	// Use this method on all human-authentication paths; GetByEmail is deprecated.
	GetHumanByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*repository.User, error)
	// GetUserBySSOSubject is the (tenant_id, sso_provider_id, sso_subject)
	// composite lookup that EnsureSSOUser consults first to defend against
	// email-recycle account takeover (REDESIGN-001 Phase 5.5). Tenant filter
	// added by SEC-040 — closes the multi-mode boundary where two tenants
	// sharing one IdP could resolve the same subject to a wrong-tenant user.
	GetUserBySSOSubject(ctx context.Context, tenantID uuid.UUID, providerID string, subject string) (*repository.User, error)
	// SetSSOSubject backfills users.sso_subject for accounts that pre-date
	// the Phase 5.5 migration so future logins flow through the subject
	// lookup. Called only on the first email-matched login of an existing
	// pre-migration row with NULL sso_subject.
	SetSSOSubject(ctx context.Context, userID uuid.UUID, subject string) error
	CreateSSOUser(ctx context.Context, req repository.CreateSSOUserRequest) (*repository.User, error)
	TouchLastLogin(ctx context.Context, id uuid.UUID) error
	// Kind-guarded helpers (FE-API-048). GetHumanByID enforces kind='human' so
	// service-account shadow users cannot be loaded onto a human identity context.
	// GetUserAnyKind is used by the SA management path where loading shadow users
	// is intentional (e.g. verifying cascade delete, loading creator snapshots).
	GetHumanByID(ctx context.Context, id uuid.UUID) (*repository.User, error)
	GetUserAnyKind(ctx context.Context, id uuid.UUID) (*repository.User, error)
	// SetGlobalAdmin updates users.is_global_admin for the given user.
	// REDESIGN-001 Phase 5.1 — typed platform-admin primitive.
	SetGlobalAdmin(ctx context.Context, userID uuid.UUID, granted bool) error
	// MarkOnboardingComplete flips users.onboarding_complete to true for the
	// given user. REDESIGN-001 Phase 4.3 — backs the post-login wizard's
	// "Done" / "Skip" buttons. Idempotent so retries are safe.
	MarkOnboardingComplete(ctx context.Context, userID uuid.UUID) (*repository.User, error)
}

// apiKeyRepo is the subset of *repository.APIKeyRepository methods used by Service.
type apiKeyRepo interface {
	Create(ctx context.Context, req repository.CreateAPIKeyRequest) (*repository.APIKey, error)
	GetByID(ctx context.Context, id uuid.UUID) (*repository.APIKey, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*repository.APIKey, error)
	// ListByServiceAccount returns active keys owned by the given service account.
	ListByServiceAccount(ctx context.Context, saID uuid.UUID) ([]*repository.APIKey, error)
	Delete(ctx context.Context, id, userID uuid.UUID) error
	// DeleteByServiceAccount revokes an SA-owned API key. Returns ErrNotFound
	// when no such (id, service_account_id) pair exists. Use Delete for
	// human-owned keys — the two paths are deliberately separate so the wrong
	// owner-column cannot authorise a delete.
	DeleteByServiceAccount(ctx context.Context, id, saID uuid.UUID) error
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
	// FUT-003 additions.
	// UpdateLastUsedAt is called by the Redis-debounced last_used_at updater
	// with a caller-supplied timestamp so tests can pin the wall clock.
	UpdateLastUsedAt(ctx context.Context, id uuid.UUID, at time.Time) error
	// SetRotationDueAt records the rotation deadline set by CreateAPIKey
	// when the workspace policy configures rotation_interval_days.
	SetRotationDueAt(ctx context.Context, id uuid.UUID, at *time.Time) error
	// RevokeWithReason soft-deletes a key and stamps the reason string.
	// Used by the idle-revoke background worker (owner-agnostic revoke).
	RevokeWithReason(ctx context.Context, id uuid.UUID, reason string) error
	// ListIdleKeys returns non-revoked keys whose last_used_at is older
	// than the cutoff (or NULL). Used by the idle-revoke worker.
	ListIdleKeys(ctx context.Context, tenantID uuid.UUID, cutoff time.Time) ([]repository.IdleKey, error)
}

// saRepo is the subset of *repository.ServiceAccountRepo methods used by
// ServiceAccountService. The interface allows test fakes without a real DB.
type saRepo interface {
	CreateAtomic(ctx context.Context, in repository.CreateServiceAccountInput) (*repository.ServiceAccount, uuid.UUID, error)
	Get(ctx context.Context, id uuid.UUID) (*repository.ServiceAccount, error)
	List(ctx context.Context, tenantID uuid.UUID, includeDisabled bool, pageSize int, pageToken string) ([]repository.ServiceAccountWithStats, string, error)
	Update(ctx context.Context, in repository.UpdateServiceAccountInput) (*repository.ServiceAccount, error)
	Delete(ctx context.Context, id uuid.UUID) error
	CountKeysAffectedByScopeShrink(ctx context.Context, saID uuid.UUID, proposed []string) (int64, error)
}

// redisClient is the minimal Redis interface the Service uses. Defining it as an
// interface (rather than holding *redis.Client directly) lets tests supply a
// targeted fake — in particular one that returns an error for specific key
// prefixes — without mocking the entire redis.Cmdable surface.
// *redis.Client satisfies this interface via the compile-time check below.
type redisClient interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Pipeline() redis.Pipeliner
	SMembers(ctx context.Context, key string) *redis.StringSliceCmd
	// SAdd / Expire are used by the API-key validation cache (Phase 6.7) to
	// maintain a per-key_id index of cached secret-hashes for fast
	// invalidation on disable/revoke. They live here on the minimal
	// interface so test-only redisClient implementations (see
	// revokeUserErrRedis) embed *redis.Client and pick them up transparently.
	SAdd(ctx context.Context, key string, members ...interface{}) *redis.IntCmd
	Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd
	// SetNX is used by the FUT-003 last_used_at debouncer to coalesce
	// per-key updates inside a 5-minute window without a full Redis SET.
	SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd
}

// Ensure the concrete repository types satisfy the interfaces at compile time.
var _ userRepo = (*repository.UserRepository)(nil)
var _ apiKeyRepo = (*repository.APIKeyRepository)(nil)
var _ saRepo = (*repository.ServiceAccountRepo)(nil)
var _ redisClient = (*redis.Client)(nil)

// UserRepo is the exported alias of the userRepo interface, so handler-package
// tests can implement it without importing repository directly.
type UserRepo = userRepo

// APIKeyRepo is the exported alias of the apiKeyRepo interface.
type APIKeyRepo = apiKeyRepo

// NewWithFakes constructs a Service with hand-written fake repositories for
// testing. This allows tests in other packages (e.g. handler tests) to build a
// fully in-memory Service without needing a real PostgreSQL pool.
// The redis.Client must already be connected (e.g. to miniredis).
//
// sa and audit may be nil; if nil, the service-account branch of ValidateAPIKey
// returns ErrInvalidCredentials and cross-tenant audit emission is skipped.
//
// This constructor is exported so handler-package tests can call it; it must
// not be called in production code.
func NewWithFakes(
	users userRepo,
	apiKeys apiKeyRepo,
	sa saRepo,
	audit AuditEmitter,
	rdb redisClient,
	privKeyB64, pubKeyB64, keyID string,
) (*Service, error) {
	// Phase 6.5 — fakes still take a single PEM pair; we wrap it into a
	// 1-element ring so the test path exercises the same code as production
	// single-key configs.
	ring, err := singleKeyRingFromB64(privKeyB64, pubKeyB64, keyID)
	if err != nil {
		return nil, err
	}
	s := &Service{
		users:           users,
		apiKeys:         apiKeys,
		serviceAccounts: sa,
		audit:           audit,
		redis:           rdb,
		keys:            ring,
	}
	// FUT-003: wire the debounced last_used_at updater so ValidateAPIKey
	// tests exercise the same touch-path as production. rdb may be nil for
	// fakes that don't need Redis; the updater tolerates that.
	if apiKeys != nil {
		s.lastUsed = newLastUsedUpdater(rdb, apiKeys, slog.Default())
	}
	return s, nil
}

// NewWithFakesAndRing is the multi-key analogue of NewWithFakes. Phase 6.5
// rotation tests use this to wire a pre-built keyRing (e.g. one containing
// kid A as the signer plus kid B as a verify-only entry) without going
// through the disk loader.
//
// Like NewWithFakes, this is test-only — production code uses NewWithKeyRing.
func NewWithFakesAndRing(
	users userRepo,
	apiKeys apiKeyRepo,
	sa saRepo,
	audit AuditEmitter,
	rdb redisClient,
	ring *keyRing,
) (*Service, error) {
	if ring == nil {
		return nil, fmt.Errorf("auth: key ring is required")
	}
	s := &Service{
		users:           users,
		apiKeys:         apiKeys,
		serviceAccounts: sa,
		audit:           audit,
		redis:           rdb,
		keys:            ring,
	}
	if apiKeys != nil {
		s.lastUsed = newLastUsedUpdater(rdb, apiKeys, slog.Default())
	}
	return s, nil
}

// SetTokenPolicyRepo wires the FUT-003 token-policy repository so CreateAPIKey
// enforces max_ttl_days and stamps rotation_due_at. Nil clears the wiring
// (equivalent to "no policy configured"). Kept as a setter so existing
// constructors don't need signature changes.
func (s *Service) SetTokenPolicyRepo(r tokenPolicyReader) {
	s.tokenPolicy = r
}
