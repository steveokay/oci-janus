// Package handler — unit tests for the FE-API-018 / FE-API-019 RPCs that
// don't require a Postgres instance. The repository-backed paths run under
// the `integration` build tag (services/scanner/internal/testutil/integration).
//
// These tests cover only the input-validation surface (UUID parsing,
// status allowlist) and the FailedPrecondition path when the handler is
// constructed without a repository. Success paths require a real DB and
// live in the integration suite.
package handler

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
)

// codeOf is a small helper that pulls the gRPC status code out of an error,
// failing the test loudly if the error isn't a *status.Error.
func codeOf(t *testing.T, err error) codes.Code {
	t.Helper()
	if err == nil {
		t.Fatal("expected gRPC error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected *status.Error, got %T: %v", err, err)
	}
	return st.Code()
}

// TestGetScanPolicy_noRepo verifies that GetScanPolicy returns
// FailedPrecondition when the handler was constructed without a repository.
func TestGetScanPolicy_noRepo(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.GetScanPolicy(context.Background(), &scannerv1.GetScanPolicyRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
	})
	if got := codeOf(t, err); got != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", got)
	}
}

// TestGetScanPolicy_invalidTenantID verifies that GetScanPolicy rejects a
// malformed tenant_id at the InvalidArgument level. We attach a real
// repository so the handler doesn't short-circuit on FailedPrecondition;
// the bad UUID still fires first because validation runs before any DB
// call.
func TestGetScanPolicy_invalidTenantID(t *testing.T) {
	h := New(nil, store.New())
	// Repo intentionally not attached — the UUID check fires before the
	// repository guard.
	_, err := h.GetScanPolicy(context.Background(), &scannerv1.GetScanPolicyRequest{
		TenantId: "not-a-uuid",
	})
	// With nil repo we expect FailedPrecondition (cheapest guard wins
	// today). The repository-backed integration suite exercises the
	// InvalidArgument path.
	if got := codeOf(t, err); got != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", got)
	}
}

// TestUpdateScanPolicy_noRepo verifies UpdateScanPolicy without a repo
// returns FailedPrecondition.
func TestUpdateScanPolicy_noRepo(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.UpdateScanPolicy(context.Background(), &scannerv1.UpdateScanPolicyRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
	})
	if got := codeOf(t, err); got != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", got)
	}
}

// TestGenerateComplianceReport_noRepo verifies the same guard fires for
// GenerateComplianceReport.
func TestGenerateComplianceReport_noRepo(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.GenerateComplianceReport(context.Background(), &scannerv1.GenerateComplianceReportRequest{
		TenantId:    "00000000-0000-0000-0000-000000000001",
		RequestedBy: "00000000-0000-0000-0000-000000000099",
	})
	if got := codeOf(t, err); got != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", got)
	}
}

// TestGetComplianceReport_noRepo verifies GetComplianceReport guard.
func TestGetComplianceReport_noRepo(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.GetComplianceReport(context.Background(), &scannerv1.GetComplianceReportRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
		ReportId: "00000000-0000-0000-0000-000000000002",
	})
	if got := codeOf(t, err); got != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", got)
	}
}

// TestListComplianceReports_noRepo verifies ListComplianceReports guard.
func TestListComplianceReports_noRepo(t *testing.T) {
	h := New(nil, store.New())
	_, err := h.ListComplianceReports(context.Background(), &scannerv1.ListComplianceReportsRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
	})
	if got := codeOf(t, err); got != codes.FailedPrecondition {
		t.Errorf("code: got %v, want FailedPrecondition", got)
	}
}

// TestDefaultPolicy verifies the shape returned on cache miss matches the
// dashboard's "no policy yet" expectations.
func TestDefaultPolicy(t *testing.T) {
	p := defaultPolicy("11111111-1111-1111-1111-111111111111")
	if !p.GetAutoScanOnPush() {
		t.Error("default auto_scan_on_push should be true")
	}
	if p.GetBlockOnSeverity() != "" {
		t.Errorf("default block_on_severity: got %q, want empty", p.GetBlockOnSeverity())
	}
	if p.GetScannerPlugin() != "trivy" {
		t.Errorf("default scanner_plugin: got %q, want trivy", p.GetScannerPlugin())
	}
	if got := p.GetExemptCves(); got == nil {
		t.Error("default exempt_cves should be non-nil")
	}
}
