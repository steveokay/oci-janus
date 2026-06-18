// Package handler contains the gRPC server implementation for registry-metadata.
package handler

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// metadataRepo is the subset of repository.Repository used by MetadataHandler.
// It is defined as an interface so unit tests can inject a hand-written fake
// without requiring a real PostgreSQL connection (CLAUDE.md §18).
type metadataRepo interface {
	// Repositories
	GetOrCreateOrganization(ctx context.Context, tenantID, orgName string) (string, error)
	CreateRepository(ctx context.Context, tenantID, orgID, name string, isPublic bool, storageQuota int64) (*metadatav1.Repository, error)
	GetRepository(ctx context.Context, tenantID, repoID string) (*metadatav1.Repository, error)
	GetRepositoryByName(ctx context.Context, tenantID, orgID, name string) (*metadatav1.Repository, error)
	GetRepositoryByFullName(ctx context.Context, tenantID, fullName string) (*metadatav1.Repository, error)
	ListRepositories(ctx context.Context, tenantID, orgID string) ([]*metadatav1.Repository, error)
	DeleteRepository(ctx context.Context, tenantID, repoID string) error
	UpdateRepositoryQuota(ctx context.Context, tenantID, repoID string, quota int64) (*metadatav1.Repository, error)
	// Tags
	PutTag(ctx context.Context, tenantID, repoID, name, manifestDigest string) (*metadatav1.Tag, error)
	GetTag(ctx context.Context, tenantID, repoID, name string) (*metadatav1.Tag, error)
	ListTags(ctx context.Context, tenantID, repoID string, pageSize int32, last string) ([]*metadatav1.Tag, error)
	DeleteTag(ctx context.Context, tenantID, repoID, name string) error
	// Manifests
	PutManifest(ctx context.Context, tenantID, repoID, digest, mediaType string, rawJSON []byte, sizeBytes int64) (*metadatav1.Manifest, error)
	GetManifest(ctx context.Context, tenantID, repoID, reference string) (*metadatav1.Manifest, error)
	DeleteManifest(ctx context.Context, tenantID, repoID, digest string) error
	ListUntaggedManifests(ctx context.Context, tenantID, repoID string) ([]*metadatav1.Manifest, error)
	// Blobs
	LinkBlob(ctx context.Context, repoID, digest, storageKey string, sizeBytes int64) error
	UnlinkBlob(ctx context.Context, repoID, digest string) error
	ListOrphanedBlobs(ctx context.Context) ([]*metadatav1.BlobRef, error)
	// Quota
	GetTenantQuotaUsage(ctx context.Context, tenantID string) (*metadatav1.QuotaUsage, error)
	UpdateTenantQuota(ctx context.Context, tenantID string, quotaBytes int64) (*metadatav1.QuotaUsage, error)
	IncrementTenantStorage(ctx context.Context, tenantID string, bytes int64) error
	DecrementTenantStorage(ctx context.Context, tenantID string, bytes int64) error
	// Scan results
	UpsertScanResult(ctx context.Context, scanID, tenantID, status string, findingsJSON []byte, severityCounts map[string]int32) error
	GetScanResult(ctx context.Context, tenantID, manifestDigest string) (*metadatav1.ScanResult, error)
	GetTenantVulnerabilityCount(ctx context.Context, tenantID string) (total, critical, high int64, err error)
	// Repository count
	CountRepositories(ctx context.Context, tenantID string) (int64, error)
}

// MetadataHandler implements metadatav1.MetadataServiceServer.
type MetadataHandler struct {
	metadatav1.UnimplementedMetadataServiceServer
	repo metadataRepo
}

// New returns a MetadataHandler backed by repo.
func New(repo *repository.Repository) *MetadataHandler {
	return &MetadataHandler{repo: repo}
}

// mapErr converts repository sentinel errors to gRPC status errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		return status.Error(codes.NotFound, "not found")
	}
	if errors.Is(err, repository.ErrAlreadyExists) {
		return status.Error(codes.AlreadyExists, "already exists")
	}
	return errcodes.MapDBError(err, "internal error")
}

// ── Repositories ─────────────────────────────────────────────────────────────

func (h *MetadataHandler) CreateRepository(ctx context.Context, req *metadatav1.CreateRepositoryRequest) (*metadatav1.Repository, error) {
	orgID := req.OrgId
	name := req.Name

	// When OrgId is absent, derive org from "org/repo" name format.
	if orgID == "" {
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			return nil, status.Error(codes.InvalidArgument, "name must be org/repo when org_id is not set")
		}
		var err error
		orgID, err = h.repo.GetOrCreateOrganization(ctx, req.TenantId, parts[0])
		if err != nil {
			return nil, mapErr(err)
		}
		name = parts[1]
	}

	repo, err := h.repo.CreateRepository(ctx, req.TenantId, orgID, name, req.IsPublic, req.StorageQuota)
	if errors.Is(err, repository.ErrAlreadyExists) {
		return h.repo.GetRepositoryByName(ctx, req.TenantId, orgID, name)
	}
	return repo, mapErr(err)
}

func (h *MetadataHandler) GetRepository(ctx context.Context, req *metadatav1.GetRepositoryRequest) (*metadatav1.Repository, error) {
	repo, err := h.repo.GetRepository(ctx, req.TenantId, req.RepoId)
	return repo, mapErr(err)
}

// GetRepositoryByName resolves a repository by its full "org/repo" name within a tenant.
// This replaces the O(n) stream-scan pattern in registry-management with a single indexed SQL lookup.
func (h *MetadataHandler) GetRepositoryByName(ctx context.Context, req *metadatav1.GetRepositoryByNameRequest) (*metadatav1.Repository, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	repo, err := h.repo.GetRepositoryByFullName(ctx, req.TenantId, req.Name)
	return repo, mapErr(err)
}

func (h *MetadataHandler) ListRepositories(req *metadatav1.ListRepositoriesRequest, stream metadatav1.MetadataService_ListRepositoriesServer) error {
	repos, err := h.repo.ListRepositories(stream.Context(), req.TenantId, req.OrgId)
	if err != nil {
		return mapErr(err)
	}
	for _, r := range repos {
		if err := stream.Send(r); err != nil {
			return err
		}
	}
	return nil
}

func (h *MetadataHandler) DeleteRepository(ctx context.Context, req *metadatav1.DeleteRepositoryRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.DeleteRepository(ctx, req.TenantId, req.RepoId))
}

func (h *MetadataHandler) UpdateRepositoryQuota(ctx context.Context, req *metadatav1.UpdateRepositoryQuotaRequest) (*metadatav1.Repository, error) {
	repo, err := h.repo.UpdateRepositoryQuota(ctx, req.TenantId, req.RepoId, req.StorageQuota)
	return repo, mapErr(err)
}

// ── Tags ─────────────────────────────────────────────────────────────────────

func (h *MetadataHandler) PutTag(ctx context.Context, req *metadatav1.PutTagRequest) (*metadatav1.Tag, error) {
	tag, err := h.repo.PutTag(ctx, req.TenantId, req.RepoId, req.Name, req.ManifestDigest)
	return tag, mapErr(err)
}

func (h *MetadataHandler) GetTag(ctx context.Context, req *metadatav1.GetTagRequest) (*metadatav1.Tag, error) {
	tag, err := h.repo.GetTag(ctx, req.TenantId, req.RepoId, req.Name)
	return tag, mapErr(err)
}

func (h *MetadataHandler) ListTags(req *metadatav1.ListTagsRequest, stream metadatav1.MetadataService_ListTagsServer) error {
	tags, err := h.repo.ListTags(stream.Context(), req.TenantId, req.RepoId, req.PageSize, req.Last)
	if err != nil {
		return mapErr(err)
	}
	for _, t := range tags {
		if err := stream.Send(t); err != nil {
			return err
		}
	}
	return nil
}

func (h *MetadataHandler) DeleteTag(ctx context.Context, req *metadatav1.DeleteTagRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.DeleteTag(ctx, req.TenantId, req.RepoId, req.Name))
}

// ── Manifests ────────────────────────────────────────────────────────────────

func (h *MetadataHandler) PutManifest(ctx context.Context, req *metadatav1.PutManifestRequest) (*metadatav1.Manifest, error) {
	m, err := h.repo.PutManifest(ctx, req.TenantId, req.RepoId, req.Digest, req.MediaType, req.RawJson, req.SizeBytes)
	return m, mapErr(err)
}

func (h *MetadataHandler) GetManifest(ctx context.Context, req *metadatav1.GetManifestRequest) (*metadatav1.Manifest, error) {
	m, err := h.repo.GetManifest(ctx, req.TenantId, req.RepoId, req.Reference)
	return m, mapErr(err)
}

func (h *MetadataHandler) DeleteManifest(ctx context.Context, req *metadatav1.DeleteManifestRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.DeleteManifest(ctx, req.TenantId, req.RepoId, req.Digest))
}

func (h *MetadataHandler) ListUntaggedManifests(req *metadatav1.ListUntaggedManifestsRequest, stream metadatav1.MetadataService_ListUntaggedManifestsServer) error {
	manifests, err := h.repo.ListUntaggedManifests(stream.Context(), req.TenantId, req.RepoId)
	if err != nil {
		return mapErr(err)
	}
	for _, m := range manifests {
		if err := stream.Send(m); err != nil {
			return err
		}
	}
	return nil
}

// ── Blobs ────────────────────────────────────────────────────────────────────

func (h *MetadataHandler) LinkBlob(ctx context.Context, req *metadatav1.LinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.LinkBlob(ctx, req.RepoId, req.BlobDigest, req.StorageKey, req.SizeBytes))
}

func (h *MetadataHandler) UnlinkBlob(ctx context.Context, req *metadatav1.UnlinkBlobRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.UnlinkBlob(ctx, req.RepoId, req.BlobDigest))
}

func (h *MetadataHandler) ListOrphanedBlobs(req *metadatav1.ListOrphanedBlobsRequest, stream metadatav1.MetadataService_ListOrphanedBlobsServer) error {
	blobs, err := h.repo.ListOrphanedBlobs(stream.Context())
	if err != nil {
		return mapErr(err)
	}
	for _, b := range blobs {
		if err := stream.Send(b); err != nil {
			return err
		}
	}
	return nil
}

// ── Quota ────────────────────────────────────────────────────────────────────

func (h *MetadataHandler) GetTenantQuotaUsage(ctx context.Context, req *metadatav1.GetTenantQuotaUsageRequest) (*metadatav1.QuotaUsage, error) {
	usage, err := h.repo.GetTenantQuotaUsage(ctx, req.TenantId)
	return usage, mapErr(err)
}

// UpdateTenantQuota updates the tenant-level storage cap and returns the fresh
// usage view. The management service is responsible for restricting this RPC to
// platform admins; this handler does not gate by role.
func (h *MetadataHandler) UpdateTenantQuota(ctx context.Context, req *metadatav1.UpdateTenantQuotaRequest) (*metadatav1.QuotaUsage, error) {
	if req.GetQuotaBytes() < 0 {
		return nil, status.Error(codes.InvalidArgument, "quota_bytes must be non-negative")
	}
	usage, err := h.repo.UpdateTenantQuota(ctx, req.GetTenantId(), req.GetQuotaBytes())
	return usage, mapErr(err)
}

func (h *MetadataHandler) IncrementTenantStorage(ctx context.Context, req *metadatav1.IncrementTenantStorageRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.IncrementTenantStorage(ctx, req.TenantId, req.Bytes))
}

func (h *MetadataHandler) DecrementTenantStorage(ctx context.Context, req *metadatav1.DecrementTenantStorageRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, mapErr(h.repo.DecrementTenantStorage(ctx, req.TenantId, req.Bytes))
}

// ── Scan results ─────────────────────────────────────────────────────────────

func (h *MetadataHandler) UpdateScanStatus(ctx context.Context, req *metadatav1.UpdateScanStatusRequest) (*emptypb.Empty, error) {
	err := h.repo.UpsertScanResult(ctx, req.ScanId, req.TenantId, req.Status, req.FindingsJson, req.SeverityCounts)
	return &emptypb.Empty{}, mapErr(err)
}

func (h *MetadataHandler) GetScanResult(ctx context.Context, req *metadatav1.GetScanResultRequest) (*metadatav1.ScanResult, error) {
	sr, err := h.repo.GetScanResult(ctx, req.TenantId, req.ManifestDigest)
	return sr, mapErr(err)
}

// GetTenantVulnerabilityCount returns the aggregated CRITICAL+HIGH vulnerability
// counts across all completed scans for the tenant.
func (h *MetadataHandler) GetTenantVulnerabilityCount(ctx context.Context, req *metadatav1.GetTenantVulnerabilityCountRequest) (*metadatav1.VulnerabilityCountResponse, error) {
	total, critical, high, err := h.repo.GetTenantVulnerabilityCount(ctx, req.TenantId)
	if err != nil {
		return nil, mapErr(err)
	}
	return &metadatav1.VulnerabilityCountResponse{
		Total:         total,
		CriticalCount: critical,
		HighCount:     high,
	}, nil
}

// CountRepositories returns the number of repositories owned by the tenant.
func (h *MetadataHandler) CountRepositories(ctx context.Context, req *metadatav1.CountRepositoriesRequest) (*metadatav1.CountRepositoriesResponse, error) {
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	n, err := h.repo.CountRepositories(ctx, req.TenantId)
	if err != nil {
		return nil, mapErr(err)
	}
	return &metadatav1.CountRepositoriesResponse{Count: n}, nil
}
