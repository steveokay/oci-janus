// Tests for the FE-API-027 custom-domain CRUD RPCs. We reuse the
// testableHandler pattern from grpc_test.go because the production
// GRPCHandler embeds *repository.Repository directly; the testable
// variant accepts an interface so we can swap a hand-written fake in.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── Domain-aware fake ────────────────────────────────────────────────────────

// fakeDomainRepo extends the contract from grpc_test.go with the new methods
// the FE-API-027 RPCs use. State is in-memory; the (tenantID, domain) pair is
// the unique key the real schema enforces via the UNIQUE constraint on domain.
type fakeDomainRepo struct {
	// rows keys on (tenant, domain) → record. Concurrent maps aren't required
	// because each test owns one fakeDomainRepo.
	rows map[string]*repository.DomainRecord

	// Forced errors for table-driven negative cases.
	listErr   error
	getErr    error
	setErr    error
	deleteErr error
	verifyErr error

	// markedVerified records the IDs MarkDomainVerified was called with so the
	// test for VerifyDomainNow can assert the repo path was exercised.
	markedVerified []uuid.UUID
}

// rowKey is the composite (tenant, domain) lookup the fake uses internally.
// Keeps the map type stable when we want to iterate by tenant.
func rowKey(tenantID uuid.UUID, domain string) string {
	return tenantID.String() + "|" + domain
}

func newFakeDomainRepo() *fakeDomainRepo {
	return &fakeDomainRepo{rows: make(map[string]*repository.DomainRecord)}
}

// add seeds a single row. Caller owns the timestamps and is_primary flag so
// each test states exactly what shape it needs.
func (f *fakeDomainRepo) add(rec *repository.DomainRecord) {
	f.rows[rowKey(rec.TenantID, rec.Domain)] = rec
}

func (f *fakeDomainRepo) ListDomainsByTenant(_ context.Context, tenantID uuid.UUID) ([]repository.DomainRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []repository.DomainRecord
	for _, r := range f.rows {
		if r.TenantID == tenantID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeDomainRepo) GetDomain(_ context.Context, tenantID uuid.UUID, domain string) (*repository.DomainRecord, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	r, ok := f.rows[rowKey(tenantID, domain)]
	if !ok {
		return nil, repository.ErrDomainNotFound
	}
	// Return a shallow copy so callers can mutate without trampling fake state.
	cp := *r
	return &cp, nil
}

func (f *fakeDomainRepo) SetPrimaryDomain(_ context.Context, tenantID uuid.UUID, domain string) (*repository.DomainRecord, error) {
	if f.setErr != nil {
		return nil, f.setErr
	}
	target, ok := f.rows[rowKey(tenantID, domain)]
	if !ok {
		return nil, repository.ErrDomainNotFound
	}
	if !target.Verified {
		return nil, repository.ErrDomainNotVerified
	}
	// Demote any current primary on this tenant — matches the SQL transaction.
	for _, r := range f.rows {
		if r.TenantID == tenantID && r.IsPrimary {
			r.IsPrimary = false
		}
	}
	target.IsPrimary = true
	cp := *target
	return &cp, nil
}

func (f *fakeDomainRepo) DeleteDomainByName(_ context.Context, tenantID uuid.UUID, domain string) (bool, error) {
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	k := rowKey(tenantID, domain)
	r, ok := f.rows[k]
	if !ok {
		return false, repository.ErrDomainNotFound
	}
	wasPrimary := r.IsPrimary
	delete(f.rows, k)
	return wasPrimary, nil
}

func (f *fakeDomainRepo) MarkDomainVerified(_ context.Context, domainID uuid.UUID) error {
	if f.verifyErr != nil {
		return f.verifyErr
	}
	f.markedVerified = append(f.markedVerified, domainID)
	for _, r := range f.rows {
		if r.ID == domainID {
			r.Verified = true
			now := time.Now()
			r.VerifiedAt = &now
			// Promote to primary when no other primary exists on this tenant —
			// mirrors the real MarkDomainVerified atomic promote.
			anyPrimary := false
			for _, other := range f.rows {
				if other.TenantID == r.TenantID && other.IsPrimary {
					anyPrimary = true
					break
				}
			}
			if !anyPrimary {
				r.IsPrimary = true
			}
		}
	}
	return nil
}

// ── testableDomainHandler ───────────────────────────────────────────────────
//
// Mirrors testableHandler in grpc_test.go but only implements the four RPCs
// FE-API-027 adds. We re-implement the handler logic instead of routing
// through the real GRPCHandler so the fake repo interface stays minimal.

type domainRepo interface {
	ListDomainsByTenant(ctx context.Context, tenantID uuid.UUID) ([]repository.DomainRecord, error)
	GetDomain(ctx context.Context, tenantID uuid.UUID, domain string) (*repository.DomainRecord, error)
	SetPrimaryDomain(ctx context.Context, tenantID uuid.UUID, domain string) (*repository.DomainRecord, error)
	DeleteDomainByName(ctx context.Context, tenantID uuid.UUID, domain string) (bool, error)
	MarkDomainVerified(ctx context.Context, domainID uuid.UUID) error
}

type testableDomainHandler struct {
	repo domainRepo
	// txt is the inline DNS lookup used by VerifyDomainNow. The default
	// matches the production package var; tests override it to simulate
	// "token present" / "lookup error" without touching real DNS.
	txt func(string) ([]string, error)
}

func newTestableDomain(repo domainRepo) *testableDomainHandler {
	return &testableDomainHandler{repo: repo, txt: func(string) ([]string, error) { return nil, errors.New("no DNS") }}
}

func (h *testableDomainHandler) ListTenantDomains(ctx context.Context, req *tenantv1.ListTenantDomainsRequest) (*tenantv1.ListTenantDomainsResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	recs, err := h.repo.ListDomainsByTenant(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	out := make([]*tenantv1.DomainEntry, 0, len(recs))
	for i := range recs {
		out = append(out, domainRecordToProto(&recs[i]))
	}
	return &tenantv1.ListTenantDomainsResponse{Domains: out}, nil
}

func (h *testableDomainHandler) VerifyDomainNow(ctx context.Context, req *tenantv1.VerifyDomainNowRequest) (*tenantv1.DomainEntry, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if !domainRE.MatchString(req.GetDomain()) {
		return nil, status.Errorf(codes.InvalidArgument, "bad domain")
	}
	rec, err := h.repo.GetDomain(ctx, tenantID, req.GetDomain())
	if err != nil {
		if errors.Is(err, repository.ErrDomainNotFound) {
			return nil, status.Errorf(codes.NotFound, "not found")
		}
		return nil, status.Errorf(codes.Internal, "get: %v", err)
	}
	if !rec.Verified {
		records, lookupErr := h.txt("_registry-verify." + rec.Domain)
		if lookupErr != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "dns: %v", lookupErr)
		}
		matched := false
		for _, r := range records {
			if r == rec.VerificationToken {
				matched = true
				break
			}
		}
		if !matched {
			return nil, status.Errorf(codes.FailedPrecondition, "token missing")
		}
		if err := h.repo.MarkDomainVerified(ctx, rec.ID); err != nil {
			return nil, status.Errorf(codes.Internal, "mark: %v", err)
		}
		rec, _ = h.repo.GetDomain(ctx, tenantID, req.GetDomain())
	}
	return domainRecordToProto(rec), nil
}

func (h *testableDomainHandler) SetPrimaryDomain(ctx context.Context, req *tenantv1.SetPrimaryDomainRequest) (*tenantv1.DomainEntry, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if !domainRE.MatchString(req.GetDomain()) {
		return nil, status.Errorf(codes.InvalidArgument, "bad domain")
	}
	rec, err := h.repo.SetPrimaryDomain(ctx, tenantID, req.GetDomain())
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrDomainNotFound):
			return nil, status.Errorf(codes.NotFound, "not found")
		case errors.Is(err, repository.ErrDomainNotVerified):
			return nil, status.Errorf(codes.FailedPrecondition, "unverified")
		default:
			return nil, status.Errorf(codes.Internal, "set: %v", err)
		}
	}
	return domainRecordToProto(rec), nil
}

func (h *testableDomainHandler) DeleteDomain(ctx context.Context, req *tenantv1.DeleteDomainRequest) (bool, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return false, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if !domainRE.MatchString(req.GetDomain()) {
		return false, status.Errorf(codes.InvalidArgument, "bad domain")
	}
	wasPrimary, err := h.repo.DeleteDomainByName(ctx, tenantID, req.GetDomain())
	if err != nil {
		if errors.Is(err, repository.ErrDomainNotFound) {
			return false, status.Errorf(codes.NotFound, "not found")
		}
		return false, status.Errorf(codes.Internal, "delete: %v", err)
	}
	return wasPrimary, nil
}

// ── ListTenantDomains tests ──────────────────────────────────────────────────

func TestListTenantDomains_EmptyTenant_ReturnsEmpty(t *testing.T) {
	h := newTestableDomain(newFakeDomainRepo())
	resp, err := h.ListTenantDomains(context.Background(), &tenantv1.ListTenantDomainsRequest{
		TenantId: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(resp.GetDomains()) != 0 {
		t.Errorf("domains: got %d, want 0", len(resp.GetDomains()))
	}
}

func TestListTenantDomains_WithDomains_ReturnsAllShape(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	now := time.Now()
	verifiedAt := now.Add(-time.Hour)
	repo.add(&repository.DomainRecord{
		ID: uuid.New(), TenantID: tid, Domain: "registry.acme.com",
		Verified: true, IsPrimary: true, VerificationToken: "tok-1",
		RegisteredAt: now.Add(-2 * time.Hour), VerifiedAt: &verifiedAt,
		Notified24h: false, Notified48h: false,
	})
	h := newTestableDomain(repo)
	resp, err := h.ListTenantDomains(context.Background(), &tenantv1.ListTenantDomainsRequest{
		TenantId: tid.String(),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(resp.GetDomains()) != 1 {
		t.Fatalf("domains: got %d, want 1", len(resp.GetDomains()))
	}
	d := resp.GetDomains()[0]
	if d.GetDomain() != "registry.acme.com" {
		t.Errorf("domain: got %q", d.GetDomain())
	}
	if !d.GetVerified() || !d.GetIsPrimary() {
		t.Errorf("flags: verified=%v primary=%v want both true", d.GetVerified(), d.GetIsPrimary())
	}
	if d.GetVerifiedAt() == nil {
		t.Errorf("verified_at: got nil, want populated")
	}
	if d.GetRegisteredAt() == nil {
		t.Errorf("registered_at: got nil, want populated")
	}
}

// ── SetPrimaryDomain tests ───────────────────────────────────────────────────

func TestSetPrimaryDomain_DemotesPriorPrimary(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	// Existing primary verified domain + a second verified non-primary.
	repo.add(&repository.DomainRecord{ID: uuid.New(), TenantID: tid, Domain: "old.acme.com",
		Verified: true, IsPrimary: true})
	repo.add(&repository.DomainRecord{ID: uuid.New(), TenantID: tid, Domain: "new.acme.com",
		Verified: true, IsPrimary: false})
	h := newTestableDomain(repo)

	resp, err := h.SetPrimaryDomain(context.Background(), &tenantv1.SetPrimaryDomainRequest{
		TenantId: tid.String(), Domain: "new.acme.com",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.GetIsPrimary() {
		t.Errorf("new domain primary: got false, want true")
	}
	// The old primary must have been demoted in the same transaction.
	old := repo.rows[rowKey(tid, "old.acme.com")]
	if old.IsPrimary {
		t.Errorf("old primary: still primary, want demoted")
	}
}

func TestSetPrimaryDomain_Unverified_ReturnsFailedPrecondition(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	repo.add(&repository.DomainRecord{ID: uuid.New(), TenantID: tid, Domain: "pending.acme.com",
		Verified: false, IsPrimary: false})
	h := newTestableDomain(repo)

	_, err := h.SetPrimaryDomain(context.Background(), &tenantv1.SetPrimaryDomainRequest{
		TenantId: tid.String(), Domain: "pending.acme.com",
	})
	if grpcCode(err) != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", grpcCode(err))
	}
}

func TestSetPrimaryDomain_Unknown_ReturnsNotFound(t *testing.T) {
	h := newTestableDomain(newFakeDomainRepo())
	_, err := h.SetPrimaryDomain(context.Background(), &tenantv1.SetPrimaryDomainRequest{
		TenantId: uuid.NewString(), Domain: "nope.example.com",
	})
	if grpcCode(err) != codes.NotFound {
		t.Errorf("code: got %v, want NotFound", grpcCode(err))
	}
}

// ── DeleteDomain tests ───────────────────────────────────────────────────────

func TestDeleteDomain_Existing_ReturnsWasPrimary(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	repo.add(&repository.DomainRecord{ID: uuid.New(), TenantID: tid, Domain: "primary.acme.com",
		Verified: true, IsPrimary: true})
	h := newTestableDomain(repo)

	wasPrimary, err := h.DeleteDomain(context.Background(), &tenantv1.DeleteDomainRequest{
		TenantId: tid.String(), Domain: "primary.acme.com",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !wasPrimary {
		t.Errorf("was_primary: got false, want true")
	}
	if _, ok := repo.rows[rowKey(tid, "primary.acme.com")]; ok {
		t.Errorf("row not removed")
	}
}

func TestDeleteDomain_NonPrimary_ReturnsFalse(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	repo.add(&repository.DomainRecord{ID: uuid.New(), TenantID: tid, Domain: "extra.acme.com",
		Verified: true, IsPrimary: false})
	h := newTestableDomain(repo)
	wasPrimary, err := h.DeleteDomain(context.Background(), &tenantv1.DeleteDomainRequest{
		TenantId: tid.String(), Domain: "extra.acme.com",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if wasPrimary {
		t.Errorf("was_primary: got true, want false")
	}
}

func TestDeleteDomain_Unknown_ReturnsNotFound(t *testing.T) {
	h := newTestableDomain(newFakeDomainRepo())
	_, err := h.DeleteDomain(context.Background(), &tenantv1.DeleteDomainRequest{
		TenantId: uuid.NewString(), Domain: "missing.example.com",
	})
	if grpcCode(err) != codes.NotFound {
		t.Errorf("code: got %v, want NotFound", grpcCode(err))
	}
}

// ── VerifyDomainNow tests ────────────────────────────────────────────────────

func TestVerifyDomainNow_TokenFound_MarksVerified(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	repo.add(&repository.DomainRecord{
		ID: uuid.New(), TenantID: tid, Domain: "new.acme.com",
		Verified: false, VerificationToken: "tok-abc",
		RegisteredAt: time.Now(),
	})
	h := newTestableDomain(repo)
	// Stub the TXT lookup to return the matching token.
	h.txt = func(name string) ([]string, error) {
		if name != "_registry-verify.new.acme.com" {
			t.Errorf("txt name: got %q", name)
		}
		return []string{"tok-abc"}, nil
	}

	resp, err := h.VerifyDomainNow(context.Background(), &tenantv1.VerifyDomainNowRequest{
		TenantId: tid.String(), Domain: "new.acme.com",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !resp.GetVerified() {
		t.Errorf("verified: got false, want true")
	}
	if len(repo.markedVerified) != 1 {
		t.Errorf("markedVerified calls: got %d, want 1", len(repo.markedVerified))
	}
	// First-verified row should auto-promote to primary (matches real behaviour).
	if !resp.GetIsPrimary() {
		t.Errorf("is_primary: got false, want true (auto-promote on first verified)")
	}
}

func TestVerifyDomainNow_TokenMissing_ReturnsFailedPrecondition(t *testing.T) {
	repo := newFakeDomainRepo()
	tid := uuid.New()
	repo.add(&repository.DomainRecord{
		ID: uuid.New(), TenantID: tid, Domain: "new.acme.com",
		Verified: false, VerificationToken: "tok-abc",
	})
	h := newTestableDomain(repo)
	h.txt = func(string) ([]string, error) { return []string{"different-token"}, nil }

	_, err := h.VerifyDomainNow(context.Background(), &tenantv1.VerifyDomainNowRequest{
		TenantId: tid.String(), Domain: "new.acme.com",
	})
	if grpcCode(err) != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", grpcCode(err))
	}
}

func TestVerifyDomainNow_Unknown_ReturnsNotFound(t *testing.T) {
	h := newTestableDomain(newFakeDomainRepo())
	_, err := h.VerifyDomainNow(context.Background(), &tenantv1.VerifyDomainNowRequest{
		TenantId: uuid.NewString(), Domain: "nope.example.com",
	})
	if grpcCode(err) != codes.NotFound {
		t.Errorf("code: got %v, want NotFound", grpcCode(err))
	}
}

// ── checkPlatformWildcard tests ──────────────────────────────────────────────

func TestCheckPlatformWildcard_WithinZone_ReturnsInvalidArgument(t *testing.T) {
	h := New(nil, "registry.example.com")
	if err := h.checkPlatformWildcard("tenant-a.registry.example.com"); grpcCode(err) != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", grpcCode(err))
	}
}

func TestCheckPlatformWildcard_OutsideZone_ReturnsNil(t *testing.T) {
	h := New(nil, "registry.example.com")
	if err := h.checkPlatformWildcard("registry.acme.com"); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

func TestCheckPlatformWildcard_EmptyBase_NoOp(t *testing.T) {
	h := New(nil, "")
	if err := h.checkPlatformWildcard("tenant-a.registry.example.com"); err != nil {
		t.Errorf("got %v, want nil (no base ⇒ skip)", err)
	}
}
