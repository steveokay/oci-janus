// Package handler_test exercises the auth HTTP handler using httptest.
// Tests construct a *service.Service backed by miniredis and in-memory fakes,
// then drive the handler via HTTP — no real Redis server or PostgreSQL required
// (CLAUDE.md §18).
package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ── Fake implementations of service.UserRepo / service.APIKeyRepo ────────────

// handlerFakeUserRepo implements service.UserRepo for handler tests.
type handlerFakeUserRepo struct {
	users        map[string]*repository.User
	failedLogins map[uuid.UUID]int
	// adminUsers controls which users return an admin role assignment from
	// GetUserRoles. Tests promote a user to admin by calling makeAdmin(uuid).
	adminUsers map[uuid.UUID]bool
}

func newHandlerFakeUserRepo() *handlerFakeUserRepo {
	return &handlerFakeUserRepo{
		users:        make(map[string]*repository.User),
		failedLogins: make(map[uuid.UUID]int),
		adminUsers:   make(map[uuid.UUID]bool),
	}
}

// makeAdmin marks a user as holding an "admin" role in the tenant, so the
// PENTEST-003 gate in createUser (and similar tenant-admin checks) passes.
func (f *handlerFakeUserRepo) makeAdmin(userID uuid.UUID) {
	f.adminUsers[userID] = true
}

func (f *handlerFakeUserRepo) Create(_ context.Context, req repository.CreateUserRequest) (*repository.User, error) {
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
	// REM-018: persist DisplayName through the fake so handler tests that
	// round-trip the create response can assert the field. Empty stays nil
	// to mirror the real SQL NULLIF behaviour.
	if req.DisplayName != "" {
		v := req.DisplayName
		u.DisplayName = &v
	}
	f.users[req.Username] = u
	return u, nil
}

func (f *handlerFakeUserRepo) GetByUsername(_ context.Context, tenantID uuid.UUID, username string) (*repository.User, error) {
	u, ok := f.users[username]
	if !ok || u.TenantID != tenantID {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (f *handlerFakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *handlerFakeUserRepo) GetHumanByID(ctx context.Context, id uuid.UUID) (*repository.User, error) {
	u, err := f.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.Kind != "" && u.Kind != "human" {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (f *handlerFakeUserRepo) GetUserAnyKind(ctx context.Context, id uuid.UUID) (*repository.User, error) {
	return f.GetByID(ctx, id)
}

func (f *handlerFakeUserRepo) RecordFailedLogin(_ context.Context, id uuid.UUID) (int, error) {
	f.failedLogins[id]++
	return f.failedLogins[id], nil
}

func (f *handlerFakeUserRepo) LockUntil(_ context.Context, id uuid.UUID, until time.Time) error {
	for _, u := range f.users {
		if u.ID == id {
			u.LockedUntil = &until
		}
	}
	return nil
}

func (f *handlerFakeUserRepo) ResetFailedLogins(_ context.Context, id uuid.UUID) error {
	delete(f.failedLogins, id)
	return nil
}

// UpdateProfile applies the optional display_name / email mutations against
// the in-memory user. Used by /users/me PATCH tests; mirrors the production
// repository's semantics (empty-string display_name clears, empty email maps
// to empty Email field — handler-level logic decides whether to allow that).
func (f *handlerFakeUserRepo) UpdateProfile(_ context.Context, id uuid.UUID, req repository.UpdateProfileRequest) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID != id {
			continue
		}
		if req.DisplayName != nil {
			if *req.DisplayName == "" {
				u.DisplayName = nil
			} else {
				v := *req.DisplayName
				u.DisplayName = &v
			}
		}
		if req.Email != nil {
			// Reject collision with another user's email in the same tenant
			// so tests can exercise the 409 path.
			for _, other := range f.users {
				if other.ID != id && other.TenantID == u.TenantID && other.Email == *req.Email && *req.Email != "" {
					return nil, repository.ErrAlreadyExists
				}
			}
			u.Email = *req.Email
		}
		return u, nil
	}
	return nil, repository.ErrNotFound
}

// UpdatePasswordHash sets the in-memory password_hash for the user.
func (f *handlerFakeUserRepo) UpdatePasswordHash(_ context.Context, id uuid.UUID, newHash string) error {
	for _, u := range f.users {
		if u.ID == id {
			u.PasswordHash = newHash
			return nil
		}
	}
	return repository.ErrNotFound
}

func (f *handlerFakeUserRepo) GetUserRoles(_ context.Context, userID, tenantID uuid.UUID) ([]repository.RoleAssignment, error) {
	if !f.adminUsers[userID] {
		return nil, nil
	}
	return []repository.RoleAssignment{{
		ID:         uuid.New(),
		TenantID:   tenantID,
		UserID:     userID,
		RoleName:   "admin",
		ScopeType:  "org",
		ScopeValue: "test-org",
	}}, nil
}
func (f *handlerFakeUserRepo) GrantRole(_ context.Context, _ repository.RoleAssignment) error {
	return nil
}
func (f *handlerFakeUserRepo) RevokeRole(_ context.Context, _, _ uuid.UUID) error { return nil }
func (f *handlerFakeUserRepo) RevokeRoleScoped(_ context.Context, _, _ uuid.UUID, _, _ string) error {
	return nil
}
func (f *handlerFakeUserRepo) CountByTenant(_ context.Context, _ uuid.UUID) (int64, error) {
	return int64(len(f.users)), nil
}

// REM-018-followup: handler-level fake just needs to satisfy the
// interface; no handler test paths inspect the result.
func (f *handlerFakeUserRepo) LookupByIDs(_ context.Context, _ uuid.UUID, _ []uuid.UUID) ([]repository.UserSummary, error) {
	return nil, nil
}

// FUT-012 Phase A: same posture as LookupByIDs — handler tests don't
// exercise these paths, but the stubs are required so the fake
// satisfies userRepo. Phase B BFF tests will use a dedicated fake.
func (f *handlerFakeUserRepo) ListTenantUsers(_ context.Context, _ uuid.UUID, _ repository.ListTenantUsersOpts) ([]repository.TenantUserSummary, string, int32, error) {
	return nil, "", 0, nil
}
func (f *handlerFakeUserRepo) CreateInvitedUser(_ context.Context, _ repository.CreateInvitedUserRequest) (*repository.User, error) {
	return nil, nil
}
func (f *handlerFakeUserRepo) SetUserStatus(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}
func (f *handlerFakeUserRepo) DisableAPIKeysForUser(_ context.Context, _, _ uuid.UUID) (int64, error) {
	return 0, nil
}

func (f *handlerFakeUserRepo) ListMembers(_ context.Context, _ uuid.UUID, _, _ string) ([]repository.Member, error) {
	return nil, nil
}

// SSO methods — FE-API-034. Existing handler tests don't exercise these; SSO
// tests live in sso_test.go and use a dedicated fake.
func (f *handlerFakeUserRepo) GetByEmail(_ context.Context, tenantID uuid.UUID, email string) (*repository.User, error) {
	for _, u := range f.users {
		if u.TenantID == tenantID && u.Email == email {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

// GetHumanByEmail mirrors the production kind='human' guard (FE-API-048 T10).
func (f *handlerFakeUserRepo) GetHumanByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*repository.User, error) {
	u, err := f.GetByEmail(ctx, tenantID, email)
	if err != nil {
		return nil, err
	}
	if u.Kind == "service_account" {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (f *handlerFakeUserRepo) CreateSSOUser(_ context.Context, req repository.CreateSSOUserRequest) (*repository.User, error) {
	u := &repository.User{
		ID:        uuid.New(),
		TenantID:  req.TenantID,
		Username:  req.Username,
		Email:     req.Email,
		IsActive:  true,
		CreatedAt: time.Now(),
		// REDESIGN-001 Phase 5.5: capture the IdP subject at creation so the
		// returning-user fast path can match without falling back to email.
		SSOSubject: req.SSOSubject,
	}
	f.users[u.Username] = u
	return u, nil
}

// GetUserBySSOSubject implements the REDESIGN-001 Phase 5.5 lookup so handler
// tests can exercise EnsureSSOUser end-to-end. The fake scans the user store
// rather than maintaining a side map — handler tests are throughput-light and
// the simpler implementation matches the rest of the file's style.
func (f *handlerFakeUserRepo) GetUserBySSOSubject(_ context.Context, _ string, subject string) (*repository.User, error) {
	if subject == "" {
		return nil, repository.ErrNotFound
	}
	for _, u := range f.users {
		if u.SSOSubject == subject && u.Kind != "service_account" {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

// SetSSOSubject back-fills the subject on a pre-Phase-5.5 user row. Matches
// the real repo's contract: refuses to overwrite an existing non-empty
// subject (the service layer guards against mismatches before invoking this).
func (f *handlerFakeUserRepo) SetSSOSubject(_ context.Context, userID uuid.UUID, subject string) error {
	if subject == "" {
		return errors.New("set sso subject: subject is empty")
	}
	for _, u := range f.users {
		if u.ID != userID {
			continue
		}
		if u.SSOSubject != "" {
			return nil
		}
		u.SSOSubject = subject
		return nil
	}
	return repository.ErrNotFound
}

func (f *handlerFakeUserRepo) TouchLastLogin(_ context.Context, _ uuid.UUID) error { return nil }

// SetGlobalAdmin updates is_global_admin on the in-memory user record.
// Satisfies the userRepo interface (REDESIGN-001 Phase 5.1).
func (f *handlerFakeUserRepo) SetGlobalAdmin(_ context.Context, userID uuid.UUID, granted bool) error {
	for _, u := range f.users {
		if u.ID == userID {
			u.IsGlobalAdmin = granted
			return nil
		}
	}
	return repository.ErrNotFound
}

// MarkOnboardingComplete flips OnboardingComplete=true on the in-memory user
// record. Idempotent — re-calling on an already-true row simply returns the
// same row. Satisfies the userRepo interface (REDESIGN-001 Phase 4.3).
func (f *handlerFakeUserRepo) MarkOnboardingComplete(_ context.Context, userID uuid.UUID) (*repository.User, error) {
	for _, u := range f.users {
		if u.ID == userID {
			u.OnboardingComplete = true
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

// handlerFakeAPIKeyRepo implements service.APIKeyRepo for handler tests.
type handlerFakeAPIKeyRepo struct {
	keys map[uuid.UUID]*repository.APIKey
}

func newHandlerFakeAPIKeyRepo() *handlerFakeAPIKeyRepo {
	return &handlerFakeAPIKeyRepo{keys: make(map[uuid.UUID]*repository.APIKey)}
}

func (f *handlerFakeAPIKeyRepo) Create(_ context.Context, req repository.CreateAPIKeyRequest) (*repository.APIKey, error) {
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

func (f *handlerFakeAPIKeyRepo) GetByID(_ context.Context, id uuid.UUID) (*repository.APIKey, error) {
	k, ok := f.keys[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return k, nil
}

func (f *handlerFakeAPIKeyRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]*repository.APIKey, error) {
	var result []*repository.APIKey
	for _, k := range f.keys {
		// UserID is now a pointer; dereference safely before comparing.
		if k.UserID != nil && *k.UserID == userID && k.IsActive {
			result = append(result, k)
		}
	}
	return result, nil
}

func (f *handlerFakeAPIKeyRepo) ListByServiceAccount(_ context.Context, saID uuid.UUID) ([]*repository.APIKey, error) {
	var result []*repository.APIKey
	for _, k := range f.keys {
		if k.ServiceAccountID != nil && *k.ServiceAccountID == saID && k.IsActive {
			result = append(result, k)
		}
	}
	return result, nil
}

func (f *handlerFakeAPIKeyRepo) Delete(_ context.Context, id, userID uuid.UUID) error {
	k, ok := f.keys[id]
	// UserID is now a pointer; treat nil UserID as no match.
	if !ok || k.UserID == nil || *k.UserID != userID {
		return repository.ErrNotFound
	}
	delete(f.keys, id)
	return nil
}

// DeleteByServiceAccount is a no-op stub satisfying the service.APIKeyRepo interface.
// SA-key deletion test coverage is deferred to T13.
func (f *handlerFakeAPIKeyRepo) DeleteByServiceAccount(_ context.Context, _, _ uuid.UUID) error {
	return nil
}

func (f *handlerFakeAPIKeyRepo) TouchLastUsed(_ context.Context, _ uuid.UUID) error { return nil }

// ── Test service builder ──────────────────────────────────────────────────────

// testCtx bundles test infrastructure for HTTP handler tests.
type testCtx struct {
	svc     *service.Service
	users   *handlerFakeUserRepo
	apiKeys *handlerFakeAPIKeyRepo
	mr      *miniredis.Miniredis
}

// buildTestService creates a Service backed by miniredis and in-memory fakes.
// Returns the context and a cleanup func.
func buildTestService(t *testing.T) (*testCtx, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		mr.Close()
		t.Fatalf("generate RSA key: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		mr.Close()
		t.Fatalf("marshal private key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	privB64 := base64.StdEncoding.EncodeToString(privPEM)

	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		mr.Close()
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	pubB64 := base64.StdEncoding.EncodeToString(pubPEM)

	ur := newHandlerFakeUserRepo()
	ar := newHandlerFakeAPIKeyRepo()

	// sa and audit are nil for handler tests: the SA branch of ValidateAPIKey is
	// not exercised by handler-level tests (covered by service-level tests).
	svc, err := service.NewWithFakes(ur, ar, nil, nil, rdb, privB64, pubB64, "test-kid")
	if err != nil {
		mr.Close()
		t.Fatalf("NewWithFakes: %v", err)
	}

	tc := &testCtx{svc: svc, users: ur, apiKeys: ar, mr: mr}
	cleanup := func() {
		_ = rdb.Close()
		mr.Close()
	}
	return tc, cleanup
}

// newTestServer starts an httptest.Server serving the auth handler, returns both
// the server and the test context (for token issuance and state setup).
func newTestServer(t *testing.T) (*httptest.Server, *testCtx) {
	t.Helper()
	tc, cleanup := buildTestService(t)

	mux := http.NewServeMux()
	h := NewHTTPHandler(tc.svc, uuid.Nil)
	h.Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		cleanup()
	})
	return srv, tc
}

// issueTestToken issues a JWT from the service and returns it.
func issueTestToken(t *testing.T, svc *service.Service, userID, tenantID string, access []service.RepositoryAccess) string {
	t.Helper()
	tok, err := svc.IssueToken(context.Background(), userID, tenantID, access, nil, false)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return tok
}

// registerTestUser creates a user via the service and returns their UUID.
func registerTestUser(t *testing.T, svc *service.Service, tenantID uuid.UUID, username, password string) uuid.UUID {
	t.Helper()
	// REM-018: tests don't care about display_name; empty string stores NULL
	// in users.display_name via NULLIF and keeps these fixtures terse.
	user, err := svc.CreateUser(context.Background(), tenantID, username, username+"@test.com", "", password)
	if err != nil {
		t.Fatalf("CreateUser %q: %v", username, err)
	}
	return user.ID
}

// ── JWKS ─────────────────────────────────────────────────────────────────────

// TestJWKS_returnsPublicKey verifies that GET /.well-known/jwks.json returns
// 200 with a key set containing at least one key with kty=RSA.
func TestJWKS_returnsPublicKey(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("GET /jwks.json: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var body struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode JWKS: %v", err)
	}
	if len(body.Keys) == 0 {
		t.Fatal("expected at least one key in JWKS")
	}
	if body.Keys[0].Kty != "RSA" {
		t.Errorf("kty: got %q, want RSA", body.Keys[0].Kty)
	}
	if body.Keys[0].Alg != "RS256" {
		t.Errorf("alg: got %q, want RS256", body.Keys[0].Alg)
	}
	if body.Keys[0].Kid != "test-kid" {
		t.Errorf("kid: got %q, want test-kid", body.Keys[0].Kid)
	}
}

// ── Login ─────────────────────────────────────────────────────────────────────

// TestLogin_missingBody verifies that POST /api/v1/login with invalid JSON returns 400.
func TestLogin_missingBody_returns400(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestLogin_invalidTenantID verifies that a non-UUID tenant_id returns 400.
func TestLogin_invalidTenantID_returns400(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"tenant_id": "not-a-uuid",
		"username":  "alice",
		"password":  "Str0ng!Password123",
	})
	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestLogin_invalidCredentials verifies that wrong credentials return 401.
func TestLogin_invalidCredentials_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"tenant_id": uuid.New().String(),
		"username":  "nobody",
		"password":  "Str0ng!Password123",
	})
	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestLogin_validCredentials verifies that correct credentials return 200 with a token.
func TestLogin_validCredentials_returns200(t *testing.T) {
	srv, tc := newTestServer(t)
	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "alice", password)

	body, _ := json.Marshal(map[string]string{
		"tenant_id": tenantID.String(),
		"username":  "alice",
		"password":  password,
	})
	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["token"] == "" {
		t.Error("expected non-empty token in response")
	}
}

// TestLogin_accountLocked_returns401_noLeakage — PENTEST-005: locked accounts
// must produce the same 401 invalid-credentials response as wrong-password
// failures so an attacker cannot enumerate which accounts are locked.
func TestLogin_accountLocked_returns401_noLeakage(t *testing.T) {
	srv, tc := newTestServer(t)
	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "locked-user", password)

	// Lock the user by recording enough failed logins to trip the lockout.
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		tc.svc.AuthenticateUser(ctx, tenantID, "locked-user", "WrongPass!1") //nolint:errcheck
	}

	body, _ := json.Marshal(map[string]string{
		"tenant_id": tenantID.String(),
		"username":  "locked-user",
		"password":  "WrongPass!1",
	})
	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (locked account must not leak via 403 — PENTEST-005)", resp.StatusCode)
	}
}

// ── Logout ────────────────────────────────────────────────────────────────────

// TestLogout_noAuth verifies that POST /api/v1/logout without a Bearer token returns 401.
func TestLogout_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Post(srv.URL+"/api/v1/logout", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestLogout_validToken verifies that a valid Bearer token results in 204 and
// that the token is revoked afterwards.
func TestLogout_validToken_returns204AndRevokes(t *testing.T) {
	srv, tc := newTestServer(t)

	tok := issueTestToken(t, tc.svc, "user-1", "tenant-1", nil)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	// The token must now be revoked.
	_, err = tc.svc.ValidateToken(context.Background(), tok)
	if err == nil {
		t.Error("expected error after logout (token should be revoked), got nil")
	}
}

// TestLogout_invalidToken verifies that a malformed Bearer token returns 401.
func TestLogout_invalidToken_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/logout", nil)
	req.Header.Set("Authorization", "Bearer totally.invalid.token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /logout: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// ── Create User ───────────────────────────────────────────────────────────────
//
// PENTEST-003 (2026-06-18): POST /api/v1/users now requires a Bearer token
// from an admin/owner in the target tenant. The helper newAdminAuthedRequest
// seeds an admin user, issues a token for it, and returns a request with the
// Authorization header set, so tests focus on the validation/business logic
// being tested rather than re-creating the auth setup each time.

// newAdminAuthedRequest builds a POST /api/v1/users request that carries a
// valid admin token. The returned tenantID matches the JWT's tenant claim.
func newAdminAuthedRequest(t *testing.T, srv *httptest.Server, tc *testCtx, body []byte) (*http.Request, string) {
	t.Helper()
	adminID := uuid.New()
	tenantID := uuid.New()
	tc.users.makeAdmin(adminID)
	tok := issueTestToken(t, tc.svc, adminID.String(), tenantID.String(), nil)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	return req, tenantID.String()
}

// TestCreateUser_noAuth_returns401 — PENTEST-003: anonymous user creation must
// be rejected. This is the *defining* security fix.
func TestCreateUser_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"username": "x",
		"email":    "x@example.com",
		"password": "Str0ng!Password123",
	})
	resp, err := http.Post(srv.URL+"/api/v1/users", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /users: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (unauthenticated creation must be blocked)", resp.StatusCode)
	}
}

// TestCreateUser_callerNotAdmin_returns403 — PENTEST-003: an authenticated but
// non-admin user must be unable to create new accounts.
func TestCreateUser_callerNotAdmin_returns403(t *testing.T) {
	srv, tc := newTestServer(t)

	nonAdminID := uuid.New()
	tenantID := uuid.New()
	tok := issueTestToken(t, tc.svc, nonAdminID.String(), tenantID.String(), nil)

	body, _ := json.Marshal(map[string]string{
		"username": "newuser",
		"email":    "n@example.com",
		"password": "Str0ng!Password123",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (non-admin must be blocked)", resp.StatusCode)
	}
}

// TestCreateUser_crossTenant_returns403 — PENTEST-003: an admin of tenant-A
// must NOT be able to create users in tenant-B by supplying its UUID in the body.
func TestCreateUser_crossTenant_returns403(t *testing.T) {
	srv, tc := newTestServer(t)

	body, _ := json.Marshal(map[string]string{
		"tenant_id": uuid.New().String(), // some OTHER tenant
		"username":  "evil",
		"email":     "evil@example.com",
		"password":  "Str0ng!Password123",
	})
	req, _ := newAdminAuthedRequest(t, srv, tc, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (cross-tenant creation must be blocked)", resp.StatusCode)
	}
}

// TestCreateUser_invalidBody verifies that POST /api/v1/users with invalid JSON
// returns 400 — but only AFTER auth passes (auth comes first).
func TestCreateUser_invalidBody_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	req, _ := newAdminAuthedRequest(t, srv, tc, []byte("{bad json"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestCreateUser_weakPassword verifies that a weak password returns 400 with
// a descriptive error message (policy errors are safe to surface to callers).
func TestCreateUser_weakPassword_returns400WithMessage(t *testing.T) {
	srv, tc := newTestServer(t)
	body, _ := json.Marshal(map[string]string{
		"username": "alice",
		"email":    "alice@example.com",
		"password": "weak",
	})
	req, _ := newAdminAuthedRequest(t, srv, tc, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	var errBody struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if len(errBody.Errors) == 0 {
		t.Error("expected at least one error in response body")
	}
}

// TestCreateUser_validInput verifies that an admin in the target tenant can
// create a new user and gets 201 back with the created details.
func TestCreateUser_validInput_returns201(t *testing.T) {
	srv, tc := newTestServer(t)
	body, _ := json.Marshal(map[string]string{
		"username":     "newuser",
		"email":        "newuser@example.com",
		"display_name": "New User",
		"password":     "Str0ng!Password123",
	})
	req, tenantID := newAdminAuthedRequest(t, srv, tc, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["username"] != "newuser" {
		t.Errorf("username: got %v, want newuser", result["username"])
	}
	if result["tenant_id"] != tenantID {
		t.Errorf("tenant_id: got %v, want %v (must match caller's tenant)", result["tenant_id"], tenantID)
	}
	// REM-018: display_name should round-trip through the create response so
	// the FE can render the new row immediately without a follow-up GET.
	if result["display_name"] != "New User" {
		t.Errorf("display_name: got %v, want %q", result["display_name"], "New User")
	}
}

// REM-018: TestCreateUser_missingDisplayName_returns400 pins the new
// non-empty display_name contract on POST /api/v1/users. Empty display_name
// must fail with 400 BADREQUEST — fail BEFORE we hit the password policy
// check so the caller sees the most specific reason first.
func TestCreateUser_missingDisplayName_returns400(t *testing.T) {
	srv, tc := newTestServer(t)
	body, _ := json.Marshal(map[string]string{
		"username": "needsname",
		"email":    "needsname@example.com",
		"password": "Str0ng!Password123",
		// display_name deliberately absent
	})
	req, _ := newAdminAuthedRequest(t, srv, tc, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (display_name is required)", resp.StatusCode)
	}
}

// TestCreateUser_duplicateUsername verifies that creating a user with an
// already-used username returns 409.
func TestCreateUser_duplicateUsername_returns409(t *testing.T) {
	srv, tc := newTestServer(t)
	// Promote a fixed admin and use the same token for both creates so they
	// share a tenant_id (otherwise the username-uniqueness check would only
	// trigger when the user lookup is also tenant-aware in the fake).
	adminID := uuid.New()
	tenantID := uuid.New()
	tc.users.makeAdmin(adminID)
	tok := issueTestToken(t, tc.svc, adminID.String(), tenantID.String(), nil)

	body, _ := json.Marshal(map[string]string{
		"username":     "duplicate",
		"email":        "dup@example.com",
		"display_name": "Duplicate One",
		"password":     "Str0ng!Password123",
	})
	req1, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users", bytes.NewReader(body))
	req1.Header.Set("Authorization", "Bearer "+tok)
	req1.Header.Set("Content-Type", "application/json")
	resp1, _ := http.DefaultClient.Do(req1)
	resp1.Body.Close()

	body2, _ := json.Marshal(map[string]string{
		"username":     "duplicate",
		"email":        "dup2@example.com",
		"display_name": "Duplicate Two",
		"password":     "Str0ng!Password123",
	})
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/users", bytes.NewReader(body2))
	req2.Header.Set("Authorization", "Bearer "+tok)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST /users (2nd): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("status: got %d, want %d", resp2.StatusCode, http.StatusConflict)
	}
}

// ── API Keys ──────────────────────────────────────────────────────────────────

// TestCreateAPIKey_noAuth verifies that POST /api/v1/apikeys without auth returns 401.
func TestCreateAPIKey_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]any{"name": "my-key", "scopes": []string{"push"}})
	resp, err := http.Post(srv.URL+"/api/v1/apikeys", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestCreateAPIKey_validAuth verifies that a valid Bearer token allows creating
// an API key and returns 201 with the raw secret.
func TestCreateAPIKey_validAuth_returns201WithSecret(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New().String()
	userID := uuid.New().String()
	tok := issueTestToken(t, tc.svc, userID, tenantID, nil)

	body, _ := json.Marshal(map[string]any{"name": "ci-key", "scopes": []string{"push", "pull"}})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/apikeys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["key"] == "" || result["key"] == nil {
		t.Error("expected non-empty raw key in response")
	}
	if result["name"] != "ci-key" {
		t.Errorf("name: got %v, want ci-key", result["name"])
	}
}

// TestListAPIKeys_noAuth verifies that GET /api/v1/apikeys without auth returns 401.
func TestListAPIKeys_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/apikeys")
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestListAPIKeys_validAuth verifies that GET /api/v1/apikeys with a valid token
// returns 200 with a JSON array.
func TestListAPIKeys_validAuth_returns200(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New().String()
	userID := uuid.New().String()
	tok := issueTestToken(t, tc.svc, userID, tenantID, nil)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// ── Delete API Key ────────────────────────────────────────────────────────────

// TestDeleteAPIKey_noAuth verifies that DELETE /api/v1/apikeys/{id} without auth returns 401.
func TestDeleteAPIKey_noAuth_returns401(t *testing.T) {
	srv, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/apikeys/"+uuid.New().String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestDeleteAPIKey_invalidUUID verifies that a non-UUID key ID returns 400.
func TestDeleteAPIKey_invalidUUID_returns400(t *testing.T) {
	srv, tc := newTestServer(t)

	tok := issueTestToken(t, tc.svc, uuid.New().String(), uuid.New().String(), nil)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/apikeys/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestDeleteAPIKey_notFound verifies that deleting a non-existent key returns 404.
func TestDeleteAPIKey_notFound_returns404(t *testing.T) {
	srv, tc := newTestServer(t)

	tok := issueTestToken(t, tc.svc, uuid.New().String(), uuid.New().String(), nil)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/apikeys/"+uuid.New().String(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// ── Token endpoint ────────────────────────────────────────────────────────────

// TestToken_noBasicAuth verifies that POST /auth/token without Basic auth returns
// 401 with WWW-Authenticate header.
func TestToken_noBasicAuth_returns401WithChallenge(t *testing.T) {
	srv, _ := newTestServer(t)

	tenantID := uuid.New().String()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	req.Header.Set("X-Tenant-ID", tenantID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if www := resp.Header.Get("Www-Authenticate"); www == "" {
		t.Error("expected Www-Authenticate header to be set")
	}
}

// TestToken_missingTenantID verifies that POST /auth/token without X-Tenant-ID
// returns 400 when no dev default is configured.
func TestToken_missingTenantID_returns400(t *testing.T) {
	srv, _ := newTestServer(t) // devDefaultTenant is uuid.Nil → no fallback

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	req.SetBasicAuth("user", "pass")
	// No X-Tenant-ID header set.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestToken_validCredentials verifies that correct Basic auth credentials return
// 200 with a token response containing both "token" and "access_token" fields.
func TestToken_validCredentials_returns200(t *testing.T) {
	srv, tc := newTestServer(t)
	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "tokenuser", password)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.SetBasicAuth("tokenuser", password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["token"] == "" || result["token"] == nil {
		t.Error("expected non-empty token field")
	}
	if result["access_token"] == "" || result["access_token"] == nil {
		t.Error("expected non-empty access_token field")
	}
}

// TestToken_getMethod verifies that GET /auth/token also works (Docker clients
// use GET for the token endpoint).
func TestToken_getMethod_handledLikePost(t *testing.T) {
	srv, tc := newTestServer(t)
	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "getuser", password)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/auth/token", nil)
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.SetBasicAuth("getuser", password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/token returned 405; handler must accept GET")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// ── Parsing helpers (internal function tests via same-package access) ────────

// TestParseScopes_emptyInput verifies that parseScopes returns nil for an empty slice.
func TestParseScopes_emptyInput_returnsNil(t *testing.T) {
	result := parseScopes(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

// TestParseScopes_validScope verifies that a well-formed Docker scope string is
// parsed into a RepositoryAccess with the correct fields.
func TestParseScopes_validScope_parsedCorrectly(t *testing.T) {
	result := parseScopes([]string{"repository:myorg/myrepo:pull,push"})
	if len(result) != 1 {
		t.Fatalf("expected 1 access entry, got %d", len(result))
	}
	if result[0].Type != "repository" {
		t.Errorf("Type: got %q, want %q", result[0].Type, "repository")
	}
	if result[0].Name != "myorg/myrepo" {
		t.Errorf("Name: got %q, want %q", result[0].Name, "myorg/myrepo")
	}
	if len(result[0].Actions) != 2 {
		t.Errorf("Actions len: got %d, want 2", len(result[0].Actions))
	}
}

// TestParseScopes_malformedScope verifies that a scope without the required
// colon separators is silently skipped.
func TestParseScopes_malformedScope_skipped(t *testing.T) {
	result := parseScopes([]string{"nocolons"})
	if len(result) != 0 {
		t.Errorf("expected 0 access entries for malformed scope, got %d", len(result))
	}
}

// TestParseScopes_multipleScopes verifies that multiple space-separated scopes
// within one parameter are all parsed.
func TestParseScopes_multipleScopes_allParsed(t *testing.T) {
	result := parseScopes([]string{
		"repository:org/repo1:pull repository:org/repo2:push",
	})
	if len(result) != 2 {
		t.Errorf("expected 2 access entries, got %d", len(result))
	}
}

// TestParseScopes_emptyActionsSkipped verifies that a scope with empty actions
// is skipped (trailing comma only → no actions → skip entry).
func TestParseScopes_emptyActionsSkipped(t *testing.T) {
	// "repository:org/repo:" has no actions after the last colon
	result := parseScopes([]string{"repository:org/repo:"})
	if len(result) != 0 {
		t.Errorf("expected 0 entries when actions are empty, got %d", len(result))
	}
}

// ── remoteIP helper ────────────────────────────────────────────────────────────

// TestRemoteIP_noProxy verifies that when no trusted proxy is configured,
// RemoteAddr is used as the client IP.
func TestRemoteIP_noProxy_usesRemoteAddr(t *testing.T) {
	saved := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = saved }()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.1:12345"

	ip := remoteIP(r)
	if ip != "203.0.113.1" {
		t.Errorf("remoteIP = %q, want 203.0.113.1", ip)
	}
}

// TestRemoteIP_noTrustedProxy_privateAddr verifies that without trusted proxies,
// XFF is not honoured and RemoteAddr is used.
func TestRemoteIP_noTrustedProxy_privateAddr(t *testing.T) {
	saved := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = saved }()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "8.8.8.8")
	ip := remoteIP(r)
	if ip != "10.0.0.1" {
		t.Errorf("expected TCP peer 10.0.0.1, got %q", ip)
	}
}

// TestRemoteIP_noPortInRemoteAddr verifies that remoteIP handles a RemoteAddr
// with no port gracefully (unusual but may occur in tests).
func TestRemoteIP_noPortInRemoteAddr_returnsAddr(t *testing.T) {
	saved := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = saved }()

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.2" // no port
	ip := remoteIP(r)
	// Without a port SplitHostPort fails and the raw value is used.
	if ip == "" {
		t.Error("expected non-empty IP from portless RemoteAddr")
	}
}

// ── Token endpoint additional paths ───────────────────────────────────────────

// TestToken_apiKeyCredentials verifies that the token endpoint accepts an API
// key UUID as the Basic auth username with the raw secret as password.
func TestToken_apiKeyCredentials_returns200(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "apikeyuser", "Str0ng!Password123")

	key, rawSecret, err := tc.svc.CreateAPIKey(
		context.Background(), tenantID, userID,
		"ci-robot", []string{"push"}, nil,
	)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	// Use API key ID as username and raw secret as password.
	req.SetBasicAuth(key.ID.String(), rawSecret)
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestToken_apiKeyWrongSecret verifies that a valid key UUID with a wrong
// secret is rejected with 401.
func TestToken_apiKeyWrongSecret_returns401(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "apikeyuser2", "Str0ng!Password123")

	key, _, err := tc.svc.CreateAPIKey(
		context.Background(), tenantID, userID,
		"ci-robot2", []string{"push"}, nil,
	)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	req.SetBasicAuth(key.ID.String(), "wrong-secret")
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestToken_accountDisabled_returns401_noLeakage — PENTEST-005: a disabled
// account must respond identically to "wrong password" (401 invalid credentials)
// so an attacker can't enumerate which usernames have been disabled.
func TestToken_accountDisabled_returns401_noLeakage(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "disabled-user", password)

	// Disable the user directly via the fake repo.
	for _, u := range tc.users.users {
		if u.Username == "disabled-user" {
			u.IsActive = false
		}
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	req.SetBasicAuth("disabled-user", password)
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (disabled account must not leak via 403 — PENTEST-005)", resp.StatusCode)
	}
}

// TestToken_accountLocked_returns401_noLeakage — PENTEST-005: locked accounts
// must respond identically to wrong-password failures.
func TestToken_accountLocked_returns401_noLeakage(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "locked-token-user", password)

	// Trip the lockout by recording failed logins.
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		tc.svc.AuthenticateUser(ctx, tenantID, "locked-token-user", "WrongPass!1") //nolint:errcheck
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/token", nil)
	req.SetBasicAuth("locked-token-user", "WrongPass!1")
	req.Header.Set("X-Tenant-ID", tenantID.String())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/token: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (locked account must not leak via 403 — PENTEST-005)", resp.StatusCode)
	}
}

// ── Additional createAPIKey paths ─────────────────────────────────────────────

// TestCreateAPIKey_invalidBody verifies that POST /api/v1/apikeys with invalid
// JSON body returns 400.
func TestCreateAPIKey_invalidBody_returns400(t *testing.T) {
	srv, tc := newTestServer(t)

	tok := issueTestToken(t, tc.svc, uuid.New().String(), uuid.New().String(), nil)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/apikeys", strings.NewReader("{bad json"))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// ── deleteAPIKey success path ─────────────────────────────────────────────────

// TestDeleteAPIKey_success verifies that deleting an existing key returns 204.
func TestDeleteAPIKey_success_returns204(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "deleteuser", "Str0ng!Password123")

	// Create an API key to delete.
	key, _, err := tc.svc.CreateAPIKey(context.Background(), tenantID, userID, "to-delete", nil, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	tok := issueTestToken(t, tc.svc, userID.String(), tenantID.String(), nil)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/apikeys/"+key.ID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

// ── listAPIKeys with keys present ─────────────────────────────────────────────

// TestListAPIKeys_withKeys_returnsArray verifies that the list endpoint returns
// a non-empty array when the user has created API keys.
func TestListAPIKeys_withKeys_returnsArray(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	userID := registerTestUser(t, tc.svc, tenantID, "listuser", "Str0ng!Password123")

	_, _, err := tc.svc.CreateAPIKey(context.Background(), tenantID, userID, "key1", []string{"pull"}, nil)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	tok := issueTestToken(t, tc.svc, userID.String(), tenantID.String(), nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/apikeys", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result) == 0 {
		t.Error("expected at least one API key in the list")
	}
}

// ── Login: account disabled ───────────────────────────────────────────────────

// TestLogin_accountDisabled_returns401_noLeakage — PENTEST-005: disabled
// accounts must respond identically to wrong-password failures.
func TestLogin_accountDisabled_returns401_noLeakage(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "inactive-user", password)

	// Disable the user directly via the fake repo.
	for _, u := range tc.users.users {
		if u.Username == "inactive-user" {
			u.IsActive = false
		}
	}

	body, _ := json.Marshal(map[string]string{
		"tenant_id": tenantID.String(),
		"username":  "inactive-user",
		"password":  password,
	})
	resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401 (disabled account must not leak via 403 — PENTEST-005)", resp.StatusCode)
	}
}

// TestAuthenticateUser_unknownUsername_runsDummyVerify — PENTEST-004: when the
// username does not exist, AuthenticateUser must still run an Argon2id verify
// (against a dummy hash) so the response time is comparable to the known-user
// wrong-password path. We assert the timing gap is bounded; a real attacker
// would need many samples to exploit a small difference but a large, obvious
// difference (Argon2 ≈ 100 ms) would be enumeration-grade. A 4× ratio is the
// conservative threshold — generous enough to avoid CI flakiness, tight
// enough to catch a regression that bypasses the dummy verify entirely.
func TestAuthenticateUser_unknownUsername_runsDummyVerify(t *testing.T) {
	tc, cleanup := buildTestService(t)
	defer cleanup()

	tenantID := uuid.New()
	registerTestUser(t, tc.svc, tenantID, "known-user", "Str0ng!Password123")

	// Warm-up call so the dummy hash is generated (sync.Once cost is amortized).
	tc.svc.AuthenticateUser(context.Background(), tenantID, "warm-up-not-real", "x") //nolint:errcheck

	known := timeCall(func() {
		tc.svc.AuthenticateUser(context.Background(), tenantID, "known-user", "Wr0ng!Password") //nolint:errcheck
	})
	unknown := timeCall(func() {
		tc.svc.AuthenticateUser(context.Background(), tenantID, "definitely-not-real", "Wr0ng!Password") //nolint:errcheck
	})

	// `unknown` should be in the same order of magnitude as `known`. If the
	// dummy verify is bypassed, unknown is typically <5 ms vs known >50 ms.
	if known < time.Millisecond {
		t.Fatalf("known-user verify suspiciously fast (%v) — argon2 not running?", known)
	}
	ratio := float64(known) / float64(unknown)
	if ratio > 4 || ratio < 0.25 {
		t.Errorf("timing gap too large: known=%v unknown=%v ratio=%.2f (PENTEST-004 dummy verify likely bypassed)",
			known, unknown, ratio)
	}
}

func timeCall(fn func()) time.Duration {
	start := time.Now()
	fn()
	return time.Since(start)
}

// TestLogin_unknownVsKnown_returnsSameStatusAndBody — PENTEST-005: probing an
// unknown username and a known username with the wrong password must yield
// IDENTICAL responses (same status, same body). This is the explicit oracle
// test: if a regression ever re-introduces a difference, this test catches it.
func TestLogin_unknownVsKnown_returnsSameStatusAndBody(t *testing.T) {
	srv, tc := newTestServer(t)

	tenantID := uuid.New()
	const password = "Str0ng!Password123"
	registerTestUser(t, tc.svc, tenantID, "known-user", password)

	probe := func(username string) (int, string) {
		body, _ := json.Marshal(map[string]string{
			"tenant_id": tenantID.String(),
			"username":  username,
			"password":  "Wr0ng!Password",
		})
		resp, err := http.Post(srv.URL+"/api/v1/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /login: %v", err)
		}
		defer resp.Body.Close()
		buf, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(buf)
	}

	unknownStatus, unknownBody := probe("nonexistent-user-xyz")
	knownStatus, knownBody := probe("known-user")

	if unknownStatus != knownStatus {
		t.Errorf("status differs: unknown=%d known=%d (PENTEST-005 enumeration oracle)", unknownStatus, knownStatus)
	}
	if unknownBody != knownBody {
		t.Errorf("body differs:\n  unknown: %q\n  known:   %q\n(PENTEST-005 enumeration oracle)", unknownBody, knownBody)
	}
}

// ── parseTenantID fallback ────────────────────────────────────────────────────

// TestParseTenantID_invalidUUID_returnsError verifies that an unparseable
// X-Tenant-ID UUID causes parseTenantID to return an error.
func TestParseTenantID_invalidUUID_returnsError(t *testing.T) {
	h := &HTTPHandler{devDefaultTenant: uuid.Nil}
	r := httptest.NewRequest(http.MethodPost, "/auth/token", nil)
	r.Header.Set("X-Tenant-ID", "not-a-uuid")

	_, err := h.parseTenantID(r)
	if err == nil {
		t.Error("expected error for invalid UUID, got nil")
	}
}

// TestParseTenantID_devDefault_usedWhenHeaderAbsent verifies that when
// X-Tenant-ID is absent but a dev default tenant is configured, it is used.
func TestParseTenantID_devDefault_usedWhenHeaderAbsent(t *testing.T) {
	devTenant := uuid.New()
	h := &HTTPHandler{devDefaultTenant: devTenant}
	r := httptest.NewRequest(http.MethodPost, "/auth/token", nil)
	// No X-Tenant-ID header.

	got, err := h.parseTenantID(r)
	if err != nil {
		t.Fatalf("parseTenantID: unexpected error: %v", err)
	}
	if got != devTenant {
		t.Errorf("parseTenantID: got %v, want %v", got, devTenant)
	}
}

// ── isTrustedProxy ────────────────────────────────────────────────────────────

// TestIsTrustedProxy_cidrMatch_returnsTrue verifies that an IP within a
// configured trusted CIDR returns true.
func TestIsTrustedProxy_cidrMatch_returnsTrue(t *testing.T) {
	saved := trustedProxyCIDRs
	defer func() { trustedProxyCIDRs = saved }()

	_, network, _ := net.ParseCIDR("10.0.0.0/8")
	trustedProxyCIDRs = []*net.IPNet{network}

	ip := net.ParseIP("10.0.0.5")
	if !isTrustedProxy(ip) {
		t.Error("expected isTrustedProxy to return true for 10.0.0.5 in 10.0.0.0/8")
	}
}

// TestIsTrustedProxy_outsideCIDR_returnsFalse verifies that an IP outside any
// trusted CIDR returns false.
func TestIsTrustedProxy_outsideCIDR_returnsFalse(t *testing.T) {
	saved := trustedProxyCIDRs
	defer func() { trustedProxyCIDRs = saved }()

	_, network, _ := net.ParseCIDR("10.0.0.0/8")
	trustedProxyCIDRs = []*net.IPNet{network}

	ip := net.ParseIP("203.0.113.1")
	if isTrustedProxy(ip) {
		t.Error("expected isTrustedProxy to return false for 203.0.113.1 outside 10.0.0.0/8")
	}
}

// TestRemoteIP_trustedProxy_honoursXFF verifies that when the TCP peer is a
// trusted proxy, X-Forwarded-For is used to find the real client IP.
func TestRemoteIP_trustedProxy_honoursXFF(t *testing.T) {
	saved := trustedProxyCIDRs
	defer func() { trustedProxyCIDRs = saved }()

	_, network, _ := net.ParseCIDR("10.0.0.0/8")
	trustedProxyCIDRs = []*net.IPNet{network}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9999"                  // trusted proxy IP
	r.Header.Set("X-Forwarded-For", "203.0.113.99") // real client

	ip := remoteIP(r)
	if ip != "203.0.113.99" {
		t.Errorf("expected 203.0.113.99 from XFF via trusted proxy, got %q", ip)
	}
}

// TestRemoteIP_trustedProxy_privateXFF_fallsBackToPeer verifies that when XFF
// only contains private addresses, the TCP peer is used as fallback.
func TestRemoteIP_trustedProxy_privateXFF_fallsBackToPeer(t *testing.T) {
	saved := trustedProxyCIDRs
	defer func() { trustedProxyCIDRs = saved }()

	_, network, _ := net.ParseCIDR("10.0.0.0/8")
	trustedProxyCIDRs = []*net.IPNet{network}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:9999"
	// XFF contains only private addresses — should fall back to peer.
	r.Header.Set("X-Forwarded-For", "192.168.1.1, 172.16.0.1")

	ip := remoteIP(r)
	if ip != "10.0.0.1" {
		t.Errorf("expected fallback to TCP peer 10.0.0.1, got %q", ip)
	}
}

// ── CreateAPIKey with service_account_id (FE-API-048 T17) ────────────────────

// TestHTTP_CreateAPIKey_ForServiceAccount_HappyPath — POST /api/v1/apikeys with
// service_account_id set by an admin caller returns 201 with the raw key.
func TestHTTP_CreateAPIKey_ForServiceAccount_HappyPath(t *testing.T) {
	// Use the SA test environment so saService is wired.
	env := newSATestEnv(t)

	adminTok, adminID := env.issueAdminToken(t)

	// Seed a service account owned by the caller's tenant with an allowed scope.
	sa := env.seedSA("ci-bot", adminID)
	// seedSA uses []string{"read"} as AllowedScopes; our key request uses "read".

	body, _ := json.Marshal(map[string]any{
		"name":               "ci-key",
		"scopes":             []string{"read"},
		"service_account_id": sa.ID.String(),
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/apikeys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// The raw key must be present — shown exactly once.
	if result["key"] == "" || result["key"] == nil {
		t.Error("expected non-empty raw key in response")
	}
	if result["name"] != "ci-key" {
		t.Errorf("name: got %v, want ci-key", result["name"])
	}
	// Verify the audit event was emitted via the SA service.
	if !env.audit.hasAction("service_account.key_issued") {
		t.Error("expected service_account.key_issued audit event")
	}
}

// TestHTTP_CreateAPIKey_ForServiceAccount_RequiresAdmin — a non-admin caller
// (role "reader") posting service_account_id must receive 403 Forbidden.
func TestHTTP_CreateAPIKey_ForServiceAccount_RequiresAdmin(t *testing.T) {
	env := newSATestEnv(t)

	// Seed an SA to provide a valid service_account_id.
	adminID := uuid.New()
	sa := env.seedSA("worker-bot", adminID)

	// Issue a reader (non-admin) token.
	readerTok := env.issueReaderToken(t)

	body, _ := json.Marshal(map[string]any{
		"name":               "should-fail",
		"scopes":             []string{"read"},
		"service_account_id": sa.ID.String(),
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/apikeys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+readerTok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want %d (non-admin must be denied)", resp.StatusCode, http.StatusForbidden)
	}
}

// TestHTTP_CreateAPIKey_ForServiceAccount_InvalidUUID — a malformed
// service_account_id (not a valid UUID) must return 400 Bad Request.
func TestHTTP_CreateAPIKey_ForServiceAccount_InvalidUUID(t *testing.T) {
	env := newSATestEnv(t)

	adminTok, _ := env.issueAdminToken(t)

	body, _ := json.Marshal(map[string]any{
		"name":               "bad-uuid-key",
		"scopes":             []string{"read"},
		"service_account_id": "not-a-valid-uuid",
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/apikeys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d (malformed UUID must be rejected)", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestHTTP_CreateAPIKey_ForServiceAccount_NotFound — a well-formed UUID that
// does not match any SA in the tenant must return 404 Not Found.
func TestHTTP_CreateAPIKey_ForServiceAccount_NotFound(t *testing.T) {
	env := newSATestEnv(t)

	adminTok, _ := env.issueAdminToken(t)

	body, _ := json.Marshal(map[string]any{
		"name":               "ghost-key",
		"scopes":             []string{"read"},
		"service_account_id": uuid.New().String(), // valid UUID, no such SA
	})
	req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/apikeys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminTok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /apikeys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want %d (unknown SA UUID must return 404)", resp.StatusCode, http.StatusNotFound)
	}
}

// ── Suppress unused import errors ─────────────────────────────────────────────
var _ = time.Now
