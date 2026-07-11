package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ErrSCIMConflict signals a provision that collides with a local-password
// account (spec D3) — the handler maps it to 409 Conflict. Linking an IdP
// identity onto an account that can still authenticate with a local password
// would be an account-takeover vector, so we refuse it.
var ErrSCIMConflict = errors.New("scim: user exists with a local password")

// ScimProvisionInput carries the validated inputs for a SCIM POST /Users.
// UserName is optional; it is derived from the email when empty.
type ScimProvisionInput struct {
	Email       string
	UserName    string
	DisplayName string
	ExternalID  string
}

// ScimProvisionResult reports what Provision did: the resulting user, whether it
// adopted an existing passwordless account (Linked), and whether it granted the
// baseline reader role (only on the create path).
type ScimProvisionResult struct {
	User          *repository.User
	Linked        bool
	GrantedReader bool
}

// Provision creates (or links) a SCIM user per spec §7 + D3/D5. It runs against
// the deployment's bootstrap tenant (s.scimTenantID).
//
//   - No user with this email → create a passwordless user + grant reader@org:*
//     (SSO parity, D5).
//   - A passwordless user already has this email → link it (backfill
//     external_id) so re-provisioning is idempotent (D3).
//   - A LOCAL-password user has this email → refuse with ErrSCIMConflict (D3):
//     the IdP must not silently adopt an account that can log in with a password.
func (s *Service) Provision(ctx context.Context, in ScimProvisionInput) (*ScimProvisionResult, error) {
	tenantID := s.scimTenantID
	username := in.UserName
	if username == "" {
		username = DeriveSSOUsername(in.Email)
	}

	// Collision check (D3). GetByEmail is tenant-scoped.
	existing, err := s.users.GetByEmail(ctx, tenantID, in.Email)
	switch {
	case err == nil:
		if existing.PasswordHash != "" {
			// Local-password account — refuse to link (takeover guard).
			return nil, ErrSCIMConflict
		}
		// Passwordless (e.g. SSO) account — link by backfilling external_id.
		if lerr := s.users.SetExternalID(ctx, tenantID, existing.ID, in.ExternalID); lerr != nil {
			return nil, lerr
		}
		return &ScimProvisionResult{User: existing, Linked: true}, nil
	case errors.Is(err, repository.ErrNotFound):
		// No collision — fall through to create.
	default:
		return nil, err
	}

	u, err := s.users.CreateSCIMUser(ctx, tenantID, username, in.Email, in.DisplayName, in.ExternalID)
	if err != nil {
		return nil, err
	}

	// D5 — baseline reader @ org "*" (SSO parity). A failed grant fails the whole
	// provision so we never leave a role-less orphan the IdP believes succeeded.
	if gerr := s.GrantRole(ctx, repository.RoleAssignment{
		TenantID:   tenantID,
		UserID:     u.ID,
		RoleName:   "reader",
		ScopeType:  "org",
		ScopeValue: "*",
	}); gerr != nil {
		return nil, gerr
	}

	return &ScimProvisionResult{User: u, GrantedReader: true}, nil
}

// SetActive maps SCIM `active` to the existing disable primitive (spec D4):
// active=false → SetUserDisabled(true) (revokes JTIs + API keys); active=true →
// SetUserDisabled(false). Runs against the bootstrap tenant so the primitive's
// own cross-tenant guard applies.
func (s *Service) SetActive(ctx context.Context, userID uuid.UUID, active bool) error {
	_, err := s.SetUserDisabled(ctx, s.scimTenantID, userID, !active)
	return err
}

// ListSCIMUsers is a thin pass-through to the repository, clamping pagination to
// sane bounds (1-based startIndex, 1..200 count) and returning the page + total
// for the handler's SCIM ListResponse envelope.
func (s *Service) ListSCIMUsers(ctx context.Context, byUsername, byExternalID string, active *bool, startIndex, count int) ([]*repository.User, int, error) {
	if startIndex < 1 {
		startIndex = 1
	}
	if count <= 0 || count > 200 {
		count = 200
	}
	return s.users.ListSCIMUsers(ctx, s.scimTenantID, byUsername, byExternalID, active, startIndex, count)
}

// GetSCIMUserByID loads a user by primary key, scoped to the SCIM tenant — a
// user outside the bootstrap tenant surfaces as ErrNotFound so the SCIM surface
// can never read across tenants. It routes through GetSCIMUserByIDForTenant
// (not the standard GetByID) so the returned User carries external_id: the
// standard read's SELECT omits that column, which left the by-id / PUT / PATCH /
// DELETE responses echoing externalId:"" and broke Okta/Entra reconciliation.
func (s *Service) GetSCIMUserByID(ctx context.Context, userID uuid.UUID) (*repository.User, error) {
	return s.users.GetSCIMUserByIDForTenant(ctx, s.scimTenantID, userID)
}
