//go:build integration

// Package integration contains end-to-end gRPC tests for registry-metadata.
// Each test starts a real PostgreSQL container via testcontainers, applies goose
// migrations, then serves the full handler stack in-process over a bufconn
// listener so no real TCP port is required.
package integration

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/handler"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
	metadatamigrations "github.com/steveokay/oci-janus/services/metadata/migrations"
)

const (
	// bufSize is the in-memory buffer capacity for the bufconn gRPC listener.
	bufSize = 1024 * 1024 // 1 MiB

	// devTenantID matches the tenant row seeded by migration 00002_seed_dev_tenant.sql.
	// All integration tests operate under this tenant so foreign-key constraints pass.
	devTenantID = "98dbe36b-ef28-4903-b25c-bff1b2921c9e"
)

// buildTestEnv spins up a PostgreSQL container, runs all goose migrations,
// wires the real repository → handler stack, and serves it in-process over
// a bufconn listener.  All resources (container, pool, gRPC server, connection)
// are registered for cleanup with t.Cleanup so callers need not do anything.
func buildTestEnv(t *testing.T) metadatav1.MetadataServiceClient {
	t.Helper()

	ctx := context.Background()

	// 1. Start a real PostgreSQL 16 container.
	dsn := containers.Postgres(t)

	// 2. Connect a pgxpool that the handler will use for all DB operations.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// 3. Apply migrations via goose so the schema matches production.
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(metadatamigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose.Up: %v", err)
	}

	// 4. Build the handler from the real repository (no mocks needed).
	repo := repository.New(pool)
	h := handler.New(repo)

	// 5. Start an in-process gRPC server via bufconn — no TCP port needed.
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 6. Dial the in-process server using lis.DialContext as the context dialer.
	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(lis.DialContext),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return metadatav1.NewMetadataServiceClient(conn)
}

// ── Test: CreateRepository / GetRepository round-trip ────────────────────────

// TestCreateRepository_GetRepository_RoundTrip verifies that a repository
// created via CreateRepository can be retrieved with GetRepository and the
// returned fields exactly match what was sent.
func TestCreateRepository_GetRepository_RoundTrip(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	// Use "org/repo" name format so the handler auto-creates the org.
	created, err := client.CreateRepository(ctx, &metadatav1.CreateRepositoryRequest{
		TenantId:     devTenantID,
		Name:         "myorg/myrepo",
		IsPublic:     true,
		StorageQuota: 5 << 30, // 5 GiB
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if created.RepoId == "" {
		t.Fatal("expected non-empty RepoId in CreateRepository response")
	}
	// The handler strips the org prefix — Name should be just the repo part.
	if created.Name != "myrepo" {
		t.Fatalf("want Name=myrepo, got %q", created.Name)
	}
	if created.TenantId != devTenantID {
		t.Fatalf("TenantId mismatch: want %s, got %s", devTenantID, created.TenantId)
	}
	if !created.IsPublic {
		t.Fatal("want IsPublic=true")
	}

	// Retrieve the same repository by its ID.
	got, err := client.GetRepository(ctx, &metadatav1.GetRepositoryRequest{
		TenantId: devTenantID,
		RepoId:   created.RepoId,
	})
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if got.RepoId != created.RepoId {
		t.Fatalf("GetRepository RepoId mismatch: got %s, want %s", got.RepoId, created.RepoId)
	}
	if got.Name != created.Name {
		t.Fatalf("GetRepository Name mismatch: got %q, want %q", got.Name, created.Name)
	}
}

// TestGetRepository_NotFound verifies a NotFound gRPC status is returned when
// querying a repository ID that does not exist.
func TestGetRepository_NotFound(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	_, err := client.GetRepository(ctx, &metadatav1.GetRepositoryRequest{
		TenantId: devTenantID,
		RepoId:   "00000000-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected error for missing repository, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want codes.NotFound, got %v", status.Code(err))
	}
}

// ── Test: PutManifest / GetManifest by digest ─────────────────────────────────

// TestPutManifest_GetManifest_ByDigest verifies that a manifest stored via
// PutManifest is retrievable by its exact SHA256 digest and that the raw JSON
// bytes are preserved verbatim.
func TestPutManifest_GetManifest_ByDigest(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	// Create a repository to own the manifest.
	repo := mustCreateRepo(t, client, devTenantID, "org1/imgA")

	rawJSON := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`)
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Store the manifest.
	m, err := client.PutManifest(ctx, &metadatav1.PutManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Digest:    digest,
		MediaType: "application/vnd.docker.distribution.manifest.v2+json",
		RawJson:   rawJSON,
		SizeBytes: int64(len(rawJSON)),
	})
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if m.ManifestId == "" {
		t.Fatal("expected non-empty ManifestId")
	}

	// Retrieve by digest.
	got, err := client.GetManifest(ctx, &metadatav1.GetManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Reference: digest,
	})
	if err != nil {
		t.Fatalf("GetManifest by digest: %v", err)
	}
	if got.Digest != digest {
		t.Fatalf("Digest mismatch: got %s, want %s", got.Digest, digest)
	}
	if !bytes.Equal(got.RawJson, rawJSON) {
		t.Fatalf("RawJson mismatch: got %q, want %q", got.RawJson, rawJSON)
	}
}

// ── Test: PutTag / GetTag / GetManifest via tag name ─────────────────────────

// TestPutTag_GetTag_ManifestViaTag tests the full tagging flow:
//  1. Push a manifest.
//  2. Tag it with PutTag.
//  3. GetTag confirms the tag row has the expected digest.
//  4. GetManifest using the tag name as the reference performs an indirect
//     lookup and must return the same manifest.
func TestPutTag_GetTag_ManifestViaTag(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	repo := mustCreateRepo(t, client, devTenantID, "org1/imgB")

	rawJSON := []byte(`{"schemaVersion":2}`)
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// Push manifest.
	_, err := client.PutManifest(ctx, &metadatav1.PutManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Digest:    digest,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   rawJSON,
		SizeBytes: int64(len(rawJSON)),
	})
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	// Tag the manifest.
	tag, err := client.PutTag(ctx, &metadatav1.PutTagRequest{
		TenantId:       devTenantID,
		RepoId:         repo.RepoId,
		Name:           "v1.0.0",
		ManifestDigest: digest,
	})
	if err != nil {
		t.Fatalf("PutTag: %v", err)
	}
	if tag.Name != "v1.0.0" {
		t.Fatalf("want tag Name=v1.0.0, got %q", tag.Name)
	}
	if tag.ManifestDigest != digest {
		t.Fatalf("tag ManifestDigest mismatch: got %s, want %s", tag.ManifestDigest, digest)
	}

	// GetTag round-trip.
	gotTag, err := client.GetTag(ctx, &metadatav1.GetTagRequest{
		TenantId: devTenantID,
		RepoId:   repo.RepoId,
		Name:     "v1.0.0",
	})
	if err != nil {
		t.Fatalf("GetTag: %v", err)
	}
	if gotTag.TagId == "" {
		t.Fatal("expected non-empty TagId from GetTag")
	}
	if gotTag.ManifestDigest != digest {
		t.Fatalf("GetTag ManifestDigest mismatch: got %s, want %s", gotTag.ManifestDigest, digest)
	}

	// GetManifest with tag name as reference — the handler resolves the tag to the digest.
	m, err := client.GetManifest(ctx, &metadatav1.GetManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Reference: "v1.0.0", // tag name, not a digest
	})
	if err != nil {
		t.Fatalf("GetManifest via tag: %v", err)
	}
	if m.Digest != digest {
		t.Fatalf("GetManifest via tag returned wrong digest: got %s, want %s", m.Digest, digest)
	}
}

// ── Test: DeleteTag ───────────────────────────────────────────────────────────

// TestDeleteTag_TagGone_ManifestStillAccessible verifies that deleting a tag
// removes only the tag row: the underlying manifest must still be accessible
// by its digest.
func TestDeleteTag_TagGone_ManifestStillAccessible(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	repo := mustCreateRepo(t, client, devTenantID, "org1/imgC")
	rawJSON := []byte(`{"schemaVersion":2}`)
	digest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	// Push manifest and tag it.
	if _, err := client.PutManifest(ctx, &metadatav1.PutManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Digest:    digest,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   rawJSON,
		SizeBytes: int64(len(rawJSON)),
	}); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if _, err := client.PutTag(ctx, &metadatav1.PutTagRequest{
		TenantId:       devTenantID,
		RepoId:         repo.RepoId,
		Name:           "latest",
		ManifestDigest: digest,
	}); err != nil {
		t.Fatalf("PutTag: %v", err)
	}

	// Delete the tag.
	if _, err := client.DeleteTag(ctx, &metadatav1.DeleteTagRequest{
		TenantId: devTenantID,
		RepoId:   repo.RepoId,
		Name:     "latest",
	}); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}

	// The tag must now be gone.
	if _, err := client.GetTag(ctx, &metadatav1.GetTagRequest{
		TenantId: devTenantID,
		RepoId:   repo.RepoId,
		Name:     "latest",
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("after DeleteTag: want codes.NotFound for GetTag, got %v", err)
	}

	// The manifest must still be accessible by digest.
	m, err := client.GetManifest(ctx, &metadatav1.GetManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Reference: digest,
	})
	if err != nil {
		t.Fatalf("manifest should survive tag deletion, but GetManifest returned: %v", err)
	}
	if m.Digest != digest {
		t.Fatalf("surviving manifest has wrong digest: got %s, want %s", m.Digest, digest)
	}
}

// ── Test: DeleteManifest ──────────────────────────────────────────────────────

// TestDeleteManifest_ManifestGone verifies that after calling DeleteManifest the
// manifest is no longer retrievable by its digest (codes.NotFound expected).
func TestDeleteManifest_ManifestGone(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	repo := mustCreateRepo(t, client, devTenantID, "org1/imgD")
	rawJSON := []byte(`{"schemaVersion":2}`)
	digest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	// Push a manifest.
	if _, err := client.PutManifest(ctx, &metadatav1.PutManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Digest:    digest,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   rawJSON,
		SizeBytes: int64(len(rawJSON)),
	}); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	// Delete the manifest.
	if _, err := client.DeleteManifest(ctx, &metadatav1.DeleteManifestRequest{
		TenantId: devTenantID,
		RepoId:   repo.RepoId,
		Digest:   digest,
	}); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	// The manifest must be gone.
	if _, err := client.GetManifest(ctx, &metadatav1.GetManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Reference: digest,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("after DeleteManifest: want codes.NotFound, got %v", err)
	}
}

// ── Test: ListTags pagination ─────────────────────────────────────────────────

// TestListTags_Pagination pushes three tags (alpha, beta, gamma) and verifies
// cursor-based pagination: page 1 (n=2) returns alpha+beta; page 2 (cursor
// after "beta") returns only gamma.
func TestListTags_Pagination(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	repo := mustCreateRepo(t, client, devTenantID, "org1/imgE")
	digest := "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	// Push a manifest that all three tags will reference.
	if _, err := client.PutManifest(ctx, &metadatav1.PutManifestRequest{
		TenantId:  devTenantID,
		RepoId:    repo.RepoId,
		Digest:    digest,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   []byte(`{}`),
		SizeBytes: 2,
	}); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	// Push tags in alphabetical order so the DB ordering is deterministic.
	for _, tagName := range []string{"alpha", "beta", "gamma"} {
		if _, err := client.PutTag(ctx, &metadatav1.PutTagRequest{
			TenantId:       devTenantID,
			RepoId:         repo.RepoId,
			Name:           tagName,
			ManifestDigest: digest,
		}); err != nil {
			t.Fatalf("PutTag %s: %v", tagName, err)
		}
	}

	// Page 1: ask for 2 tags starting from the beginning.
	page1 := mustListTags(t, client, devTenantID, repo.RepoId, 2, "")
	if len(page1) != 2 {
		t.Fatalf("page 1: want 2 tags, got %d", len(page1))
	}
	if page1[0].Name != "alpha" || page1[1].Name != "beta" {
		t.Fatalf("page 1 tag names: got %q %q, want alpha beta", page1[0].Name, page1[1].Name)
	}

	// Page 2: cursor after "beta" should return exactly "gamma".
	page2 := mustListTags(t, client, devTenantID, repo.RepoId, 2, "beta")
	if len(page2) != 1 {
		t.Fatalf("page 2: want 1 tag, got %d", len(page2))
	}
	if page2[0].Name != "gamma" {
		t.Fatalf("page 2 tag name: got %q, want gamma", page2[0].Name)
	}
}

// ── Test: GetTenantQuotaUsage ─────────────────────────────────────────────────

// TestGetTenantQuotaUsage_SaneValues creates a repository for the dev tenant
// and confirms GetTenantQuotaUsage returns non-negative usage and a positive quota.
func TestGetTenantQuotaUsage_SaneValues(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	// Create at least one repository so quota rows exist for this tenant.
	mustCreateRepo(t, client, devTenantID, "org1/quotatest")

	usage, err := client.GetTenantQuotaUsage(ctx, &metadatav1.GetTenantQuotaUsageRequest{
		TenantId: devTenantID,
	})
	if err != nil {
		t.Fatalf("GetTenantQuotaUsage: %v", err)
	}
	if usage.TenantId != devTenantID {
		t.Fatalf("TenantId mismatch: want %s, got %s", devTenantID, usage.TenantId)
	}
	// A fresh repository starts with 0 used bytes.
	if usage.UsedBytes < 0 {
		t.Fatalf("UsedBytes must be >= 0, got %d", usage.UsedBytes)
	}
	// Default quota is 10 GiB (10737418240 bytes), so QuotaBytes must be positive.
	if usage.QuotaBytes <= 0 {
		t.Fatalf("QuotaBytes must be > 0, got %d", usage.QuotaBytes)
	}
}

// ── Test: LinkBlob / UnlinkBlob ───────────────────────────────────────────────

// TestLinkBlob_UnlinkBlob verifies the full blob-link lifecycle:
//   - LinkBlob succeeds and is idempotent.
//   - UnlinkBlob removes the link.
//   - A second UnlinkBlob on the same (repo, digest) returns codes.NotFound.
func TestLinkBlob_UnlinkBlob(t *testing.T) {
	client := buildTestEnv(t)
	ctx := context.Background()

	repo := mustCreateRepo(t, client, devTenantID, "org1/blobtest")

	const (
		blobDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		storageKey = "blobs/98dbe36b-ef28-4903-b25c-bff1b2921c9e/sha256/ff/ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		sizeBytes  = int64(1024)
	)

	// LinkBlob must succeed on the first call.
	if _, err := client.LinkBlob(ctx, &metadatav1.LinkBlobRequest{
		RepoId:     repo.RepoId,
		BlobDigest: blobDigest,
		StorageKey: storageKey,
		SizeBytes:  sizeBytes,
	}); err != nil {
		t.Fatalf("LinkBlob (first): %v", err)
	}

	// LinkBlob is idempotent — a second call with the same arguments must not fail.
	if _, err := client.LinkBlob(ctx, &metadatav1.LinkBlobRequest{
		RepoId:     repo.RepoId,
		BlobDigest: blobDigest,
		StorageKey: storageKey,
		SizeBytes:  sizeBytes,
	}); err != nil {
		t.Fatalf("LinkBlob (idempotent): %v", err)
	}

	// UnlinkBlob must succeed.
	if _, err := client.UnlinkBlob(ctx, &metadatav1.UnlinkBlobRequest{
		RepoId:     repo.RepoId,
		BlobDigest: blobDigest,
	}); err != nil {
		t.Fatalf("UnlinkBlob: %v", err)
	}

	// Unlinking a blob that is no longer linked must return codes.NotFound.
	if _, err := client.UnlinkBlob(ctx, &metadatav1.UnlinkBlobRequest{
		RepoId:     repo.RepoId,
		BlobDigest: blobDigest,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("second UnlinkBlob: want codes.NotFound, got %v", err)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// mustCreateRepo calls CreateRepository using the "org/repo" name format and
// fails the test immediately on any error.
func mustCreateRepo(t *testing.T, client metadatav1.MetadataServiceClient, tenantID, name string) *metadatav1.Repository {
	t.Helper()
	repo, err := client.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: tenantID,
		Name:     name,
	})
	if err != nil {
		t.Fatalf("mustCreateRepo(%q): %v", name, err)
	}
	return repo
}

// mustListTags drains the ListTags server-streaming RPC into a slice.
// pageSize=0 means no limit; last is the cursor tag name (empty for the first page).
func mustListTags(t *testing.T, client metadatav1.MetadataServiceClient, tenantID, repoID string, pageSize int32, last string) []*metadatav1.Tag {
	t.Helper()
	stream, err := client.ListTags(context.Background(), &metadatav1.ListTagsRequest{
		TenantId: tenantID,
		RepoId:   repoID,
		PageSize: pageSize,
		Last:     last,
	})
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	var tags []*metadatav1.Tag
	for {
		tag, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ListTags stream.Recv: %v", err)
		}
		tags = append(tags, tag)
	}
	return tags
}
