// Package service — unit tests for the delegation guards (Phase 5.3).
//
// These tests exercise the pure helpers (scopeDominates, VerifyDelegationBound,
// VerifyAllowedScopesSubset) without touching the database. Handler-level
// integration coverage lives in services/auth/internal/handler.
package service

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// TestScopeDominates_TenantContainment is the regression test for the Phase
// 5.3 code-review BLOCKER: tenant-scope assignments must dominate org- and
// repo-scope targets. Without this rule the existing tenant-admin → org-admin
// elevation flow (handler.handleElevateToOrgAdmin) breaks because the
// delegator-dominates guard wrongly returns PermissionDenied.
func TestScopeDominates_TenantContainment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		holderType  string
		holderValue string
		targetType  string
		targetValue string
		want        bool
	}{
		{"tenant_dominates_org", "tenant", "tenant-uuid-1", "org", "myorg", true},
		{"tenant_dominates_repo", "tenant", "tenant-uuid-1", "repo", "myorg/myrepo", true},
		// Tenant-to-tenant only dominates the same tenant; this is the
		// same-pair branch but verifying the rule reads cleanly.
		{"tenant_dominates_same_tenant", "tenant", "tenant-uuid-1", "tenant", "tenant-uuid-1", true},
		{"tenant_does_not_dominate_other_tenant", "tenant", "tenant-uuid-1", "tenant", "tenant-uuid-2", false},
		// Repo and org holders never escalate to tenant scope.
		{"org_does_not_dominate_tenant", "org", "myorg", "tenant", "tenant-uuid-1", false},
		{"repo_does_not_dominate_tenant", "repo", "myorg/myrepo", "tenant", "tenant-uuid-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scopeDominates(tc.holderType, tc.holderValue, tc.targetType, tc.targetValue)
			if got != tc.want {
				t.Fatalf("scopeDominates(%q,%q,%q,%q) = %v, want %v",
					tc.holderType, tc.holderValue, tc.targetType, tc.targetValue, got, tc.want)
			}
		})
	}
}

// TestVerifyDelegationBound_TenantAdminGrantsOrgAdmin is the end-to-end
// behaviour check for the bug fix: a tenant admin must be able to mint an
// org-admin grant (used by handler.handleElevateToOrgAdmin).
func TestVerifyDelegationBound_TenantAdminGrantsOrgAdmin(t *testing.T) {
	t.Parallel()
	caller := []repository.RoleAssignment{
		{ScopeType: "tenant", ScopeValue: "tenant-uuid-1", RoleName: "admin"},
	}
	// Admin on tenant should be allowed to grant admin on any org in the
	// tenant — the elevation surface the code-review-agent flagged.
	if err := VerifyDelegationBound(caller, "admin", "org", "myorg"); err != nil {
		t.Fatalf("tenant-admin granting org-admin: unexpected error: %v", err)
	}
	// Admin on tenant should also be allowed to grant lesser roles
	// (writer/reader) on org or repo targets within the tenant.
	if err := VerifyDelegationBound(caller, "writer", "repo", "myorg/myrepo"); err != nil {
		t.Fatalf("tenant-admin granting repo-writer: unexpected error: %v", err)
	}
	// But tenant-admin cannot grant owner (rank above admin).
	err := VerifyDelegationBound(caller, "owner", "org", "myorg")
	if err == nil {
		t.Fatal("tenant-admin granting org-owner: expected error, got nil")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot delegate role") {
		t.Fatalf("error message changed shape: %v", err)
	}
}
