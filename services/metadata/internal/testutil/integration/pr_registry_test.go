//go:build integration

// Package integration — FUT-023 Phase 1 PR-registry repository coverage.
//
// These tests hit the repository layer directly (not the gRPC handler) so they
// exercise the SQL the unit fakes cannot: the ON CONFLICT idempotency clause on
// UpsertPRNamespace, the ON DELETE SET NULL + explicit teardown UPDATE
// lifecycle, the config round-trip (incl. the sealed-secret bytes), and keyset
// pagination across a page boundary.
//
// They mirror the promotions_test.go harness: buildRepo spins up a PG16
// container, applies goose migrations, and returns a *repository.Repository
// under the seeded dev tenant (devTenantID). The `integration` build tag keeps
// them out of the default `go test` run; CI's integration job runs them.

package integration

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// prProvider is the only SCM provider Phase 1 supports; used verbatim by every
// namespace fixture so the (tenant, provider, source_repo, pr_number) key is
// stable across a test.
const prProvider = "github"

// seedPRNamespace provisions a real ephemeral org + an active pr_namespaces row
// for the given PR via the production repository methods (not raw SQL) so the
// fixture exercises the same code path production does. Returns the persisted
// namespace row (with its server-assigned id + org_id).
func seedPRNamespace(t *testing.T, repo *repository.Repository, sourceRepo, orgName string, prNumber int) *repository.PRNamespace {
	t.Helper()
	ctx := context.Background()

	orgIDStr, err := repo.GetOrCreateOrganization(ctx, devTenantID, orgName)
	if err != nil {
		t.Fatalf("GetOrCreateOrganization(%q): %v", orgName, err)
	}
	orgID := uuid.MustParse(orgIDStr)

	ns, err := repo.UpsertPRNamespace(ctx, repository.PRNamespace{
		TenantID:   uuid.MustParse(devTenantID),
		OrgID:      &orgID,
		Provider:   prProvider,
		SourceRepo: sourceRepo,
		PRNumber:   prNumber,
		OrgName:    orgName,
	})
	if err != nil {
		t.Fatalf("UpsertPRNamespace: %v", err)
	}
	return ns
}

// TestUpsertPRNamespace_ProvisionAndIdempotency verifies the provision path:
// UpsertPRNamespace captures the org_id, and re-running it for the same (tenant,
// provider, repo, pr_number) re-activates the SAME row (one row, refreshed
// org_id) rather than duplicating — the ON CONFLICT clause the unit fakes can't
// exercise.
func TestUpsertPRNamespace_ProvisionAndIdempotency(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	tenantUUID := uuid.MustParse(devTenantID)

	first := seedPRNamespace(t, repo, "acme/widget", "pr-widget-7", 7)
	if first.OrgID == nil {
		t.Fatal("provision must capture org_id")
	}
	if first.Status != "active" {
		t.Fatalf("provision status: got %q, want active", first.Status)
	}

	// Re-run with a DIFFERENT ephemeral org to prove the conflict path updates
	// org_id in place instead of inserting a second row.
	newOrgIDStr, err := repo.GetOrCreateOrganization(ctx, devTenantID, "pr-widget-7-v2")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization v2: %v", err)
	}
	newOrgID := uuid.MustParse(newOrgIDStr)
	second, err := repo.UpsertPRNamespace(ctx, repository.PRNamespace{
		TenantID:   tenantUUID,
		OrgID:      &newOrgID,
		Provider:   prProvider,
		SourceRepo: "acme/widget",
		PRNumber:   7,
		OrgName:    "pr-widget-7-v2",
	})
	if err != nil {
		t.Fatalf("UpsertPRNamespace (re-provision): %v", err)
	}

	// Same lifecycle row (same id), refreshed org.
	if second.ID != first.ID {
		t.Fatalf("re-provision must re-use the same row: got id %s, want %s", second.ID, first.ID)
	}
	if second.OrgID == nil || *second.OrgID != newOrgID {
		t.Fatalf("re-provision must refresh org_id to %s, got %v", newOrgID, second.OrgID)
	}

	// Exactly one active row for this key.
	rows, _, err := repo.ListPRNamespaces(ctx, tenantUUID, "active", 50, "")
	if err != nil {
		t.Fatalf("ListPRNamespaces: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 active namespace after idempotent re-provision, got %d", len(rows))
	}
}

// TestTearDownPRNamespace_DeletesOrgKeepsLifecycleRow verifies teardown deletes
// the referenced org AND leaves the pr_namespaces row present with
// status='torn_down', org_id IS NULL — proving ON DELETE SET NULL + the
// explicit teardown UPDATE preserve the lifecycle record.
func TestTearDownPRNamespace_DeletesOrgKeepsLifecycleRow(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	tenantUUID := uuid.MustParse(devTenantID)

	ns := seedPRNamespace(t, repo, "acme/api", "pr-api-11", 11)
	orgID := *ns.OrgID

	if err := repo.TearDownPRNamespace(ctx, tenantUUID, ns.ID, orgID); err != nil {
		t.Fatalf("TearDownPRNamespace: %v", err)
	}

	// The ephemeral org must be gone.
	if err := repo.DeleteOrganization(ctx, tenantUUID, orgID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("org should already be deleted by teardown, DeleteOrganization returned: %v", err)
	}

	// The lifecycle row must survive, torn_down, with a NULL org_id.
	got, err := repo.GetPRNamespace(ctx, tenantUUID, prProvider, "acme/api", 11)
	if err != nil {
		t.Fatalf("GetPRNamespace after teardown: %v", err)
	}
	if got.Status != "torn_down" {
		t.Errorf("status after teardown: got %q, want torn_down", got.Status)
	}
	if got.OrgID != nil {
		t.Errorf("org_id after teardown: got %v, want nil", got.OrgID)
	}
	if got.TornDownAt == nil {
		t.Error("torn_down_at must be stamped after teardown")
	}
}

// TestTearDownPRNamespace_TenantScoped verifies the tenant guard (SEC-085 #3):
// a teardown keyed with the WRONG tenant id must NOT flip the row or delete the
// org — the namespace stays active and the org survives.
func TestTearDownPRNamespace_TenantScoped(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	tenantUUID := uuid.MustParse(devTenantID)

	ns := seedPRNamespace(t, repo, "acme/scoped", "pr-scoped-3", 3)
	orgID := *ns.OrgID

	// A different tenant id must match no row / no org — the UPDATE and DELETE
	// both no-op, and the call still succeeds (both are unconditional-on-match).
	otherTenant := uuid.New()
	if err := repo.TearDownPRNamespace(ctx, otherTenant, ns.ID, orgID); err != nil {
		t.Fatalf("TearDownPRNamespace(wrong tenant): %v", err)
	}

	// The namespace must still be active under the real tenant.
	got, err := repo.GetPRNamespace(ctx, tenantUUID, prProvider, "acme/scoped", 3)
	if err != nil {
		t.Fatalf("GetPRNamespace: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("cross-tenant teardown must not flip status: got %q, want active", got.Status)
	}
	if got.OrgID == nil || *got.OrgID != orgID {
		t.Errorf("cross-tenant teardown must not null org_id: got %v, want %s", got.OrgID, orgID)
	}
	// And the org must still be deletable under the correct tenant (i.e. it
	// still exists) — a successful delete proves it wasn't removed above.
	if err := repo.DeleteOrganization(ctx, tenantUUID, orgID); err != nil {
		t.Errorf("org should have survived cross-tenant teardown, DeleteOrganization: %v", err)
	}
}

// TestPRRegistryConfig_RoundTrip verifies Upsert + Get preserve every field,
// including the sealed webhook_secret_enc bytes and promote_target_org, and
// that a second Upsert replaces the row (tenant_id is the PK).
func TestPRRegistryConfig_RoundTrip(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	tenantUUID := uuid.MustParse(devTenantID)

	sealed := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01}
	updatedBy := uuid.New()
	in := repository.PRRegistryConfig{
		TenantID:         tenantUUID,
		Enabled:          true,
		WebhookSecretEnc: sealed,
		KEKVersion:       1,
		PromoteTargetOrg: "prod-org",
		UpdatedBy:        &updatedBy,
	}
	if err := repo.UpsertPRRegistryConfig(ctx, in); err != nil {
		t.Fatalf("UpsertPRRegistryConfig: %v", err)
	}

	got, err := repo.GetPRRegistryConfig(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("GetPRRegistryConfig: %v", err)
	}
	if !got.Enabled {
		t.Error("enabled did not round-trip")
	}
	if string(got.WebhookSecretEnc) != string(sealed) {
		t.Errorf("webhook_secret_enc mismatch: got %x, want %x", got.WebhookSecretEnc, sealed)
	}
	if got.PromoteTargetOrg != "prod-org" {
		t.Errorf("promote_target_org: got %q, want prod-org", got.PromoteTargetOrg)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != updatedBy {
		t.Errorf("updated_by: got %v, want %s", got.UpdatedBy, updatedBy)
	}

	// A second upsert with an empty promote target + nil secret must replace
	// the row (tenant_id PK) — proving ON CONFLICT DO UPDATE, not a duplicate.
	if err := repo.UpsertPRRegistryConfig(ctx, repository.PRRegistryConfig{
		TenantID:         tenantUUID,
		Enabled:          false,
		WebhookSecretEnc: nil,
		KEKVersion:       1,
		PromoteTargetOrg: "", // NULLIF('', '') → SQL NULL → "" on read
	}); err != nil {
		t.Fatalf("UpsertPRRegistryConfig (replace): %v", err)
	}
	got2, err := repo.GetPRRegistryConfig(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("GetPRRegistryConfig (after replace): %v", err)
	}
	if got2.Enabled {
		t.Error("replace should have set enabled=false")
	}
	if len(got2.WebhookSecretEnc) != 0 {
		t.Errorf("replace should have cleared the secret, got %x", got2.WebhookSecretEnc)
	}
	if got2.PromoteTargetOrg != "" {
		t.Errorf("replace should have cleared promote_target_org, got %q", got2.PromoteTargetOrg)
	}
}

// TestGetPRRegistryConfig_NotConfigured verifies a tenant that never wrote a
// config gets ErrNotFound (the handler maps this to a defaults response).
func TestGetPRRegistryConfig_NotConfigured(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	// A random tenant id that never wrote a config row.
	_, err := repo.GetPRRegistryConfig(ctx, uuid.New())
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unconfigured tenant, got %v", err)
	}
}

// TestListPRNamespaces_StatusFilterAndPagination verifies the status filter
// (active vs torn_down) and keyset pagination across a page boundary.
func TestListPRNamespaces_StatusFilterAndPagination(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	tenantUUID := uuid.MustParse(devTenantID)

	// Seed three active namespaces.
	for i := 1; i <= 3; i++ {
		seedPRNamespace(t, repo, "acme/list", "pr-list-"+strconv.Itoa(i), i)
	}
	// Tear the first one down so the status filter has something to exclude.
	torn, err := repo.GetPRNamespace(ctx, tenantUUID, prProvider, "acme/list", 1)
	if err != nil {
		t.Fatalf("GetPRNamespace #1: %v", err)
	}
	if err := repo.TearDownPRNamespace(ctx, tenantUUID, torn.ID, *torn.OrgID); err != nil {
		t.Fatalf("TearDownPRNamespace #1: %v", err)
	}

	// status='active' must return exactly the two survivors.
	active, _, err := repo.ListPRNamespaces(ctx, tenantUUID, "active", 50, "")
	if err != nil {
		t.Fatalf("ListPRNamespaces(active): %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("want 2 active namespaces, got %d", len(active))
	}
	for _, ns := range active {
		if ns.Status != "active" {
			t.Errorf("active filter leaked a %q row", ns.Status)
		}
	}

	// status='torn_down' must return exactly the one we tore down.
	down, _, err := repo.ListPRNamespaces(ctx, tenantUUID, "torn_down", 50, "")
	if err != nil {
		t.Fatalf("ListPRNamespaces(torn_down): %v", err)
	}
	if len(down) != 1 || down[0].PRNumber != 1 {
		t.Fatalf("want 1 torn_down namespace (pr 1), got %+v", down)
	}

	// Keyset pagination across a page boundary: page size 1 over the two
	// active rows must yield a cursor, then the final row, then no cursor.
	page1, next1, err := repo.ListPRNamespaces(ctx, tenantUUID, "active", 1, "")
	if err != nil {
		t.Fatalf("ListPRNamespaces(page1): %v", err)
	}
	if len(page1) != 1 {
		t.Fatalf("page1: want 1 row, got %d", len(page1))
	}
	if next1 == "" {
		t.Fatal("page1: expected a non-empty next_page_token across the boundary")
	}

	page2, next2, err := repo.ListPRNamespaces(ctx, tenantUUID, "active", 1, next1)
	if err != nil {
		t.Fatalf("ListPRNamespaces(page2): %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page2: want 1 row, got %d", len(page2))
	}
	if next2 != "" {
		t.Errorf("page2: expected no further cursor at the end, got %q", next2)
	}
	// The two pages must be distinct rows (no overlap/skip).
	if page1[0].ID == page2[0].ID {
		t.Error("pagination returned the same row twice")
	}
}
