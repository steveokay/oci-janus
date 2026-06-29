// Unit tests for callerIsTenantAdmin — REDESIGN-001 Phase 5.4 deny path.
//
// The gate is the single chokepoint reached by every admin-only HTTP route
// on registry-auth (user creation, service-account management, SA API-key
// issuance). The Phase 5.4 contract: an API-key (service-account) bearer
// must be denied here regardless of the shadow user's inherited role
// assignments — the role lookup must not even fire.
package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestCallerIsTenantAdmin_ServiceAccount_Denied confirms that the gate
// short-circuits before any GetUserRoles call when the principal kind is
// "service_account". The shadow user is pre-promoted to admin so any
// post-deny role lookup would return true — but the deny fires first
// and returns false. This is the load-bearing assertion of Phase 5.4.
func TestCallerIsTenantAdmin_ServiceAccount_Denied(t *testing.T) {
	tc, cleanup := buildTestService(t)
	defer cleanup()

	userID := uuid.New()
	tenantID := uuid.New()
	// Promote the shadow user so a role-based gate would pass — only the
	// principal-kind check should keep this caller out.
	tc.users.makeAdmin(userID)

	got := callerIsTenantAdmin(context.Background(), tc.svc, userID, tenantID, "service_account")
	if got {
		t.Fatal("callerIsTenantAdmin must deny service_account principals even when the shadow user has admin roles")
	}
}

// TestCallerIsTenantAdmin_Human_Admin_Allowed is the positive control.
// A human caller with an admin role MUST pass the gate; otherwise the
// deny would be over-broad and lock out legitimate operators.
func TestCallerIsTenantAdmin_Human_Admin_Allowed(t *testing.T) {
	tc, cleanup := buildTestService(t)
	defer cleanup()

	userID := uuid.New()
	tenantID := uuid.New()
	tc.users.makeAdmin(userID)

	got := callerIsTenantAdmin(context.Background(), tc.svc, userID, tenantID, "human")
	if !got {
		t.Fatal("callerIsTenantAdmin must allow human admin callers")
	}
}

// TestCallerIsTenantAdmin_Human_NoRole_Denied verifies the existing
// role-based deny still fires for human callers without admin grants —
// regression coverage so the Phase 5.4 edit didn't accidentally widen
// the gate.
func TestCallerIsTenantAdmin_Human_NoRole_Denied(t *testing.T) {
	tc, cleanup := buildTestService(t)
	defer cleanup()

	userID := uuid.New()
	tenantID := uuid.New()
	// No makeAdmin call — the fake repo returns nil roles.

	got := callerIsTenantAdmin(context.Background(), tc.svc, userID, tenantID, "human")
	if got {
		t.Fatal("callerIsTenantAdmin must deny human callers without admin/owner roles")
	}
}

// TestCallerIsTenantAdmin_EmptyKind_TreatedAsHuman covers the legacy
// compatibility path. Tokens issued before Phase 5.4 carried no
// principal_kind claim, so the gate must treat empty kind as "human"
// rather than denying every legacy session.
func TestCallerIsTenantAdmin_EmptyKind_TreatedAsHuman(t *testing.T) {
	tc, cleanup := buildTestService(t)
	defer cleanup()

	userID := uuid.New()
	tenantID := uuid.New()
	tc.users.makeAdmin(userID)

	// Empty principalKind matches the legacy token shape and must NOT
	// trigger the SA deny.
	got := callerIsTenantAdmin(context.Background(), tc.svc, userID, tenantID, "")
	if !got {
		t.Fatal("callerIsTenantAdmin must NOT deny legacy tokens (principal_kind=='') with admin roles")
	}
}
