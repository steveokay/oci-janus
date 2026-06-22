//go:build integration

// Package testutil provides test helpers for registry-auth integration tests.
// This file adds SA (service account) fixtures: helpers that create real SA
// rows and their associated API keys directly via the repository layer without
// going through the service layer's audit emission, making them suitable for
// tests that want to seed state without invoking business-logic side effects.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// NewServiceAccount inserts a service account plus its shadow user via
// ServiceAccountRepo.CreateAtomic and returns the SA and the shadow user's ID.
//
// A "creator" human user is seeded automatically using a deterministic email
// (fixture-admin@example.com) so the created_by FK on service_accounts is
// satisfied. The creator row is upserted on each call: if the email already
// exists in the tenant the existing row is reused.
//
// allowedScopes may be empty — an SA with no scopes is valid and will reject
// all API-key authentication attempts at the scope-intersection layer.
//
// The test fails immediately (t.Fatal) on any error. Call t.Helper() in the
// enclosing test before calling NewServiceAccount so failure lines are reported
// at the correct call site.
func NewServiceAccount(
	t testing.TB,
	ctx context.Context,
	repo *repository.ServiceAccountRepo,
	users *repository.UserRepository,
	tenant uuid.UUID,
	name string,
	allowedScopes ...string,
) (*repository.ServiceAccount, uuid.UUID) {
	t.Helper()

	// Seed a deterministic "creator" human user so created_by is not nil.
	// If the row already exists we reuse it (ErrAlreadyExists is not fatal here).
	creatorID := seedCreatorUser(t, ctx, users, tenant)

	scopes := allowedScopes
	if scopes == nil {
		scopes = []string{}
	}

	sa, shadowUserID, err := repo.CreateAtomic(ctx, repository.CreateServiceAccountInput{
		TenantID:      tenant,
		Name:          name,
		Description:   "fixture service account",
		AllowedScopes: scopes,
		CreatedBy:     creatorID,
	})
	if err != nil {
		t.Fatalf("NewServiceAccount: CreateAtomic: %v", err)
	}
	return sa, shadowUserID
}

// NewAPIKeyForSA issues an API key owned by the given service account.
// It mirrors the production key-generation path in service.CreateAPIKey:
//
//   - 32 random bytes → 64-char lowercase hex raw secret
//   - argon2id hash stored in key_hash
//   - first 12 chars of raw secret stored in key_prefix (display-only)
//
// Returns (keyID, rawSecret). The raw secret is formatted the same way the
// production POST /service-accounts/{id}/apikeys endpoint returns it — just
// the 64-char hex string. To authenticate as this SA, present:
//
//	Basic <key.ID>:<rawSecret>
//
// The test fails immediately (t.Fatal) on any error.
func NewAPIKeyForSA(
	t testing.TB,
	ctx context.Context,
	keys *repository.APIKeyRepository,
	sa *repository.ServiceAccount,
	name string,
	scopes ...string,
) (keyID string, rawSecret string) {
	t.Helper()

	// Generate 32 random bytes → 64-char hex secret (same as service layer).
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		t.Fatalf("NewAPIKeyForSA: generate secret: %v", err)
	}
	rawSecret = hex.EncodeToString(rawBytes)

	// Hash the raw secret with argon2id — the same as production.
	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		t.Fatalf("NewAPIKeyForSA: hash secret: %v", err)
	}

	if scopes == nil {
		scopes = []string{}
	}

	// Ownership pointer: ServiceAccountID set, UserID nil (polymorphic model,
	// enforced by the api_keys_owner_exactly_one CHECK constraint).
	saID := sa.ID
	key, err := keys.Create(ctx, repository.CreateAPIKeyRequest{
		TenantID:         sa.TenantID,
		ServiceAccountID: &saID,
		Name:             name,
		KeyHash:          hash,
		KeyPrefix:        rawSecret[:12], // first 12 chars, display-only
		Scopes:           scopes,
	})
	if err != nil {
		t.Fatalf("NewAPIKeyForSA: Create: %v", err)
	}
	return key.ID.String(), rawSecret
}

// seedCreatorUser upserts a deterministic human user (fixture-admin@example.com)
// in the given tenant. It returns the user's ID regardless of whether the row
// was just created or already existed.
//
// This helper exists so NewServiceAccount does not need to require the caller to
// provide a creator ID — tests that only care about the SA row should not be
// forced to seed a human user manually.
func seedCreatorUser(t testing.TB, ctx context.Context, users *repository.UserRepository, tenant uuid.UUID) uuid.UUID {
	t.Helper()

	const (
		fixtureUsername = "fixture-admin"
		fixtureEmail    = "fixture-admin@example.com"
		// Use a deterministic password hash for the fixture user so tests that
		// never authenticate as this user are not slowed down by Argon2id.
		// Argon2id is not used here because this user never authenticates through
		// the normal login path — it only satisfies the FK constraint.
		// The empty string password_hash makes it impossible to log in as this
		// user via AuthenticateUser (argon2.Verify rejects an empty hash).
		fixturePasswordHash = ""
	)

	// Try to create; on duplicate, fall back to a lookup.
	user, err := users.Create(ctx, repository.CreateUserRequest{
		TenantID:     tenant,
		Username:     fixtureUsername,
		Email:        fixtureEmail,
		PasswordHash: fixturePasswordHash,
	})
	if err == nil {
		return user.ID
	}
	if err != repository.ErrAlreadyExists {
		t.Fatalf("seedCreatorUser: Create: %v", err)
	}

	// Creator already exists — look it up by username.
	existing, lookupErr := users.GetByUsername(ctx, tenant, fixtureUsername)
	if lookupErr != nil {
		t.Fatalf("seedCreatorUser: GetByUsername after AlreadyExists: %v", lookupErr)
	}
	return existing.ID
}
