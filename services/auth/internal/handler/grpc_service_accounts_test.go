// grpc_service_accounts_test.go — gRPC handler tests for FUT-082
// ListServiceAccounts. Reuses the in-memory SA fakes (handlerFakeSARepo,
// saTestKeyRepo, capturingAuditEmitterH, newRedisAdapterH) defined in
// http_service_accounts_test.go so no real PostgreSQL / Redis is required.
package handler

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// buildGRPCHandlerWithSA builds a GRPCHandler wired with a ServiceAccountService
// backed by in-memory fakes. It returns the handler plus the fake SA repo so a
// test can seed accounts and stats directly.
func buildGRPCHandlerWithSA(t *testing.T) (*GRPCHandler, *handlerFakeSARepo) {
	t.Helper()
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)

	// A dedicated miniredis backs the SA service's best-effort revoke-key path.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run (SA gRPC): %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	saRepo := newHandlerFakeSARepo()
	keyRepo := newSATestKeyRepo()
	audit := &capturingAuditEmitterH{}
	saSvc := service.NewServiceAccountService(saRepo, tc.users, keyRepo, audit, newRedisAdapterH(rdb))

	h := NewGRPCHandler(tc.svc, nil).WithServiceAccountService(saSvc)
	return h, saRepo
}

// TestListServiceAccounts_mapsRowsToSummaries verifies the repository →
// ServiceAccountSummary mapping: id, disabled flag derived from DisabledAt,
// active key count, scopes, and the nil-vs-set LastUsedAt timestamp.
func TestListServiceAccounts_mapsRowsToSummaries(t *testing.T) {
	h, saRepo := buildGRPCHandlerWithSA(t)
	tenantID := uuid.New()

	// Active SA with a used key → Disabled=false, LastUsedAt set.
	activeID := uuid.New()
	lastUsed := time.Now().Add(-time.Hour).UTC()
	saRepo.accounts[activeID] = &repository.ServiceAccount{
		ID:            activeID,
		TenantID:      tenantID,
		ShadowUserID:  uuid.New(),
		Name:          "ci-bot",
		Description:   "builds",
		AllowedScopes: []string{"push", "pull"},
		CreatedAt:     time.Now().Add(-24 * time.Hour),
	}
	saRepo.stats[activeID] = saListStats{activeKeyCount: 3, lastUsedAt: &lastUsed}

	// Disabled SA with no key ever used → Disabled=true, LastUsedAt nil.
	disabledID := uuid.New()
	disabledAt := time.Now().Add(-2 * time.Hour)
	saRepo.accounts[disabledID] = &repository.ServiceAccount{
		ID:            disabledID,
		TenantID:      tenantID,
		ShadowUserID:  uuid.New(),
		Name:          "old-bot",
		AllowedScopes: []string{"pull"},
		CreatedAt:     time.Now().Add(-48 * time.Hour),
		DisabledAt:    &disabledAt,
	}
	saRepo.stats[disabledID] = saListStats{activeKeyCount: 0, lastUsedAt: nil}

	resp, err := h.ListServiceAccounts(context.Background(), &authv1.ListServiceAccountsRequest{
		TenantId:        tenantID.String(),
		IncludeDisabled: true,
	})
	if err != nil {
		t.Fatalf("ListServiceAccounts: unexpected error: %v", err)
	}
	if len(resp.ServiceAccounts) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(resp.ServiceAccounts))
	}

	// Index by id for order-independent assertions.
	byID := make(map[string]*authv1.ServiceAccountSummary, len(resp.ServiceAccounts))
	for _, s := range resp.ServiceAccounts {
		byID[s.Id] = s
	}

	active := byID[activeID.String()]
	if active == nil {
		t.Fatalf("active SA %s missing from response", activeID)
	}
	if active.Disabled {
		t.Error("active SA: expected Disabled=false")
	}
	if active.ActiveKeyCount != 3 {
		t.Errorf("active SA: ActiveKeyCount got %d, want 3", active.ActiveKeyCount)
	}
	if active.TenantId != tenantID.String() {
		t.Errorf("active SA: TenantId got %q, want %q", active.TenantId, tenantID.String())
	}
	if len(active.AllowedScopes) != 2 || active.AllowedScopes[0] != "push" {
		t.Errorf("active SA: AllowedScopes got %v, want [push pull]", active.AllowedScopes)
	}
	if active.LastUsedAt == nil {
		t.Error("active SA: expected non-nil LastUsedAt")
	} else if !active.LastUsedAt.AsTime().Equal(lastUsed) {
		t.Errorf("active SA: LastUsedAt got %v, want %v", active.LastUsedAt.AsTime(), lastUsed)
	}
	if active.CreatedAt == nil {
		t.Error("active SA: expected non-nil CreatedAt")
	}

	disabled := byID[disabledID.String()]
	if disabled == nil {
		t.Fatalf("disabled SA %s missing from response", disabledID)
	}
	if !disabled.Disabled {
		t.Error("disabled SA: expected Disabled=true (DisabledAt set)")
	}
	if disabled.ActiveKeyCount != 0 {
		t.Errorf("disabled SA: ActiveKeyCount got %d, want 0", disabled.ActiveKeyCount)
	}
	if disabled.LastUsedAt != nil {
		t.Errorf("disabled SA: expected nil LastUsedAt, got %v", disabled.LastUsedAt.AsTime())
	}
}

// TestListServiceAccounts_invalidTenant_returnsInvalidArgument verifies a
// non-UUID tenant_id is rejected with codes.InvalidArgument.
func TestListServiceAccounts_invalidTenant_returnsInvalidArgument(t *testing.T) {
	h, _ := buildGRPCHandlerWithSA(t)

	_, err := h.ListServiceAccounts(context.Background(), &authv1.ListServiceAccountsRequest{
		TenantId: "not-a-uuid",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
}

// TestListServiceAccounts_notConfigured_returnsUnimplemented verifies that when
// the SA service is not wired (nil), the RPC reports Unimplemented rather than
// panicking — mirroring the HTTP handler's requireSAService posture.
func TestListServiceAccounts_notConfigured_returnsUnimplemented(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	// Deliberately omit WithServiceAccountService so saService == nil.
	h := NewGRPCHandler(tc.svc, nil)

	_, err := h.ListServiceAccounts(context.Background(), &authv1.ListServiceAccountsRequest{
		TenantId: uuid.New().String(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("code: got %v, want Unimplemented", st.Code())
	}
}
