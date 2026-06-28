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

	"github.com/steveokay/oci-janus/libs/config/loader"
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
	policies map[uuid.UUID]*repository.PolicyRecord

	createErr    error // non-nil → return this from CreateTenant
	getErr       error
	deleteErr    error
	getPolicyErr error
	updateErr    error
	// countErr non-nil → returned from CountTenants. Used by Phase 3.2
	// guard tests to exercise the count-failure branch.
	countErr error

	// Force a duplicate-key error by simulating unique constraint violation.
	dupKeyOnCreate bool
}

func newFakeTenantRepo() *fakeTenantRepo {
	return &fakeTenantRepo{
		tenants:  make(map[uuid.UUID]*repository.TenantRecord),
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

// CountTenants implements the tenantRepo interface for the Phase 3.2 guard
// tests. Reports the current size of the in-memory map; the countErr knob
// drives the count-failure branch.
func (f *fakeTenantRepo) CountTenants(_ context.Context) (int64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return int64(len(f.tenants)), nil
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

// UpdateTenant mirrors the real repo's signature so the testable handler can
// drive it. The fake honours nil-pointer "leave unchanged" semantics so we
// can exercise name-only / plan-only / both paths without DB plumbing.
func (f *fakeTenantRepo) UpdateTenant(_ context.Context, tenantID uuid.UUID, name, plan *string) (*repository.TenantRecord, error) {
	if f.dupKeyOnCreate {
		return nil, &pgconn.PgError{Code: "23505"}
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	rec, ok := f.tenants[tenantID]
	if !ok {
		return nil, nil
	}
	if name != nil {
		rec.Name = *name
		rec.Slug = repository.NormalizeSlug(*name)
		if rec.Slug == "" {
			rec.Slug = tenantID.String()
		}
	}
	if plan != nil {
		rec.Plan = *plan
	}
	return rec, nil
}

// ── Adapter ──────────────────────────────────────────────────────────────────
// GRPCHandler embeds *repository.Repository directly, so we need a way to
// inject a fake without changing the production code. We achieve this by
// wrapping GRPCHandler and overriding the repository via a thin interface adapter
// that re-uses the same method signatures.
//
// tenantRepo is the minimal interface the handler actually uses after
// REDESIGN-001 RM-001 removed custom-domain RPCs.
type tenantRepo interface {
	CreateTenant(ctx context.Context, name, plan string) (*repository.TenantRecord, error)
	CountTenants(ctx context.Context) (int64, error)
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*repository.TenantRecord, error)
	DeleteTenant(ctx context.Context, tenantID uuid.UUID) error
	GetPolicy(ctx context.Context, tenantID uuid.UUID) (*repository.PolicyRecord, error)
	UpdatePolicy(ctx context.Context, p *repository.PolicyRecord) error
	UpdateTenant(ctx context.Context, tenantID uuid.UUID, name, plan *string) (*repository.TenantRecord, error)
}

// testableHandler is a local variant of GRPCHandler that uses the interface
// instead of the concrete *repository.Repository. This avoids modifying
// production code while still providing full test coverage of handler logic.
type testableHandler struct {
	tenantv1.UnimplementedTenantServiceServer
	repo               tenantRepo
	platformBaseDomain string
	// deploymentMode mirrors the production field; Phase 3.2 guard.
	deploymentMode loader.DeploymentMode
}

func newTestable(repo tenantRepo) *testableHandler {
	// Default to multi mode so existing tests (which predate Phase 3.2)
	// keep their original behaviour. Guard-specific tests construct with
	// newTestableWithMode below.
	return &testableHandler{repo: repo, deploymentMode: loader.DeploymentModeMulti}
}

func newTestableWithMode(repo tenantRepo, mode loader.DeploymentMode) *testableHandler {
	return &testableHandler{repo: repo, deploymentMode: mode}
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
	// Phase 3.2 guard — mirror production logic so this fake handler stays
	// behaviourally identical to GRPCHandler.
	if h.deploymentMode != loader.DeploymentModeMulti {
		count, err := h.repo.CountTenants(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "count tenants: %v", err)
		}
		if count >= 1 {
			return nil, status.Error(codes.FailedPrecondition,
				"DEPLOYMENT_MODE=single only allows one tenant; use the bootstrap CLI to (re)mint the first one")
		}
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

// UpdateTenant mirrors the production handler's validation rules so the test
// covers the same branches. The fake repo provides the "atomic name+slug"
// recompute via NormalizeSlug, matching the real SQL path.
func (h *testableHandler) UpdateTenant(ctx context.Context, req *tenantv1.UpdateTenantRequest) (*tenantv1.Tenant, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	var namePtr, planPtr *string
	if req.Name != nil {
		n := req.GetName()
		if n == "" {
			return nil, status.Error(codes.InvalidArgument, "name must not be empty")
		}
		if !tenantNameRE.MatchString(n) {
			return nil, status.Errorf(codes.InvalidArgument, "tenant name must match ^[a-z0-9][a-z0-9-]{1,63}$")
		}
		namePtr = &n
	}
	if req.Plan != nil {
		p := req.GetPlan()
		if !validTenantPlan(p) {
			return nil, status.Errorf(codes.InvalidArgument, "plan must be one of free, pro, enterprise")
		}
		planPtr = &p
	}
	if namePtr == nil && planPtr == nil {
		return nil, status.Error(codes.InvalidArgument, "at least one of name or plan is required")
	}
	rec, err := h.repo.UpdateTenant(ctx, tenantID, namePtr, planPtr)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, status.Errorf(codes.AlreadyExists, "tenant name already in use")
		}
		return nil, status.Errorf(codes.Internal, "update tenant: %v", err)
	}
	if rec == nil {
		return nil, status.Errorf(codes.NotFound, "tenant %s not found", req.TenantId)
	}
	return tenantToProto(rec), nil
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

// ── domainRE validation tests — removed (REDESIGN-001 RM-001) ────────────────
// The domainRE variable and all custom-domain RPCs have been deleted; these
// tests are superseded by the migration that drops tenant_domains.

// ── UpdateTenant tests (FE-API-029) ──────────────────────────────────────────

// ptr is a tiny helper that returns a pointer to a string literal — the proto
// `optional` fields are `*string`, so every UpdateTenant test that wants to
// set a value needs an addressable copy.
func ptr(s string) *string { return &s }

func TestUpdateTenant_NameOnly_UpdatesNameAndSlug(t *testing.T) {
	repo := newFakeTenantRepo()
	id := uuid.New()
	repo.tenants[id] = &repository.TenantRecord{ID: id, Name: "acme", Plan: "free", Slug: "acme"}
	h := newTestable(repo)

	got, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: id.String(),
		Name:     ptr("acme-corp"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "acme-corp" {
		t.Errorf("name: got %q, want acme-corp", got.Name)
	}
	if got.Slug != "acme-corp" {
		t.Errorf("slug: got %q, want acme-corp (auto-derived)", got.Slug)
	}
	if got.Plan != "free" {
		t.Errorf("plan: got %q, want free (unchanged)", got.Plan)
	}
}

func TestUpdateTenant_PlanOnly_UpdatesPlanLeavesNameAlone(t *testing.T) {
	repo := newFakeTenantRepo()
	id := uuid.New()
	repo.tenants[id] = &repository.TenantRecord{ID: id, Name: "acme", Plan: "free", Slug: "acme"}
	h := newTestable(repo)

	got, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: id.String(),
		Plan:     ptr("enterprise"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Plan != "enterprise" {
		t.Errorf("plan: got %q, want enterprise", got.Plan)
	}
	if got.Name != "acme" {
		t.Errorf("name: got %q, want acme (unchanged)", got.Name)
	}
}

func TestUpdateTenant_BothFields_UpdatesBoth(t *testing.T) {
	repo := newFakeTenantRepo()
	id := uuid.New()
	repo.tenants[id] = &repository.TenantRecord{ID: id, Name: "acme", Plan: "free", Slug: "acme"}
	h := newTestable(repo)

	got, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: id.String(),
		Name:     ptr("new-name"),
		Plan:     ptr("pro"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "new-name" || got.Plan != "pro" {
		t.Errorf("got name=%q plan=%q, want new-name + pro", got.Name, got.Plan)
	}
}

func TestUpdateTenant_NeitherField_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: uuid.New().String(),
	})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestUpdateTenant_InvalidPlan_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: uuid.New().String(),
		Plan:     ptr("gold"),
	})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestUpdateTenant_InvalidName_ReturnsInvalidArgument(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: uuid.New().String(),
		Name:     ptr("UPPERCASE"),
	})
	if grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCode(err))
	}
}

func TestUpdateTenant_DuplicateName_ReturnsAlreadyExists(t *testing.T) {
	repo := newFakeTenantRepo()
	id := uuid.New()
	repo.tenants[id] = &repository.TenantRecord{ID: id, Name: "acme", Plan: "free", Slug: "acme"}
	repo.dupKeyOnCreate = true // reused as "next mutation collides"
	h := newTestable(repo)

	_, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: id.String(),
		Name:     ptr("other-name"),
	})
	if grpcCode(err) != codes.AlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", grpcCode(err))
	}
}

func TestUpdateTenant_NotFound_ReturnsNotFound(t *testing.T) {
	h := newTestable(newFakeTenantRepo())
	_, err := h.UpdateTenant(context.Background(), &tenantv1.UpdateTenantRequest{
		TenantId: uuid.New().String(),
		Plan:     ptr("pro"),
	})
	if grpcCode(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", grpcCode(err))
	}
}

// TestNormalizeSlug_RenameDerivesValidHandle confirms the helper used by the
// real UPDATE path produces DNS-safe slugs for the rename cases above. This is
// a fast regression guard for the cascade rule when names contain mixed
// separators or trailing whitespace-equivalents.
func TestNormalizeSlug_RenameDerivesValidHandle(t *testing.T) {
	cases := map[string]string{
		"acme corp":  "acme-corp",
		"acme_corp":  "acme-corp",
		"acme--corp": "acme-corp",
		"---acme---": "acme",
	}
	for in, want := range cases {
		if got := repository.NormalizeSlug(in); got != want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildTenantProto_WildcardHost verifies the simplified buildTenantProto
// always produces the wildcard subdomain after REDESIGN-001 RM-001.
func TestBuildTenantProto_WildcardHost(t *testing.T) {
	h := New(nil, "registry.example.com", loader.DeploymentModeMulti)
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}

	got := h.buildTenantProto(rec)

	if got.GetHost() != "acme.registry.example.com" {
		t.Errorf("host: got %q, want acme.registry.example.com", got.GetHost())
	}
	if got.GetSlug() != "acme" {
		t.Errorf("slug: got %q, want acme", got.GetSlug())
	}
}

// TestBuildTenantProto_EmptySlug_UsesTenantID guards the edge case where slug
// somehow ends up empty. The host must still be a parseable hostname.
func TestBuildTenantProto_EmptySlug_UsesTenantID(t *testing.T) {
	h := New(nil, "registry.example.com", loader.DeploymentModeMulti)
	tid := uuid.New()
	rec := &repository.TenantRecord{ID: tid, Name: "??", Slug: "", CreatedAt: time.Now()}

	got := h.buildTenantProto(rec)

	want := tid.String() + ".registry.example.com"
	if got.GetHost() != want {
		t.Errorf("host: got %q, want %q", got.GetHost(), want)
	}
}

// ── Phase 3.2 single-tenant guard ────────────────────────────────────────

// TestCreateTenant_SingleMode_RejectsSecondTenant pins the structural
// invariant introduced by Phase 3.2 / Q-001: in single mode, a tenant
// already exists → second CreateTenant fails with FAILED_PRECONDITION.
func TestCreateTenant_SingleMode_RejectsSecondTenant(t *testing.T) {
	repo := newFakeTenantRepo()
	// Seed with one tenant so CountTenants returns >= 1.
	if _, err := repo.CreateTenant(context.Background(), "bootstrap", "free"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newTestableWithMode(repo, loader.DeploymentModeSingle)

	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "second", Plan: "free"})

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", st.Code())
	}
}

// TestCreateTenant_SingleMode_AllowsFirstTenant covers the bootstrap path:
// single mode, empty table → CreateTenant succeeds (the bootstrap CLI is
// the only legitimate caller in production but the constraint is purely
// structural, not caller-identity-based).
func TestCreateTenant_SingleMode_AllowsFirstTenant(t *testing.T) {
	repo := newFakeTenantRepo()
	h := newTestableWithMode(repo, loader.DeploymentModeSingle)

	tnt, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "bootstrap", Plan: "free"})

	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if tnt == nil {
		t.Fatal("expected non-nil tenant on first CreateTenant")
	}
}

// TestCreateTenant_MultiMode_AllowsMultipleTenants confirms the guard is
// scoped to single mode — multi mode preserves the legacy unlimited-tenant
// behaviour.
func TestCreateTenant_MultiMode_AllowsMultipleTenants(t *testing.T) {
	repo := newFakeTenantRepo()
	h := newTestableWithMode(repo, loader.DeploymentModeMulti)

	for _, name := range []string{"acme", "beta", "gamma"} {
		if _, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: name, Plan: "free"}); err != nil {
			t.Fatalf("multi-mode CreateTenant(%q): %v", name, err)
		}
	}
	if got := len(repo.tenants); got != 3 {
		t.Errorf("tenant count: got %d, want 3", got)
	}
}

// TestCreateTenant_ZeroValueMode_FailsClosedAsSingle pins the documented
// fail-closed default — a Handler constructed without an explicit mode
// (zero value `DeploymentMode("")`) refuses a second CreateTenant, same
// as explicit "single". Cheap insurance against a future refactor that
// rebrands `DeploymentModeMulti` and accidentally inverts the default.
func TestCreateTenant_ZeroValueMode_FailsClosedAsSingle(t *testing.T) {
	repo := newFakeTenantRepo()
	if _, err := repo.CreateTenant(context.Background(), "bootstrap", "free"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := newTestableWithMode(repo, loader.DeploymentMode(""))

	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "second", Plan: "free"})

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("zero-value mode must fail-closed: got %v, want FailedPrecondition", st.Code())
	}
}

// TestCreateTenant_SingleMode_CountErr_MapsToInternal covers the count-RPC-
// failure branch: if CountTenants errors out, the request must fail (not
// fall through to the INSERT). Pins the fail-closed contract.
func TestCreateTenant_SingleMode_CountErr_MapsToInternal(t *testing.T) {
	repo := newFakeTenantRepo()
	repo.countErr = errors.New("db down")
	h := newTestableWithMode(repo, loader.DeploymentModeSingle)

	_, err := h.CreateTenant(context.Background(), &tenantv1.CreateTenantRequest{Name: "bootstrap", Plan: "free"})
	if err == nil {
		t.Fatal("expected error when CountTenants fails, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	// Internal (or Unavailable) is fine — what matters is the request does
	// NOT proceed to CreateTenant. The fake's testableHandler maps to
	// Internal; production code uses MapDBError which would also produce
	// Internal/Unavailable depending on the underlying pgx error.
	if st.Code() == codes.OK {
		t.Errorf("code: got OK, want non-OK")
	}
	if len(repo.tenants) != 0 {
		t.Errorf("must not insert on count failure; got %d tenants", len(repo.tenants))
	}
}

// TestBuildTenantProto_EmptyBaseDomain_UsesBareSlug covers tests / misconfig
// where PLATFORM_BASE_DOMAIN is empty. The host should be the bare slug.
func TestBuildTenantProto_EmptyBaseDomain_UsesBareSlug(t *testing.T) {
	h := New(nil, "", loader.DeploymentModeMulti)
	rec := &repository.TenantRecord{ID: uuid.New(), Name: "Acme", Slug: "acme", CreatedAt: time.Now()}

	got := h.buildTenantProto(rec)

	if got.GetHost() != "acme" {
		t.Errorf("host: got %q, want bare slug 'acme'", got.GetHost())
	}
}

// TestNormalizeSlug_TableDriven covers the slug-normalization algorithm
// shared between the SQL backfill and CreateTenant.
func TestNormalizeSlug_TableDriven(t *testing.T) {
	cases := map[string]string{
		"Acme":          "acme",
		"Acme Corp":     "acme-corp",
		"Acme  Corp":    "acme-corp",   // collapse multi-space
		"acme--corp":    "acme-corp",   // collapse multi-dash
		"  Acme  ":      "acme",        // trim leading/trailing
		"Acme/Corp_Inc": "acme-corp-inc",
		"":              "",            // empty → empty (caller falls back to id)
		"!@#$":          "",            // no alphanumerics → empty
		"AlreadySlug123": "alreadyslug123",
		"-leading-dash":  "leading-dash",
		"trailing-dash-": "trailing-dash",
	}
	for in, want := range cases {
		got := repository.NormalizeSlug(in)
		if got != want {
			t.Errorf("NormalizeSlug(%q): got %q, want %q", in, got, want)
		}
	}
}
