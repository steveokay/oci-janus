//go:build integration

// Package repository — real-DB coverage for RewriteRepoRoleScopes (repo
// rename / transfer RBAC-scope migration). Runs against a PostgreSQL 16
// container via the shared setupRBACWithSA / seedHuman helpers.
package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteRepoRoleScopes_movesOnlyMatchingRepoScope(t *testing.T) {
	ctx := context.Background()
	rbac, _, users := setupRBACWithSA(t, ctx)
	tenant, user := seedHuman(t, ctx, users, "u@example.com")

	// Three grants: a repo-scoped one on the repo being renamed, an org-scoped
	// one (must NOT move — org grants key on the org name alone), and a
	// repo-scoped one on a different repo (must NOT move).
	require.NoError(t, rbac.GrantRole(ctx, RoleAssignment{
		TenantID: tenant, UserID: user, RoleName: "writer",
		ScopeType: "repo", ScopeValue: "dev/old", GrantedBy: user,
	}))
	require.NoError(t, rbac.GrantRole(ctx, RoleAssignment{
		TenantID: tenant, UserID: user, RoleName: "admin",
		ScopeType: "org", ScopeValue: "dev", GrantedBy: user,
	}))
	require.NoError(t, rbac.GrantRole(ctx, RoleAssignment{
		TenantID: tenant, UserID: user, RoleName: "reader",
		ScopeType: "repo", ScopeValue: "other/x", GrantedBy: user,
	}))

	n, err := rbac.RewriteRepoRoleScopes(ctx, tenant, "dev/old", "dev/new")
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "exactly one repo-scoped grant should move")

	// The grant now lives under the new scope.
	newMembers, err := rbac.ListMembers(ctx, tenant, "repo", "dev/new")
	require.NoError(t, err)
	require.Len(t, newMembers, 1, "moved grant should appear under dev/new")

	// ...and no longer under the old scope.
	oldMembers, err := rbac.ListMembers(ctx, tenant, "repo", "dev/old")
	require.NoError(t, err)
	require.Empty(t, oldMembers, "no grant should remain under dev/old")

	// The org-scoped grant and the unrelated repo grant are untouched.
	orgMembers, err := rbac.ListMembers(ctx, tenant, "org", "dev")
	require.NoError(t, err)
	require.Len(t, orgMembers, 1, "org-scoped grant must not move")

	otherMembers, err := rbac.ListMembers(ctx, tenant, "repo", "other/x")
	require.NoError(t, err)
	require.Len(t, otherMembers, 1, "unrelated repo grant must not move")

	// Idempotent: re-running after the move affects zero rows.
	n2, err := rbac.RewriteRepoRoleScopes(ctx, tenant, "dev/old", "dev/new")
	require.NoError(t, err)
	require.Equal(t, int64(0), n2, "re-run should be a no-op")
}
