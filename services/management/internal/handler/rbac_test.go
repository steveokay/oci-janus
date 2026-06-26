package handler

import (
	"testing"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// TestHasScopedRole_orgGrantCoversRepoInThatOrg verifies the containment rule
// from PENTEST-002 — an admin grant on org "myorg" must implicitly grant admin
// on any repo within that org (e.g. "myorg/myimage"), since org admins are
// expected to manage all repos in their org.
func TestHasScopedRole_orgGrantCoversRepoInThatOrg(t *testing.T) {
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "myorg"},
	}
	if !hasScopedRole(assignments, "repo", "myorg/myimage", "admin") {
		t.Fatal("org admin should be admin of repos within that org")
	}
	if !hasScopedRole(assignments, "repo", "myorg/anything", "writer") {
		t.Fatal("org admin should satisfy writer requirement on any repo in org")
	}
}

// TestHasScopedRole_orgGrantDoesNotCoverSiblingOrg is the core PENTEST-002 fix:
// admin of org-A must NOT be allowed to act as admin of org-B just because
// they have "admin" somewhere. This was the privilege-escalation bug.
func TestHasScopedRole_orgGrantDoesNotCoverSiblingOrg(t *testing.T) {
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "org-a"},
	}
	if hasScopedRole(assignments, "org", "org-b", "admin") {
		t.Fatal("admin of org-a must NOT be admin of org-b (PENTEST-002)")
	}
	if hasScopedRole(assignments, "repo", "org-b/anyrepo", "admin") {
		t.Fatal("admin of org-a must NOT be admin of any repo in org-b (PENTEST-002)")
	}
	if hasScopedRole(assignments, "repo", "org-b/anyrepo", "writer") {
		t.Fatal("admin of org-a must NOT be writer of repos in org-b")
	}
}

// TestHasScopedRole_repoGrantDoesNotCoverSiblingRepo confirms that a repo-scoped
// grant only covers the exact repo, not sibling repos in the same org or the
// parent org itself.
func TestHasScopedRole_repoGrantDoesNotCoverSiblingRepo(t *testing.T) {
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "repo", ScopeValue: "myorg/repo-a"},
	}
	if hasScopedRole(assignments, "repo", "myorg/repo-b", "admin") {
		t.Fatal("admin of repo-a must NOT be admin of sibling repo-b")
	}
	if hasScopedRole(assignments, "org", "myorg", "admin") {
		t.Fatal("admin of a repo must NOT imply admin of the parent org")
	}
	if !hasScopedRole(assignments, "repo", "myorg/repo-a", "admin") {
		t.Fatal("repo admin should be admin of that exact repo")
	}
}

// TestHasScopedRole_roleHierarchy verifies the ordering reader < writer < admin < owner.
// A grant at a higher level satisfies any lower-level requirement on the same scope.
func TestHasScopedRole_roleHierarchy(t *testing.T) {
	cases := []struct {
		grant    string
		need     string
		expected bool
	}{
		{"owner", "admin", true},
		{"owner", "writer", true},
		{"owner", "reader", true},
		{"admin", "writer", true},
		{"admin", "reader", true},
		{"writer", "admin", false},
		{"writer", "owner", false},
		{"writer", "reader", true},
		{"reader", "writer", false},
		{"reader", "reader", true},
	}
	for _, c := range cases {
		assignments := []*authv1.RoleAssignment{
			{Role: c.grant, ScopeType: "repo", ScopeValue: "myorg/myrepo"},
		}
		got := hasScopedRole(assignments, "repo", "myorg/myrepo", c.need)
		if got != c.expected {
			t.Errorf("grant=%q need=%q: got %v, want %v", c.grant, c.need, got, c.expected)
		}
	}
}

// TestHasScopedRole_emptyAndUnknown covers the fail-closed paths.
func TestHasScopedRole_emptyAndUnknown(t *testing.T) {
	if hasScopedRole(nil, "org", "myorg", "admin") {
		t.Fatal("empty assignment list must never grant access")
	}
	if hasScopedRole(nil, "org", "myorg", "reader") {
		t.Fatal("empty assignment list must never grant access (even reader)")
	}
	if hasScopedRole(
		[]*authv1.RoleAssignment{{Role: "admin", ScopeType: "org", ScopeValue: "myorg"}},
		"org", "myorg", "bogus-role",
	) {
		t.Fatal("unknown minimum role must fail closed")
	}
}

// TestHasScopedRole_orgPrefixIsNotSubstring guards against a subtle bug class:
// an admin of org "my" must NOT accidentally cover repos in org "myorg" via
// substring matching. The implementation must require a "/" separator.
func TestHasScopedRole_orgPrefixIsNotSubstring(t *testing.T) {
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "my"},
	}
	if hasScopedRole(assignments, "repo", "myorg/something", "admin") {
		t.Fatal("org name 'my' must not match repos under 'myorg/' (prefix-vs-name bug)")
	}
	if !hasScopedRole(assignments, "repo", "my/something", "admin") {
		t.Fatal("org name 'my' must match repos under 'my/'")
	}
}

// ---------------------------------------------------------------------------
// effectiveTenantAdmin — Phase 5.2 unit tests (Review §A1, Top-5 #2)
// ---------------------------------------------------------------------------

// TestEffectiveTenantAdmin_OrgAdminOnly_ReturnsFalse verifies that an org-scoped
// admin grant does NOT satisfy the tenant-admin gate introduced by Phase 5.2.
// This is the core security regression test — it encodes the bug that existed
// before the fix: any org admin could act as tenant admin.
func TestEffectiveTenantAdmin_OrgAdminOnly_ReturnsFalse(t *testing.T) {
	// org-A admin — the "broken" pre-Phase-5.2 state.
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "org-a"},
	}
	if effectiveTenantAdmin(assignments, "some-tenant-id") {
		t.Fatal("org-scoped admin MUST NOT satisfy effectiveTenantAdmin (Review §A1 Top-5 #2)")
	}
}

// TestEffectiveTenantAdmin_MultipleOrgAdmins_StillDenied confirms that holding
// admin grants on several orgs in the tenant does not collectively equal
// tenant-admin. The fix is about scope type, not accumulation.
func TestEffectiveTenantAdmin_MultipleOrgAdmins_StillDenied(t *testing.T) {
	// Admin on multiple orgs — still NOT tenant-admin.
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "org-a"},
		{Role: "admin", ScopeType: "org", ScopeValue: "org-b"},
		{Role: "admin", ScopeType: "org", ScopeValue: "org-c"},
	}
	if effectiveTenantAdmin(assignments, "tenant-xyz") {
		t.Fatal("holding multiple org-admin grants must NOT satisfy effectiveTenantAdmin")
	}
}

// TestEffectiveTenantAdmin_TenantAdmin_ReturnsTrue verifies the new
// migration-20260625000001 scope_type='tenant' path is accepted.
func TestEffectiveTenantAdmin_TenantAdmin_ReturnsTrue(t *testing.T) {
	const tid = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "tenant", ScopeValue: tid},
	}
	if !effectiveTenantAdmin(assignments, tid) {
		t.Fatal("tenant-scoped admin MUST satisfy effectiveTenantAdmin (migration 20260625000001)")
	}
}

// TestEffectiveTenantAdmin_TenantAdmin_WrongTenantID_Denied verifies cross-tenant
// isolation: a tenant-admin grant on a different tenant must NOT pass.
func TestEffectiveTenantAdmin_TenantAdmin_WrongTenantID_Denied(t *testing.T) {
	const thisTenant = "11111111-1111-1111-1111-111111111111"
	const otherTenant = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "tenant", ScopeValue: otherTenant},
	}
	if effectiveTenantAdmin(assignments, thisTenant) {
		t.Fatal("tenant-admin on a DIFFERENT tenant must NOT satisfy effectiveTenantAdmin for this tenant")
	}
}

// TestEffectiveTenantAdmin_PlatformAdmin_ReturnsTrue verifies the legacy
// (admin, org, "*") marker is still accepted (backwards compat with
// existing platform-admin grants before Phase 5.1 is complete).
func TestEffectiveTenantAdmin_PlatformAdmin_ReturnsTrue(t *testing.T) {
	assignments := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "*"},
	}
	if !effectiveTenantAdmin(assignments, "any-tenant") {
		t.Fatal("platform-admin marker (admin, org, '*') MUST satisfy effectiveTenantAdmin")
	}
}

// TestEffectiveTenantAdmin_EmptyAssignments_Denied is the fail-closed baseline.
func TestEffectiveTenantAdmin_EmptyAssignments_Denied(t *testing.T) {
	if effectiveTenantAdmin(nil, "any-tenant") {
		t.Fatal("nil assignments must never grant effectiveTenantAdmin")
	}
	if effectiveTenantAdmin([]*authv1.RoleAssignment{}, "any-tenant") {
		t.Fatal("empty assignment slice must never grant effectiveTenantAdmin")
	}
}

// TestEffectiveTenantAdmin_TenantReader_Denied verifies that reader-level
// grants at tenant scope do not qualify — admin minimum is required.
func TestEffectiveTenantAdmin_TenantReader_Denied(t *testing.T) {
	const tid = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	assignments := []*authv1.RoleAssignment{
		{Role: "reader", ScopeType: "tenant", ScopeValue: tid},
	}
	if effectiveTenantAdmin(assignments, tid) {
		t.Fatal("tenant-scoped reader must NOT satisfy effectiveTenantAdmin (requires admin minimum)")
	}
}

// TestEffectiveTenantAdmin_TenantWriter_Denied verifies writer-level grants at
// tenant scope also do not qualify.
func TestEffectiveTenantAdmin_TenantWriter_Denied(t *testing.T) {
	const tid = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	assignments := []*authv1.RoleAssignment{
		{Role: "writer", ScopeType: "tenant", ScopeValue: tid},
	}
	if effectiveTenantAdmin(assignments, tid) {
		t.Fatal("tenant-scoped writer must NOT satisfy effectiveTenantAdmin (requires admin minimum)")
	}
}

// TestHasScopedRole_platformAdminMarker — PENTEST-024: the literal "*" scope
// value is reserved for platform-admin grants. handleSetTenantQuota checks
// hasScopedRole(assignments, "org", "*", "admin"). An admin of a regular org
// must NOT be allowed through that gate just because they happen to be in the
// platform-admin tenant.
func TestHasScopedRole_platformAdminMarker(t *testing.T) {
	// Regular org admin — must NOT satisfy the platform-admin gate.
	regularAdmin := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "engineering"},
	}
	if hasScopedRole(regularAdmin, "org", "*", "admin") {
		t.Fatal("admin of a regular org must not satisfy the platform-admin '*' gate (PENTEST-024)")
	}

	// Explicit platform admin — must pass the gate.
	platformAdmin := []*authv1.RoleAssignment{
		{Role: "admin", ScopeType: "org", ScopeValue: "*"},
	}
	if !hasScopedRole(platformAdmin, "org", "*", "admin") {
		t.Fatal("explicit platform-admin grant (org=*, admin) must pass the gate")
	}

	// Platform admin must NOT bleed into a specific org check — the literal
	// "*" string is its own scope, not a wildcard the matcher expands.
	if hasScopedRole(platformAdmin, "org", "engineering", "admin") {
		t.Fatal("'*' marker must not match a specific org name (it is a literal, not a wildcard)")
	}
}
