// http_service_accounts_test.go — HTTP handler tests for FE-API-048 T13.
//
// Tests cover the five CRUD handlers (list/create/get/update/delete) plus the
// 501-when-not-wired path. They reuse the shared fakes (handlerFakeUserRepo,
// handlerFakeAPIKeyRepo, buildTestService, issueTestToken) from http_test.go
// and add a minimal in-memory SA repo (handlerFakeSARepo) that is local to
// this file.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// ── In-memory SA repo (handler test variant) ─────────────────────────────────

// handlerFakeSARepo is a minimal in-memory implementation of service.saRepo
// used exclusively by SA handler tests. It is separate from the service-layer
// fakeSARepo so that both test packages can co-exist without symbol conflicts.
type handlerFakeSARepo struct {
	// accounts maps SA id → SA row.
	accounts map[uuid.UUID]*repository.ServiceAccount
	// nameConflict, when true, causes CreateAtomic and Update to return
	// repository.ErrAlreadyExists instead of inserting / updating. Use this to
	// exercise the 409 Conflict path.
	nameConflict bool
}

// newHandlerFakeSARepo allocates a fresh, empty handlerFakeSARepo.
func newHandlerFakeSARepo() *handlerFakeSARepo {
	return &handlerFakeSARepo{accounts: make(map[uuid.UUID]*repository.ServiceAccount)}
}

// CreateAtomic inserts a new SA and returns a synthesised shadow user UUID.
// Returns ErrAlreadyExists when f.nameConflict is set.
func (f *handlerFakeSARepo) CreateAtomic(
	_ context.Context,
	in repository.CreateServiceAccountInput,
) (*repository.ServiceAccount, uuid.UUID, error) {
	if f.nameConflict {
		return nil, uuid.Nil, repository.ErrAlreadyExists
	}
	saID := uuid.New()
	shadowID := uuid.New()
	scopes := in.AllowedScopes
	if scopes == nil {
		scopes = []string{}
	}
	cb := in.CreatedBy
	sa := &repository.ServiceAccount{
		ID:            saID,
		TenantID:      in.TenantID,
		ShadowUserID:  shadowID,
		Name:          in.Name,
		Description:   in.Description,
		AllowedScopes: scopes,
		CreatedBy:     &cb,
		CreatedAt:     time.Now(),
	}
	f.accounts[saID] = sa
	return sa, shadowID, nil
}

// Get returns the SA with the given primary key, or ErrNotFound.
func (f *handlerFakeSARepo) Get(_ context.Context, id uuid.UUID) (*repository.ServiceAccount, error) {
	sa, ok := f.accounts[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return sa, nil
}

// List returns SAs for the tenant, honouring the includeDisabled and pageSize
// parameters. pageToken is ignored (single-page fake).
func (f *handlerFakeSARepo) List(
	_ context.Context,
	tenantID uuid.UUID,
	includeDisabled bool,
	pageSize int,
	_ string,
) ([]repository.ServiceAccountWithStats, string, error) {
	var out []repository.ServiceAccountWithStats
	for _, sa := range f.accounts {
		if sa.TenantID != tenantID {
			continue
		}
		if !includeDisabled && sa.DisabledAt != nil {
			continue
		}
		out = append(out, repository.ServiceAccountWithStats{ServiceAccount: *sa})
	}
	if pageSize > 0 && len(out) > pageSize {
		out = out[:pageSize]
	}
	return out, "", nil
}

// Update applies partial mutations to the stored SA. Returns ErrNotFound when
// the (id, tenantID) pair doesn't match and ErrAlreadyExists when nameConflict
// is set and a name change is requested.
func (f *handlerFakeSARepo) Update(
	_ context.Context,
	in repository.UpdateServiceAccountInput,
) (*repository.ServiceAccount, error) {
	sa, ok := f.accounts[in.ID]
	if !ok || sa.TenantID != in.TenantID {
		return nil, repository.ErrNotFound
	}
	if in.Name != nil {
		if f.nameConflict {
			return nil, repository.ErrAlreadyExists
		}
		sa.Name = *in.Name
	}
	if in.Description != nil {
		sa.Description = *in.Description
	}
	if in.AllowedScopes != nil {
		sa.AllowedScopes = *in.AllowedScopes
	}
	if in.Disabled != nil {
		if *in.Disabled {
			now := time.Now()
			sa.DisabledAt = &now
		} else {
			sa.DisabledAt = nil
		}
	}
	return sa, nil
}

// Delete hard-deletes the SA row, returning ErrNotFound when absent.
func (f *handlerFakeSARepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := f.accounts[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.accounts, id)
	return nil
}

// CountKeysAffectedByScopeShrink always returns 0 — not exercised by handler
// tests (that path is covered by service-layer tests in T8).
func (f *handlerFakeSARepo) CountKeysAffectedByScopeShrink(
	_ context.Context, _ uuid.UUID, _ []string,
) (int64, error) {
	return 0, nil
}

// ── redisAdapterH ────────────────────────────────────────────────────────────

// redisAdapterH adapts *redis.Client to service.RedisCmdable for handler tests.
// Named with an "H" suffix to avoid clashing with the service-layer redisAdapter
// defined in service_account_test.go (which lives in package service, not handler).
type redisAdapterH struct {
	rdb *redis.Client
}

func newRedisAdapterH(rdb *redis.Client) service.RedisCmdable {
	return &redisAdapterH{rdb: rdb}
}

func (a *redisAdapterH) Set(
	ctx context.Context, key string, value interface{}, expiration time.Duration,
) interface{ Err() error } {
	return a.rdb.Set(ctx, key, value, expiration)
}

func (a *redisAdapterH) Del(ctx context.Context, keys ...string) interface{ Err() error } {
	return a.rdb.Del(ctx, keys...)
}

// ── capturingAuditEmitterH ────────────────────────────────────────────────────

// capturingAuditEmitterH accumulates AuditEvents emitted during a test.
// Named with an "H" suffix to avoid clashing with the service-layer type.
type capturingAuditEmitterH struct {
	Events []service.AuditEvent
}

// Emit records ev and always returns nil (no simulated failures needed here).
func (a *capturingAuditEmitterH) Emit(_ context.Context, ev service.AuditEvent) error {
	a.Events = append(a.Events, ev)
	return nil
}

// hasAction returns true when at least one recorded event has the given action.
func (a *capturingAuditEmitterH) hasAction(action string) bool {
	for _, ev := range a.Events {
		if ev.Action == action {
			return true
		}
	}
	return false
}

// ── SA test server builder ────────────────────────────────────────────────────

// saTestEnv bundles all the pieces needed to drive SA handler tests.
type saTestEnv struct {
	// srv is the running test HTTP server.
	srv *httptest.Server
	// tc holds the core auth service context (users, apiKeys, svc).
	tc *testCtx
	// saRepo is the in-memory SA store; tests can inspect/seed it directly.
	saRepo *handlerFakeSARepo
	// audit captures all events emitted during the test.
	audit *capturingAuditEmitterH
	// tenantID is the fixed tenant used by issueAdminToken and issueReaderToken.
	tenantID uuid.UUID
}

// newSATestEnv starts an httptest.Server whose HTTPHandler has a fully-wired
// ServiceAccountService backed by in-memory fakes. Returns a cleanup function.
func newSATestEnv(t *testing.T) *saTestEnv {
	t.Helper()

	// Build the core auth service (miniredis + fake user/key repos).
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	// Start a second miniredis for the SA service's revoke-key path.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run (SA): %v", err)
	}
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	saRepo := newHandlerFakeSARepo()
	audit := &capturingAuditEmitterH{}

	// Build the ServiceAccountService backed by the handler fakes.
	// Note: the user repo and key repo reuse the same instances as tc.svc so
	// that tokens issued by tc.svc can be matched back to users the SA service
	// sees when loading creator snapshots.
	saSvc := service.NewServiceAccountService(saRepo, tc.users, tc.apiKeys, audit, newRedisAdapterH(rdb))

	tenantID := uuid.New()

	mux := http.NewServeMux()
	h := NewHTTPHandler(tc.svc, tenantID).WithServiceAccountService(saSvc)
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &saTestEnv{
		srv:      srv,
		tc:       tc,
		saRepo:   saRepo,
		audit:    audit,
		tenantID: tenantID,
	}
}

// issueAdminToken seeds an admin user in the in-memory user store and returns
// a JWT that will pass the requireSAAdmin gate. The user is marked as admin
// via handlerFakeUserRepo.makeAdmin so callerIsTenantAdmin returns true.
func (e *saTestEnv) issueAdminToken(t *testing.T) (token string, userID uuid.UUID) {
	t.Helper()
	userID = uuid.New()
	e.tc.users.makeAdmin(userID)
	u := &repository.User{
		ID:       userID,
		TenantID: e.tenantID,
		Username: fmt.Sprintf("admin-%s", userID.String()[:8]),
		Email:    fmt.Sprintf("%s@test.example", userID.String()[:8]),
		IsActive: true,
		Kind:     "human",
	}
	e.tc.users.users[u.Username] = u
	tok, err := e.tc.svc.IssueToken(context.Background(), userID.String(), e.tenantID.String(), nil, []string{"admin"})
	if err != nil {
		t.Fatalf("IssueToken (admin): %v", err)
	}
	return tok, userID
}

// issueReaderToken seeds a non-admin user and returns a JWT with role "reader".
// Calls to requireSAAdmin with this token will return 403.
func (e *saTestEnv) issueReaderToken(t *testing.T) string {
	t.Helper()
	userID := uuid.New()
	// Do NOT call makeAdmin — this user has no admin role.
	u := &repository.User{
		ID:       userID,
		TenantID: e.tenantID,
		Username: fmt.Sprintf("reader-%s", userID.String()[:8]),
		Email:    fmt.Sprintf("r%s@test.example", userID.String()[:8]),
		IsActive: true,
		Kind:     "human",
	}
	e.tc.users.users[u.Username] = u
	tok, err := e.tc.svc.IssueToken(context.Background(), userID.String(), e.tenantID.String(), nil, []string{"reader"})
	if err != nil {
		t.Fatalf("IssueToken (reader): %v", err)
	}
	return tok
}

// seedSA inserts a service account directly into the fake SA repo without going
// through the service layer (no audit event). Used by tests that need a pre-
// existing SA.
func (e *saTestEnv) seedSA(name string, actorID uuid.UUID) *repository.ServiceAccount {
	saID := uuid.New()
	shadowID := uuid.New()
	sa := &repository.ServiceAccount{
		ID:            saID,
		TenantID:      e.tenantID,
		ShadowUserID:  shadowID,
		Name:          name,
		AllowedScopes: []string{"read"},
		CreatedBy:     &actorID,
		CreatedAt:     time.Now(),
	}
	e.saRepo.accounts[saID] = sa
	return sa
}

// doSAReq issues an HTTP request to e.srv with optional Bearer token and body.
func doSAReq(t *testing.T, env *saTestEnv, method, path, token string, body any) *http.Response {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, env.srv.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestHTTP_CreateServiceAccount_RequiresAdmin verifies that a non-admin caller
// (role "reader") receives 403 Forbidden when attempting to create a SA.
func TestHTTP_CreateServiceAccount_RequiresAdmin(t *testing.T) {
	env := newSATestEnv(t)
	readerTok := env.issueReaderToken(t)

	resp := doSAReq(t, env, http.MethodPost, "/api/v1/service-accounts", readerTok, map[string]any{
		"name":           "ci-bot",
		"allowed_scopes": []string{"read"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want %d (Forbidden)", resp.StatusCode, http.StatusForbidden)
	}
}

// TestHTTP_CreateServiceAccount_HappyPath verifies that an admin caller can
// create a service account and receives 201 Created with the SA fields plus
// a service_account.created audit event.
func TestHTTP_CreateServiceAccount_HappyPath(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, _ := env.issueAdminToken(t)

	resp := doSAReq(t, env, http.MethodPost, "/api/v1/service-accounts", adminTok, map[string]any{
		"name":           "deploy-bot",
		"description":    "CI deploy key",
		"allowed_scopes": []string{"push", "pull"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := json.Marshal(nil)
		_ = body
		t.Errorf("status: got %d, want %d (Created)", resp.StatusCode, http.StatusCreated)
	}

	var got serviceAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID == "" {
		t.Error("response.id must be non-empty")
	}
	if got.Name != "deploy-bot" {
		t.Errorf("name: got %q, want %q", got.Name, "deploy-bot")
	}
	if got.TenantID != env.tenantID.String() {
		t.Errorf("tenant_id: got %q, want %q", got.TenantID, env.tenantID)
	}

	// Verify audit event was recorded.
	if !env.audit.hasAction("service_account.created") {
		t.Error("expected service_account.created audit event, none recorded")
	}
}

// TestHTTP_CreateServiceAccount_InvalidName_400 verifies that a name that
// violates the allowlist regex (uppercase letters, spaces) returns 400.
func TestHTTP_CreateServiceAccount_InvalidName_400(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, _ := env.issueAdminToken(t)

	resp := doSAReq(t, env, http.MethodPost, "/api/v1/service-accounts", adminTok, map[string]any{
		"name": "Bad Name", // uppercase + space — invalid
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (Bad Request)", resp.StatusCode)
	}
}

// TestHTTP_GetServiceAccount_NotFound verifies that requesting a SA by an
// unknown UUID returns 404 Not Found.
func TestHTTP_GetServiceAccount_NotFound(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, _ := env.issueAdminToken(t)

	resp := doSAReq(t, env, http.MethodGet,
		"/api/v1/service-accounts/"+uuid.New().String(), adminTok, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

// TestHTTP_GetServiceAccount_HappyPath verifies that an existing SA is returned
// with 200 OK and the correct name and tenant fields.
func TestHTTP_GetServiceAccount_HappyPath(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, adminID := env.issueAdminToken(t)
	sa := env.seedSA("scanner-bot", adminID)

	resp := doSAReq(t, env, http.MethodGet,
		"/api/v1/service-accounts/"+sa.ID.String(), adminTok, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var got serviceAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != sa.ID.String() {
		t.Errorf("id: got %q, want %q", got.ID, sa.ID)
	}
	if got.Name != "scanner-bot" {
		t.Errorf("name: got %q, want %q", got.Name, "scanner-bot")
	}
	if got.TenantID != env.tenantID.String() {
		t.Errorf("tenant_id: got %q, want %q", got.TenantID, env.tenantID)
	}
}

// TestHTTP_UpdateServiceAccount_DescriptionChange verifies that PATCHing only
// the description field returns 200 OK with the updated description value.
func TestHTTP_UpdateServiceAccount_DescriptionChange(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, adminID := env.issueAdminToken(t)
	sa := env.seedSA("desc-bot", adminID)

	newDesc := "updated description"
	resp := doSAReq(t, env, http.MethodPatch,
		"/api/v1/service-accounts/"+sa.ID.String(), adminTok, map[string]any{
			"description": newDesc,
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var got serviceAccountResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Description != newDesc {
		t.Errorf("description: got %q, want %q", got.Description, newDesc)
	}
	// Name must be unchanged.
	if got.Name != "desc-bot" {
		t.Errorf("name: got %q, want %q (should be unchanged)", got.Name, "desc-bot")
	}
}

// TestHTTP_UpdateServiceAccount_NameConflict verifies that a name collision on
// PATCH returns 409 Conflict.
func TestHTTP_UpdateServiceAccount_NameConflict(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, adminID := env.issueAdminToken(t)
	sa := env.seedSA("original-bot", adminID)

	// Flip the repo into name-conflict mode so the next Update returns ErrAlreadyExists.
	env.saRepo.nameConflict = true

	resp := doSAReq(t, env, http.MethodPatch,
		"/api/v1/service-accounts/"+sa.ID.String(), adminTok, map[string]any{
			"name": "existing-name",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status: got %d, want 409 (Conflict)", resp.StatusCode)
	}
}

// TestHTTP_DeleteServiceAccount_HappyPath verifies that DELETE on a known SA
// returns 204 No Content and the SA is no longer retrievable.
func TestHTTP_DeleteServiceAccount_HappyPath(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, adminID := env.issueAdminToken(t)
	sa := env.seedSA("ephemeral-bot", adminID)

	resp := doSAReq(t, env, http.MethodDelete,
		"/api/v1/service-accounts/"+sa.ID.String(), adminTok, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status: got %d, want 204", resp.StatusCode)
	}

	// Confirm the SA is gone — subsequent GET must return 404.
	resp2 := doSAReq(t, env, http.MethodGet,
		"/api/v1/service-accounts/"+sa.ID.String(), adminTok, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE status: got %d, want 404", resp2.StatusCode)
	}

	// Audit must record the delete event.
	if !env.audit.hasAction("service_account.deleted") {
		t.Error("expected service_account.deleted audit event, none recorded")
	}
}

// TestHTTP_ListServiceAccounts_ReturnsForTenant verifies that LIST returns only
// the SAs belonging to the caller's tenant and that the response envelope
// contains the "service_accounts" array.
func TestHTTP_ListServiceAccounts_ReturnsForTenant(t *testing.T) {
	env := newSATestEnv(t)
	adminTok, adminID := env.issueAdminToken(t)

	// Seed two SAs in the caller's tenant.
	env.seedSA("bot-one", adminID)
	env.seedSA("bot-two", adminID)

	// Seed one SA in a different tenant — should NOT appear in the response.
	otherTenantID := uuid.New()
	otherSAID := uuid.New()
	otherShadowID := uuid.New()
	env.saRepo.accounts[otherSAID] = &repository.ServiceAccount{
		ID:            otherSAID,
		TenantID:      otherTenantID,
		ShadowUserID:  otherShadowID,
		Name:          "foreign-bot",
		AllowedScopes: []string{},
		CreatedAt:     time.Now(),
	}

	resp := doSAReq(t, env, http.MethodGet, "/api/v1/service-accounts", adminTok, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var envelope struct {
		ServiceAccounts []serviceAccountResponse `json:"service_accounts"`
		NextPageToken   string                   `json:"next_page_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(envelope.ServiceAccounts) != 2 {
		t.Errorf("service_accounts count: got %d, want 2", len(envelope.ServiceAccounts))
	}
	// Verify all returned SAs belong to the correct tenant.
	for _, s := range envelope.ServiceAccounts {
		if s.TenantID != env.tenantID.String() {
			t.Errorf("SA %q has tenant_id %q, want %q", s.Name, s.TenantID, env.tenantID)
		}
	}
}

// TestHTTP_ServiceAccountRoutes_When_NoSAService_Return501 verifies that all
// SA routes return 501 Not Implemented when the HTTPHandler was constructed
// without calling WithServiceAccountService (saService == nil).
func TestHTTP_ServiceAccountRoutes_When_NoSAService_Return501(t *testing.T) {
	// Build a handler WITHOUT WithServiceAccountService.
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	tenantID := uuid.New()
	mux := http.NewServeMux()
	// Deliberately omit .WithServiceAccountService(...) so h.saService is nil.
	h := NewHTTPHandler(tc.svc, tenantID)
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Issue a valid admin token so auth passes (it shouldn't matter — the 501
	// check happens before admin-gate, but we test both paths together).
	userID := uuid.New()
	tc.users.makeAdmin(userID)
	u := &repository.User{
		ID:       userID,
		TenantID: tenantID,
		Username: "admin-501",
		Email:    "admin501@test.example",
		IsActive: true,
		Kind:     "human",
	}
	tc.users.users[u.Username] = u
	tok, err := tc.svc.IssueToken(context.Background(), userID.String(), tenantID.String(), nil, []string{"admin"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	saID := uuid.New().String()
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/service-accounts"},
		{http.MethodPost, "/api/v1/service-accounts"},
		{http.MethodGet, "/api/v1/service-accounts/" + saID},
		{http.MethodPatch, "/api/v1/service-accounts/" + saID},
		{http.MethodDelete, "/api/v1/service-accounts/" + saID},
	}

	for _, rt := range routes {
		rt := rt // capture
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			var body *bytes.Reader
			if rt.method == http.MethodPost || rt.method == http.MethodPatch {
				b, _ := json.Marshal(map[string]any{"name": "x"})
				body = bytes.NewReader(b)
			} else {
				body = bytes.NewReader(nil)
			}
			req, err := http.NewRequest(rt.method, srv.URL+rt.path, body)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			if rt.method == http.MethodPost || rt.method == http.MethodPatch {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNotImplemented {
				t.Errorf("%s %s: got %d, want 501", rt.method, rt.path, resp.StatusCode)
			}
		})
	}
}
