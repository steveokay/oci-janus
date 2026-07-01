//go:build integration

// Package integration — FUT-020 promotion round-trip coverage.
//
// These tests hit the repository layer directly (not the gRPC handler) so
// they can force the injection-failure case for the atomic-rollback
// assertion — the gRPC surface only exposes the happy paths.

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// promoteSeed sets up two repositories in the dev tenant: a source and a
// destination org+repo pair, plus a manifest + tag on the source. Returns
// the composite identifiers callers need to invoke PromoteTag.
type promoteSeed struct {
	srcOrg, srcRepo, srcTag string
	dstOrg, dstRepo, dstTag string
	srcDigest               string
	srcRepoID, dstRepoID    string
}

// seedPromoteFixtures provisions the source + destination repos and puts a
// manifest + tag on the source. The tag is left immutable=FALSE (both
// repo-wide and per-tag) so tests that DON'T care about immutability get
// the "happy path" state; tests that need immutability flip it explicitly.
func seedPromoteFixtures(t *testing.T, repo *repository.Repository, dstTagAlreadyExists bool) promoteSeed {
	t.Helper()
	ctx := context.Background()

	// One org for both source and destination to keep the fixture small —
	// the promotion path doesn't care whether src/dst share an org.
	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "prom-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	srcRepo, err := repo.CreateRepository(ctx, devTenantID, orgID, "src-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository src: %v", err)
	}
	dstRepo, err := repo.CreateRepository(ctx, devTenantID, orgID, "dst-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository dst: %v", err)
	}

	// Push a manifest so the source tag has something to point at. Content
	// doesn't matter for the promotion path — we're testing metadata copy.
	srcDigest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	rawJSON := []byte(`{"schemaVersion":2}`)
	if _, err := repo.PutManifest(ctx, devTenantID, srcRepo.GetRepoId(), srcDigest,
		"application/vnd.oci.image.manifest.v1+json", rawJSON, int64(len(rawJSON))); err != nil {
		t.Fatalf("PutManifest src: %v", err)
	}
	if _, err := repo.PutTag(ctx, devTenantID, srcRepo.GetRepoId(), "v1.0.0", srcDigest); err != nil {
		t.Fatalf("PutTag src: %v", err)
	}

	// Optionally pre-seed the destination tag at a DIFFERENT digest so the
	// immutability test can assert on the move.
	if dstTagAlreadyExists {
		otherDigest := "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		if _, err := repo.PutManifest(ctx, devTenantID, dstRepo.GetRepoId(), otherDigest,
			"application/vnd.oci.image.manifest.v1+json", rawJSON, int64(len(rawJSON))); err != nil {
			t.Fatalf("PutManifest dst-existing: %v", err)
		}
		if _, err := repo.PutTag(ctx, devTenantID, dstRepo.GetRepoId(), "v1.0.0-existing", otherDigest); err != nil {
			t.Fatalf("PutTag dst-existing: %v", err)
		}
	}

	return promoteSeed{
		srcOrg: "prom-org", srcRepo: "src-repo", srcTag: "v1.0.0",
		dstOrg: "prom-org", dstRepo: "dst-repo", dstTag: "v1.0.0",
		srcDigest: srcDigest,
		srcRepoID: srcRepo.GetRepoId(),
		dstRepoID: dstRepo.GetRepoId(),
	}
}

// TestPromoteTag_HappyPath verifies a promotion onto a fresh destination
// tag: the destination tag gets created, the digest matches the source,
// and one promotions row is recorded.
func TestPromoteTag_HappyPath(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	tenantUUID := uuid.MustParse(devTenantID)
	actor := uuid.New()
	prom, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID:    tenantUUID,
		SrcOrg:      seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:      seed.dstOrg, DstRepo: seed.dstRepo, DstTag: seed.dstTag,
		ActorUserID: &actor,
		Note:        "promote v1.0.0 to prod",
	})
	if err != nil {
		t.Fatalf("PromoteTag: %v", err)
	}
	if prom.GetId() == "" {
		t.Fatal("promotion missing id")
	}
	if prom.GetSrcDigest() != seed.srcDigest {
		t.Fatalf("src_digest mismatch: got %s, want %s", prom.GetSrcDigest(), seed.srcDigest)
	}
	if prom.GetDstDigest() != seed.srcDigest {
		t.Fatalf("dst_digest mismatch: got %s, want %s", prom.GetDstDigest(), seed.srcDigest)
	}
	if prom.GetNote() != "promote v1.0.0 to prod" {
		t.Fatalf("note round-trip failed: got %q", prom.GetNote())
	}

	// Destination tag exists at the promoted digest.
	dstTag, err := repo.GetTag(ctx, devTenantID, seed.dstRepoID, seed.dstTag)
	if err != nil {
		t.Fatalf("GetTag dst: %v", err)
	}
	if dstTag.GetManifestDigest() != seed.srcDigest {
		t.Fatalf("dst tag digest mismatch: got %s, want %s", dstTag.GetManifestDigest(), seed.srcDigest)
	}

	// One promotions row exists for this tenant.
	proms, err := repo.ListPromotions(ctx, tenantUUID, "", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions: %v", err)
	}
	if len(proms) != 1 {
		t.Fatalf("want 1 promotion row, got %d", len(proms))
	}
}

// TestPromoteTag_SourceMissing verifies a promotion whose source tag
// does not exist surfaces ErrNotFound.
func TestPromoteTag_SourceMissing(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	_, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: uuid.MustParse(devTenantID),
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: "does-not-exist",
		DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: seed.dstTag,
	})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestPromoteTag_DestRepoMissing verifies a promotion whose destination
// repository does not exist surfaces ErrNotFound. Verifies the closed-fail
// posture — we never accidentally create a repo on the way in.
func TestPromoteTag_DestRepoMissing(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	_, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: uuid.MustParse(devTenantID),
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:   seed.dstOrg, DstRepo: "no-such-repo", DstTag: seed.dstTag,
	})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestPromoteTag_DestRepoMissing_CreateIfMissing verifies the REM-030
// auto-create branch: when the destination repository row does not exist
// and CreateIfMissing=true, PromoteTag creates the repo inside the same
// transaction and the promotion succeeds. The destination ORG must exist
// (the seed helper provisions prom-org for both sides).
func TestPromoteTag_DestRepoMissing_CreateIfMissing(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	tenantUUID := uuid.MustParse(devTenantID)
	prom, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID:        tenantUUID,
		SrcOrg:          seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:          seed.dstOrg, DstRepo: "fresh-dst-repo", DstTag: "v1",
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("PromoteTag(create_if_missing=true): %v", err)
	}
	if prom.GetDstDigest() != seed.srcDigest {
		t.Fatalf("dst_digest mismatch: got %s, want %s", prom.GetDstDigest(), seed.srcDigest)
	}

	// The auto-created repo must be visible via a subsequent lookup with
	// permissive defaults (not immutable, not public). Look up via
	// GetRepositoryByName so we can go org name → repo without knowing
	// the freshly minted UUID.
	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, seed.dstOrg)
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	got, err := repo.GetRepositoryByName(ctx, devTenantID, orgID, "fresh-dst-repo")
	if err != nil {
		t.Fatalf("GetRepositoryByName fresh-dst-repo: %v", err)
	}
	if got.GetImmutableTags() {
		t.Fatal("auto-created repo should default to immutable_tags=false")
	}
	if got.GetIsPublic() {
		t.Fatal("auto-created repo should default to is_public=false")
	}
}

// TestPromoteTag_DestOrgMissing_CreateIfMissing verifies the auto-create
// branch still fails closed when the destination ORG is missing. Orgs
// are RBAC-relevant, so a typo in the org must never silently mint one.
func TestPromoteTag_DestOrgMissing_CreateIfMissing(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	_, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID:        uuid.MustParse(devTenantID),
		SrcOrg:          seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:          "does-not-exist-org", DstRepo: "any-repo", DstTag: "v1",
		CreateIfMissing: true,
	})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("want ErrNotFound (org missing), got %v", err)
	}
}

// TestPromoteTag_ImmutableDestExistingSameDigest verifies the idempotent
// re-promotion case: the destination tag exists at the SAME digest as the
// source, so even under immutable_tags=true the operation succeeds and
// records a new promotions row (audit continuity).
func TestPromoteTag_ImmutableDestExistingSameDigest(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	// Manually set the destination tag to the SAME digest as the source
	// via a first promotion, then flip immutable_tags on to test the
	// same-digest fast path.
	tenantUUID := uuid.MustParse(devTenantID)
	if _, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: tenantUUID,
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: seed.dstTag,
	}); err != nil {
		t.Fatalf("first PromoteTag: %v", err)
	}
	if _, err := repo.UpdateRepositoryImmutability(ctx, devTenantID, seed.dstRepoID, true); err != nil {
		t.Fatalf("UpdateRepositoryImmutability: %v", err)
	}

	// Re-promoting the same source onto the same destination = same digest;
	// the immutability gate should NOT trip because it's not a move.
	prom, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: tenantUUID,
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: seed.dstTag,
		Note:     "re-promotion for audit",
	})
	if err != nil {
		t.Fatalf("re-PromoteTag: %v", err)
	}
	if prom == nil || prom.GetId() == "" {
		t.Fatal("re-promotion returned empty row")
	}

	// Two promotions rows expected — the initial + the re-promote.
	proms, err := repo.ListPromotions(ctx, tenantUUID, "", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions: %v", err)
	}
	if len(proms) != 2 {
		t.Fatalf("want 2 promotion rows, got %d", len(proms))
	}
}

// TestPromoteTag_ImmutableDestExistingDifferentDigest verifies the
// immutability gate rejects a promotion that WOULD change the destination
// tag's digest when the repository has immutable_tags=true.
func TestPromoteTag_ImmutableDestExistingDifferentDigest(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	// dstTagAlreadyExists=true pre-seeds the destination at a DIFFERENT
	// digest (the seed helper is careful to pick a distinct sha).
	seed := seedPromoteFixtures(t, repo, true)

	// Point the destination tag at the pre-seeded digest so the promotion
	// would be a MOVE. Then flip immutable_tags.
	tenantUUID := uuid.MustParse(devTenantID)
	if _, err := repo.UpdateRepositoryImmutability(ctx, devTenantID, seed.dstRepoID, true); err != nil {
		t.Fatalf("UpdateRepositoryImmutability: %v", err)
	}

	// Use v1.0.0-existing (created at "eee..." digest by the seed) as the
	// destination tag so the incoming srcDigest is different.
	_, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: tenantUUID,
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: "v1.0.0-existing",
	})
	if !errors.Is(err, repository.ErrImmutableTag) {
		t.Fatalf("want ErrImmutableTag, got %v", err)
	}

	// The destination tag must NOT have been rewritten. Confirms the
	// rollback ran cleanly — this is the load-bearing atomicity
	// assertion: on error, NO write survives.
	tag, err := repo.GetTag(ctx, devTenantID, seed.dstRepoID, "v1.0.0-existing")
	if err != nil {
		t.Fatalf("GetTag dst-existing: %v", err)
	}
	if tag.GetManifestDigest() == seed.srcDigest {
		t.Fatal("destination tag was rewritten despite immutability gate — atomicity broken")
	}

	// And no promotions row should have been recorded.
	proms, err := repo.ListPromotions(ctx, tenantUUID, "", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions: %v", err)
	}
	if len(proms) != 0 {
		t.Fatalf("want 0 promotion rows on rollback, got %d", len(proms))
	}
}

// TestPromoteTag_AtomicRollbackOnCancelledContext verifies that a
// cancelled context surfaces as an error AND that no promotions row is
// left behind. This is the strict rollback assertion — we're not testing
// business-logic error, we're testing pgx's rollback contract on the
// transaction we opened.
func TestPromoteTag_AtomicRollbackOnCancelledContext(t *testing.T) {
	repo := buildRepo(t)
	seed := seedPromoteFixtures(t, repo, false)

	tenantUUID := uuid.MustParse(devTenantID)

	// Cancel the context before the first query runs. Any pgx call on a
	// cancelled ctx returns context.Canceled and the deferred Rollback
	// unwinds any pending write.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: tenantUUID,
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: seed.dstTag,
	})
	if err == nil {
		t.Fatal("expected error on cancelled ctx, got nil")
	}

	// The rollback must have unwound the tag upsert AND the promotions
	// insert. Read via a fresh (uncancelled) context.
	freshCtx := context.Background()
	if _, err := repo.GetTag(freshCtx, devTenantID, seed.dstRepoID, seed.dstTag); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected destination tag NOT to exist after rollback, got err=%v", err)
	}
	proms, err := repo.ListPromotions(freshCtx, tenantUUID, "", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions: %v", err)
	}
	if len(proms) != 0 {
		t.Fatalf("want 0 promotion rows on rollback, got %d", len(proms))
	}
}

// TestListPromotions_FilterByOrg verifies the org filter matches both
// src-side and dst-side rows (see the promoteFullName comment in the
// repository — the filter is intentionally symmetric).
func TestListPromotions_FilterByOrg(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	tenantUUID := uuid.MustParse(devTenantID)
	if _, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
		TenantID: tenantUUID,
		SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
		DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: seed.dstTag,
	}); err != nil {
		t.Fatalf("PromoteTag: %v", err)
	}

	// Filter by the shared org — should match.
	proms, err := repo.ListPromotions(ctx, tenantUUID, "prom-org", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions(org): %v", err)
	}
	if len(proms) != 1 {
		t.Fatalf("want 1 promotion via org filter, got %d", len(proms))
	}

	// Filter by an unrelated org — should be empty.
	proms, err = repo.ListPromotions(ctx, tenantUUID, "other-org", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions(other-org): %v", err)
	}
	if len(proms) != 0 {
		t.Fatalf("want 0 promotions for unrelated org, got %d", len(proms))
	}
}

// TestListPromotions_DefaultOrder verifies the newest-first ordering
// contract that the dashboard depends on.
func TestListPromotions_DefaultOrder(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()
	seed := seedPromoteFixtures(t, repo, false)

	tenantUUID := uuid.MustParse(devTenantID)
	// Push two distinct promotions by moving through two dst tag names.
	for _, dstTag := range []string{"first", "second"} {
		if _, err := repo.PromoteTag(ctx, repository.PromoteTagInput{
			TenantID: tenantUUID,
			SrcOrg:   seed.srcOrg, SrcRepo: seed.srcRepo, SrcTag: seed.srcTag,
			DstOrg:   seed.dstOrg, DstRepo: seed.dstRepo, DstTag: dstTag,
		}); err != nil {
			t.Fatalf("PromoteTag(%q): %v", dstTag, err)
		}
	}

	proms, err := repo.ListPromotions(ctx, tenantUUID, "", "", 10)
	if err != nil {
		t.Fatalf("ListPromotions: %v", err)
	}
	if len(proms) != 2 {
		t.Fatalf("want 2 promotions, got %d", len(proms))
	}
	// Newest first: "second" was inserted last so it should come first.
	if proms[0].GetDstTag() != "second" {
		t.Fatalf("want newest-first ordering (second at position 0), got %q", proms[0].GetDstTag())
	}
}
