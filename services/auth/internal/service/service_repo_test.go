// Package service — repository-dependent tests using hand-written fakes.
// All tests in this file use miniredis (no real Redis) and in-memory fakes
// (no real PostgreSQL). They must run without any external infrastructure so
// that plain `go test ./...` always passes (CLAUDE.md §18).
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── Fakes ────────────────────────────────────────────────────────────────────

// fakeUserRepo is an in-memory userRepo fake. Methods are implemented minimally
// to satisfy test needs. Zero-value is ready to use with an empty user store.
type fakeUserRepo struct {
	// users maps username → User
	users         map[string]*repository.User
	failedLogins  map[uuid.UUID]int
	recordFailErr error // if non-nil, RecordFailedLogin returns this error
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{
		users:        make(map[string]*repository.User),
		failedLogins: make(map[uuid.UUID]int),
	}
}

// addUser inserts a user into the fake store. The password must already be hashed.
func (f *fakeUserRepo) addUser(u *repository.User) {
	f.users[u.Username] = u
}

func (f *fakeUserRepo) Create(_ context.Context, req repository.CreateUserRequest) (*repository.User, error) {
	if _, exists := f.users[req.Username]; exists {
		return nil, repository.ErrAlreadyExists
	}
	u := &repository.User{
		ID:           uuid.New(),
		TenantID:     req.TenantID,
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: req.PasswordHash,
		IsActive:     true,
		CreatedAt:    time.Now(),
	}
	f.users[req.Username] = u
	return u, nil
}

func (f *fakeUserRepo) GetByUsername(_ context.Context, tenantID uuid.UUID, username string) (*repository.User, error) {
	u, ok := f.users[username]
	if !ok || u.TenantID != tenantID {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (f *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) RecordFailedLogin(_ context.Context, id uuid.UUID) (int, error) {
	if f.recordFailErr != nil {
		return 0, f.recordFailErr
	}
	f.failedLogins[id]++
	return f.failedLogins[id], nil
}

func (f *fakeUserRepo) LockUntil(_ context.Context, id uuid.UUID, until time.Time) error {
	for _, u := range f.users {
		if u.ID == id {
			u.LockedUntil = &until
			return nil
		}
	}
	return nil
}

func (f *fakeUserRepo) ResetFailedLogins(_ context.Context, id uuid.UUID) error {
	delete(f.failedLogins, id)
	for _, u := range f.users {
		if u.ID == id {
			u.LockedUntil = nil
		}
	}
	return nil
}

// UpdateProfile mutates the in-memory user record so unit tests of
// UpdateUserProfile / ChangePassword can observe the change without a real DB.
// Returns ErrNotFound when no user matches the supplied ID.
func (f *fakeUserRepo) UpdateProfile(_ context.Context, id uuid.UUID, req repository.UpdateProfileRequest) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID != id {
			continue
		}
		if req.DisplayName != nil {
			// Empty string clears (matches the repo's NULLIF behaviour).
			if *req.DisplayName == "" {
				u.DisplayName = nil
			} else {
				v := *req.DisplayName
				u.DisplayName = &v
			}
		}
		if req.Email != nil {
			u.Email = *req.Email
		}
		return u, nil
	}
	return nil, repository.ErrNotFound
}

// UpdatePasswordHash overwrites the stored hash for the given user in-memory.
func (f *fakeUserRepo) UpdatePasswordHash(_ context.Context, id uuid.UUID, newHash string) error {
	for _, u := range f.users {
		if u.ID == id {
			u.PasswordHash = newHash
			return nil
		}
	}
	return repository.ErrNotFound
}

func (f *fakeUserRepo) GetUserRoles(_ context.Context, _, _ uuid.UUID) ([]repository.RoleAssignment, error) {
	return nil, nil
}
func (f *fakeUserRepo) GrantRole(_ context.Context, _ repository.RoleAssignment) error { return nil }
func (f *fakeUserRepo) RevokeRole(_ context.Context, _, _ uuid.UUID) error             { return nil }
func (f *fakeUserRepo) RevokeRoleScoped(_ context.Context, _, _ uuid.UUID, _, _ string) error {
	return nil
}
func (f *fakeUserRepo) ListMembers(_ context.Context, _ uuid.UUID, _, _ string) ([]repository.RoleAssignment, error) {
	return nil, nil
}
func (f *fakeUserRepo) CountByTenant(_ context.Context, _ uuid.UUID) (int64, error) {
	return int64(len(f.users)), nil
}

// SSO fake methods — FE-API-034. Note that the in-memory store is keyed by
// username, so lookups by email/ID iterate the map.
func (f *fakeUserRepo) GetByEmail(_ context.Context, tenantID uuid.UUID, email string) (*repository.User, error) {
	for _, u := range f.users {
		if u.TenantID == tenantID && strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) CreateSSOUser(_ context.Context, req repository.CreateSSOUserRequest) (*repository.User, error) {
	for _, u := range f.users {
		if u.TenantID == req.TenantID && (u.Username == req.Username || (req.Email != "" && strings.EqualFold(u.Email, req.Email))) {
			return nil, repository.ErrAlreadyExists
		}
	}
	u := &repository.User{
		ID:        uuid.New(),
		TenantID:  req.TenantID,
		Username:  req.Username,
		Email:     req.Email,
		IsActive:  true,
		CreatedAt: time.Now(),
	}
	if req.DisplayName != "" {
		v := req.DisplayName
		u.DisplayName = &v
	}
	f.users[u.Username] = u
	return u, nil
}

func (f *fakeUserRepo) TouchLastLogin(_ context.Context, id uuid.UUID) error {
	for _, u := range f.users {
		if u.ID == id {
			now := time.Now()
			u.LastLoginAt = &now
			return nil
		}
	}
	return nil
}

// fakeAPIKeyRepo is an in-memory apiKeyRepo fake.
type fakeAPIKeyRepo struct {
	keys        map[uuid.UUID]*repository.APIKey
	createErr   error
	getByIDErr  error // if set, GetByID returns this instead of looking up
}

func newFakeAPIKeyRepo() *fakeAPIKeyRepo {
	return &fakeAPIKeyRepo{keys: make(map[uuid.UUID]*repository.APIKey)}
}

func (f *fakeAPIKeyRepo) Create(_ context.Context, req repository.CreateAPIKeyRequest) (*repository.APIKey, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	// Mirror the repository defence-in-depth check so fake behaviour matches real.
	bothNil := req.UserID == nil && req.ServiceAccountID == nil
	bothSet := req.UserID != nil && req.ServiceAccountID != nil
	if bothNil || bothSet {
		return nil, fmt.Errorf("apikey: exactly one of UserID/ServiceAccountID must be set")
	}
	k := &repository.APIKey{
		ID:               uuid.New(),
		TenantID:         req.TenantID,
		UserID:           req.UserID,
		ServiceAccountID: req.ServiceAccountID,
		Name:             req.Name,
		KeyHash:          req.KeyHash,
		KeyPrefix:        req.KeyPrefix,
		Scopes:           req.Scopes,
		ExpiresAt:        req.ExpiresAt,
		IsActive:         true,
		CreatedAt:        time.Now(),
	}
	f.keys[k.ID] = k
	return k, nil
}

func (f *fakeAPIKeyRepo) GetByID(_ context.Context, id uuid.UUID) (*repository.APIKey, error) {
	if f.getByIDErr != nil {
		return nil, f.getByIDErr
	}
	k, ok := f.keys[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return k, nil
}

func (f *fakeAPIKeyRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]*repository.APIKey, error) {
	var result []*repository.APIKey
	for _, k := range f.keys {
		// UserID is now a pointer; dereference safely before comparing.
		if k.UserID != nil && *k.UserID == userID && k.IsActive {
			result = append(result, k)
		}
	}
	return result, nil
}

func (f *fakeAPIKeyRepo) ListByServiceAccount(_ context.Context, saID uuid.UUID) ([]*repository.APIKey, error) {
	var result []*repository.APIKey
	for _, k := range f.keys {
		if k.ServiceAccountID != nil && *k.ServiceAccountID == saID && k.IsActive {
			result = append(result, k)
		}
	}
	return result, nil
}

func (f *fakeAPIKeyRepo) Delete(_ context.Context, id, userID uuid.UUID) error {
	k, ok := f.keys[id]
	// UserID is now a pointer; treat a nil UserID as no match.
	if !ok || k.UserID == nil || *k.UserID != userID {
		return repository.ErrNotFound
	}
	delete(f.keys, id)
	return nil
}

func (f *fakeAPIKeyRepo) TouchLastUsed(_ context.Context, _ uuid.UUID) error { return nil }

// ── Helpers ───────────────────────────────────────────────────────────────────

// setupServiceWithRepos creates a Service backed by fake repos and miniredis.
// Callers get a fully functional service without any external process.
func setupServiceWithRepos(t *testing.T) (*Service, *fakeUserRepo, *fakeAPIKeyRepo, func()) {
	t.Helper()
	svc, cleanup := setupService(t)
	ur := newFakeUserRepo()
	ar := newFakeAPIKeyRepo()
	// Swap in fakes after construction (struct fields accessible from same package).
	svc.users = ur
	svc.apiKeys = ar
	return svc, ur, ar, cleanup
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestCreateUser_validInput verifies that a valid password and unique username
// result in a persisted user returned from the service.
func TestCreateUser_validInput_returnsUser(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	user, err := svc.CreateUser(context.Background(), tenantID, "alice", "alice@example.com", "Str0ng!Password123")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Username != "alice" {
		t.Errorf("Username: got %q, want %q", user.Username, "alice")
	}
	if user.TenantID != tenantID {
		t.Errorf("TenantID mismatch")
	}
	if user.IsActive != true {
		t.Errorf("expected IsActive=true")
	}
}

// TestCreateUser_weakPassword verifies that an invalid password is rejected
// before any database call is made.
func TestCreateUser_weakPassword_returnsError(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	_, err := svc.CreateUser(context.Background(), uuid.New(), "bob", "bob@example.com", "weak")
	if err == nil {
		t.Fatal("expected error for weak password, got nil")
	}
	if !IsPasswordPolicyError(err) {
		t.Errorf("expected IsPasswordPolicyError=true, got false for: %v", err)
	}
}

// TestCreateUser_duplicateUsername verifies that creating a user with an
// already-existing username returns ErrAlreadyExists.
func TestCreateUser_duplicateUsername_returnsAlreadyExists(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	_, err := svc.CreateUser(context.Background(), tenantID, "alice", "alice@example.com", "Str0ng!Password123")
	if err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, err = svc.CreateUser(context.Background(), tenantID, "alice", "alice2@example.com", "Str0ng!Password123")
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

// TestIsPasswordPolicyError_policyError verifies the helper returns true for
// *PasswordPolicyError (returned by ValidatePassword).
func TestIsPasswordPolicyError_policyError_returnsTrue(t *testing.T) {
	err := ValidatePassword("weak")
	if !IsPasswordPolicyError(err) {
		t.Errorf("expected IsPasswordPolicyError=true for policy violation, got false")
	}
}

// TestIsPasswordPolicyError_otherError verifies the helper returns false for
// a plain errors.New error.
func TestIsPasswordPolicyError_otherError_returnsFalse(t *testing.T) {
	err := errors.New("some internal error")
	if IsPasswordPolicyError(err) {
		t.Errorf("expected IsPasswordPolicyError=false for plain error, got true")
	}
}

// TestIsPasswordPolicyError_nilError verifies that nil returns false without panicking.
func TestIsPasswordPolicyError_nilError_returnsFalse(t *testing.T) {
	if IsPasswordPolicyError(nil) {
		t.Errorf("expected IsPasswordPolicyError=false for nil, got true")
	}
}

// TestPasswordPolicyError_errorMessage verifies that the error message is
// forwarded verbatim, so handlers can safely return it to callers (SEC-033).
func TestPasswordPolicyError_errorMessage_isForwarded(t *testing.T) {
	const msg = "password must be at least 12 characters"
	err := &PasswordPolicyError{msg: msg}
	if err.Error() != msg {
		t.Errorf("Error() = %q, want %q", err.Error(), msg)
	}
}

// ── AuthenticateUser ──────────────────────────────────────────────────────────

// authenticateSetup creates a service with a single active user whose password
// is hashed via the service itself (using CreateUser), then returns the service,
// the user's username, and the plaintext password for subsequent test calls.
func authenticateSetup(t *testing.T) (*Service, uuid.UUID, string, string, func()) {
	t.Helper()
	svc, _, _, cleanup := setupServiceWithRepos(t)

	tenantID := uuid.New()
	const (
		username = "testuser"
		password = "Str0ng!Password123"
	)
	_, err := svc.CreateUser(context.Background(), tenantID, username, "test@example.com", password)
	if err != nil {
		cleanup()
		t.Fatalf("create test user: %v", err)
	}
	return svc, tenantID, username, password, cleanup
}

// TestAuthenticateUser_validCredentials verifies that correct credentials
// return the user without error.
func TestAuthenticateUser_validCredentials_returnsUser(t *testing.T) {
	svc, tenantID, username, password, cleanup := authenticateSetup(t)
	defer cleanup()

	user, err := svc.AuthenticateUser(context.Background(), tenantID, username, password)
	if err != nil {
		t.Fatalf("AuthenticateUser: %v", err)
	}
	if user.Username != username {
		t.Errorf("Username: got %q, want %q", user.Username, username)
	}
}

// TestAuthenticateUser_wrongPassword verifies that a wrong password returns
// ErrInvalidCredentials and does not panic.
func TestAuthenticateUser_wrongPassword_returnsInvalidCreds(t *testing.T) {
	svc, tenantID, username, _, cleanup := authenticateSetup(t)
	defer cleanup()

	_, err := svc.AuthenticateUser(context.Background(), tenantID, username, "WrongPassword!999")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestAuthenticateUser_unknownUsername verifies that a non-existent username
// returns ErrInvalidCredentials (not a database error).
func TestAuthenticateUser_unknownUsername_returnsInvalidCreds(t *testing.T) {
	svc, tenantID, _, _, cleanup := authenticateSetup(t)
	defer cleanup()

	_, err := svc.AuthenticateUser(context.Background(), tenantID, "nobody", "Password!123456")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestAuthenticateUser_disabledAccount verifies that an inactive user is
// rejected with ErrAccountDisabled before the password is checked.
func TestAuthenticateUser_disabledAccount_returnsAccountDisabled(t *testing.T) {
	svc, ur, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	disabledUser := &repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "disabled",
		Email:        "d@example.com",
		PasswordHash: "irrelevant",
		IsActive:     false,
	}
	ur.addUser(disabledUser)

	_, err := svc.AuthenticateUser(context.Background(), tenantID, "disabled", "AnyPassword!1")
	if !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("expected ErrAccountDisabled, got %v", err)
	}
}

// TestAuthenticateUser_lockedAccount verifies that a user whose LockedUntil is
// in the future is rejected with ErrAccountLocked.
func TestAuthenticateUser_lockedAccount_returnsAccountLocked(t *testing.T) {
	svc, ur, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	future := time.Now().Add(30 * time.Minute)
	lockedUser := &repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "locked",
		Email:        "l@example.com",
		PasswordHash: "irrelevant",
		IsActive:     true,
		LockedUntil:  &future,
	}
	ur.addUser(lockedUser)

	_, err := svc.AuthenticateUser(context.Background(), tenantID, "locked", "AnyPassword!1")
	if !errors.Is(err, ErrAccountLocked) {
		t.Errorf("expected ErrAccountLocked, got %v", err)
	}
}

// TestAuthenticateUser_lockoutAfterFiveFailures verifies that the fifth failed
// login triggers account lockout (LockedUntil is set on the user record).
func TestAuthenticateUser_lockoutAfterFiveFailures_setsLockedUntil(t *testing.T) {
	svc, tenantID, username, _, cleanup := authenticateSetup(t)
	defer cleanup()

	ctx := context.Background()
	// Five wrong-password attempts — the 5th should trigger lockout.
	for i := 0; i < maxFailedLogins; i++ {
		_, err := svc.AuthenticateUser(ctx, tenantID, username, "WrongPassword!999")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("attempt %d: expected ErrInvalidCredentials, got %v", i+1, err)
		}
	}

	// The sixth attempt should now return ErrAccountLocked (LockedUntil is set).
	_, err := svc.AuthenticateUser(ctx, tenantID, username, "WrongPassword!999")
	if !errors.Is(err, ErrAccountLocked) {
		t.Errorf("after lockout: expected ErrAccountLocked, got %v", err)
	}
}

// ── Login ─────────────────────────────────────────────────────────────────────

// TestLogin_validCredentials verifies that Login returns a non-empty JWT for
// correct credentials.
func TestLogin_validCredentials_returnsToken(t *testing.T) {
	svc, tenantID, username, password, cleanup := authenticateSetup(t)
	defer cleanup()

	tok, err := svc.Login(context.Background(), tenantID, username, password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token")
	}
}

// TestLogin_invalidCredentials verifies that Login propagates the error from
// AuthenticateUser on bad credentials.
func TestLogin_invalidCredentials_returnsError(t *testing.T) {
	svc, tenantID, username, _, cleanup := authenticateSetup(t)
	defer cleanup()

	_, err := svc.Login(context.Background(), tenantID, username, "WrongPassword!999")
	if err == nil {
		t.Error("expected error for wrong password, got nil")
	}
}

// ── GetUserByID ───────────────────────────────────────────────────────────────

// TestGetUserByID_existingUser verifies that GetUserByID returns the user when found.
func TestGetUserByID_existingUser_returnsUser(t *testing.T) {
	svc, tenantID, username, _, cleanup := authenticateSetup(t)
	defer cleanup()

	// Look up the user by username to get the ID.
	ctx := context.Background()
	user, err := svc.AuthenticateUser(ctx, tenantID, username, "Str0ng!Password123")
	if err != nil {
		t.Fatalf("AuthenticateUser: %v", err)
	}

	got, err := svc.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("ID mismatch: got %v, want %v", got.ID, user.ID)
	}
}

// TestGetUserByID_missingUser verifies that GetUserByID returns ErrNotFound.
func TestGetUserByID_missingUser_returnsNotFound(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	_, err := svc.GetUserByID(context.Background(), uuid.New())
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── CreateAPIKey / ValidateAPIKey / ListAPIKeys / DeleteAPIKey ───────────────

// TestCreateAPIKey_validInput verifies that CreateAPIKey returns both the key
// record and a non-empty raw secret.
func TestCreateAPIKey_validInput_returnsKeyAndSecret(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(context.Background(), tenantID, userID, "ci-key", []string{"push", "pull"}, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if key == nil {
		t.Fatal("expected non-nil APIKey")
	}
	if rawSecret == "" {
		t.Error("expected non-empty raw secret")
	}
	if len(rawSecret) != 64 {
		t.Errorf("raw secret length: got %d, want 64 (hex-encoded 32 bytes)", len(rawSecret))
	}
	if key.Name != "ci-key" {
		t.Errorf("Name: got %q, want %q", key.Name, "ci-key")
	}
}

// TestValidateAPIKey_validSecret verifies that the secret returned by
// CreateAPIKey can be verified against the stored hash.
func TestValidateAPIKey_validSecret_returnsKey(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()
	key, rawSecret, err := svc.CreateAPIKey(ctx, tenantID, userID, "my-key", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	validated, err := svc.ValidateAPIKey(ctx, key.ID, rawSecret)
	if err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	if validated.ID != key.ID {
		t.Errorf("ID mismatch: got %v, want %v", validated.ID, key.ID)
	}
}

// TestValidateAPIKey_wrongSecret verifies that an incorrect secret is rejected.
func TestValidateAPIKey_wrongSecret_returnsInvalidCreds(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	key, _, err := svc.CreateAPIKey(ctx, uuid.New(), uuid.New(), "my-key", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	_, err = svc.ValidateAPIKey(ctx, key.ID, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestValidateAPIKey_notFound verifies that a non-existent key ID returns ErrInvalidCredentials.
func TestValidateAPIKey_notFound_returnsInvalidCreds(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	_, err := svc.ValidateAPIKey(context.Background(), uuid.New(), "somerawsecret")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestValidateAPIKey_expiredKey verifies that an expired key is rejected with ErrKeyExpired.
func TestValidateAPIKey_expiredKey_returnsKeyExpired(t *testing.T) {
	svc, _, ar, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	// Directly insert an expired key into the fake repo.
	past := time.Now().Add(-1 * time.Hour)
	ownerID := uuid.New()
	expiredKey := &repository.APIKey{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		UserID:    &ownerID, // UserID is now *uuid.UUID (FE-API-048 Task 6)
		Name:      "expired",
		KeyHash:   "hash",
		IsActive:  true,
		ExpiresAt: &past,
		CreatedAt: time.Now(),
	}
	ar.keys[expiredKey.ID] = expiredKey

	_, err := svc.ValidateAPIKey(context.Background(), expiredKey.ID, "anything")
	if !errors.Is(err, ErrKeyExpired) {
		t.Errorf("expected ErrKeyExpired, got %v", err)
	}
}

// TestListAPIKeys_returnsOwnedKeys verifies that ListAPIKeys returns only the
// keys belonging to the given user.
func TestListAPIKeys_returnsOwnedKeys(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	userID := uuid.New()
	otherUserID := uuid.New()
	tenantID := uuid.New()

	_, _, err := svc.CreateAPIKey(ctx, tenantID, userID, "key-1", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey 1: %v", err)
	}
	_, _, err = svc.CreateAPIKey(ctx, tenantID, userID, "key-2", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey 2: %v", err)
	}
	_, _, err = svc.CreateAPIKey(ctx, tenantID, otherUserID, "other-key", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey other: %v", err)
	}

	keys, err := svc.ListAPIKeys(ctx, userID)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys for userID, got %d", len(keys))
	}
}

// TestDeleteAPIKey_ownKey verifies that a user can delete their own key.
func TestDeleteAPIKey_ownKey_succeeds(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	userID := uuid.New()
	tenantID := uuid.New()

	key, _, err := svc.CreateAPIKey(ctx, tenantID, userID, "my-key", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	if err := svc.DeleteAPIKey(ctx, key.ID, userID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}

	keys, err := svc.ListAPIKeys(ctx, userID)
	if err != nil {
		t.Fatalf("ListAPIKeys after delete: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

// TestDeleteAPIKey_otherUsersKey verifies that deleting another user's key
// fails with ErrNotFound.
func TestDeleteAPIKey_otherUsersKey_returnsNotFound(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	ctx := context.Background()
	ownerID := uuid.New()
	otherID := uuid.New()
	tenantID := uuid.New()

	key, _, err := svc.CreateAPIKey(ctx, tenantID, ownerID, "my-key", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	if err := svc.DeleteAPIKey(ctx, key.ID, otherID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestRevokeToken_alreadyExpired verifies that revoking an already-expired token
// is a no-op (no Redis entry is written, no error returned).
func TestRevokeToken_alreadyExpired_isNoop(t *testing.T) {
	svc, cleanup := setupService(t)
	defer cleanup()

	past := time.Now().Add(-1 * time.Hour)
	claims := &Claims{}
	claims.ExpiresAt = jwt.NewNumericDate(past)

	// Should succeed without error even though the token is already expired.
	if err := svc.RevokeToken(context.Background(), claims); err != nil {
		t.Errorf("RevokeToken on expired token: unexpected error: %v", err)
	}
}
