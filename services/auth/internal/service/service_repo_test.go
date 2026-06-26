// Package service — repository-dependent tests using hand-written fakes.
// All tests in this file use miniredis (no real Redis) and in-memory fakes
// (no real PostgreSQL). They must run without any external infrastructure so
// that plain `go test ./...` always passes (CLAUDE.md §18).
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
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
func (f *fakeUserRepo) ListMembers(_ context.Context, _ uuid.UUID, _, _ string) ([]repository.Member, error) {
	return nil, nil
}
func (f *fakeUserRepo) CountByTenant(_ context.Context, _ uuid.UUID) (int64, error) {
	return int64(len(f.users)), nil
}
// FUT-012 Phase A: service-layer fakes get the same minimal stubs.
// Tests that exercise the new tenant-user methods build dedicated
// fixtures rather than wiring in-memory state into these existing
// fakes (the service-layer tests for FUT-012 use a real
// testcontainers DB via the integration path).
func (f *fakeUserRepo) ListTenantUsers(_ context.Context, _ uuid.UUID, _ repository.ListTenantUsersOpts) ([]repository.TenantUserSummary, string, int32, error) {
	return nil, "", 0, nil
}
func (f *fakeUserRepo) CreateInvitedUser(_ context.Context, _ repository.CreateInvitedUserRequest) (*repository.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) SetUserStatus(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}
func (f *fakeUserRepo) DisableAPIKeysForUser(_ context.Context, _, _ uuid.UUID) (int64, error) {
	return 0, nil
}

func (f *fakeUserRepo) LookupByIDs(_ context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]repository.UserSummary, error) {
	// REM-018-followup fake: filters in-memory users by (tenant_id, id) so
	// LookupUsernames service tests can assert the lookup is tenant-scoped
	// without standing up a real DB.
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]repository.UserSummary, 0, len(ids))
	for _, u := range f.users {
		if u.TenantID != tenantID {
			continue
		}
		if _, ok := want[u.ID]; !ok {
			continue
		}
		dn := ""
		if u.DisplayName != nil {
			dn = *u.DisplayName
		}
		if dn == "" {
			dn = u.Username
		}
		out = append(out, repository.UserSummary{ID: u.ID, Username: u.Username, DisplayName: dn})
	}
	return out, nil
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

// GetHumanByEmail is the kind-guarded variant (FE-API-048, Task 10). It
// returns ErrNotFound for service-account shadow users (kind='service_account')
// so synthetic emails (sa+N@internal.invalid) cannot be matched on the SSO
// login path, mirroring the SQL-layer kind='human' guard in the real repo.
func (f *fakeUserRepo) GetHumanByEmail(_ context.Context, tenantID uuid.UUID, email string) (*repository.User, error) {
	for _, u := range f.users {
		if u.TenantID == tenantID && strings.EqualFold(u.Email, email) {
			// Refuse shadow users — same behaviour as the real GetHumanByEmail
			// which filters by kind='human'.
			if u.Kind == "service_account" {
				return nil, repository.ErrNotFound
			}
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

// GetHumanByID returns ErrNotFound for service_account kind users, matching the
// production repository guard (FE-API-048).
func (f *fakeUserRepo) GetHumanByID(_ context.Context, id uuid.UUID) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			if u.Kind == "service_account" {
				return nil, repository.ErrNotFound
			}
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

// GetUserAnyKind returns any user by ID regardless of kind. Used by the SA
// management path where loading shadow users is intentional.
func (f *fakeUserRepo) GetUserAnyKind(_ context.Context, id uuid.UUID) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

// fakeAPIKeyRepo is an in-memory apiKeyRepo fake.
type fakeAPIKeyRepo struct {
	keys             map[uuid.UUID]*repository.APIKey
	createErr        error
	getByIDErr       error // if set, GetByID returns this instead of looking up
	// poisonTouchLastUsed, when true, makes TouchLastUsed return an error so
	// T7 (TestValidateAPIKey_LastUsedWritebackFailureIsolated) can assert that
	// a writeback failure does not propagate to the caller.
	poisonTouchLastUsed bool
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

// DeleteByServiceAccount is a no-op stub satisfying the apiKeyRepo interface.
// SA-key deletion test coverage is deferred to T13.
func (f *fakeAPIKeyRepo) DeleteByServiceAccount(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (f *fakeAPIKeyRepo) TouchLastUsed(_ context.Context, _ uuid.UUID) error {
	if f.poisonTouchLastUsed {
		return fmt.Errorf("simulated touch-last-used failure")
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// setupServiceWithRepos creates a Service backed by fake repos and miniredis.
// Callers get a fully functional service without any external process.
// It also wires a fakeSARepo and capturingAuditEmitter so the SA branch of
// ValidateAPIKey is exercisable from service_repo_test.go.
func setupServiceWithRepos(t *testing.T) (*Service, *fakeUserRepo, *fakeAPIKeyRepo, func()) {
	t.Helper()
	svc, cleanup := setupService(t)
	ur := newFakeUserRepo()
	ar := newFakeAPIKeyRepo()
	sr := newFakeSARepo()
	ae := &capturingAuditEmitter{}
	// Swap in fakes after construction (struct fields accessible from same package).
	svc.users = ur
	svc.apiKeys = ar
	svc.serviceAccounts = sr
	svc.audit = ae
	return svc, ur, ar, cleanup
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestCreateUser_validInput verifies that a valid password and unique username
// result in a persisted user returned from the service.
func TestCreateUser_validInput_returnsUser(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	tenantID := uuid.New()
	user, err := svc.CreateUser(context.Background(), tenantID, "alice", "alice@example.com", "", "Str0ng!Password123")
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

	_, err := svc.CreateUser(context.Background(), uuid.New(), "bob", "bob@example.com", "", "weak")
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
	_, err := svc.CreateUser(context.Background(), tenantID, "alice", "alice@example.com", "", "Str0ng!Password123")
	if err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, err = svc.CreateUser(context.Background(), tenantID, "alice", "alice2@example.com", "", "Str0ng!Password123")
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
	_, err := svc.CreateUser(context.Background(), tenantID, username, "test@example.com", "", password)
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

	validated, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: key.ID, RawSecret: rawSecret})
	if err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	// ValidatedKey carries UserID (the key owner) rather than the APIKey.ID;
	// assert that the result is for the correct human user.
	if validated.UserID != userID {
		t.Errorf("UserID mismatch: got %v, want %v", validated.UserID, userID)
	}
	if validated.PrincipalKind != "human" {
		t.Errorf("PrincipalKind: got %q, want %q", validated.PrincipalKind, "human")
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

	_, err = svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{
		KeyID:     key.ID,
		RawSecret: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestValidateAPIKey_notFound verifies that a non-existent key ID returns ErrInvalidCredentials.
func TestValidateAPIKey_notFound_returnsInvalidCreds(t *testing.T) {
	svc, _, _, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	_, err := svc.ValidateAPIKey(context.Background(), ValidateAPIKeyOpts{KeyID: uuid.New(), RawSecret: "somerawsecret"})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

// TestValidateAPIKey_expiredKey verifies that an expired key is rejected with ErrKeyExpired.
func TestValidateAPIKey_expiredKey_returnsKeyExpired(t *testing.T) {
	svc, _, ar, cleanup := setupServiceWithRepos(t)
	defer cleanup()

	// Directly insert an expired key into the fake repo. Use a real argon2
	// hash so the verify step (which now runs BEFORE the expiry check to
	// close the timing oracle) passes and the test exercises the expiry
	// branch rather than failing at hash verification.
	rawSecret := "expired-test-secret"
	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		t.Fatalf("argon2pkg.Hash: %v", err)
	}
	past := time.Now().Add(-1 * time.Hour)
	ownerID := uuid.New()
	expiredKey := &repository.APIKey{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		UserID:    &ownerID, // UserID is now *uuid.UUID (FE-API-048 Task 6)
		Name:      "expired",
		KeyHash:   hash,
		IsActive:  true,
		ExpiresAt: &past,
		CreatedAt: time.Now(),
	}
	ar.keys[expiredKey.ID] = expiredKey

	_, err = svc.ValidateAPIKey(context.Background(), ValidateAPIKeyOpts{KeyID: expiredKey.ID, RawSecret: rawSecret})
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

// ── ValidateAPIKey T5 / T6 / T7 / T10 ────────────────────────────────────────
//
// authFakes bundles the fakes needed by the T5–T12 test suite. It exposes
// helper methods for seeding SAs, issuing keys with real argon2id hashes,
// issuing JWTs, and asserting audit state.
type authFakes struct {
	saRepo   *fakeSARepo
	userRepo *fakeUserRepo
	keyRepo  *fakeAPIKeyRepo
	audit    *capturingAuditEmitter
	// redis is the miniredis-backed *redis.Client from the parent Service so
	// tests can seed Redis keys (e.g. revoke:user:<id>) directly.
	redis *redis.Client
}

// buildAuthFakesService wires all fakes into the service and returns the
// populated authFakes. Used by both newAuthService (T) and bench variants (B).
func buildAuthFakesService(svc *Service) *authFakes {
	ur := newFakeUserRepo()
	ar := newFakeAPIKeyRepo()
	sr := newFakeSARepo()
	ae := &capturingAuditEmitter{}

	// Same-package access: wire fakes directly into the unexported struct fields.
	svc.users = ur
	svc.apiKeys = ar
	svc.serviceAccounts = sr
	svc.audit = ae

	// svc.redis is now typed as redisClient (interface) but in normal test setup
	// it is always backed by a *redis.Client (via setupService / miniredis). The
	// type assertion here is intentional: authFakes.redis must be *redis.Client
	// so tests can seed Redis keys directly and so revokeUserErrRedis can embed
	// it as a concrete type.
	rdb, ok := svc.redis.(*redis.Client)
	if !ok {
		panic("buildAuthFakesService: svc.redis is not *redis.Client — was it already replaced with a fake?")
	}
	return &authFakes{saRepo: sr, userRepo: ur, keyRepo: ar, audit: ae, redis: rdb}
}

// newAuthService constructs a Service backed by in-memory fakes and miniredis
// for the T5–T7 unit tests.
func newAuthService(t *testing.T, _ context.Context) (*Service, *authFakes) {
	t.Helper()
	svc, cleanup := setupService(t)
	t.Cleanup(cleanup)
	return svc, buildAuthFakesService(svc)
}

// newAuthServiceB constructs a Service+fakes for the T10 benchmark.
// It constructs a real Service using the benchServiceHelper (defined below)
// to avoid requiring a *testing.T when only a *testing.B is available.
func newAuthServiceB(b *testing.B) (*Service, *authFakes) {
	b.Helper()
	svc, cleanup := newBenchService(b)
	b.Cleanup(cleanup)
	return svc, buildAuthFakesService(svc)
}

// seedSAInTenant creates a service account in the given tenant and returns it.
func (f *authFakes) seedSAInTenant(tenantID uuid.UUID, name string) *repository.ServiceAccount {
	saID := uuid.New()
	shadowID := uuid.New()
	// Register the shadow user so the user repo is consistent.
	f.userRepo.users["shadow:"+shadowID.String()] = &repository.User{
		ID:       shadowID,
		TenantID: tenantID,
		Kind:     "service_account",
	}
	sa := &repository.ServiceAccount{
		ID:            saID,
		TenantID:      tenantID,
		ShadowUserID:  shadowID,
		Name:          name,
		AllowedScopes: []string{"pull", "push"},
		CreatedAt:     time.Now(),
	}
	f.saRepo.accounts[saID] = sa
	return sa
}

// seedSA allocates a fresh tenant for the SA and delegates to seedSAInTenant.
func (f *authFakes) seedSA(name string) *repository.ServiceAccount {
	return f.seedSAInTenant(uuid.New(), name)
}

// issueSAKey inserts a real argon2id-hashed API key for the given SA. Returns
// (keyID, rawSecret) so callers can pass them to ValidateAPIKey.
func (f *authFakes) issueSAKey(sa *repository.ServiceAccount, scopes ...string) (uuid.UUID, string) {
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		panic("issueSAKey: rand.Read: " + err.Error())
	}
	rawSecret := hex.EncodeToString(rawBytes)
	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		panic("issueSAKey: argon2pkg.Hash: " + err.Error())
	}
	k := &repository.APIKey{
		ID:               uuid.New(),
		TenantID:         sa.TenantID,
		ServiceAccountID: &sa.ID,
		Name:             "test-sa-key",
		KeyHash:          hash,
		KeyPrefix:        rawSecret[:12],
		Scopes:           scopes,
		IsActive:         true,
		CreatedAt:        time.Now(),
	}
	f.keyRepo.keys[k.ID] = k
	return k.ID, rawSecret
}

// issueHumanKey seeds a fresh human user + API key. Returns (keyID, rawSecret).
func (f *authFakes) issueHumanKey(username string) (uuid.UUID, string) {
	tenantID := uuid.New()
	userID := uuid.New()
	f.userRepo.users[username] = &repository.User{
		ID:       userID,
		TenantID: tenantID,
		Username: username,
		Email:    username + "@example.com",
		IsActive: true,
		Kind:     "human",
	}
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		panic("issueHumanKey: rand.Read: " + err.Error())
	}
	rawSecret := hex.EncodeToString(rawBytes)
	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		panic("issueHumanKey: argon2pkg.Hash: " + err.Error())
	}
	k := &repository.APIKey{
		ID:       uuid.New(),
		TenantID: tenantID,
		UserID:   &userID,
		Name:     "human-key",
		KeyHash:  hash,
		Scopes:   []string{"pull"},
		IsActive: true,
		CreatedAt: time.Now(),
	}
	f.keyRepo.keys[k.ID] = k
	return k.ID, rawSecret
}

// setAllowedScopes replaces the SA's AllowedScopes in the fake repo.
func (f *authFakes) setAllowedScopes(sa *repository.ServiceAccount, scopes ...string) {
	sa.AllowedScopes = scopes
}

// HasAction returns true when at least one captured audit event has the given action.
func (f *authFakes) HasAction(action string) bool {
	for _, ev := range f.audit.Events {
		if ev.Action == action {
			return true
		}
	}
	return false
}

// issueJWT issues a real RS256 JWT for the given username via svc.IssueToken and
// returns the raw token string plus the parsed claims. The subject of the JWT is
// a freshly generated UUID (simulating a real user ID) embedded in claims.Subject.
// This helper is used by T12 tests that need to inject Redis revoke keys and then
// call ValidateToken to assert rejection.
func (f *authFakes) issueJWT(svc *Service, username string) (string, *Claims) {
	ctx := context.Background()
	userID := uuid.New()
	tenantID := uuid.New()
	token, err := svc.IssueToken(ctx, userID.String(), tenantID.String(), nil, nil)
	if err != nil {
		panic("issueJWT: IssueToken failed for " + username + ": " + err.Error())
	}
	// Parse the signed token to obtain the claims so callers can use
	// claims.Subject as the key for revoke:user:<sub>.
	var claims Claims
	// ParseWithClaims validates the signature against svc.pubKey; we access it
	// via the same-package field since both this file and auth.go are in package service.
	tok, err := jwt.ParseWithClaims(token, &claims, func(_ *jwt.Token) (any, error) {
		return svc.pubKey, nil
	})
	if err != nil || !tok.Valid {
		panic("issueJWT: parse claims failed: " + err.Error())
	}
	return token, &claims
}

// ── T5: cross-tenant guard ────────────────────────────────────────────────────

// TestValidateAPIKey_CrossTenantGuard_T5 verifies that an SA key used with a
// mismatched X-Tenant-ID is rejected with ErrInvalidCredentials and that a
// pentest.cross_tenant_attempt audit event is emitted (spec §5.4 / H1).
func TestValidateAPIKey_CrossTenantGuard_T5(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newAuthService(t, ctx)
	tenantA, tenantB := uuid.New(), uuid.New()

	// Seed an SA in tenantB, issue a key.
	sa := fakes.seedSAInTenant(tenantB, "ci")
	keyID, rawSecret := fakes.issueSAKey(sa, "pull", "push")

	// Attempt to validate the key while claiming tenantA.
	_, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{
		KeyID:           keyID,
		RawSecret:       rawSecret,
		RequestTenantID: &tenantA,
	})
	require.Error(t, err, "cross-tenant SA key must be rejected")
	require.ErrorIs(t, err, ErrInvalidCredentials, "must return ErrInvalidCredentials")

	// The attempt must be recorded in the audit log.
	require.True(t, fakes.HasAction("pentest.cross_tenant_attempt"),
		"cross-tenant attempt must emit audit event")
}

// ── T6: scope intersection ────────────────────────────────────────────────────

// TestValidateAPIKey_ScopeIntersection_T6 verifies that the effective scopes
// returned by ValidateAPIKey are the intersection of the key's scopes and the
// SA's current AllowedScopes (spec §5.4).
func TestValidateAPIKey_ScopeIntersection_T6(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newAuthService(t, ctx)

	// SA initially allows pull+push; key is issued with both.
	sa := fakes.seedSA("c")
	keyID, secret := fakes.issueSAKey(sa, "pull", "push")

	// Admin narrows the SA's allowlist to pull-only after key issuance.
	fakes.setAllowedScopes(sa, "pull")

	vk, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: keyID, RawSecret: secret})
	require.NoError(t, err)
	require.Equal(t, []string{"pull"}, vk.EffectiveScopes,
		"effective scopes must be intersected with SA AllowedScopes")
	require.Equal(t, "service_account", vk.PrincipalKind)
}

// ── T7: last_used writeback failure isolation ─────────────────────────────────

// TestValidateAPIKey_LastUsedWritebackFailureIsolated_T7 verifies that a
// failure in the fire-and-forget last_used writeback does not cause
// ValidateAPIKey to return an error (spec §5.4 — best-effort).
func TestValidateAPIKey_LastUsedWritebackFailureIsolated_T7(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newAuthService(t, ctx)

	// Poison TouchLastUsed so the goroutine will error.
	fakes.keyRepo.poisonTouchLastUsed = true
	keyID, secret := fakes.issueHumanKey("alice")

	vk, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: keyID, RawSecret: secret})
	require.NoError(t, err, "TouchLastUsed failure must not propagate to the caller")
	require.Equal(t, "human", vk.PrincipalKind)
}

// ── T12: ValidateToken revoke:user check ──────────────────────────────────────

// TestValidateToken_RespectsUserRevoke verifies that when the Redis key
// "revoke:user:<sub>" is set, ValidateToken rejects the token with
// codes.Unauthenticated. This covers the SA disable flow (spec §5.5): T8's
// SetDisabled writes the same key; this test asserts that ValidateToken reads it.
func TestValidateToken_RespectsUserRevoke(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newAuthService(t, ctx)

	// Issue a real JWT and capture the claims so we know the subject UUID.
	token, claims := fakes.issueJWT(svc, "alice")

	// Manually set the revoke key — simulates what SetDisabled does when an SA
	// (or any principal's shadow user) is disabled.
	require.NoError(t, fakes.redis.Set(ctx, "revoke:user:"+claims.Subject, "1", time.Minute).Err())

	// ValidateToken must now reject the token.
	_, err := svc.ValidateToken(ctx, token)
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err),
		"revoked principal must return codes.Unauthenticated")
}

// TestValidateToken_NoRevokeKey_Succeeds verifies that absent revoke:user:<sub>
// does not break the normal validate-token path — the positive case for T12.
func TestValidateToken_NoRevokeKey_Succeeds(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newAuthService(t, ctx)

	// Issue a valid JWT; no revoke key is set.
	token, claims := fakes.issueJWT(svc, "bob")

	// Ensure no stale key exists for this subject.
	_ = fakes.redis.Del(ctx, "revoke:user:"+claims.Subject)

	// ValidateToken must succeed and return the same subject.
	got, err := svc.ValidateToken(ctx, token)
	require.NoError(t, err, "valid token without revoke key must succeed")
	require.Equal(t, claims.Subject, got.Subject,
		"returned claims must carry the original subject")
}

// ── T12b: Redis fail-closed on principal-revocation check (Review §B) ────────

// revokeUserErrRedis is a test-only redisClient implementation that wraps a
// real *redis.Client (backed by miniredis) but injects a synthetic error for
// any Get call whose key begins with "revoke:user:". This lets us isolate the
// principal-revocation check in ValidateToken from the JTI-revocation check
// (which uses a "jwt:revoked:" prefix) and observe that a Redis error on the
// principal check causes ValidateToken to fail CLOSED with codes.Unavailable.
//
// All other methods (Set, Del, Pipeline, SMembers) delegate transparently to
// the underlying *redis.Client so the rest of the Service remains functional.
type revokeUserErrRedis struct {
	*redis.Client
}

// Get returns an injected error for "revoke:user:*" keys and delegates to the
// real client for all other keys (including the JTI revocation check).
func (r *revokeUserErrRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	if len(key) > len("revoke:user:") && key[:len("revoke:user:")] == "revoke:user:" {
		// Return a non-Nil error to exercise the fail-closed branch in ValidateToken.
		return redis.NewStringResult("", errors.New("injected Redis unavailable error"))
	}
	return r.Client.Get(ctx, key)
}

// TestValidateToken_RedisError_FailsClosed verifies Review §B: when the Redis
// call for the principal-revocation check returns a real error (not redis.Nil),
// ValidateToken must fail CLOSED with codes.Unavailable rather than allowing
// the token through.
//
// Human users have no second-layer DB check (only SA principals get a
// ValidateAPIKey DB lookup), so failing open on a Redis error in the
// principal-revocation check would silently allow a disabled human user to
// continue using their JWT for up to the full JWT TTL (5 minutes).
//
// Technique: we swap in a revokeUserErrRedis wrapper that injects an error
// only for "revoke:user:" key Gets, leaving JTI-revocation Gets (which use the
// "jwt:revoked:" prefix) backed by the real miniredis. This isolates the exact
// branch under test.
func TestValidateToken_RedisError_FailsClosed(t *testing.T) {
	ctx := context.Background()

	// Build a normal service (backed by real miniredis and RSA keys).
	svc, fakes := newAuthService(t, ctx)

	// Swap the redis field for a wrapper that injects errors on "revoke:user:" Gets.
	// Same-package access allows writing the unexported field directly.
	svc.redis = &revokeUserErrRedis{Client: fakes.redis}

	// Issue a valid JWT. IssueToken does not write to Redis, so the swap does
	// not affect token issuance. The token is signed and structurally valid.
	token, _ := fakes.issueJWT(svc, "human-user")

	// ValidateToken must fail CLOSED with codes.Unavailable:
	//   1. isRevoked (JTI check, "jwt:revoked:" prefix) → returns redis.Nil → not
	//      revoked → continues normally.
	//   2. principal-revocation check ("revoke:user:" prefix) → injected error →
	//      must fail closed with codes.Unavailable.
	_, validateErr := svc.ValidateToken(ctx, token)
	require.Error(t, validateErr,
		"injected Redis error on principal-revocation check must cause ValidateToken to return an error")
	require.Equal(t, codes.Unavailable, status.Code(validateErr),
		"Redis error on revoke:user: check must return codes.Unavailable (fail closed, Review §B)")
}

// newBenchService is the *testing.B counterpart of setupService. It shares
// the same construction logic (miniredis + RSA) but accepts a *testing.B so
// that Fatalf / Cleanup go to the right destination.
// It lives in this file so the benchmark below can call it without importing
// additional packages — miniredis and redis are already imported via
// service_redis_test.go (same package).
func newBenchService(b *testing.B) (*Service, func()) {
	b.Helper()

	// We can call setupService here by passing a fresh *testing.T because
	// b.Fatalf and t.Fatalf use the same underlying mechanism when the test
	// binary panics on failure. setupService is in service_redis_test.go and
	// is visible here since both files are in package service.
	//
	// NOTE: using a zero-value *testing.T is intentional and safe here because
	// setupService only calls t.Fatalf (which panics in tests) and t.Helper
	// (which is a no-op on a zero value). Any failure in setupService will
	// surface as a panic that b.Run will attribute to the benchmark.
	t := &testing.T{}
	return setupService(t)
}

// BenchmarkValidateAPIKey_T10 measures the end-to-end cost of ValidateAPIKey
// for a human-owned key (argon2id verify + in-memory fake lookup).
// Run with: go test ./internal/service/ -bench BenchmarkValidateAPIKey_T10 -benchmem
func BenchmarkValidateAPIKey_T10(b *testing.B) {
	// Use newAuthServiceB for a clean service with all fakes wired.
	svc, fakes := newAuthServiceB(b)
	keyID, secret := fakes.issueHumanKey("bench-alice")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := svc.ValidateAPIKey(context.Background(), ValidateAPIKeyOpts{
			KeyID:     keyID,
			RawSecret: secret,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
