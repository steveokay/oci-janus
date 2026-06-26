package service

import (
	"context"
	"fmt"
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
	privKey, err := parsePrivateKey(privKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	pubKey, err := parsePublicKey(pubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("parse JWT public key: %w", err)
	}
	return &Service{
		users:           users,
		apiKeys:         apiKeys,
		serviceAccounts: sa,
		audit:           audit,
		redis:           rdb,
		privKey:         privKey,
		pubKey:          pubKey,
		keyID:           keyID,
	}, nil
}
