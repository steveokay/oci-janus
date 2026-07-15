//go:build integration

// Package integration — real-DB coverage for RenameRepository and
// TransferRepository (PR B of the repo rename/transfer feature). Both mutators
// reuse the CTE-then-SELECT shape shared by the other repo flag flips, so these
// tests also guard that the RETURNING/select column set resolves max_cvss_score
// (a mismatch would fail here with SQLSTATE 42703 rather than a bad value).
//
// The collision and missing-org paths exercise the sentinel-error mapping that
// the gRPC handler relies on to answer AlreadyExists / NotFound.

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

func TestRenameRepository_RoundTrip(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "rename-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	created, err := repo.CreateRepository(ctx, devTenantID, orgID, "before", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	renamed, err := repo.RenameRepository(ctx, devTenantID, created.GetRepoId(), "after")
	if err != nil {
		t.Fatalf("RenameRepository: %v", err)
	}
	if renamed.GetName() != "after" {
		t.Errorf("name = %q after rename, want %q", renamed.GetName(), "after")
	}
	// The row id is unchanged — rename is an in-place UPDATE, so any tags /
	// manifests keyed on repo_id stay attached.
	if renamed.GetRepoId() != created.GetRepoId() {
		t.Errorf("repo_id changed on rename: got %q, want %q", renamed.GetRepoId(), created.GetRepoId())
	}

	// A fresh read by the new full name resolves; the old name does not.
	byNew, err := repo.GetRepositoryByFullName(ctx, devTenantID, "rename-org/after")
	if err != nil {
		t.Fatalf("GetRepositoryByFullName(new): %v", err)
	}
	if byNew.GetRepoId() != created.GetRepoId() {
		t.Errorf("by-new-name resolved to wrong repo: got %q", byNew.GetRepoId())
	}
	if _, err := repo.GetRepositoryByFullName(ctx, devTenantID, "rename-org/before"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("old name still resolves after rename: err = %v, want ErrNotFound", err)
	}
}

func TestRenameRepository_Collision(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "rename-collide-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	if _, err := repo.CreateRepository(ctx, devTenantID, orgID, "taken", "", false, 1<<30); err != nil {
		t.Fatalf("CreateRepository(taken): %v", err)
	}
	victim, err := repo.CreateRepository(ctx, devTenantID, orgID, "victim", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository(victim): %v", err)
	}

	// Renaming victim onto the sibling name trips UNIQUE(org_id, name).
	if _, err := repo.RenameRepository(ctx, devTenantID, victim.GetRepoId(), "taken"); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("rename onto existing name: err = %v, want ErrAlreadyExists", err)
	}
}

func TestTransferRepository_RoundTrip(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	srcOrgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "xfer-src")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization(src): %v", err)
	}
	if _, err := repo.GetOrCreateOrganization(ctx, devTenantID, "xfer-dest"); err != nil {
		t.Fatalf("GetOrCreateOrganization(dest): %v", err)
	}
	created, err := repo.CreateRepository(ctx, devTenantID, srcOrgID, "mover", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	moved, err := repo.TransferRepository(ctx, devTenantID, created.GetRepoId(), "xfer-dest")
	if err != nil {
		t.Fatalf("TransferRepository: %v", err)
	}
	if moved.GetOrg() != "xfer-dest" {
		t.Errorf("org = %q after transfer, want %q", moved.GetOrg(), "xfer-dest")
	}
	if moved.GetRepoId() != created.GetRepoId() {
		t.Errorf("repo_id changed on transfer: got %q, want %q", moved.GetRepoId(), created.GetRepoId())
	}

	// Resolves under the new org's namespace; not under the old one.
	if _, err := repo.GetRepositoryByFullName(ctx, devTenantID, "xfer-dest/mover"); err != nil {
		t.Fatalf("GetRepositoryByFullName(dest): %v", err)
	}
	if _, err := repo.GetRepositoryByFullName(ctx, devTenantID, "xfer-src/mover"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("repo still resolves under source org after transfer: err = %v, want ErrNotFound", err)
	}
}

func TestTransferRepository_MissingDestOrg(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "xfer-noorg-src")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	created, err := repo.CreateRepository(ctx, devTenantID, orgID, "lonely", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	if _, err := repo.TransferRepository(ctx, devTenantID, created.GetRepoId(), "org-that-does-not-exist"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("transfer to missing org: err = %v, want ErrNotFound", err)
	}
}

func TestTransferRepository_Collision(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	srcOrgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "xfer-collide-src")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization(src): %v", err)
	}
	destOrgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "xfer-collide-dest")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization(dest): %v", err)
	}
	// Dest already holds a repo named "dup".
	if _, err := repo.CreateRepository(ctx, devTenantID, destOrgID, "dup", "", false, 1<<30); err != nil {
		t.Fatalf("CreateRepository(dest/dup): %v", err)
	}
	// Source has one of the same name.
	created, err := repo.CreateRepository(ctx, devTenantID, srcOrgID, "dup", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository(src/dup): %v", err)
	}

	if _, err := repo.TransferRepository(ctx, devTenantID, created.GetRepoId(), "xfer-collide-dest"); !errors.Is(err, repository.ErrAlreadyExists) {
		t.Errorf("transfer into org with name clash: err = %v, want ErrAlreadyExists", err)
	}
}
