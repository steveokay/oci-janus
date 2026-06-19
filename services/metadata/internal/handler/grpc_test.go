// Package handler — unit tests for MetadataHandler.
//
// All tests use a hand-written fakeRepo that implements metadataRepo.
// No real PostgreSQL or network connections are required (CLAUDE.md §18).
package handler

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// ── fakeRepo ─────────────────────────────────────────────────────────────────

// fakeRepo is a controllable in-memory stub for metadataRepo.
// Each group of fields corresponds to one RPC family. Tests set the fields they
// need and leave the rest at their zero values (nil error, nil proto pointer).
type fakeRepo struct {
	// CreateRepository
	createRepoResult *metadatav1.Repository
	createRepoErr    error

	// GetOrCreateOrganization (used by CreateRepository when OrgId is absent)
	getOrCreateOrgID  string
	getOrCreateOrgErr error

	// GetRepository
	getRepoResult *metadatav1.Repository
	getRepoErr    error

	// GetRepositoryByName / GetRepositoryByFullName
	getRepoByNameResult *metadatav1.Repository
	getRepoByNameErr    error

	getRepoByFullNameResult *metadatav1.Repository
	getRepoByFullNameErr    error

	// ListRepositories
	listReposResult []*metadatav1.Repository
	listReposErr    error

	// DeleteRepository
	deleteRepoErr error

	// UpdateRepositoryQuota
	updateQuotaResult *metadatav1.Repository
	updateQuotaErr    error

	// PutTag
	putTagResult *metadatav1.Tag
	putTagErr    error

	// GetTag
	getTagResult *metadatav1.Tag
	getTagErr    error

	// ListTags
	listTagsResult []*metadatav1.Tag
	listTagsErr    error

	// DeleteTag
	deleteTagErr error

	// PutManifest
	putManifestResult *metadatav1.Manifest
	putManifestErr    error

	// GetManifest
	getManifestResult *metadatav1.Manifest
	getManifestErr    error

	// DeleteManifest
	deleteManifestErr error

	// ListUntaggedManifests
	listUntaggedResult []*metadatav1.Manifest
	listUntaggedErr    error

	// LinkBlob / UnlinkBlob
	linkBlobErr   error
	unlinkBlobErr error

	// ListOrphanedBlobs
	listOrphanedResult []*metadatav1.BlobRef
	listOrphanedErr    error

	// Quota
	quotaUsageResult *metadatav1.QuotaUsage
	quotaUsageErr    error
	incrStorageErr   error
	decrStorageErr   error

	// Scan results
	upsertScanErr   error
	getScanResult   *metadatav1.ScanResult
	getScanErr      error
	vulnTotal       int64
	vulnCritical    int64
	vulnHigh        int64
	vulnErr         error
	// Repository count
	repoCount    int64
	repoCountErr error
}

// ── metadataRepo implementation on fakeRepo ───────────────────────────────────

func (f *fakeRepo) GetOrCreateOrganization(_ context.Context, _, _ string) (string, error) {
	return f.getOrCreateOrgID, f.getOrCreateOrgErr
}

func (f *fakeRepo) CreateRepository(_ context.Context, _, _, _ string, _ bool, _ int64) (*metadatav1.Repository, error) {
	return f.createRepoResult, f.createRepoErr
}

func (f *fakeRepo) GetRepository(_ context.Context, _, _ string) (*metadatav1.Repository, error) {
	return f.getRepoResult, f.getRepoErr
}

func (f *fakeRepo) GetRepositoryByName(_ context.Context, _, _, _ string) (*metadatav1.Repository, error) {
	return f.getRepoByNameResult, f.getRepoByNameErr
}

func (f *fakeRepo) GetRepositoryByFullName(_ context.Context, _, _ string) (*metadatav1.Repository, error) {
	return f.getRepoByFullNameResult, f.getRepoByFullNameErr
}

func (f *fakeRepo) ListRepositories(_ context.Context, _, _ string) ([]*metadatav1.Repository, error) {
	return f.listReposResult, f.listReposErr
}

func (f *fakeRepo) DeleteRepository(_ context.Context, _, _ string) error {
	return f.deleteRepoErr
}

func (f *fakeRepo) UpdateRepositoryQuota(_ context.Context, _, _ string, _ int64) (*metadatav1.Repository, error) {
	return f.updateQuotaResult, f.updateQuotaErr
}

func (f *fakeRepo) PutTag(_ context.Context, _, _, _, _ string) (*metadatav1.Tag, error) {
	return f.putTagResult, f.putTagErr
}

func (f *fakeRepo) GetTag(_ context.Context, _, _, _ string) (*metadatav1.Tag, error) {
	return f.getTagResult, f.getTagErr
}

func (f *fakeRepo) ListTags(_ context.Context, _, _ string, _ int32, _ string) ([]*metadatav1.Tag, error) {
	return f.listTagsResult, f.listTagsErr
}

func (f *fakeRepo) DeleteTag(_ context.Context, _, _, _ string) error {
	return f.deleteTagErr
}

func (f *fakeRepo) PutManifest(_ context.Context, _, _, _, _ string, _ []byte, _ int64) (*metadatav1.Manifest, error) {
	return f.putManifestResult, f.putManifestErr
}

func (f *fakeRepo) GetManifest(_ context.Context, _, _, _ string) (*metadatav1.Manifest, error) {
	return f.getManifestResult, f.getManifestErr
}

func (f *fakeRepo) DeleteManifest(_ context.Context, _, _, _ string) error {
	return f.deleteManifestErr
}

func (f *fakeRepo) ListUntaggedManifests(_ context.Context, _, _ string) ([]*metadatav1.Manifest, error) {
	return f.listUntaggedResult, f.listUntaggedErr
}

func (f *fakeRepo) LinkBlob(_ context.Context, _, _, _ string, _ int64) error {
	return f.linkBlobErr
}

func (f *fakeRepo) UnlinkBlob(_ context.Context, _, _ string) error {
	return f.unlinkBlobErr
}

func (f *fakeRepo) ListOrphanedBlobs(_ context.Context) ([]*metadatav1.BlobRef, error) {
	return f.listOrphanedResult, f.listOrphanedErr
}

func (f *fakeRepo) GetTenantQuotaUsage(_ context.Context, _ string) (*metadatav1.QuotaUsage, error) {
	return f.quotaUsageResult, f.quotaUsageErr
}

func (f *fakeRepo) UpdateTenantQuota(_ context.Context, _ string, _ int64) (*metadatav1.QuotaUsage, error) {
	return f.quotaUsageResult, f.quotaUsageErr
}

func (f *fakeRepo) IncrementTenantStorage(_ context.Context, _ string, _ int64) error {
	return f.incrStorageErr
}

func (f *fakeRepo) DecrementTenantStorage(_ context.Context, _ string, _ int64) error {
	return f.decrStorageErr
}

func (f *fakeRepo) UpsertScanResult(_ context.Context, _, _, _ string, _ []byte, _ map[string]int32) error {
	return f.upsertScanErr
}

func (f *fakeRepo) GetScanResult(_ context.Context, _, _ string) (*metadatav1.ScanResult, error) {
	return f.getScanResult, f.getScanErr
}

func (f *fakeRepo) GetTenantVulnerabilityCount(_ context.Context, _ string) (int64, int64, int64, error) {
	return f.vulnTotal, f.vulnCritical, f.vulnHigh, f.vulnErr
}

func (f *fakeRepo) CountRepositories(_ context.Context, _ string) (int64, error) {
	return f.repoCount, f.repoCountErr
}

// ── test helpers ──────────────────────────────────────────────────────────────

// newHandler wires a fakeRepo into a MetadataHandler for testing.
func newHandler(r metadataRepo) *MetadataHandler {
	return &MetadataHandler{repo: r}
}

// requireCode asserts that err carries the expected gRPC status code.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != want {
		t.Errorf("status code: got %v, want %v", st.Code(), want)
	}
}

// requireNoErr fails the test if err is non-nil.
func requireNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── CreateRepository ──────────────────────────────────────────────────────────

// TestCreateRepository_withOrgID_returnsRepository verifies the happy path when
// OrgId is explicitly provided (no org resolution required).
func TestCreateRepository_withOrgID_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "repo-1", TenantId: "t1", OrgId: "org-1", Name: "myrepo"}
	h := newHandler(&fakeRepo{createRepoResult: want})

	got, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Name:     "myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestCreateRepository_withFullName_resolvesOrg verifies that when OrgId is
// absent the handler parses the "org/repo" name and calls GetOrCreateOrganization.
func TestCreateRepository_withFullName_resolvesOrg(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "repo-2", TenantId: "t1", OrgId: "org-auto", Name: "myrepo"}
	h := newHandler(&fakeRepo{
		getOrCreateOrgID: "org-auto",
		createRepoResult: want,
	})

	got, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestCreateRepository_singleComponentName_returnsInvalidArgument ensures names
// without a "/" are rejected when OrgId is not set.
func TestCreateRepository_singleComponentName_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		Name:     "noslash",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestCreateRepository_orgResolutionError_returnsInternal verifies that a
// failure in GetOrCreateOrganization surfaces as codes.Internal.
func TestCreateRepository_orgResolutionError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getOrCreateOrgErr: errors.New("db down")})

	_, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireCode(t, err, codes.Internal)
}

// TestCreateRepository_alreadyExists_returnsExistingRepo verifies that when the
// repo already exists the handler falls back to GetRepositoryByName and returns
// the existing record without an error.
func TestCreateRepository_alreadyExists_returnsExistingRepo(t *testing.T) {
	existing := &metadatav1.Repository{RepoId: "repo-existing", Name: "myrepo"}
	h := newHandler(&fakeRepo{
		createRepoErr:       repository.ErrAlreadyExists,
		getRepoByNameResult: existing,
	})

	got, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Name:     "myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != existing.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, existing.RepoId)
	}
}

// TestCreateRepository_repoError_returnsInternal verifies that unexpected repo
// errors are translated to codes.Internal.
func TestCreateRepository_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{createRepoErr: errors.New("unexpected")})

	_, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Name:     "myrepo",
	})
	requireCode(t, err, codes.Internal)
}

// ── GetRepository ─────────────────────────────────────────────────────────────

// TestGetRepository_found_returnsRepository verifies the happy path.
func TestGetRepository_found_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "r1", TenantId: "t1"}
	h := newHandler(&fakeRepo{getRepoResult: want})

	got, err := h.GetRepository(context.Background(), &metadatav1.GetRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestGetRepository_notFound_returnsNotFound verifies ErrNotFound maps to
// codes.NotFound.
func TestGetRepository_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoErr: repository.ErrNotFound})

	_, err := h.GetRepository(context.Background(), &metadatav1.GetRepositoryRequest{TenantId: "t1", RepoId: "missing"})
	requireCode(t, err, codes.NotFound)
}

// TestGetRepository_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetRepository_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoErr: errors.New("db fail")})

	_, err := h.GetRepository(context.Background(), &metadatav1.GetRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireCode(t, err, codes.Internal)
}

// ── GetRepositoryByName ───────────────────────────────────────────────────────

// TestGetRepositoryByName_found_returnsRepository verifies the happy path.
func TestGetRepositoryByName_found_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "r2", Name: "myorg/myrepo"}
	h := newHandler(&fakeRepo{getRepoByFullNameResult: want})

	got, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestGetRepositoryByName_emptyTenantID_returnsInvalidArgument verifies that a
// missing tenant_id is rejected early.
func TestGetRepositoryByName_emptyTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "",
		Name:     "myorg/myrepo",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetRepositoryByName_emptyName_returnsInvalidArgument verifies that a
// missing name is rejected early.
func TestGetRepositoryByName_emptyName_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetRepositoryByName_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetRepositoryByName_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoByFullNameErr: repository.ErrNotFound})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "myorg/missing",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetRepositoryByName_repoError_returnsInternal verifies unexpected errors
// map to codes.Internal.
func TestGetRepositoryByName_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoByFullNameErr: errors.New("timeout")})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireCode(t, err, codes.Internal)
}

// ── DeleteRepository ──────────────────────────────────────────────────────────

// TestDeleteRepository_success_returnsEmpty verifies that a successful delete
// returns an empty proto with no error.
func TestDeleteRepository_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DeleteRepository(context.Background(), &metadatav1.DeleteRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDeleteRepository_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestDeleteRepository_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteRepoErr: repository.ErrNotFound})

	_, err := h.DeleteRepository(context.Background(), &metadatav1.DeleteRepositoryRequest{TenantId: "t1", RepoId: "missing"})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteRepository_repoError_returnsInternal verifies unexpected errors map
// to codes.Internal.
func TestDeleteRepository_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{deleteRepoErr: errors.New("db error")})

	_, err := h.DeleteRepository(context.Background(), &metadatav1.DeleteRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireCode(t, err, codes.Internal)
}

// ── PutTag ────────────────────────────────────────────────────────────────────

// TestPutTag_success_returnsTag verifies the happy path.
func TestPutTag_success_returnsTag(t *testing.T) {
	want := &metadatav1.Tag{TagId: "tag-1", Name: "v1.0.0", ManifestDigest: "sha256:abc"}
	h := newHandler(&fakeRepo{putTagResult: want})

	got, err := h.PutTag(context.Background(), &metadatav1.PutTagRequest{
		TenantId:       "t1",
		RepoId:         "r1",
		Name:           "v1.0.0",
		ManifestDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if got.TagId != want.TagId {
		t.Errorf("TagId: got %q, want %q", got.TagId, want.TagId)
	}
	if got.ManifestDigest != want.ManifestDigest {
		t.Errorf("ManifestDigest: got %q, want %q", got.ManifestDigest, want.ManifestDigest)
	}
}

// TestPutTag_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestPutTag_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{putTagErr: errors.New("constraint violation")})

	_, err := h.PutTag(context.Background(), &metadatav1.PutTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0", ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// TestPutTag_alreadyExists_returnsAlreadyExists verifies ErrAlreadyExists maps
// to codes.AlreadyExists.
func TestPutTag_alreadyExists_returnsAlreadyExists(t *testing.T) {
	h := newHandler(&fakeRepo{putTagErr: repository.ErrAlreadyExists})

	_, err := h.PutTag(context.Background(), &metadatav1.PutTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0", ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.AlreadyExists)
}

// ── GetTag ────────────────────────────────────────────────────────────────────

// TestGetTag_found_returnsTag verifies the happy path.
func TestGetTag_found_returnsTag(t *testing.T) {
	want := &metadatav1.Tag{TagId: "tag-2", Name: "latest"}
	h := newHandler(&fakeRepo{getTagResult: want})

	got, err := h.GetTag(context.Background(), &metadatav1.GetTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "latest",
	})
	requireNoErr(t, err)
	if got.TagId != want.TagId {
		t.Errorf("TagId: got %q, want %q", got.TagId, want.TagId)
	}
}

// TestGetTag_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetTag_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getTagErr: repository.ErrNotFound})

	_, err := h.GetTag(context.Background(), &metadatav1.GetTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "ghost",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetTag_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetTag_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getTagErr: errors.New("timeout")})

	_, err := h.GetTag(context.Background(), &metadatav1.GetTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "latest",
	})
	requireCode(t, err, codes.Internal)
}

// ── DeleteTag ─────────────────────────────────────────────────────────────────

// TestDeleteTag_success_returnsEmpty verifies the happy path.
func TestDeleteTag_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DeleteTag(context.Background(), &metadatav1.DeleteTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDeleteTag_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestDeleteTag_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteTagErr: repository.ErrNotFound})

	_, err := h.DeleteTag(context.Background(), &metadatav1.DeleteTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "gone",
	})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteTag_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestDeleteTag_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{deleteTagErr: errors.New("db error")})

	_, err := h.DeleteTag(context.Background(), &metadatav1.DeleteTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0",
	})
	requireCode(t, err, codes.Internal)
}

// ── PutManifest ───────────────────────────────────────────────────────────────

// TestPutManifest_success_returnsManifest verifies the happy path.
func TestPutManifest_success_returnsManifest(t *testing.T) {
	want := &metadatav1.Manifest{
		ManifestId: "m-1",
		Digest:     "sha256:deadbeef",
		MediaType:  "application/vnd.oci.image.manifest.v1+json",
		SizeBytes:  512,
	}
	h := newHandler(&fakeRepo{putManifestResult: want})

	got, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId:  "t1",
		RepoId:    "r1",
		Digest:    "sha256:deadbeef",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   []byte(`{}`),
		SizeBytes: 512,
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
	if got.Digest != want.Digest {
		t.Errorf("Digest: got %q, want %q", got.Digest, want.Digest)
	}
}

// TestPutManifest_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestPutManifest_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{putManifestErr: errors.New("storage full")})

	_, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc", MediaType: "application/json",
	})
	requireCode(t, err, codes.Internal)
}

// TestPutManifest_alreadyExists_returnsAlreadyExists verifies ErrAlreadyExists
// maps to codes.AlreadyExists.
func TestPutManifest_alreadyExists_returnsAlreadyExists(t *testing.T) {
	h := newHandler(&fakeRepo{putManifestErr: repository.ErrAlreadyExists})

	_, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc", MediaType: "application/json",
	})
	requireCode(t, err, codes.AlreadyExists)
}

// TestPutManifest_nilRawJSON_stillSucceeds verifies that nil RawJson does not
// cause a handler panic (nil slice is valid protobuf bytes field).
func TestPutManifest_nilRawJSON_stillSucceeds(t *testing.T) {
	want := &metadatav1.Manifest{ManifestId: "m-nil", Digest: "sha256:aaa"}
	h := newHandler(&fakeRepo{putManifestResult: want})

	got, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId:  "t1",
		RepoId:    "r1",
		Digest:    "sha256:aaa",
		MediaType: "application/json",
		RawJson:   nil,
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
}

// TestPutManifest_oversizeJSON_returnsInvalidArgument verifies that raw_json
// payloads above the 4 MiB cap are rejected before reaching the repository.
func TestPutManifest_oversizeJSON_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{}) // repo should never be called

	_, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId:  "t1",
		RepoId:    "r1",
		Digest:    "sha256:big",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   make([]byte, 4<<20+1), // one byte over the 4 MiB limit
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── GetManifest ───────────────────────────────────────────────────────────────

// TestGetManifest_found_returnsManifest verifies the happy path.
func TestGetManifest_found_returnsManifest(t *testing.T) {
	want := &metadatav1.Manifest{ManifestId: "m-2", Digest: "sha256:cafebabe"}
	h := newHandler(&fakeRepo{getManifestResult: want})

	got, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "sha256:cafebabe",
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
}

// TestGetManifest_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetManifest_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getManifestErr: repository.ErrNotFound})

	_, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "sha256:missing",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetManifest_tagReference_resolvedByRepo verifies that a non-digest
// reference (tag name) is passed straight to the repo layer, which handles
// resolution internally.
func TestGetManifest_tagReference_resolvedByRepo(t *testing.T) {
	want := &metadatav1.Manifest{ManifestId: "m-3", Digest: "sha256:resolved"}
	h := newHandler(&fakeRepo{getManifestResult: want})

	got, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "latest",
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
}

// TestGetManifest_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetManifest_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getManifestErr: errors.New("timeout")})

	_, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// TestGetManifest_tagNotFound_returnsNotFound verifies that when tag resolution
// fails with ErrNotFound the handler propagates codes.NotFound.
func TestGetManifest_tagNotFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getManifestErr: repository.ErrNotFound})

	_, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "nonexistent-tag",
	})
	requireCode(t, err, codes.NotFound)
}

// ── DeleteManifest ────────────────────────────────────────────────────────────

// TestDeleteManifest_success_returnsEmpty verifies the happy path.
func TestDeleteManifest_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DeleteManifest(context.Background(), &metadatav1.DeleteManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDeleteManifest_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestDeleteManifest_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteManifestErr: repository.ErrNotFound})

	_, err := h.DeleteManifest(context.Background(), &metadatav1.DeleteManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:gone",
	})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteManifest_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestDeleteManifest_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{deleteManifestErr: errors.New("foreign key")})

	_, err := h.DeleteManifest(context.Background(), &metadatav1.DeleteManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// ── LinkBlob / UnlinkBlob ─────────────────────────────────────────────────────

// TestLinkBlob_success_returnsEmpty verifies the happy path.
func TestLinkBlob_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.LinkBlob(context.Background(), &metadatav1.LinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:abc",
		StorageKey: "blobs/t1/sha256/ab/abc",
		SizeBytes:  1024,
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestLinkBlob_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestLinkBlob_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{linkBlobErr: errors.New("tx failed")})

	_, err := h.LinkBlob(context.Background(), &metadatav1.LinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:abc",
		StorageKey: "blobs/t1/sha256/ab/abc",
		SizeBytes:  1024,
	})
	requireCode(t, err, codes.Internal)
}

// TestUnlinkBlob_success_returnsEmpty verifies the happy path.
func TestUnlinkBlob_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.UnlinkBlob(context.Background(), &metadatav1.UnlinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestUnlinkBlob_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestUnlinkBlob_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{unlinkBlobErr: repository.ErrNotFound})

	_, err := h.UnlinkBlob(context.Background(), &metadatav1.UnlinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:gone",
	})
	requireCode(t, err, codes.NotFound)
}

// ── GetTenantQuotaUsage ───────────────────────────────────────────────────────

// TestGetTenantQuotaUsage_success_returnsUsage verifies the happy path.
func TestGetTenantQuotaUsage_success_returnsUsage(t *testing.T) {
	want := &metadatav1.QuotaUsage{TenantId: "t1", UsedBytes: 512, QuotaBytes: 10240}
	h := newHandler(&fakeRepo{quotaUsageResult: want})

	got, err := h.GetTenantQuotaUsage(context.Background(), &metadatav1.GetTenantQuotaUsageRequest{TenantId: "t1"})
	requireNoErr(t, err)
	if got.UsedBytes != want.UsedBytes {
		t.Errorf("UsedBytes: got %d, want %d", got.UsedBytes, want.UsedBytes)
	}
	if got.QuotaBytes != want.QuotaBytes {
		t.Errorf("QuotaBytes: got %d, want %d", got.QuotaBytes, want.QuotaBytes)
	}
}

// TestGetTenantQuotaUsage_repoError_returnsInternal verifies unexpected errors
// map to codes.Internal.
func TestGetTenantQuotaUsage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{quotaUsageErr: errors.New("db fail")})

	_, err := h.GetTenantQuotaUsage(context.Background(), &metadatav1.GetTenantQuotaUsageRequest{TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// ── IncrementTenantStorage / DecrementTenantStorage ──────────────────────────

// TestIncrementTenantStorage_success_returnsEmpty verifies the happy path.
func TestIncrementTenantStorage_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.IncrementTenantStorage(context.Background(), &metadatav1.IncrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    1024,
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestIncrementTenantStorage_repoError_returnsInternal verifies unexpected
// errors map to codes.Internal.
func TestIncrementTenantStorage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{incrStorageErr: errors.New("db error")})

	_, err := h.IncrementTenantStorage(context.Background(), &metadatav1.IncrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    512,
	})
	requireCode(t, err, codes.Internal)
}

// TestDecrementTenantStorage_success_returnsEmpty verifies the happy path.
func TestDecrementTenantStorage_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DecrementTenantStorage(context.Background(), &metadatav1.DecrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    256,
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDecrementTenantStorage_repoError_returnsInternal verifies unexpected
// errors map to codes.Internal.
func TestDecrementTenantStorage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{decrStorageErr: errors.New("db error")})

	_, err := h.DecrementTenantStorage(context.Background(), &metadatav1.DecrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    256,
	})
	requireCode(t, err, codes.Internal)
}

// ── UpdateScanStatus ──────────────────────────────────────────────────────────

// TestUpdateScanStatus_success_returnsEmpty verifies the happy path.
func TestUpdateScanStatus_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.UpdateScanStatus(context.Background(), &metadatav1.UpdateScanStatusRequest{
		ScanId:   "scan-1",
		TenantId: "t1",
		Status:   "complete",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestUpdateScanStatus_notFound_returnsNotFound verifies that a missing scan ID
// maps to codes.NotFound.
func TestUpdateScanStatus_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{upsertScanErr: repository.ErrNotFound})

	_, err := h.UpdateScanStatus(context.Background(), &metadatav1.UpdateScanStatusRequest{
		ScanId:   "scan-missing",
		TenantId: "t1",
		Status:   "complete",
	})
	requireCode(t, err, codes.NotFound)
}

// TestUpdateScanStatus_repoError_returnsInternal verifies unexpected errors map
// to codes.Internal.
func TestUpdateScanStatus_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{upsertScanErr: errors.New("db fail")})

	_, err := h.UpdateScanStatus(context.Background(), &metadatav1.UpdateScanStatusRequest{
		ScanId:   "scan-1",
		TenantId: "t1",
		Status:   "running",
	})
	requireCode(t, err, codes.Internal)
}

// ── GetScanResult ─────────────────────────────────────────────────────────────

// TestGetScanResult_found_returnsScanResult verifies the happy path including
// that SeverityCounts fields are passed through.
func TestGetScanResult_found_returnsScanResult(t *testing.T) {
	want := &metadatav1.ScanResult{
		ScanId:         "scan-1",
		ManifestDigest: "sha256:abc",
		Status:         "complete",
		SeverityCounts: map[string]int32{"CRITICAL": 2, "HIGH": 5},
	}
	h := newHandler(&fakeRepo{getScanResult: want})

	got, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if got.ScanId != want.ScanId {
		t.Errorf("ScanId: got %q, want %q", got.ScanId, want.ScanId)
	}
	if got.SeverityCounts["CRITICAL"] != 2 {
		t.Errorf("CRITICAL count: got %d, want 2", got.SeverityCounts["CRITICAL"])
	}
}

// TestGetScanResult_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetScanResult_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getScanErr: repository.ErrNotFound})

	_, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:missing",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetScanResult_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetScanResult_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getScanErr: errors.New("timeout")})

	_, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// TestGetScanResult_pendingStatus_returnsScanResult verifies that a scan in
// pending state is returned without error.
func TestGetScanResult_pendingStatus_returnsScanResult(t *testing.T) {
	want := &metadatav1.ScanResult{ScanId: "scan-2", Status: "pending"}
	h := newHandler(&fakeRepo{getScanResult: want})

	got, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:def",
	})
	requireNoErr(t, err)
	if got.Status != "pending" {
		t.Errorf("Status: got %q, want pending", got.Status)
	}
}

// TestGetScanResult_emptySeverityCounts_returnsResult verifies that a scan
// result with no severity counts is returned cleanly.
func TestGetScanResult_emptySeverityCounts_returnsResult(t *testing.T) {
	want := &metadatav1.ScanResult{ScanId: "scan-3", Status: "complete", SeverityCounts: map[string]int32{}}
	h := newHandler(&fakeRepo{getScanResult: want})

	got, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:clean",
	})
	requireNoErr(t, err)
	if got.ScanId != "scan-3" {
		t.Errorf("ScanId: got %q, want scan-3", got.ScanId)
	}
}

// ── GetTenantVulnerabilityCount ───────────────────────────────────────────────

// TestGetTenantVulnerabilityCount_success_returnsCorrectTotals verifies the
// happy path and that total = critical + high.
func TestGetTenantVulnerabilityCount_success_returnsCorrectTotals(t *testing.T) {
	h := newHandler(&fakeRepo{vulnTotal: 7, vulnCritical: 3, vulnHigh: 4})

	got, err := h.GetTenantVulnerabilityCount(context.Background(), &metadatav1.GetTenantVulnerabilityCountRequest{
		TenantId: "t1",
	})
	requireNoErr(t, err)
	if got.Total != 7 {
		t.Errorf("Total: got %d, want 7", got.Total)
	}
	if got.CriticalCount != 3 {
		t.Errorf("CriticalCount: got %d, want 3", got.CriticalCount)
	}
	if got.HighCount != 4 {
		t.Errorf("HighCount: got %d, want 4", got.HighCount)
	}
}

// TestGetTenantVulnerabilityCount_repoError_returnsInternal verifies unexpected
// errors map to codes.Internal.
func TestGetTenantVulnerabilityCount_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{vulnErr: errors.New("db fail")})

	_, err := h.GetTenantVulnerabilityCount(context.Background(), &metadatav1.GetTenantVulnerabilityCountRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.Internal)
}

// ── UpdateRepositoryQuota ─────────────────────────────────────────────────────

// TestUpdateRepositoryQuota_success_returnsRepository verifies the happy path.
func TestUpdateRepositoryQuota_success_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "r1", StorageQuota: 20 << 30}
	h := newHandler(&fakeRepo{updateQuotaResult: want})

	got, err := h.UpdateRepositoryQuota(context.Background(), &metadatav1.UpdateRepositoryQuotaRequest{
		TenantId:     "t1",
		RepoId:       "r1",
		StorageQuota: 20 << 30,
	})
	requireNoErr(t, err)
	if got.StorageQuota != want.StorageQuota {
		t.Errorf("StorageQuota: got %d, want %d", got.StorageQuota, want.StorageQuota)
	}
}

// TestUpdateRepositoryQuota_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestUpdateRepositoryQuota_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{updateQuotaErr: repository.ErrNotFound})

	_, err := h.UpdateRepositoryQuota(context.Background(), &metadatav1.UpdateRepositoryQuotaRequest{
		TenantId: "t1", RepoId: "missing", StorageQuota: 1024,
	})
	requireCode(t, err, codes.NotFound)
}

// ── mapErr (internal helper) ──────────────────────────────────────────────────

// TestMapErr_nil_returnsNil verifies that mapErr passes through nil unchanged.
func TestMapErr_nil_returnsNil(t *testing.T) {
	if mapErr(nil) != nil {
		t.Error("expected mapErr(nil) == nil")
	}
}

// TestMapErr_notFound_returnsNotFoundCode verifies ErrNotFound sentinel mapping.
func TestMapErr_notFound_returnsNotFoundCode(t *testing.T) {
	requireCode(t, mapErr(repository.ErrNotFound), codes.NotFound)
}

// TestMapErr_alreadyExists_returnsAlreadyExistsCode verifies ErrAlreadyExists
// sentinel mapping.
func TestMapErr_alreadyExists_returnsAlreadyExistsCode(t *testing.T) {
	requireCode(t, mapErr(repository.ErrAlreadyExists), codes.AlreadyExists)
}

// TestMapErr_unknownError_returnsInternalCode verifies that any non-sentinel
// error maps to codes.Internal.
func TestMapErr_unknownError_returnsInternalCode(t *testing.T) {
	requireCode(t, mapErr(errors.New("random")), codes.Internal)
}

// ── Streaming stream fakes ────────────────────────────────────────────────────

// baseServerStream is a minimal grpc.ServerStream implementation shared by all
// typed stream fakes below. It satisfies the interface without real gRPC
// transport — it stores a background context and discards all control calls.
type baseServerStream struct{}

func (b *baseServerStream) SetHeader(metadata.MD) error  { return nil }
func (b *baseServerStream) SendHeader(metadata.MD) error { return nil }
func (b *baseServerStream) SetTrailer(metadata.MD)       {}
func (b *baseServerStream) Context() context.Context     { return context.Background() }
func (b *baseServerStream) SendMsg(m any) error          { return nil }
func (b *baseServerStream) RecvMsg(m any) error          { return nil }

// fakeListReposStream implements metadatav1.MetadataService_ListRepositoriesServer.
// It captures every Send call so tests can inspect the items streamed.
type fakeListReposStream struct {
	baseServerStream
	sent    []*metadatav1.Repository
	sendErr error
}

func (f *fakeListReposStream) Send(r *metadatav1.Repository) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, r)
	return nil
}

// Compile-time check: fakeListReposStream satisfies the generated stream interface.
var _ metadatav1.MetadataService_ListRepositoriesServer = (*fakeListReposStream)(nil)

// fakeListTagsStream implements metadatav1.MetadataService_ListTagsServer.
type fakeListTagsStream struct {
	baseServerStream
	sent    []*metadatav1.Tag
	sendErr error
}

func (f *fakeListTagsStream) Send(t *metadatav1.Tag) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, t)
	return nil
}

var _ metadatav1.MetadataService_ListTagsServer = (*fakeListTagsStream)(nil)

// fakeListUntaggedStream implements metadatav1.MetadataService_ListUntaggedManifestsServer.
type fakeListUntaggedStream struct {
	baseServerStream
	sent    []*metadatav1.Manifest
	sendErr error
}

func (f *fakeListUntaggedStream) Send(m *metadatav1.Manifest) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, m)
	return nil
}

var _ metadatav1.MetadataService_ListUntaggedManifestsServer = (*fakeListUntaggedStream)(nil)

// fakeListOrphanedStream implements metadatav1.MetadataService_ListOrphanedBlobsServer.
type fakeListOrphanedStream struct {
	baseServerStream
	sent    []*metadatav1.BlobRef
	sendErr error
}

func (f *fakeListOrphanedStream) Send(b *metadatav1.BlobRef) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, b)
	return nil
}

var _ metadatav1.MetadataService_ListOrphanedBlobsServer = (*fakeListOrphanedStream)(nil)

// ── ListRepositories (streaming) ──────────────────────────────────────────────

// TestListRepositories_success_sendsAllRepos verifies that every repository
// returned by the repo layer is forwarded to the stream.
func TestListRepositories_success_sendsAllRepos(t *testing.T) {
	repos := []*metadatav1.Repository{
		{RepoId: "r1", Name: "myorg/a"},
		{RepoId: "r2", Name: "myorg/b"},
	}
	h := newHandler(&fakeRepo{listReposResult: repos})
	stream := &fakeListReposStream{}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d repos, want 2", len(stream.sent))
	}
}

// TestListRepositories_repoError_returnsInternal verifies that a repo layer
// error stops the stream with codes.Internal.
func TestListRepositories_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listReposErr: errors.New("db fail")})
	stream := &fakeListReposStream{}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListRepositories_empty_sendsNothing verifies that an empty result
// completes without error and without sending any messages.
func TestListRepositories_empty_sendsNothing(t *testing.T) {
	h := newHandler(&fakeRepo{listReposResult: nil})
	stream := &fakeListReposStream{}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}

// TestListRepositories_sendError_propagates verifies that a stream Send error
// is returned to the caller.
func TestListRepositories_sendError_propagates(t *testing.T) {
	repos := []*metadatav1.Repository{{RepoId: "r1"}}
	h := newHandler(&fakeRepo{listReposResult: repos})
	stream := &fakeListReposStream{sendErr: errors.New("client disconnected")}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	if err == nil {
		t.Fatal("expected error from send failure, got nil")
	}
}

// ── ListTags (streaming) ──────────────────────────────────────────────────────

// TestListTags_success_sendsAllTags verifies that every tag is forwarded.
func TestListTags_success_sendsAllTags(t *testing.T) {
	tags := []*metadatav1.Tag{
		{TagId: "t1", Name: "latest"},
		{TagId: "t2", Name: "v1.0.0"},
	}
	h := newHandler(&fakeRepo{listTagsResult: tags})
	stream := &fakeListTagsStream{}

	err := h.ListTags(&metadatav1.ListTagsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d tags, want 2", len(stream.sent))
	}
}

// TestListTags_repoError_returnsInternal verifies a repo error stops the stream.
func TestListTags_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listTagsErr: errors.New("db fail")})
	stream := &fakeListTagsStream{}

	err := h.ListTags(&metadatav1.ListTagsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListTags_empty_sendsNothing verifies an empty result completes cleanly.
func TestListTags_empty_sendsNothing(t *testing.T) {
	h := newHandler(&fakeRepo{listTagsResult: nil})
	stream := &fakeListTagsStream{}

	err := h.ListTags(&metadatav1.ListTagsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}

// ── ListUntaggedManifests (streaming) ─────────────────────────────────────────

// TestListUntaggedManifests_success_sendsAll verifies that all untagged
// manifests are forwarded to the stream.
func TestListUntaggedManifests_success_sendsAll(t *testing.T) {
	manifests := []*metadatav1.Manifest{
		{ManifestId: "m1", Digest: "sha256:aaa"},
		{ManifestId: "m2", Digest: "sha256:bbb"},
	}
	h := newHandler(&fakeRepo{listUntaggedResult: manifests})
	stream := &fakeListUntaggedStream{}

	err := h.ListUntaggedManifests(&metadatav1.ListUntaggedManifestsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d manifests, want 2", len(stream.sent))
	}
}

// TestListUntaggedManifests_repoError_returnsInternal verifies a repo error
// stops the stream.
func TestListUntaggedManifests_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listUntaggedErr: errors.New("db fail")})
	stream := &fakeListUntaggedStream{}

	err := h.ListUntaggedManifests(&metadatav1.ListUntaggedManifestsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireCode(t, err, codes.Internal)
}

// ── ListOrphanedBlobs (streaming) ─────────────────────────────────────────────

// TestListOrphanedBlobs_success_sendsAll verifies that all orphaned blobs are
// forwarded to the stream.
func TestListOrphanedBlobs_success_sendsAll(t *testing.T) {
	blobs := []*metadatav1.BlobRef{
		{Digest: "sha256:orphan1", SizeBytes: 512},
		{Digest: "sha256:orphan2", SizeBytes: 1024},
	}
	h := newHandler(&fakeRepo{listOrphanedResult: blobs})
	stream := &fakeListOrphanedStream{}

	err := h.ListOrphanedBlobs(&metadatav1.ListOrphanedBlobsRequest{}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d blobs, want 2", len(stream.sent))
	}
}

// TestListOrphanedBlobs_repoError_returnsInternal verifies a repo error stops
// the stream.
func TestListOrphanedBlobs_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listOrphanedErr: errors.New("db fail")})
	stream := &fakeListOrphanedStream{}

	err := h.ListOrphanedBlobs(&metadatav1.ListOrphanedBlobsRequest{}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListOrphanedBlobs_empty_sendsNothing verifies an empty result completes
// cleanly.
func TestListOrphanedBlobs_empty_sendsNothing(t *testing.T) {
	h := newHandler(&fakeRepo{listOrphanedResult: nil})
	stream := &fakeListOrphanedStream{}

	err := h.ListOrphanedBlobs(&metadatav1.ListOrphanedBlobsRequest{}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}
