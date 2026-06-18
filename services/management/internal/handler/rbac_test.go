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
