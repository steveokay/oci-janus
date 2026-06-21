// Package handler contains the gRPC server implementation for registry-metadata.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	CreateRepository(ctx context.Context, tenantID, orgID, name, description string, isPublic bool, storageQuota int64) (*metadatav1.Repository, error)
	GetRepository(ctx context.Context, tenantID, repoID string) (*metadatav1.Repository, error)
	GetRepositoryByName(ctx context.Context, tenantID, orgID, name string) (*metadatav1.Repository, error)
	GetRepositoryByFullName(ctx context.Context, tenantID, fullName string) (*metadatav1.Repository, error)
	ListRepositories(ctx context.Context, tenantID, orgID string) ([]*metadatav1.Repository, error)
	DeleteRepository(ctx context.Context, tenantID, repoID string) error
	UpdateRepositoryQuota(ctx context.Context, tenantID, repoID string, quota int64) (*metadatav1.Repository, error)
	UpdateRepository(ctx context.Context, tenantID, repoID, description string) (*metadatav1.Repository, error)
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
	// Storage breakdown (FE-API-031) — top-50 repos + tenant total.
	GetTenantStorageBreakdown(ctx context.Context, tenantID string) (*metadatav1.GetTenantStorageBreakdownResponse, error)
	// Tenant usage aggregate (FE-API-028) — storage + repo + org counts.
	GetTenantUsage(ctx context.Context, tenantID string) (*metadatav1.TenantUsage, error)
	// Scan results
	UpsertScanResult(ctx context.Context, scanID, tenantID, status string, findingsJSON []byte, severityCounts map[string]int32, repoID, manifestDigest, scannerName, scannerVersion string) error
	GetScanResult(ctx context.Context, tenantID, manifestDigest string) (*metadatav1.ScanResult, error)
	// Per-tag SBOM (FE-API-033) — keyed on the latest scan_results row for the
	// (tenant_id, manifest_digest) pair.
	UpsertScanSBOM(ctx context.Context, tenantID, manifestDigest, format string, sbomJSON []byte) error
	GetScanSBOM(ctx context.Context, tenantID, manifestDigest string) (*repository.SBOMResult, error)
	GetTenantVulnerabilityCount(ctx context.Context, tenantID string) (total, critical, high, medium, low, negligible int64, err error)
	// Security overview (FE-API-020) — single tenant-scoped aggregate.
	GetSecurityOverview(ctx context.Context, tenantID string) (*repository.SecurityOverview, error)
	// Vulnerability list (FE-API-014) — paginated CVE rollup across the
	// latest scan per (repo, tag).
	ListTenantVulnerabilities(ctx context.Context, tenantID, severityFilter, pageToken string, limit int) ([]repository.VulnerabilityRow, string, error)
	// Scan history (FE-API-015) — paginated flat feed ordered by completed_at DESC.
	ListScanHistory(ctx context.Context, tenantID string, since time.Time, pageToken string, limit int) ([]repository.ScanHistoryRow, string, error)
	// Remediation suggestions (FE-API-017) — paginated upgrade groupings
	// derived from the latest complete scan per (tenant, repo, tag).
	ListTenantRemediations(ctx context.Context, tenantID, pageToken string, limit int) ([]repository.RemediationRow, string, error)
	// Repository count
	CountRepositories(ctx context.Context, tenantID string) (int64, error)
	// FE-API-037: per-repo retention policy CRUD. The handler enforces input
	// validation; the repository owns the preview_until reset semantics.
	GetRepoRetentionPolicy(ctx context.Context, tenantID, repoID string) (*metadatav1.RetentionPolicy, error)
	UpsertRepoRetentionPolicy(
		ctx context.Context,
		tenantID, repoID string,
		enabled bool,
		rules []*metadatav1.RetentionRule,
		protectedPatterns []string,
		updatedBy string,
	) (*metadatav1.RetentionPolicy, error)
	DeleteRepoRetentionPolicy(ctx context.Context, tenantID, repoID string) error
	// FE-API-038: read-only evaluator. Materialises the would-delete /
	// protected-skipped sets for a candidate policy without persisting it.
	// Used by both the dry-run endpoint and the preview-window state endpoint
	// (which loads the saved policy and feeds it back through this same RPC
	// so the metadata API surface stays small).
	EvaluateRetention(
		ctx context.Context,
		tenantID, repoID string,
		candidate *metadatav1.RetentionPolicyCandidate,
		maxDeleteResults, maxProtectedResults int,
	) (*repository.EvaluationResult, error)
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

	repo, err := h.repo.CreateRepository(ctx, req.TenantId, orgID, name, req.GetDescription(), req.IsPublic, req.StorageQuota)
	if errors.Is(err, repository.ErrAlreadyExists) {
		return h.repo.GetRepositoryByName(ctx, req.TenantId, orgID, name)
	}
	return repo, mapErr(err)
}

func (h *MetadataHandler) UpdateRepository(ctx context.Context, req *metadatav1.UpdateRepositoryRequest) (*metadatav1.Repository, error) {
	if req.RepoId == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	if req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	repo, err := h.repo.UpdateRepository(ctx, req.TenantId, req.RepoId, req.Description)
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
		// Temporary diagnostic logging — same reason as UpdateTenantQuota.
		slog.ErrorContext(stream.Context(), "ListRepositories repo error",
			"tenant_id", req.TenantId, "org_id", req.OrgId, "err", err)
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

// maxManifestJSONBytes is the maximum size accepted for a manifest's raw JSON.
// OCI manifests are tiny; 4 MiB is already generous and guards against
// allocating unbounded memory in parseImageSize.
const maxManifestJSONBytes = 4 << 20 // 4 MiB

func (h *MetadataHandler) PutManifest(ctx context.Context, req *metadatav1.PutManifestRequest) (*metadatav1.Manifest, error) {
	if len(req.RawJson) > maxManifestJSONBytes {
		return nil, status.Errorf(codes.InvalidArgument, "manifest JSON exceeds maximum size of %d bytes", maxManifestJSONBytes)
	}
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
	if err != nil {
		// Log the underlying error so we can diagnose without rebuilding —
		// mapErr swallows it as a generic codes.Internal. Temporary
		// instrumentation; leave in until we land structured DB-error
		// classification in libs/errors/codes.
		slog.ErrorContext(ctx, "UpdateTenantQuota repo error",
			"tenant_id", req.GetTenantId(),
			"quota_bytes", req.GetQuotaBytes(),
			"err", err)
	}
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
	err := h.repo.UpsertScanResult(
		ctx,
		req.ScanId, req.TenantId, req.Status,
		req.FindingsJson, req.SeverityCounts,
		req.RepoId, req.ManifestDigest,
		req.ScannerName, req.ScannerVersion,
	)
	return &emptypb.Empty{}, mapErr(err)
}

func (h *MetadataHandler) GetScanResult(ctx context.Context, req *metadatav1.GetScanResultRequest) (*metadatav1.ScanResult, error) {
	sr, err := h.repo.GetScanResult(ctx, req.TenantId, req.ManifestDigest)
	return sr, mapErr(err)
}

// ── SBOM (FE-API-033) ────────────────────────────────────────────────────────

// validSBOMFormats lists the SBOM wire formats accepted by UpsertScanSBOM. Held
// as a small map (not a slice + linear scan) so future formats are added with
// a single line. "spdx-json" is the only one the scanner emits today;
// "cyclonedx-json" is reserved so the schema doesn't need to change when it
// lands. Anything else is rejected as InvalidArgument so a typo from a
// future caller surfaces immediately rather than silently writing garbage.
var validSBOMFormats = map[string]bool{
	"spdx-json":      true,
	"cyclonedx-json": true,
}

// UpsertScanSBOM persists the SBOM blob produced for the latest scan of a
// manifest. Returns InvalidArgument on missing/unknown fields, NotFound when
// no scan_results row exists for the (tenant, manifest_digest) pair.
func (h *MetadataHandler) UpsertScanSBOM(ctx context.Context, req *metadatav1.UpsertScanSBOMRequest) (*emptypb.Empty, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetManifestDigest() == "" {
		return nil, status.Error(codes.InvalidArgument, "manifest_digest is required")
	}
	if !validSBOMFormats[req.GetFormat()] {
		// Don't echo the format value back — the response body is a candidate
		// for log injection if the caller is hostile and the message is rendered
		// without escaping. A generic message is sufficient.
		return nil, status.Error(codes.InvalidArgument, "unsupported sbom format")
	}
	if len(req.GetSbomJson()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "sbom_json is required")
	}
	err := h.repo.UpsertScanSBOM(ctx, req.GetTenantId(), req.GetManifestDigest(), req.GetFormat(), req.GetSbomJson())
	return &emptypb.Empty{}, mapErr(err)
}

// GetScanSBOM returns the stored SBOM bytes + format for the latest scan of a
// manifest. NotFound covers both "never scanned" and "scanned but no SBOM
// recorded" — the management BFF maps either case to the same 404 response.
func (h *MetadataHandler) GetScanSBOM(ctx context.Context, req *metadatav1.GetScanSBOMRequest) (*metadatav1.GetScanSBOMResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetManifestDigest() == "" {
		return nil, status.Error(codes.InvalidArgument, "manifest_digest is required")
	}
	res, err := h.repo.GetScanSBOM(ctx, req.GetTenantId(), req.GetManifestDigest())
	if err != nil {
		return nil, mapErr(err)
	}
	return &metadatav1.GetScanSBOMResponse{
		Format:   res.Format,
		SbomJson: res.SBOMJSON,
	}, nil
}

// GetTenantVulnerabilityCount returns the aggregated CRITICAL+HIGH vulnerability
// counts across all completed scans for the tenant.
func (h *MetadataHandler) GetTenantVulnerabilityCount(ctx context.Context, req *metadatav1.GetTenantVulnerabilityCountRequest) (*metadatav1.VulnerabilityCountResponse, error) {
	total, critical, high, medium, low, negligible, err := h.repo.GetTenantVulnerabilityCount(ctx, req.TenantId)
	if err != nil {
		return nil, mapErr(err)
	}
	return &metadatav1.VulnerabilityCountResponse{
		Total:            total,
		CriticalCount:    critical,
		HighCount:        high,
		MediumCount:      medium,
		LowCount:         low,
		NegligibleCount:  negligible,
	}, nil
}

// GetSecurityOverview returns the tenant-scoped FE-API-020 payload. Maps the
// repository's typed SecurityOverview to the proto message; the tenant_id
// check is performed in the repository SQL (every CTE branch filters on $1).
func (h *MetadataHandler) GetSecurityOverview(ctx context.Context, req *metadatav1.GetSecurityOverviewRequest) (*metadatav1.SecurityOverview, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	ov, err := h.repo.GetSecurityOverview(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapErr(err)
	}
	var pct float64
	if ov.TagsTotal > 0 {
		pct = float64(ov.TagsScanned) / float64(ov.TagsTotal) * 100.0
	}
	return &metadatav1.SecurityOverview{
		OpenVulnerabilitiesTotal: ov.OpenVulnerabilitiesTotal,
		SeverityCounts: &metadatav1.SecurityCounts{
			Critical:   ov.Critical,
			High:       ov.High,
			Medium:     ov.Medium,
			Low:        ov.Low,
			Negligible: ov.Negligible,
		},
		ScanCoverage: &metadatav1.ScanCoverage{
			TagsTotal:   ov.TagsTotal,
			TagsScanned: ov.TagsScanned,
			Percent:     pct,
		},
		RecentScans_24H:    ov.RecentScans24h,
		DaysSinceLastScan:  ov.DaysSinceLastScan,
	}, nil
}

// validSeverityFilter accepts only the canonical upper-case severity names.
// Empty string is allowed and signals "no filter". Kept in this package so
// the handler can return InvalidArgument before the gRPC call reaches the
// repository layer.
func validSeverityFilter(s string) bool {
	switch s {
	case "", "CRITICAL", "HIGH", "MEDIUM", "LOW", "NEGLIGIBLE":
		return true
	}
	return false
}

// ListTenantVulnerabilities (FE-API-014) returns the workspace-wide
// vulnerability list. Tenant isolation is enforced in the repository SQL;
// the handler validates inputs and maps repository rows to proto messages.
func (h *MetadataHandler) ListTenantVulnerabilities(ctx context.Context, req *metadatav1.ListTenantVulnerabilitiesRequest) (*metadatav1.ListTenantVulnerabilitiesResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	sev := strings.ToUpper(req.GetSeverity())
	if !validSeverityFilter(sev) {
		return nil, status.Error(codes.InvalidArgument, "invalid severity filter")
	}
	rows, next, err := h.repo.ListTenantVulnerabilities(ctx, req.GetTenantId(), sev, req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		// Bad cursor / bad severity surfaces as InvalidArgument so the BFF
		// can return a 400. Anything else is internal.
		if strings.Contains(err.Error(), "page_token") || strings.Contains(err.Error(), "decode") {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		return nil, mapErr(err)
	}
	out := &metadatav1.ListTenantVulnerabilitiesResponse{
		Vulnerabilities: make([]*metadatav1.TenantVulnerability, 0, len(rows)),
		NextPageToken:   next,
	}
	for _, v := range rows {
		// Build the affected-tag slice once per CVE — n is small (the
		// number of tags affected by this specific CVE).
		affected := make([]*metadatav1.AffectedTag, 0, len(v.Affected))
		for _, a := range v.Affected {
			affected = append(affected, &metadatav1.AffectedTag{
				Repo:   a.Repo,
				Tag:    a.Tag,
				Digest: a.Digest,
			})
		}
		var first, last *timestamppb.Timestamp
		if !v.FirstSeen.IsZero() {
			first = timestamppb.New(v.FirstSeen)
		}
		if !v.LastSeen.IsZero() {
			last = timestamppb.New(v.LastSeen)
		}
		out.Vulnerabilities = append(out.Vulnerabilities, &metadatav1.TenantVulnerability{
			CveId:          v.CVE,
			Severity:       v.Severity,
			Title:          v.Title,
			Description:    v.Description,
			FixedIn:        v.FixedIn,
			PackageName:    v.PackageName,
			PackageVersion: v.PackageVersion,
			Affected:       affected,
			FirstSeen:      first,
			LastSeen:       last,
		})
	}
	return out, nil
}

// ListScanHistory (FE-API-015) returns the flat scan-history feed for the
// tenant ordered by completed_at DESC. `since` defaults to 30 days ago when
// unset, matching the dashboard's default time window.
func (h *MetadataHandler) ListScanHistory(ctx context.Context, req *metadatav1.ListScanHistoryRequest) (*metadatav1.ListScanHistoryResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	since := time.Now().Add(-30 * 24 * time.Hour)
	if ts := req.GetSince(); ts != nil && ts.IsValid() {
		since = ts.AsTime()
	}
	rows, next, err := h.repo.ListScanHistory(ctx, req.GetTenantId(), since, req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		if strings.Contains(err.Error(), "page_token") || strings.Contains(err.Error(), "decode") {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		return nil, mapErr(err)
	}
	out := &metadatav1.ListScanHistoryResponse{
		Scans:         make([]*metadatav1.ScanHistoryEntry, 0, len(rows)),
		NextPageToken: next,
	}
	for _, r := range rows {
		var started, completed *timestamppb.Timestamp
		if !r.StartedAt.IsZero() {
			started = timestamppb.New(r.StartedAt)
		}
		if !r.CompletedAt.IsZero() {
			completed = timestamppb.New(r.CompletedAt)
		}
		// Translate the repository's "complete" status to the proto string
		// used by the dashboard: "completed". Other statuses map 1:1.
		st := r.Status
		if st == "complete" {
			st = "completed"
		}
		out.Scans = append(out.Scans, &metadatav1.ScanHistoryEntry{
			ScanId:         r.ScanID,
			Repo:           r.Repo,
			Tag:            r.Tag,
			ManifestDigest: r.ManifestDigest,
			Scanner:        r.Scanner,
			StartedAt:      started,
			CompletedAt:    completed,
			Status:         st,
			SeverityCounts: &metadatav1.SecurityCounts{
				Critical:   r.Critical,
				High:       r.High,
				Medium:     r.Medium,
				Low:        r.Low,
				Negligible: r.Negligible,
			},
			Trigger: r.Trigger,
		})
	}
	return out, nil
}

// ListTenantRemediations (FE-API-017) returns actionable upgrade groupings
// for the tenant — "upgrade package X from A to B fixes N CVEs across M
// (repo, tag) tuples." Tenant isolation is enforced in the repository SQL;
// the handler validates inputs and maps repository rows to proto messages.
func (h *MetadataHandler) ListTenantRemediations(ctx context.Context, req *metadatav1.ListTenantRemediationsRequest) (*metadatav1.ListTenantRemediationsResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	rows, next, err := h.repo.ListTenantRemediations(ctx, req.GetTenantId(), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		// Malformed cursor surfaces as InvalidArgument so the BFF can return
		// a 400 rather than a 500. Anything else is internal.
		if strings.Contains(err.Error(), "page_token") || strings.Contains(err.Error(), "decode") {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		return nil, mapErr(err)
	}
	out := &metadatav1.ListTenantRemediationsResponse{
		Remediations:  make([]*metadatav1.Remediation, 0, len(rows)),
		NextPageToken: next,
	}
	for _, r := range rows {
		// Allocate (not append-nil) so the wire shape is always a JSON array
		// even when the group has zero entries — the dashboard relies on
		// stable shape for serde.
		affected := make([]*metadatav1.RemediationAffected, 0, len(r.Affected))
		for _, a := range r.Affected {
			affected = append(affected, &metadatav1.RemediationAffected{
				Repo:   a.Repo,
				Tag:    a.Tag,
				Digest: a.Digest,
			})
		}
		out.Remediations = append(out.Remediations, &metadatav1.Remediation{
			PackageName:    r.PackageName,
			FromVersion:    r.FromVersion,
			ToVersion:      r.ToVersion,
			CvesFixed:      r.CVEsFixed,
			CvesFixedCount: r.CVEsFixedCount,
			MaxSeverity:    r.MaxSeverity,
			Affected:       affected,
			AffectedCount:  r.AffectedCount,
		})
	}
	return out, nil
}

// GetTenantStorageBreakdown returns the tenant's total storage usage plus the
// top-50 repositories sorted by storage_used DESC. Backs FE-API-031.
func (h *MetadataHandler) GetTenantStorageBreakdown(ctx context.Context, req *metadatav1.GetTenantStorageBreakdownRequest) (*metadatav1.GetTenantStorageBreakdownResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	resp, err := h.repo.GetTenantStorageBreakdown(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapErr(err)
	}
	return resp, nil
}

// GetTenantUsage returns the metadata-owned slice of FE-API-028's admin
// tenant-detail card: storage_used / storage_quota / repository_count /
// organization_count. A missing tenants row (lazy creation) returns zero
// values rather than NotFound so newly created tenants render cleanly.
func (h *MetadataHandler) GetTenantUsage(ctx context.Context, req *metadatav1.GetTenantUsageRequest) (*metadatav1.TenantUsage, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	usage, err := h.repo.GetTenantUsage(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapErr(err)
	}
	return usage, nil
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
