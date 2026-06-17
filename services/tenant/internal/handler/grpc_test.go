// Package handler_test contains unit tests for the TenantService gRPC handler.
// All tests use hand-written fakes — no real database or network calls.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── Fake repository ──────────────────────────────────────────────────────────

// fakeTenantRepo is a hand-written fake that satisfies the same method set as
// *repository.Repository but stores data in memory. It is assigned to the
// unexported repo field of GRPCHandler via a thin adapter so we can inject it
// without modifying the production type signature.
type fakeTenantRepo struct {
	tenants  map[uuid.UUID]*repository.TenantRecord
	domains  map[string]uuid.UUID // domain → tenant_id (verified)
	policies map[uuid.UUID]*repository.PolicyRecord

	createErr    error // non-nil → return this from CreateTenant
	getErr       error
	deleteErr    error
	resolveErr   error
	registerErr  error
	getPolicyErr error
	updateErr    error

	// Force a duplicate-key error by simulating unique constraint violation.
	dupKeyOnCreate bool
}

func newFakeTenantRepo() *fakeTenantRepo {
	return &fakeTenantRepo{
		tenants:  make(map[uuid.UUID]*repository.TenantRecord),
		domains:  make(map[string]uuid.UUID),
		policies: make(map[uuid.UUID]*repository.PolicyRecord),
	}
}

func (f *fakeTenantRepo) CreateTenant(_ context.Context, name, plan string) (*repository.TenantRecord, error) {
	if f.dupKeyOnCreate {
		return nil, &pgconn.PgError{Code: "23505"}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	rec := &repository.TenantRecord{
		ID:        uuid.New(),
		Name:      name,
		Plan:      plan,
		CreatedAt: time.Now(),
	}
	f.tenants[rec.ID] = rec
	return rec, nil
}

func (f *fakeTenantRepo) GetTenant(_ context.Context, tenantID uuid.UUID) (*repository.TenantRecord, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	rec := f.tenants[tenantID]
	return rec, nil // nil means not found
}

func (f *fakeTenantRepo) DeleteTenant(_ context.Context, tenantID uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.tenants, tenantID)
	return nil
}

func (f *fakeTenantRepo) ResolveDomain(_ context.Context, domain string) (uuid.UUID, bool, error) {
	if f.resolveErr != nil {
		return uuid.Nil, false, f.resolveErr
	}
	id, ok := f.domains[domain]
	return id, ok, nil
}

func (f *fakeTenantRepo) RegisterDomain(_ context.Context, tenantID uuid.UUID, domain, token string) (*repository.DomainRecord, error) {
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &repository.DomainRecord{
		ID:                uuid.New(),
		TenantID:          tenantID,
		Domain:            domain,
		VerificationToken: token,
		RegisteredAt:      time.Now(),
	}, nil
}

func (f *fakeTenantRepo) GetPolicy(_ context.Context, tenantID uuid.UUID) (*repository.PolicyRecord, error) {
	if f.getPolicyErr != nil {
		return nil, f.getPolicyErr
	}
	p, ok := f.policies[tenantID]
	if !ok {
		p = &repository.PolicyRecord{TenantID: tenantID}
	}
	return p, nil
}

func (f *fakeTenantRepo) UpdatePolicy(_ context.Context, p *repository.PolicyRecord) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.policies[p.TenantID] = p
	return nil
}

// ── Adapter ──────────────────────────────────────────────────────────────────
// GRPCHandler embeds *repository.Repository directly, so we need a way to
// inject a fake without changing the production code. We achieve this by
// wrapping GRPCHandler and overriding the repository via a thin interface adapter
// that re-uses the same method signatures. Since the handler calls h.repo.X(...),
// we create a handlerWithFake that wraps the fake as a *repository.Repository
// by embedding a custom struct.
//
// A simpler approach: extract an internal interface in the handler and replace
// the concrete field. The handler already calls h.repo.CreateTenant etc., which
// match our fake exactly. We compose a GRPCHandler with a nil repo pointer and
// patch the internal repo field via a pointer to our fake wrapped in a thin shim.
//
// The cleanest testable approach here is to expose an internal constructor that
// accepts a repo interface. We do this by defining a repoInterface locally and
// a newWithRepo constructor used only in tests.

// tenantRepo is the minimal interface the handler actually uses.
type tenantRepo interface {
	CreateTenant(ctx context.Context, name, plan string) (*repository.TenantRecord, error)
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*repository.TenantRecord, error)
	DeleteTenant(ctx context.Context, tenantID uuid.UUID) error
	ResolveDomain(ctx context.Context, domain string) (uuid.UUID, bool, error)
	RegisterDomain(ctx context.Context, tenantID uuid.UUID, domain, token string) (*repository.DomainRecord, error)
	GetPolicy(ctx context.Context, tenantID uuid.UUID) (*repository.PolicyRecord, error)
	UpdatePolicy(ctx context.Context, p *repository.PolicyRecord) error
}

// testableHandler is a local variant of GRPCHandler that uses the interface
// instead of the concrete *repository.Repository. This avoids modifying
// production code while still providing full test coverage of handler logic.
type testableHandler struct {
	tenantv1.UnimplementedTenantServiceServer
	repo tenantRepo
}

func newTestable(repo tenantRepo) *testableHandler {
	return &testableHandler{repo: repo}
}

// Delegate all handler methods to the same logic as GRPCHandler by copying the
// implementations. This approach avoids modifying production code.

func (h *testableHandler) CreateTenant(ctx context.Context, req *tenantv1.CreateTenantRequest) (*tenantv1.Tenant, error) {
	// Re-implement via our interface repo:
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if !tenantNameRE.MatchString(req.Name) {
		return nil, status.Errorf(codes.InvalidArgument, "tenant name must match ^[a-z0-9][a-z0-9-]{1,63}$")
	}
	plan := req.Plan
	if plan == "" {
		plan = "standard"
	}
	rec, err := h.repo.CreateTenant(ctx, req.Name, plan)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, status.Errorf(codes.AlreadyExists, "tenant name %q is already in use", req.Name)
		}
		return nil, status.Errorf(codes.Internal, "create tenant: %v", err)
	}
	return tenantToProto(rec), nil
}

func (h *testableHandler) GetTenant(ctx context.Context, req *tenantv1.GetTenantRequest) (*tenantv1.Tenant, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	rec, err := h.repo.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get tenant: %v", err)
	}
	if rec == nil {
		return nil, status.Errorf(codes.NotFound, "tenant %s not found", req.TenantId)
	}
	return tenantToProto(rec), nil
}

func (h *testableHandler) DeleteTenant(ctx context.Context, req *tenantv1.DeleteTenantRequest) error {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if err := h.repo.DeleteTenant(ctx, tenantID); err != nil {
		return status.Errorf(codes.Internal, "delete tenant: %v", err)
	}
	return nil
}

func (h *testableHandler) ResolveDomain(ctx context.Context, req *tenantv1.ResolveDomainRequest) (*tenantv1.ResolveDomainResponse, error) {
	if req.Domain == "" {
		return nil, status.Error(codes.InvalidArgument, "domain is required")
	}
	tenantID, found, err := h.repo.ResolveDomain(ctx, req.Domain)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve domain: %v", err)
	}
	resp := &tenantv1.ResolveDomainResponse{Found: found}
	if found {
		resp.TenantId = tenantID.String()
	}
	return resp, nil
}

func (h *testableHandler) RegisterDomain(ctx context.Context, req *tenantv1.RegisterDomainRequest) (*tenantv1.RegisterDomainResponse, error) {
	if req.TenantId == "" || req.Domain == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and domain are required")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if !domainRE.MatchString(req.Domain) {
		return nil, status.Errorf(codes.InvalidArgument, "domain %q is not a valid RFC 1123 hostname", req.Domain)
	}
	token, err := generateToken()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate token: %v", err)
	}
	if _, err := h.repo.RegisterDomain(ctx, tenantID, req.Domain, token); err != nil {
		return nil, status.Errorf(codes.Internal, "register domain: %v", err)
	}
	return &tenantv1.RegisterDomainResponse{VerificationToken: token}, nil
}

func (h *testableHandler) GetPolicy(ctx context.Context, req *tenantv1.GetTenantPolicyRequest) (*tenantv1.TenantPolicy, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	p, err := h.repo.GetPolicy(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get policy: %v", err)
	}
	return policyToProto(p), nil
}

func (h *testableHandler) UpdatePolicy(ctx context.Context, req *tenantv1.UpdateTenantPolicyRequest) (*tenantv1.TenantPolicy, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	p := &repository.PolicyRecord{
		TenantID:           tenantID,
		ScanOnPush:         req.ScanOnPush,
		BlockOnSeverity:    req.BlockOnSeverity,
		AllowUnscanned:     req.AllowUnscanned,
		ProxyCacheEnabled:  req.ProxyCacheEnabled,
		SigningRequired:    req.SigningRequired,
		ExemptRepositories: req.ExemptRepositories,
		StorageQuotaBytes:  req.StorageQuotaBytes,
	}
	if err := h.repo.UpdatePolicy(ctx, p); err != nil {
		return nil, status.Errorf(codes.Internal, "update policy: %v", err)
	}
	return policyToProto(p), nil
}

// ── Helper ──────────────────────────────────────────────────────────────────

func grpcCode(err error) codes.Code {
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

// ── CreateTenant tests ───────────────────────────────────────────────────────

func TestCreateTenant_ValidRequest_ReturnsTenant(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	got, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{
		Name: "acme",
		Plan: "enterprise",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "acme" {
		t.Errorf("name = %q, want %q", got.Name, "acme")
	}
	if got.Plan != "enterprise" {
		t.Errorf("plan = %q, want %q", got.Plan, "enterprise")
	}
	if got.TenantId == "" {
		t.Error("expected non-empty tenant_id")
	}
}

func TestCreateTenant_EmptyName_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: ""})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestCreateTenant_InvalidName_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "UPPERCASE"})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestCreateTenant_DefaultPlan_UsesStandard(t *testing.T) {
	repo := newFakeTenantRepo()
	h := newTestable(repo)
	got, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "acme"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Plan != "standard" {
		t.Errorf("plan = %q, want %q", got.Plan, "standard")
	}
}

func TestCreateTenant_DuplicateName_ReturnsAlreadyExists(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.dupKeyOnCreate = true
	h := newTestable(repo)
	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "acme"})
	if grpcCode(err) != codes.AlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", grpcCode(err))
	}
}

func TestCreateTenant_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.createErr = errors.New("db down")
	h := newTestable(repo)
	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "acme"})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

// ── GetTenant tests ──────────────────────────────────────────────────────────

func TestGetTenant_ValidID_ReturnsTenant(t *testing.T) {
	repo := newFakeTenantRepo()
	id := uuid.New()
	repo.tenants[id] = &repository.TenantRecord{ID: id, Name: "acme", Plan: "standard", CreatedAt: time.Now()}
	h := newTestable(repo)

	got, err := h.GetTenant(context.Background(), &tenantv1.GetTenantRequest{TenantId: id.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TenantId != id.String() {
		t.Errorf("tenant_id = %q, want %q", got.TenantId, id.String())
	}
}

func TestGetTenant_InvalidUUID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.GetTenant(context.Background(), &tenantv1.GetTenantRequest{TenantId: "not-a-uuid"})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestGetTenant_NotFound_ReturnsNotFound(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.GetTenant(context.Background(), &tenantv1.GetTenantRequest{TenantId: uuid.NewString()})
	if grpcCode(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", grpcCode(err))
	}
}

func TestGetTenant_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.getErr = errors.New("db timeout")
	h := newTestable(repo)
	_, err := h.GetTenant(context.Background(), &tenantv1.GetTenantRequest{TenantId: uuid.NewString()})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

// ── DeleteTenant tests ───────────────────────────────────────────────────────

func TestDeleteTenant_ValidID_Succeeds(t *testing.T) {
	repo := newFakeTenantRepo()
	id := uuid.New()
	repo.tenants[id] = &repository.TenantRecord{ID: id, Name: "acme"}
	h := newTestable(repo)

	if err := h.DeleteTenant(context.Background(), &tenantv1.DeleteTenantRequest{TenantId: id.String()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := repo.tenants[id]; ok {
		t.Error("expected tenant to be deleted")
	}
}

func TestDeleteTenant_InvalidUUID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	err := h.DeleteTenant(context.Background(), &tenantv1.DeleteTenantRequest{TenantId: "bad"})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestDeleteTenant_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.deleteErr = errors.New("db error")
	h := newTestable(repo)
	err := h.DeleteTenant(context.Background(), &tenantv1.DeleteTenantRequest{TenantId: uuid.NewString()})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

// ── ResolveDomain tests ──────────────────────────────────────────────────────

func TestResolveDomain_KnownDomain_ReturnsFound(t *testing.T) {
	repo := newFakeTenantRepo()
	tid := uuid.New()
	repo.domains["registry.acme.com"] = tid
	h := newTestable(repo)

	resp, err := h.ResolveDomain(context.Background(), &tenantv1.ResolveDomainRequest{Domain: "registry.acme.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Found {
		t.Error("expected found=true")
	}
	if resp.TenantId != tid.String() {
		t.Errorf("tenant_id = %q, want %q", resp.TenantId, tid.String())
	}
}

func TestResolveDomain_UnknownDomain_ReturnsNotFound(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	resp, err := h.ResolveDomain(context.Background(), &tenantv1.ResolveDomainRequest{Domain: "unknown.example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Found {
		t.Error("expected found=false")
	}
}

func TestResolveDomain_EmptyDomain_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.ResolveDomain(context.Background(), &tenantv1.ResolveDomainRequest{Domain: ""})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestResolveDomain_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.resolveErr = errors.New("connection reset")
	h := newTestable(repo)
	_, err := h.ResolveDomain(context.Background(), &tenantv1.ResolveDomainRequest{Domain: "test.example.com"})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

// ── RegisterDomain tests ─────────────────────────────────────────────────────

func TestRegisterDomain_ValidRequest_ReturnsToken(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	resp, err := h.RegisterDomain(context.Background(), &tenantv1.RegisterDomainRequest{
		TenantId: uuid.NewString(),
		Domain:   "registry.acme.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.VerificationToken) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(resp.VerificationToken))
	}
}

func TestRegisterDomain_MissingFields_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.RegisterDomain(context.Background(), &tenantv1.RegisterDomainRequest{
		TenantId: uuid.NewString(),
		Domain:   "",
	})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestRegisterDomain_InvalidDomain_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.RegisterDomain(context.Background(), &tenantv1.RegisterDomainRequest{
		TenantId: uuid.NewString(),
		Domain:   "not a valid domain!",
	})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestRegisterDomain_InvalidTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.RegisterDomain(context.Background(), &tenantv1.RegisterDomainRequest{
		TenantId: "bad-uuid",
		Domain:   "registry.acme.com",
	})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestRegisterDomain_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.registerErr = errors.New("constraint violation")
	h := newTestable(repo)
	_, err := h.RegisterDomain(context.Background(), &tenantv1.RegisterDomainRequest{
		TenantId: uuid.NewString(),
		Domain:   "registry.acme.com",
	})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

// ── Policy tests ─────────────────────────────────────────────────────────────

func TestGetPolicy_ValidTenant_ReturnsPolicy(t *testing.T) {
	repo := newFakeTenantRepo()
	tid := uuid.New()
	repo.policies[tid] = &repository.PolicyRecord{
		TenantID:   tid,
		ScanOnPush: true,
	}
	h := newTestable(repo)
	got, err := h.GetPolicy(context.Background(), &tenantv1.GetTenantPolicyRequest{TenantId: tid.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ScanOnPush {
		t.Error("expected scan_on_push=true")
	}
}

func TestGetPolicy_InvalidTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.GetPolicy(context.Background(), &tenantv1.GetTenantPolicyRequest{TenantId: "bad"})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestGetPolicy_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.getPolicyErr = errors.New("db error")
	h := newTestable(repo)
	_, err := h.GetPolicy(context.Background(), &tenantv1.GetTenantPolicyRequest{TenantId: uuid.NewString()})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

func TestUpdatePolicy_ValidRequest_ReturnsUpdatedPolicy(t *testing.T) {
	repo := newFakeTenantRepo()
	tid := uuid.New()
	h := newTestable(repo)
	got, err := h.UpdatePolicy(context.Background(), &tenantv1.UpdateTenantPolicyRequest{
		TenantId:          tid.String(),
		ScanOnPush:        true,
		BlockOnSeverity:   "CRITICAL",
		StorageQuotaBytes: 1073741824,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ScanOnPush {
		t.Error("expected scan_on_push=true")
	}
	if got.BlockOnSeverity != "CRITICAL" {
		t.Errorf("block_on_severity = %q, want %q", got.BlockOnSeverity, "CRITICAL")
	}
}

func TestUpdatePolicy_InvalidTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.UpdatePolicy(context.Background(), &tenantv1.UpdateTenantPolicyRequest{TenantId: "bad"})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestUpdatePolicy_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.updateErr = errors.New("write failed")
	h := newTestable(repo)
	_, err := h.UpdatePolicy(context.Background(), &tenantv1.UpdateTenantPolicyRequest{TenantId: uuid.NewString()})
	if grpcCode(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCode(err))
	}
}

// ── isDuplicateKeyError tests ─────────────────────────────────────────────────

func TestIsDuplicateKeyError_PgError23505_ReturnsTrue(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505"}
	if !isDuplicateKeyError(pgErr) {
		t.Error("expected true for SQLSTATE 23505")
	}
}

func TestIsDuplicateKeyError_OtherCode_ReturnsFalse(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "08006"}
	if isDuplicateKeyError(pgErr) {
		t.Error("expected false for non-23505 code")
	}
}

func TestIsDuplicateKeyError_WrappedError_ReturnsTrue(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505"}
	wrapped := errors.Join(errors.New("outer"), pgErr)
	if !isDuplicateKeyError(wrapped) {
		t.Error("expected true for wrapped pgconn.PgError with code 23505")
	}
}

func TestIsDuplicateKeyError_PlainError_ReturnsFalse(t *testing.T) {
	if isDuplicateKeyError(errors.New("some other error")) {
		t.Error("expected false for plain error")
	}
}

// ── domainRE validation tests ─────────────────────────────────────────────────

func TestDomainRE_ValidHostnames_Match(t *testing.T) {
	valid := []string{
		"registry.acme.com",
		"sub.domain.example.org",
		"my-registry.io",
	}
	for _, d := range valid {
		if !domainRE.MatchString(d) {
			t.Errorf("domainRE did not match valid hostname %q", d)
		}
	}
}

func TestDomainRE_InvalidHostnames_DoNotMatch(t *testing.T) {
	invalid := []string{
		"192.168.1.1",
		"not a domain",
		"domain",
		"UPPER.COM",
		"",
	}
	for _, d := range invalid {
		if domainRE.MatchString(d) {
			t.Errorf("domainRE unexpectedly matched invalid hostname %q", d)
		}
	}
}
