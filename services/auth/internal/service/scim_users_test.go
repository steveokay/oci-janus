package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// newSCIMTestService builds a Service backed by fake repos + miniredis (via the
// shared setupServiceWithRepos harness) with the SCIM bootstrap tenant id set,
// so the SCIM service methods (which read s.scimTenantID) operate against a
// known tenant and the deprovision path's Redis/API-key machinery works.
func newSCIMTestService(t *testing.T) (*Service, *fakeUserRepo, uuid.UUID) {
	t.Helper()
	svc, users, _, cleanup := setupServiceWithRepos(t)
	t.Cleanup(cleanup)
	tenantID := uuid.New()
	svc.scimTenantID = tenantID
	return svc, users, tenantID
}

func TestSCIMProvision_newUser_createsAndGrantsReader(t *testing.T) {
	svc, users, tenantID := newSCIMTestService(t)

	res, err := svc.Provision(context.Background(), ScimProvisionInput{
		Email: "a@x.io", UserName: "a", ExternalID: "ext1",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.User == nil || res.Linked {
		t.Fatalf("expected a freshly-created (non-linked) user, got %+v", res)
	}
	if !res.GrantedReader {
		t.Error("expected a reader grant on the new user")
	}
	if len(users.grantedRoles) != 1 {
		t.Fatalf("expected exactly one grant, got %d", len(users.grantedRoles))
	}
	g := users.grantedRoles[0]
	if g.RoleName != "reader" || g.ScopeType != "org" || g.ScopeValue != "*" {
		t.Errorf("baseline grant should be reader@org:* , got %+v", g)
	}
	if g.TenantID != tenantID {
		t.Errorf("grant tenant mismatch: got %s want %s", g.TenantID, tenantID)
	}
}

func TestSCIMProvision_derivesUsernameFromEmail(t *testing.T) {
	svc, _, _ := newSCIMTestService(t)

	res, err := svc.Provision(context.Background(), ScimProvisionInput{
		Email: "noname@x.io", ExternalID: "ext-derive",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.User.Username == "" {
		t.Error("username should be derived from the email when omitted")
	}
}

func TestSCIMProvision_collisionPasswordless_links(t *testing.T) {
	svc, users, tenantID := newSCIMTestService(t)

	// Seed an existing passwordless user (e.g. an SSO account) with this email.
	existing := &repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "existing",
		Email:        "dup@x.io",
		PasswordHash: "", // passwordless — linkable
		IsActive:     true,
		Kind:         "human",
	}
	users.users["existing"] = existing

	res, err := svc.Provision(context.Background(), ScimProvisionInput{
		Email: "dup@x.io", UserName: "ignored", ExternalID: "ext-link",
	})
	if err != nil {
		t.Fatalf("provision (link): %v", err)
	}
	if !res.Linked {
		t.Fatal("expected the existing passwordless user to be linked, not recreated")
	}
	if res.User.ID != existing.ID {
		t.Error("linked result should reference the existing user")
	}
	if users.externalIDs[existing.ID] != "ext-link" {
		t.Errorf("expected external_id backfilled to ext-link, got %q", users.externalIDs[existing.ID])
	}
	// No new user created and no baseline grant on the link path.
	if len(users.grantedRoles) != 0 {
		t.Errorf("link path must not grant a role, got %d grants", len(users.grantedRoles))
	}
}

func TestSCIMProvision_collisionLocalPassword_conflict(t *testing.T) {
	svc, users, tenantID := newSCIMTestService(t)

	// Seed an existing user WITH a local password → takeover must be refused.
	users.users["local"] = &repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "local",
		Email:        "local@x.io",
		PasswordHash: "$argon2id$fakehash",
		IsActive:     true,
		Kind:         "human",
	}

	_, err := svc.Provision(context.Background(), ScimProvisionInput{
		Email: "local@x.io", UserName: "local2", ExternalID: "ext-conflict",
	})
	if !errors.Is(err, ErrSCIMConflict) {
		t.Fatalf("expected ErrSCIMConflict for a local-password collision, got %v", err)
	}
	if len(users.grantedRoles) != 0 {
		t.Error("conflict path must not grant a role")
	}
}

func TestSCIMSetActive_false_disables(t *testing.T) {
	svc, users, tenantID := newSCIMTestService(t)

	// Seed an active user in the bootstrap tenant.
	u := &repository.User{
		ID:       uuid.New(),
		TenantID: tenantID,
		Username: "toggle",
		Email:    "toggle@x.io",
		IsActive: true,
		Kind:     "human",
	}
	users.users["toggle"] = u

	if err := svc.SetActive(context.Background(), u.ID, false); err != nil {
		t.Fatalf("SetActive(false): %v", err)
	}
	if u.IsActive {
		t.Error("active:false must disable the user (is_active=false)")
	}

	if err := svc.SetActive(context.Background(), u.ID, true); err != nil {
		t.Fatalf("SetActive(true): %v", err)
	}
	if !u.IsActive {
		t.Error("active:true must re-enable the user")
	}
}

func TestSCIMGetByID_rejectsCrossTenant(t *testing.T) {
	svc, users, tenantID := newSCIMTestService(t)

	// A user in a DIFFERENT tenant must not be visible through the SCIM read.
	other := &repository.User{
		ID:       uuid.New(),
		TenantID: uuid.New(), // not the bootstrap tenant
		Username: "other",
		IsActive: true,
		Kind:     "human",
	}
	users.users["other"] = other
	_ = tenantID

	if _, err := svc.GetSCIMUserByID(context.Background(), other.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-tenant read must return ErrNotFound, got %v", err)
	}
}
