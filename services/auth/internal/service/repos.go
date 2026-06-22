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
	// SSO methods (FE-API-034). GetByEmail is used to match an IdP-asserted
	// email to an existing user; CreateSSOUser provisions a new account when
	// auto_provision=true; TouchLastLogin records the SSO login time for
	// existing users.
	GetByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*repository.User, error)
	CreateSSOUser(ctx context.Context, req repository.CreateSSOUserRequest) (*repository.User, error)
	TouchLastLogin(ctx context.Context, id uuid.UUID) error
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

// Ensure the concrete repository types satisfy the interfaces at compile time.
var _ userRepo = (*repository.UserRepository)(nil)
var _ apiKeyRepo = (*repository.APIKeyRepository)(nil)

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
// This constructor is exported so handler-package tests can call it; it must
// not be called in production code.
func NewWithFakes(
	users userRepo,
	apiKeys apiKeyRepo,
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
	return &Service{
		users:   users,
		apiKeys: apiKeys,
		redis:   rdb,
		privKey: privKey,
		pubKey:  pubKey,
		keyID:   keyID,
	}, nil
}
