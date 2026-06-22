//go:build integration

// Package repository — TestRBAC_ListMembers_ProjectsKind verifies that
// ListMembers (FE-API-048 Task 7) joins users and service_accounts so the
// returned []Member carries the correct Kind, DisplayName, and
// ServiceAccountID for both human and service-account principals.
//
// The test runs against a real PostgreSQL 16 container via testcontainers and
// shares the gooseUpTo / containers.Postgres helpers defined in
// migrations_test.go (same package, same build tag).
package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// setupRBACWithSA boots a fresh PostgreSQL container, applies all migrations
// up to and including 20260622000003 (polymorphic api_keys — the last T1–T3
// migration), and returns a UserRepository, a ServiceAccountRepo, and the
// shared pool. The container and pool are cleaned up via t.Cleanup.
func setupRBACWithSA(t *testing.T, ctx context.Context) (*UserRepository, *ServiceAccountRepo, *UserRepository) {
	t.Helper()

	// Spin up a fresh PostgreSQL 16 container. containers.Postgres registers
	// its own t.Cleanup for the container lifetime.
	dsn := containers.Postgres(t)

	// Apply all schema migrations through the polymorphic api_keys migration
	// (the latest T1–T3 migration in this sprint).
	gooseUpTo(t, dsn, "20260622000003")

	// Build the shared pgxpool used by all three repositories.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	users := NewUserRepository(pool)
	sa := NewServiceAccountRepo(pool)
	// Return users twice: once as the RBAC repo owner (same type), once as the
	// seedHuman helper target. Both share the same pool.
	return users, sa, users
}

// TestRBAC_ListMembers_ProjectsKind verifies that ListMembers enriches each
// member row with the users.kind column and — for service-account members —
// populates ServiceAccountID with the service_accounts.id and DisplayName
// with the SA name.
func TestRBAC_ListMembers_ProjectsKind(t *testing.T) {
	ctx := context.Background()

	// setupRBACWithSA returns (rbacRepo, saRepo, users) where rbacRepo and
	// users are the same *UserRepository under the hood (ListMembers and Create
	// live on the same type).
	rbac, saRepo, users := setupRBACWithSA(t, ctx)

	// Seed a human admin user in a fresh tenant.
	// seedHuman is defined in service_account_test.go (same package + build tag).
	tenant, admin := seedHuman(t, ctx, users, "admin@example.com")

	// Create a service account and retrieve its shadow user id.
	sa, _, err := saRepo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID:  tenant,
		Name:      "ci-prod",
		CreatedBy: admin,
	})
	require.NoError(t, err, "CreateAtomic must not fail")

	// Grant the human admin an "admin" role on org "acme".
	require.NoError(t, rbac.GrantRole(ctx, RoleAssignment{
		TenantID:   tenant,
		UserID:     admin,
		RoleName:   "admin",
		ScopeType:  "org",
		ScopeValue: "acme",
		GrantedBy:  admin,
	}), "GrantRole for human must succeed")

	// Grant the SA shadow user a "writer" role on org "acme".
	require.NoError(t, rbac.GrantRole(ctx, RoleAssignment{
		TenantID:   tenant,
		UserID:     sa.ShadowUserID,
		RoleName:   "writer",
		ScopeType:  "org",
		ScopeValue: "acme",
		GrantedBy:  admin,
	}), "GrantRole for SA shadow user must succeed")

	// ListMembers must return exactly two members.
	members, err := rbac.ListMembers(ctx, tenant, "org", "acme")
	require.NoError(t, err)
	require.Len(t, members, 2, "expected exactly 2 members (1 human + 1 SA)")

	// Validate both members; order is not guaranteed so we switch on Kind.
	var sawHuman, sawSA bool
	for _, m := range members {
		// Every member must carry a non-zero AssignmentID (role_assignments.id).
		// A zero value means the SELECT ra.id column was dropped from the query,
		// which breaks the frontend revoke flow (useRevokeOrgRole /
		// useRevokeRepoRole DELETE /orgs/{org}/members/{assignmentId}).
		require.NotEqual(t, [16]byte{}, m.AssignmentID,
			"AssignmentID must be non-zero for Kind=%q", m.Kind)

		switch m.Kind {
		case "human":
			// The human member must reference the admin user id and carry no SA id.
			require.Equal(t, admin, m.UserID,
				"human member UserID must match the seeded admin user")
			require.Nil(t, m.ServiceAccountID,
				"human member must have nil ServiceAccountID")
			require.Equal(t, "admin", m.Role,
				"human member must carry the granted role name")
			sawHuman = true

		case "service_account":
			// The SA member must reference the shadow user id, carry the SA's
			// primary key in ServiceAccountID, and use the SA name as DisplayName.
			require.Equal(t, sa.ShadowUserID, m.UserID,
				"SA member UserID must equal the shadow user id")
			require.NotNil(t, m.ServiceAccountID,
				"SA member must have a non-nil ServiceAccountID")
			require.Equal(t, sa.ID, *m.ServiceAccountID,
				"SA member ServiceAccountID must equal service_accounts.id")
			require.Equal(t, "ci-prod", m.DisplayName,
				"SA member DisplayName must equal the SA name")
			require.Equal(t, "writer", m.Role,
				"SA member must carry the granted role name")
			sawSA = true

		default:
			t.Errorf("unexpected Kind %q in ListMembers result", m.Kind)
		}
	}

	require.True(t, sawHuman, "expected a human member in the result")
	require.True(t, sawSA, "expected a service-account member in the result")
}
