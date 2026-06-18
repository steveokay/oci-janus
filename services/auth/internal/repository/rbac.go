package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

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

// ListMembers returns all role assignments within a tenant for the given scope.
// scopeType must be "org" or "repo"; scopeValue is the org name or "org/repo" string.
func (r *UserRepository) ListMembers(ctx context.Context, tenantID uuid.UUID, scopeType, scopeValue string) ([]RoleAssignment, error) {
	const q = `
		SELECT ra.id, ra.tenant_id, ra.user_id, ro.name, ra.scope_type, ra.scope_value,
		       COALESCE(ra.granted_by, '00000000-0000-0000-0000-000000000000'::uuid), ra.created_at
		FROM   role_assignments ra
		JOIN   roles ro ON ro.id = ra.role_id
		WHERE  ra.tenant_id = $1 AND ra.scope_type = $2 AND ra.scope_value = $3`

	rows, err := r.pool.Query(ctx, q, tenantID, scopeType, scopeValue)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var out []RoleAssignment
	for rows.Next() {
		var a RoleAssignment
		if err := rows.Scan(
			&a.ID, &a.TenantID, &a.UserID, &a.RoleName,
			&a.ScopeType, &a.ScopeValue, &a.GrantedBy, &a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
