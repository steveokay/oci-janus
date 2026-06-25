package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Member is the enriched view of a role assignment returned by ListMembers.
// It joins the users table for kind and the service_accounts table (via
// shadow_user_id) so callers can distinguish human users from service accounts
// and render the correct display name without a second round-trip.
type Member struct {
	// AssignmentID is the primary key of the role_assignments row. It is the
	// value callers must supply to RevokeRole / the frontend DELETE flow
	// (useRevokeOrgRole / useRevokeRepoRole). Must never be zero.
	AssignmentID uuid.UUID
	// UserID is the users.id of the principal holding the role. For service
	// accounts this is the shadow user id.
	UserID uuid.UUID
	// Kind is "human" or "service_account" (from users.kind).
	Kind string
	// Username is the literal users.username column (REM-018). Always
	// non-empty because the auth service enforces a non-empty username at
	// account creation. Surfaced separately from DisplayName so the FE
	// UserCell can render "<display_name> (@<username>)" without the BFF
	// having to parse the DisplayName fallback chain back apart.
	Username string
	// DisplayName is the human-friendly label for the principal.
	// For humans: users.display_name if set, else users.username, else users.email.
	// For service accounts: service_accounts.name.
	DisplayName string
	// ServiceAccountID is non-nil only when Kind is "service_account"; it is
	// the service_accounts.id (not the shadow user id).
	ServiceAccountID *uuid.UUID
	// Role is the human-readable role name (e.g. "admin", "writer").
	Role string
	// GrantedBy is the user who created this assignment, or the zero UUID when
	// the assignment was created by the system.
	GrantedBy uuid.UUID
	// GrantedByUsername + GrantedByDisplayName are the literal username +
	// best-available label for the granted-by principal (REM-018). Both are
	// empty when GrantedBy is the system zero-UUID — the LEFT JOIN finds no
	// row and the COALESCE chain falls through to ''. The BFF emits a stable
	// "system" placeholder when both are empty so the FE doesn't have to
	// inspect the zero-UUID.
	GrantedByUsername    string
	GrantedByDisplayName string
}

// RoleAssignment is the database model for a user's role within a tenant scope.
type RoleAssignment struct {
	// ID is the primary key of the role_assignments row.
	ID uuid.UUID
	// TenantID scopes the assignment to a specific tenant.
	TenantID uuid.UUID
	// UserID is the user who holds this role.
	UserID uuid.UUID
	// RoleName is the human-readable role name (e.g. "owner", "admin", "writer", "reader").
	RoleName string
	// ScopeType is either "org" or "repo".
	ScopeType string
	// ScopeValue is the org name or "org/repo" string that the role applies to.
	ScopeValue string
	// GrantedBy is the user who granted this role, or the zero UUID if granted by the system.
	GrantedBy uuid.UUID
	// CreatedAt is when the assignment was created.
	CreatedAt time.Time
}

// GetUserRoles returns all role assignments for the given user within a tenant.
// It joins the roles table so callers receive the human-readable role name directly.
func (r *UserRepository) GetUserRoles(ctx context.Context, userID, tenantID uuid.UUID) ([]RoleAssignment, error) {
	const q = `
		SELECT ra.id, ra.tenant_id, ra.user_id, ro.name, ra.scope_type, ra.scope_value,
		       COALESCE(ra.granted_by, '00000000-0000-0000-0000-000000000000'::uuid), ra.created_at
		FROM   role_assignments ra
		JOIN   roles ro ON ro.id = ra.role_id
		WHERE  ra.user_id = $1 AND ra.tenant_id = $2`

	rows, err := r.pool.Query(ctx, q, userID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("get user roles: %w", err)
	}
	defer rows.Close()

	var out []RoleAssignment
	for rows.Next() {
		var a RoleAssignment
		if err := rows.Scan(
			&a.ID, &a.TenantID, &a.UserID, &a.RoleName,
			&a.ScopeType, &a.ScopeValue, &a.GrantedBy, &a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan role assignment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GrantRole creates a role assignment for a user. The role is looked up by name
// so callers supply a human-readable role name (e.g. "admin") rather than a UUID.
// If the exact same assignment already exists the call is a no-op (idempotent).
func (r *UserRepository) GrantRole(ctx context.Context, a RoleAssignment) error {
	const q = `
		INSERT INTO role_assignments (tenant_id, user_id, role_id, scope_type, scope_value, granted_by)
		SELECT $1, $2, id, $3, $4, $5
		FROM   roles
		WHERE  name = $6
		ON CONFLICT (user_id, role_id, scope_type, scope_value) DO NOTHING`

	_, err := r.pool.Exec(ctx, q,
		a.TenantID, a.UserID, a.ScopeType, a.ScopeValue, a.GrantedBy, a.RoleName,
	)
	if err != nil {
		return fmt.Errorf("grant role: %w", err)
	}
	return nil
}

// RevokeRole deletes the role assignment with the given ID, scoped to the tenant
// to prevent cross-tenant revocation.
func (r *UserRepository) RevokeRole(ctx context.Context, assignmentID, tenantID uuid.UUID) error {
	return r.RevokeRoleScoped(ctx, assignmentID, tenantID, "", "")
}

// RevokeRoleScoped deletes the role assignment with the given ID only when the
// scope matches the supplied expected values (PENTEST-011). Empty expected
// values disable the corresponding check; passing both empty is equivalent to
// the plain RevokeRole call. A mismatch returns ErrNotFound so callers cannot
// distinguish "missing row" from "wrong scope" — preventing scope enumeration.
func (r *UserRepository) RevokeRoleScoped(ctx context.Context, assignmentID, tenantID uuid.UUID, expectedScopeType, expectedScopeValue string) error {
	const q = `
		DELETE FROM role_assignments
		WHERE id        = $1
		  AND tenant_id = $2
		  AND ($3 = '' OR scope_type  = $3)
		  AND ($4 = '' OR scope_value = $4)`
	tag, err := r.pool.Exec(ctx, q, assignmentID, tenantID, expectedScopeType, expectedScopeValue)
	if err != nil {
		return fmt.Errorf("revoke role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMembers returns the enriched membership list for the given tenant scope.
// scopeType must be "org" or "repo"; scopeValue is the org name or "org/repo"
// string. Each Member carries the principal's kind, a display name, and — for
// service-account principals — the service_accounts.id so callers can link back
// to the SA without a separate lookup.
//
// The query joins users for kind and LEFT JOINs service_accounts on
// shadow_user_id so the SA name is available in a single round-trip. For human
// users the COALESCE chain falls back through display_name → username → email
// to guarantee a non-empty label.
func (r *UserRepository) ListMembers(ctx context.Context, tenantID uuid.UUID, scopeType, scopeValue string) ([]Member, error) {
	// REM-018: gb LEFT JOIN enriches the granted-by user with username +
	// display_name in the same round-trip. COALESCE chains fall through to
	// '' when granted_by is the system zero-UUID (no matching row) so the
	// caller can render a "system" placeholder without inspecting the UUID.
	const q = `
		SELECT ra.id AS assignment_id,
		       u.id,
		       u.kind,
		       u.username,
		       COALESCE(sa.name, u.display_name, u.username, COALESCE(u.email, '')) AS display_name,
		       sa.id AS sa_id,
		       ro.name,
		       COALESCE(ra.granted_by, '00000000-0000-0000-0000-000000000000'::uuid),
		       COALESCE(gb.username, '') AS granted_by_username,
		       COALESCE(gb.display_name, gb.username, '') AS granted_by_display_name
		FROM   role_assignments ra
		JOIN   roles ro               ON ro.id              = ra.role_id
		JOIN   users u                ON u.id               = ra.user_id
		LEFT   JOIN service_accounts sa ON sa.shadow_user_id = u.id
		LEFT   JOIN users gb           ON gb.id              = ra.granted_by
		WHERE  ra.tenant_id  = $1
		  AND  ra.scope_type  = $2
		  AND  ra.scope_value = $3
		ORDER BY display_name, ra.id`

	rows, err := r.pool.Query(ctx, q, tenantID, scopeType, scopeValue)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		// sa_id is NULL for human users — scan into a pointer so pgx sets it to nil.
		var saID *uuid.UUID
		if err := rows.Scan(
			&m.AssignmentID, &m.UserID, &m.Kind, &m.Username, &m.DisplayName, &saID, &m.Role, &m.GrantedBy,
			&m.GrantedByUsername, &m.GrantedByDisplayName,
		); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		m.ServiceAccountID = saID
		out = append(out, m)
	}
	return out, rows.Err()
}
