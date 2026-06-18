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
	// RBAC methods — used by the GRPC handler's role management endpoints.
	GetUserRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]repository.RoleAssignment, error)
	GrantRole(ctx context.Context, a repository.RoleAssignment) error
	RevokeRole(ctx context.Context, assignmentID, tenantID uuid.UUID) error
	RevokeRoleScoped(ctx context.Context, assignmentID, tenantID uuid.UUID, expectedScopeType, expectedScopeValue string) error
	ListMembers(ctx context.Context, tenantID uuid.UUID, scopeType, scopeValue string) ([]repository.RoleAssignment, error)
}

// apiKeyRepo is the subset of *repository.APIKeyRepository methods used by Service.
type apiKeyRepo interface {
	Create(ctx context.Context, req repository.CreateAPIKeyRequest) (*repository.APIKey, error)
	GetByID(ctx context.Context, id uuid.UUID) (*repository.APIKey, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*repository.APIKey, error)
	Delete(ctx context.Context, id, userID uuid.UUID) error
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
