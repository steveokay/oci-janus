// Package handler implements the ScannerService gRPC server.
//
// Two surfaces live here:
//
//   - Scan lifecycle (TriggerScan / GetScanStatus) — backed by the in-memory
//     scan store + worker pool. These existed before FE-API-018.
//   - Scan policies + compliance reports — FE-API-018 + FE-API-019. These
//     require persistent state, so they go through the new `repository`
//     package against the scanner's own Postgres DB.
//
// The repository handle is optional on the handler struct. When nil (e.g.
// in unit tests that exercise only the scan lifecycle), the policy and
// report RPCs return codes.FailedPrecondition. Production wiring always
// supplies a non-nil repo.
package handler

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/scanner/plugin"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	internalPlugin "github.com/steveokay/oci-janus/services/scanner/internal/plugin"
	"github.com/steveokay/oci-janus/services/scanner/internal/policy"
	scannerregistry "github.com/steveokay/oci-janus/services/scanner/internal/registry"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"
)

// TestScanFixture is the deterministic input for RunTestScan. The fixture
// is a real (tenant, repo, tag) tuple that the dev compose stack seeds at
// boot; production deployments override it via SCANNER_TEST_* env vars
// so the admin UI's "run test scan" button targets a real canary image.
type TestScanFixture struct {
	TenantID       string
	RepositoryName string
	ManifestRef    string // tag name or digest accepted by metadata.GetTag/Manifest
}

// GRPCHandler implements scannerv1.ScannerServiceServer.
type GRPCHandler struct {
	scannerv1.UnimplementedScannerServiceServer
	pool      *worker.Pool
	scanStore *store.Store
	// repo is optional — when nil the policy + report RPCs short-circuit
	// with FailedPrecondition. Tests that only exercise the scan lifecycle
	// can leave it unset.
	repo *repository.Repository
	// adapterReg is optional — when nil the REM-011 Phase 2 adapter
	// management RPCs (ListInstalledAdapters et al.) return FailedPrecondition.
	// Unit tests that only exercise the scan lifecycle leave it unset.
	adapterReg *scannerregistry.Registry
	// testScanFixture is the (tenant, repo, tag) tuple RunTestScan exercises.
	// Zero-value fixture causes RunTestScan to return FailedPrecondition.
	testScanFixture TestScanFixture
	// metaClient is needed only by RunTestScan to (a) resolve the tag into
	// a manifest digest + repo_id, and (b) poll the resulting scan_results
	// row. Optional — RunTestScan returns FailedPrecondition when unset.
	metaClient metadatav1.MetadataServiceClient
}

// New creates a GRPCHandler.
func New(pool *worker.Pool, scanStore *store.Store) *GRPCHandler {
	return &GRPCHandler{pool: pool, scanStore: scanStore}
}

// WithRepository attaches the scanner DB repository so the policy + report
// RPCs become available. Returns the handler for fluent setup.
func (h *GRPCHandler) WithRepository(r *repository.Repository) *GRPCHandler {
	h.repo = r
	return h
}

// WithAdapterRegistry enables the REM-011 Phase 2 adapter-management RPCs
// (ListInstalledAdapters, GetActiveAdapter, SetActiveAdapter, RunTestScan,
// GetScannerHealth). Returns the handler for fluent setup.
func (h *GRPCHandler) WithAdapterRegistry(reg *scannerregistry.Registry) *GRPCHandler {
	h.adapterReg = reg
	return h
}

// WithTestScanFixture configures the deterministic input RunTestScan uses
// to exercise the active adapter. Empty fields cause RunTestScan to return
// FailedPrecondition so a misconfigured deployment surfaces clearly.
func (h *GRPCHandler) WithTestScanFixture(f TestScanFixture) *GRPCHandler {
	h.testScanFixture = f
	return h
}

// WithMetadataClient wires the metadata gRPC client so RunTestScan can
// (1) resolve its fixture tag → manifest digest and (2) poll for the
// scan_results row that confirms the scan completed end-to-end.
func (h *GRPCHandler) WithMetadataClient(c metadatav1.MetadataServiceClient) *GRPCHandler {
	h.metaClient = c
	return h
}

// TriggerScan manually queues a scan for a manifest that has already been pushed.
// This is used by CI/CD pipelines to force a re-scan or scan on demand.
func (h *GRPCHandler) TriggerScan(_ context.Context, req *scannerv1.TriggerScanRequest) (*scannerv1.TriggerScanResponse, error) {
	if req.TenantId == "" || req.ManifestDigest == "" || req.RepositoryName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, repository_name, and manifest_digest are required")
	}

	// repo_id is unknown at this point — the worker will look up the manifest by digest.
	// We pass repository_name as the repo identifier; the metadata service resolves it.
	scanID := h.pool.TriggerScanJob(req.TenantId, "", req.RepositoryName, req.ManifestDigest)
	return &scannerv1.TriggerScanResponse{ScanId: scanID}, nil
}

// GetScanStatus returns the current status of a scan job by scan_id.
func (h *GRPCHandler) GetScanStatus(_ context.Context, req *scannerv1.GetScanStatusRequest) (*scannerv1.GetScanStatusResponse, error) {
	if req.ScanId == "" {
		return nil, status.Error(codes.InvalidArgument, "scan_id is required")
	}

	rec, ok := h.scanStore.Get(req.ScanId)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "scan %s not found", req.ScanId)
	}

	counts := make(map[string]int32, len(rec.SeverityCounts))
	for k, v := range rec.SeverityCounts {
		counts[k] = int32(v)
	}

	resp := &scannerv1.GetScanStatusResponse{
		Status:         rec.Status,
		SeverityCounts: counts,
	}
	if rec.CompletedAt != nil {
		resp.CompletedAt = timestampProto(rec.CompletedAt)
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// FE-API-018 — scan policies
// ---------------------------------------------------------------------------

// defaultPolicy is the response the BFF gets when no row exists for the
// tenant. Matches the dashboard's "sane defaults" copy for fresh tenants.
func defaultPolicy(tenantID string) *scannerv1.ScanPolicy {
	return &scannerv1.ScanPolicy{
		TenantId:          tenantID,
		AutoScanOnPush:    true,
		BlockOnSeverity:   "",
		ExemptCves:        []string{},
		ScannerPlugin:     "trivy",
		ScannerVersionPin: "",
	}
}

// GetScanPolicy returns the per-tenant policy. Missing row → default policy
// (not NOT_FOUND) so the dashboard renders the "no policy yet" state
// without a separate error path.
func (h *GRPCHandler) GetScanPolicy(ctx context.Context, req *scannerv1.GetScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	rec, err := h.repo.GetScanPolicy(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return defaultPolicy(req.GetTenantId()), nil
		}
		return nil, status.Errorf(codes.Internal, "get scan policy: %v", err)
	}
	return policyToProto(rec), nil
}

// UpdateScanPolicy upserts the per-tenant policy. The BFF performs allowlist
// validation on block_on_severity / scanner_plugin / exempt_cves; the
// handler only validates the tenant_id UUID here.
func (h *GRPCHandler) UpdateScanPolicy(ctx context.Context, req *scannerv1.UpdateScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	var updatedBy uuid.UUID
	if s := req.GetUpdatedBy(); s != "" {
		updatedBy, err = uuid.Parse(s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "updated_by must be a UUID")
		}
	}

	exempts := req.GetExemptCves()
	if exempts == nil {
		// Non-nil empty slice keeps Postgres array semantics clean (text[]
		// does not accept NULL via pgx without an explicit cast).
		exempts = []string{}
	}

	out, err := h.repo.UpsertScanPolicy(ctx, &repository.ScanPolicy{
		TenantID:          tenantID,
		AutoScanOnPush:    req.GetAutoScanOnPush(),
		BlockOnSeverity:   req.GetBlockOnSeverity(),
		ExemptCVEs:        exempts,
		ScannerPlugin:     req.GetScannerPlugin(),
		ScannerVersionPin: req.GetScannerVersionPin(),
		UpdatedBy:         updatedBy,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update scan policy: %v", err)
	}
	return policyToProto(out), nil
}

// policyToProto converts a repository row to its proto form.
//
// FE-API-049 extension: surfaces OrgID + RepoID + Enabled when set so the
// shared editor on the dashboard can render the same proto for any of
// the three scopes (tenant / org / repo). Zero-value UUIDs are omitted —
// the proto fields stay empty strings, which is the wire-side signal
// that no scope identifier is set.
func policyToProto(p *repository.ScanPolicy) *scannerv1.ScanPolicy {
	out := &scannerv1.ScanPolicy{
		TenantId:          p.TenantID.String(),
		AutoScanOnPush:    p.AutoScanOnPush,
		BlockOnSeverity:   p.BlockOnSeverity,
		ExemptCves:        p.ExemptCVEs,
		ScannerPlugin:     p.ScannerPlugin,
		ScannerVersionPin: p.ScannerVersionPin,
		Enabled:           p.Enabled,
		UpdatedAt:         timestamppb.New(p.UpdatedAt),
	}
	if p.OrgID != uuid.Nil {
		out.OrgId = p.OrgID.String()
	}
	if p.RepoID != uuid.Nil {
		out.RepoId = p.RepoID.String()
	}
	if p.UpdatedBy != uuid.Nil {
		out.UpdatedBy = p.UpdatedBy.String()
	}
	return out
}

// ---------------------------------------------------------------------------
// FE-API-049 — org + repo scan policies + inheritance
// ---------------------------------------------------------------------------

// GetOrgScanPolicy returns the org default. NotFound is propagated to the
// caller — unlike the per-tenant GetScanPolicy which synthesises a
// default, the org route lets the BFF render an explicit "no org default
// yet" empty state so the dashboard can distinguish inherited vs absent.
func (h *GRPCHandler) GetOrgScanPolicy(ctx context.Context, req *scannerv1.GetOrgScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "org_id must be a UUID")
	}
	rec, err := h.repo.GetOrgScanPolicy(ctx, tenantID, orgID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "org scan policy not found")
		}
		return nil, status.Errorf(codes.Internal, "get org scan policy: %v", err)
	}
	return policyToProto(rec), nil
}

// UpsertOrgScanPolicy creates or updates the org-default row.
func (h *GRPCHandler) UpsertOrgScanPolicy(ctx context.Context, req *scannerv1.UpsertOrgScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "org_id must be a UUID")
	}
	var updatedBy uuid.UUID
	if s := req.GetUpdatedBy(); s != "" {
		updatedBy, err = uuid.Parse(s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "updated_by must be a UUID")
		}
	}
	exempts := req.GetExemptCves()
	if exempts == nil {
		exempts = []string{}
	}
	out, err := h.repo.UpsertOrgScanPolicy(ctx, &repository.ScanPolicy{
		TenantID:          tenantID,
		OrgID:             orgID,
		AutoScanOnPush:    req.GetAutoScanOnPush(),
		BlockOnSeverity:   req.GetBlockOnSeverity(),
		ExemptCVEs:        exempts,
		ScannerPlugin:     req.GetScannerPlugin(),
		ScannerVersionPin: req.GetScannerVersionPin(),
		Enabled:           req.GetEnabled(),
		UpdatedBy:         updatedBy,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert org scan policy: %v", err)
	}
	return policyToProto(out), nil
}

// DeleteOrgScanPolicy removes the org-default row. NotFound when nothing
// existed — caller (the BFF) maps that to 404 so the operator gets a
// clear "nothing to remove" rather than a misleading 200.
func (h *GRPCHandler) DeleteOrgScanPolicy(ctx context.Context, req *scannerv1.DeleteOrgScanPolicyRequest) (*emptypb.Empty, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "org_id must be a UUID")
	}
	if err := h.repo.DeleteOrgScanPolicy(ctx, tenantID, orgID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "org scan policy not found")
		}
		return nil, status.Errorf(codes.Internal, "delete org scan policy: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// GetRepoScanPolicy returns a per-repo override row, NotFound when none.
// Mirrors the org route — explicit absence so the dashboard can render
// "inheriting from org" vs "no override yet".
func (h *GRPCHandler) GetRepoScanPolicy(ctx context.Context, req *scannerv1.GetRepoScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "repo_id must be a UUID")
	}
	rec, err := h.repo.GetRepoScanPolicy(ctx, tenantID, repoID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "repo scan policy not found")
		}
		return nil, status.Errorf(codes.Internal, "get repo scan policy: %v", err)
	}
	return policyToProto(rec), nil
}

// UpsertRepoScanPolicy creates or updates a per-repo override. The BFF
// resolves repo→org via the metadata service before calling this RPC so
// the org_id is always non-empty on the wire — the scanner doesn't have
// a local repo→org table.
func (h *GRPCHandler) UpsertRepoScanPolicy(ctx context.Context, req *scannerv1.UpsertRepoScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "repo_id must be a UUID")
	}
	// org_id is part of the per-repo row so the inheritance helper can
	// find the parent org cheaply later. The BFF is responsible for
	// resolving it before calling here.
	orgID, err := parseOrgIDOptional(req.GetTenantId(), repoID)
	if err != nil {
		// parseOrgIDOptional always returns nil currently; reserved for
		// a future enrichment hook if we move repo→org resolution into
		// the scanner.
		return nil, err
	}
	var updatedBy uuid.UUID
	if s := req.GetUpdatedBy(); s != "" {
		updatedBy, err = uuid.Parse(s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "updated_by must be a UUID")
		}
	}
	exempts := req.GetExemptCves()
	if exempts == nil {
		exempts = []string{}
	}
	out, err := h.repo.UpsertRepoScanPolicy(ctx, &repository.ScanPolicy{
		TenantID:          tenantID,
		RepoID:            repoID,
		OrgID:             orgID,
		AutoScanOnPush:    req.GetAutoScanOnPush(),
		BlockOnSeverity:   req.GetBlockOnSeverity(),
		ExemptCVEs:        exempts,
		ScannerPlugin:     req.GetScannerPlugin(),
		ScannerVersionPin: req.GetScannerVersionPin(),
		Enabled:           req.GetEnabled(),
		UpdatedBy:         updatedBy,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert repo scan policy: %v", err)
	}
	return policyToProto(out), nil
}

// parseOrgIDOptional is a placeholder for repo→org lookup if/when the
// scanner gains its own repo→org index. Today the org_id is supplied by
// the BFF on every upsert (resolved via metadata.LookupOrgIDByName);
// this function exists so the handler signature is stable when the
// dependency direction changes. Returns the zero UUID + nil today; the
// row's org_id column will be overwritten with the BFF-supplied value.
func parseOrgIDOptional(_ string, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}

// DeleteRepoScanPolicy removes a per-repo override.
func (h *GRPCHandler) DeleteRepoScanPolicy(ctx context.Context, req *scannerv1.DeleteRepoScanPolicyRequest) (*emptypb.Empty, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	repoID, err := uuid.Parse(req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "repo_id must be a UUID")
	}
	if err := h.repo.DeleteRepoScanPolicy(ctx, tenantID, repoID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "repo scan policy not found")
		}
		return nil, status.Errorf(codes.Internal, "delete repo scan policy: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// GetEffectiveScanPolicy walks the per-repo → org → tenant → default
// inheritance chain via the shared policy.Resolve helper so the worker
// (HandlePushCompleted) and the gRPC RPC use one source of truth. Never
// returns NotFound — the synthesised default tier guarantees a result.
func (h *GRPCHandler) GetEffectiveScanPolicy(ctx context.Context, req *scannerv1.GetEffectiveScanPolicyRequest) (*scannerv1.EffectiveScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	var repoID uuid.UUID
	if s := req.GetRepoId(); s != "" {
		repoID, err = uuid.Parse(s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "repo_id must be a UUID")
		}
	}
	var orgID uuid.UUID
	if s := req.GetOrgId(); s != "" {
		orgID, err = uuid.Parse(s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "org_id must be a UUID")
		}
	}
	res, err := policy.Resolve(ctx, h.repo, tenantID, repoID, orgID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve effective scan policy: %v", err)
	}
	return &scannerv1.EffectiveScanPolicy{
		Policy:        policyToProto(res.Policy),
		InheritedFrom: string(res.Source),
	}, nil
}

// ---------------------------------------------------------------------------
// FUT-017 — proxy-cache scan policies
// ---------------------------------------------------------------------------

// allowedSeverityThresholds is the wire-level enum for the FUT-017
// proxy-cache severity_threshold field. Kept tight at the gRPC layer
// so a hand-edited row that bypassed the table CHECK still surfaces a
// clean InvalidArgument on write. Empty string maps to "never block",
// matching the FE-API-018 convention.
var allowedSeverityThresholds = map[string]struct{}{
	"":         {},
	"none":     {},
	"low":      {},
	"medium":   {},
	"high":     {},
	"critical": {},
}

// upstreamNamePattern is the relaxed allowlist for upstream handles.
// The BFF performs the stricter user-facing validation; we keep this
// loose enough to accept anything the proxy already accepts but tight
// enough to reject SQL-poisoning attempts. Lower-case alnum, dashes,
// dots, underscores; 2..128 chars.
func isValidUpstreamName(s string) bool {
	if len(s) < 2 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}

// proxyCachePolicyToProto serialises a repository row to its proto
// shape. Zero UUID on UpdatedBy is rendered as an empty string so the
// dashboard can render "(system)" without parsing a sentinel UUID.
func proxyCachePolicyToProto(p *repository.ProxyCacheScanPolicy) *scannerv1.ProxyCacheScanPolicy {
	out := &scannerv1.ProxyCacheScanPolicy{
		TenantId:          p.TenantID.String(),
		UpstreamName:      p.UpstreamName,
		AutoScan:          p.AutoScan,
		SeverityThreshold: p.SeverityThreshold,
		UpdatedAt:         timestamppb.New(p.UpdatedAt),
	}
	if p.UpdatedBy != uuid.Nil {
		out.UpdatedBy = p.UpdatedBy.String()
	}
	return out
}

// defaultProxyCachePolicy is the "never configured" empty state. We
// return this from GetProxyCacheScanPolicy on a cache miss rather than
// NotFound so the dashboard can render the default toggle without a
// separate code path (mirrors GetScanPolicy's per-tenant default).
func defaultProxyCachePolicy(tenantID, upstreamName string) *scannerv1.ProxyCacheScanPolicy {
	return &scannerv1.ProxyCacheScanPolicy{
		TenantId:          tenantID,
		UpstreamName:      upstreamName,
		AutoScan:          false,
		SeverityThreshold: "",
	}
}

// GetProxyCacheScanPolicy returns the per-upstream policy or the empty
// default when no row exists. Never NotFound — the empty state is the
// "fresh tenant" wire shape.
func (h *GRPCHandler) GetProxyCacheScanPolicy(ctx context.Context, req *scannerv1.GetProxyCacheScanPolicyRequest) (*scannerv1.ProxyCacheScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if !isValidUpstreamName(req.GetUpstreamName()) {
		return nil, status.Error(codes.InvalidArgument, "upstream_name must match [a-z0-9._-]{2,128}")
	}
	rec, err := h.repo.GetProxyCacheScanPolicy(ctx, tenantID, req.GetUpstreamName())
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return defaultProxyCachePolicy(req.GetTenantId(), req.GetUpstreamName()), nil
		}
		return nil, status.Errorf(codes.Internal, "get proxy cache scan policy: %v", err)
	}
	return proxyCachePolicyToProto(rec), nil
}

// SetProxyCacheScanPolicy upserts the (tenant_id, upstream_name) row.
// The BFF enforces the RBAC check upstream; this handler validates the
// severity_threshold enum + upstream_name shape so an out-of-band call
// (via grpcurl or a misbehaving CLI) can't bypass the wire contract.
func (h *GRPCHandler) SetProxyCacheScanPolicy(ctx context.Context, req *scannerv1.SetProxyCacheScanPolicyRequest) (*scannerv1.ProxyCacheScanPolicy, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if !isValidUpstreamName(req.GetUpstreamName()) {
		return nil, status.Error(codes.InvalidArgument, "upstream_name must match [a-z0-9._-]{2,128}")
	}
	threshold := req.GetSeverityThreshold()
	if _, ok := allowedSeverityThresholds[threshold]; !ok {
		return nil, status.Error(codes.InvalidArgument, "severity_threshold must be one of '', none, low, medium, high, critical")
	}
	var updatedBy uuid.UUID
	if s := req.GetUpdatedBy(); s != "" {
		updatedBy, err = uuid.Parse(s)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "updated_by must be a UUID")
		}
	}
	out, err := h.repo.UpsertProxyCacheScanPolicy(ctx, &repository.ProxyCacheScanPolicy{
		TenantID:          tenantID,
		UpstreamName:      req.GetUpstreamName(),
		AutoScan:          req.GetAutoScan(),
		SeverityThreshold: threshold,
		UpdatedBy:         updatedBy,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert proxy cache scan policy: %v", err)
	}
	return proxyCachePolicyToProto(out), nil
}

// ListProxyCacheScanPolicies streams every row for the tenant ordered
// by upstream_name. We use a server-stream so the FE can render long
// tables incrementally; chunking is one row per Send() since rows are
// small (no findings blobs).
func (h *GRPCHandler) ListProxyCacheScanPolicies(
	req *scannerv1.ListProxyCacheScanPoliciesRequest,
	stream scannerv1.ScannerService_ListProxyCacheScanPoliciesServer,
) error {
	if h.repo == nil {
		return status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	rows, err := h.repo.ListProxyCacheScanPolicies(stream.Context(), tenantID)
	if err != nil {
		return status.Errorf(codes.Internal, "list proxy cache scan policies: %v", err)
	}
	for _, r := range rows {
		if err := stream.Send(proxyCachePolicyToProto(r)); err != nil {
			return status.Errorf(codes.Internal, "send proxy cache scan policy: %v", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// FE-API-019 — compliance reports
// ---------------------------------------------------------------------------

// GenerateComplianceReport creates a pending row and returns its id. The
// background worker picks it up on the next poll tick.
func (h *GRPCHandler) GenerateComplianceReport(ctx context.Context, req *scannerv1.GenerateComplianceReportRequest) (*scannerv1.GenerateComplianceReportResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	requestedBy, err := uuid.Parse(req.GetRequestedBy())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "requested_by must be a UUID")
	}
	id := uuid.New()
	if _, err := h.repo.CreateReport(ctx, id, tenantID, requestedBy); err != nil {
		return nil, status.Errorf(codes.Internal, "create report: %v", err)
	}
	return &scannerv1.GenerateComplianceReportResponse{
		ReportId: id.String(),
		Status:   "pending",
	}, nil
}

// GetComplianceReport returns one report row scoped by tenant.
func (h *GRPCHandler) GetComplianceReport(ctx context.Context, req *scannerv1.GetComplianceReportRequest) (*scannerv1.ComplianceReport, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	reportID, err := uuid.Parse(req.GetReportId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "report_id must be a UUID")
	}
	rec, err := h.repo.GetReport(ctx, reportID, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "report not found")
		}
		return nil, status.Errorf(codes.Internal, "get report: %v", err)
	}
	return reportToProto(rec), nil
}

// ListComplianceReports returns recent reports, paginated.
func (h *GRPCHandler) ListComplianceReports(ctx context.Context, req *scannerv1.ListComplianceReportsRequest) (*scannerv1.ListComplianceReportsResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	limit := int(req.GetPageSize())
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	statusFilter := req.GetStatus()
	switch statusFilter {
	case "", "pending", "running", "succeeded", "failed":
	default:
		return nil, status.Error(codes.InvalidArgument, "status must be one of pending|running|succeeded|failed or empty")
	}

	rows, next, err := h.repo.ListReports(ctx, tenantID, statusFilter, limit, req.GetPageToken())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list reports: %v", err)
	}

	out := &scannerv1.ListComplianceReportsResponse{
		Reports:       make([]*scannerv1.ComplianceReport, 0, len(rows)),
		NextPageToken: next,
	}
	for _, r := range rows {
		out.Reports = append(out.Reports, reportToProto(r))
	}
	return out, nil
}

// reportChunkBytes is the streaming chunk size for
// DownloadComplianceReport. 64 KiB is the sweet spot between gRPC
// per-message overhead and memory footprint on the management proxy
// (which io.Copy's straight into the HTTP response without buffering
// the whole file). Tunable here without touching the proto.
const reportChunkBytes = 64 * 1024

// DownloadComplianceReport (REM-012) streams the rendered PDF or SPDX
// JSON for a succeeded report.
//
// Auth: the request is forwarded by the management BFF, which has
// already done JWT + RBAC. We still scope to the supplied tenant_id by
// looking up the report row via GetReport (tenant-filtered) — a
// malformed report_id from another tenant 404s instead of leaking a
// file path.
//
// Error mapping:
//   - InvalidArgument: malformed UUIDs, unknown format, mismatched
//     extension (defence-in-depth against a corrupt DB row whose
//     pdf_path actually points at a JSON file or vice versa)
//   - NotFound: report does not exist for this tenant
//   - FailedPrecondition: report not yet in succeeded state
//   - Internal: artifact missing on disk despite "succeeded" status
//     (storage corruption — distinct from "report not found" so an
//     operator can tell the difference)
func (h *GRPCHandler) DownloadComplianceReport(
	req *scannerv1.DownloadComplianceReportRequest,
	stream scannerv1.ScannerService_DownloadComplianceReportServer,
) error {
	if h.repo == nil {
		return status.Error(codes.FailedPrecondition, "scanner repository not configured")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	reportID, err := uuid.Parse(req.GetReportId())
	if err != nil {
		return status.Error(codes.InvalidArgument, "report_id must be a UUID")
	}

	rec, err := h.repo.GetReport(stream.Context(), reportID, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return status.Error(codes.NotFound, "report not found")
		}
		return status.Errorf(codes.Internal, "get report: %v", err)
	}
	if rec.Status != "succeeded" {
		return status.Errorf(codes.FailedPrecondition,
			"report not ready (status=%s)", rec.Status)
	}

	// Pick artifact + content type by format. Keeps the allowlist tight
	// — anything outside {pdf, sbom} 400s rather than blindly streaming
	// whatever the row happens to hold.
	var (
		artifactPath string
		contentType  string
		expectedExt  string
	)
	switch strings.ToLower(req.GetFormat()) {
	case "pdf":
		artifactPath = rec.PDFPath
		contentType = "application/pdf"
		expectedExt = ".pdf"
	case "sbom":
		artifactPath = rec.SBOMPath
		contentType = "application/json"
		expectedExt = ".json"
	default:
		return status.Error(codes.InvalidArgument, "format must be one of pdf|sbom")
	}

	if artifactPath == "" {
		// Row says succeeded but never recorded a path — counts as
		// storage corruption from the operator's POV.
		return status.Errorf(codes.Internal,
			"report artifact missing for format %q", req.GetFormat())
	}

	// filepath.Clean + extension match guard against a corrupt DB row
	// that swapped pdf_path/sbom_path or escaped its working tree.
	// These are defence-in-depth — the report worker writes the rows
	// itself, so this is paranoia for the day someone hand-edits the
	// table.
	cleaned := filepath.Clean(artifactPath)
	if !strings.HasSuffix(strings.ToLower(cleaned), expectedExt) {
		return status.Errorf(codes.InvalidArgument,
			"report path %q does not match expected extension %q", cleaned, expectedExt)
	}

	f, err := os.Open(cleaned)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return status.Error(codes.Internal, "report artifact missing on disk")
		}
		return status.Errorf(codes.Internal, "open report artifact: %v", err)
	}
	defer f.Close()

	// First chunk: content_type only (zero-byte data). Sending the
	// content_type up-front lets the proxy commit Content-Type on the
	// HTTP response BEFORE any bytes flow — once io.Copy starts it's too
	// late to set headers. The first content chunk lands on the next
	// Send call.
	if err := stream.Send(&scannerv1.ReportChunk{ContentType: contentType}); err != nil {
		return status.Errorf(codes.Internal, "send first chunk: %v", err)
	}

	buf := make([]byte, reportChunkBytes)
	for {
		// Honor cancellation — a slow client or BFF disconnect should
		// stop the file read promptly rather than tying up the file
		// descriptor.
		if err := stream.Context().Err(); err != nil {
			return err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			// Slice to actual bytes read; Send copies the slice into the
			// outbound proto so reusing the buffer in the next loop is
			// safe.
			if err := stream.Send(&scannerv1.ReportChunk{Data: buf[:n]}); err != nil {
				return status.Errorf(codes.Internal, "send chunk: %v", err)
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return status.Errorf(codes.Internal, "read report artifact: %v", readErr)
		}
	}
}

// reportToProto converts a repository row to its proto form. Download URLs
// are populated only when the row is in the succeeded state — for v1 the
// "URL" is the on-disk path the file lives at.
func reportToProto(r *repository.ComplianceReport) *scannerv1.ComplianceReport {
	out := &scannerv1.ComplianceReport{
		ReportId:     r.ReportID.String(),
		TenantId:     r.TenantID.String(),
		RequestedBy:  r.RequestedBy.String(),
		RequestedAt:  timestamppb.New(r.RequestedAt),
		Status:       r.Status,
		ErrorMessage: r.ErrorMessage,
	}
	if !r.StartedAt.IsZero() && r.StartedAt.Year() > 1970 {
		out.StartedAt = timestamppb.New(r.StartedAt)
	}
	if !r.CompletedAt.IsZero() && r.CompletedAt.Year() > 1970 {
		out.CompletedAt = timestamppb.New(r.CompletedAt)
	}
	if r.Status == "succeeded" {
		out.DownloadPdfUrl = r.PDFPath
		out.DownloadSbomUrl = r.SBOMPath
	}
	return out
}

func timestampProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// ---------------------------------------------------------------------------
// REM-011 Phase 2 — adapter management
// ---------------------------------------------------------------------------

// adapterToProto serialises one registry.Adapter to its proto form.
// active is computed by the caller so the proto doesn't have to re-read
// the registry per row (the list RPC already knows the active path).
func adapterToProto(a scannerregistry.Adapter, active bool) *scannerv1.Adapter {
	return &scannerv1.Adapter{
		Name:      a.Name,
		Version:   a.Version,
		Path:      a.Path,
		Checksum:  a.Checksum,
		SizeBytes: a.SizeBytes,
		EnvKeys:   a.EnvKeys,
		Active:    active,
	}
}

// ListInstalledAdapters returns the full registry snapshot. Empty list +
// empty active path is valid (boot before any binary was discovered).
func (h *GRPCHandler) ListInstalledAdapters(_ context.Context, _ *emptypb.Empty) (*scannerv1.ListInstalledAdaptersResponse, error) {
	if h.adapterReg == nil {
		return nil, status.Error(codes.FailedPrecondition, "adapter registry not configured")
	}
	activePath := h.adapterReg.ActivePath()
	list := h.adapterReg.List()
	out := make([]*scannerv1.Adapter, 0, len(list))
	for _, a := range list {
		out = append(out, adapterToProto(a, a.Path == activePath))
	}
	return &scannerv1.ListInstalledAdaptersResponse{
		Adapters:          out,
		ActiveAdapterPath: activePath,
	}, nil
}

// GetActiveAdapter returns the currently active adapter. NotFound when
// no adapter is selected (boot misconfiguration — recoverable via
// SetActiveAdapter).
func (h *GRPCHandler) GetActiveAdapter(_ context.Context, _ *emptypb.Empty) (*scannerv1.Adapter, error) {
	if h.adapterReg == nil {
		return nil, status.Error(codes.FailedPrecondition, "adapter registry not configured")
	}
	a := h.adapterReg.Active()
	if a == nil {
		return nil, status.Error(codes.NotFound, "no active adapter")
	}
	return adapterToProto(*a, true), nil
}

// SetActiveAdapter swaps the worker pool's active scanner adapter and
// persists the choice in scanner_settings so it survives a restart.
// In-flight scans complete on their pre-swap adapter; the next job
// picks up the new one (see worker.Pool.SetScanner).
func (h *GRPCHandler) SetActiveAdapter(ctx context.Context, req *scannerv1.SetActiveAdapterRequest) (*scannerv1.Adapter, error) {
	if h.adapterReg == nil || h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "adapter registry or repository not configured")
	}
	if req.GetAdapterPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "adapter_path is required")
	}
	target := h.adapterReg.FindByPath(req.GetAdapterPath())
	if target == nil {
		// Surface InvalidArgument (not NotFound) so the caller is clear
		// they sent a value the registry rejects, rather than a path
		// that "might exist somewhere".
		return nil, status.Errorf(codes.InvalidArgument, "adapter %q is not installed", req.GetAdapterPath())
	}

	// Build a fresh ProcessPlugin. plugin.New re-verifies the checksum;
	// passing the registry's stored value asks plugin.New to confirm
	// nothing has tampered with the binary since boot discovery.
	newPlugin, err := internalPlugin.New(target.Path, target.Checksum)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build plugin for %q: %v", target.Path, err)
	}
	// Atomic swap on the worker pool — in-flight scans untouched.
	h.pool.SetScanner(plugin.Scanner(newPlugin))
	// Record the new active path in the registry's in-memory state. Done
	// after the SetScanner call so a registry update without a pool swap
	// is impossible (would lie about which adapter is running).
	if err := h.adapterReg.SetActive(target.Path); err != nil {
		// Logically unreachable — we just confirmed the path is in the
		// registry above — but treat as Internal because some external
		// race (binary removed between FindByPath and SetActive) could
		// in theory hit this path.
		return nil, status.Errorf(codes.Internal, "registry SetActive: %v", err)
	}
	// Persist last. Failure here logs but doesn't roll back the
	// in-memory swap; the next restart will fall back to the env-var
	// path (which is the same Phase 1 behaviour, no worse).
	if err := h.repo.SetActiveAdapter(ctx, target.Path, req.GetActorUserId()); err != nil {
		// Returning Internal here is correct — caller sees an error and
		// can retry; the registry is fine, only persistence failed.
		return nil, status.Errorf(codes.Internal, "persist active adapter: %v", err)
	}
	updated := h.adapterReg.Active()
	if updated == nil {
		// Defensive — should be impossible after SetActive succeeded.
		return adapterToProto(*target, true), nil
	}
	return adapterToProto(*updated, true), nil
}

// RunTestScan exercises the active adapter end-to-end against the
// pre-configured fixture (tenant + repo + tag) and waits up to 30s for
// the scan_results row to land. Returns timing + severity counts so the
// admin UI can verify the adapter swap worked.
func (h *GRPCHandler) RunTestScan(ctx context.Context, _ *emptypb.Empty) (*scannerv1.TestScanResponse, error) {
	if h.adapterReg == nil || h.metaClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "adapter registry or metadata client not configured")
	}
	if h.testScanFixture.TenantID == "" || h.testScanFixture.RepositoryName == "" || h.testScanFixture.ManifestRef == "" {
		return &scannerv1.TestScanResponse{
			Ok:           false,
			ErrorMessage: "scanner test fixture not configured (SCANNER_TEST_TENANT_ID/REPOSITORY/MANIFEST_REF)",
		}, nil
	}
	active := h.adapterReg.Active()
	if active == nil {
		return &scannerv1.TestScanResponse{
			Ok:           false,
			ErrorMessage: "no active adapter",
		}, nil
	}

	start := time.Now()

	// 1. Resolve repo_id from "<org>/<repo>" name.
	repoResp, err := h.metaClient.GetRepositoryByName(ctx, &metadatav1.GetRepositoryByNameRequest{
		TenantId: h.testScanFixture.TenantID,
		Name:     h.testScanFixture.RepositoryName,
	})
	if err != nil {
		return &scannerv1.TestScanResponse{
			Ok: false,
			// Use a friendly message — the fixture is a known dev seed
			// that often won't exist in production.
			ErrorMessage: "fixture repository not found: " + err.Error(),
		}, nil
	}

	// 2. Resolve tag → manifest_digest. ManifestRef may be a tag name
	//    (the common case for dev/alpine:latest) or — if the operator
	//    overrode the env var — a raw sha256 digest. Accept both: try
	//    tag first, fall through to using the value as a digest if the
	//    tag lookup fails.
	manifestDigest := h.testScanFixture.ManifestRef
	tagResp, tagErr := h.metaClient.GetTag(ctx, &metadatav1.GetTagRequest{
		TenantId: h.testScanFixture.TenantID,
		RepoId:   repoResp.GetRepoId(),
		Name:     h.testScanFixture.ManifestRef,
	})
	if tagErr == nil && tagResp.GetManifestDigest() != "" {
		manifestDigest = tagResp.GetManifestDigest()
	}

	// 3. Enqueue a scan job directly on the pool — bypasses RabbitMQ
	//    so a broken broker doesn't mask an adapter problem.
	scanID := h.pool.TriggerScanJob(
		h.testScanFixture.TenantID,
		repoResp.GetRepoId(),
		h.testScanFixture.RepositoryName,
		manifestDigest,
	)

	// 4. Poll the metadata service for the scan_results row. 30s deadline
	//    accommodates a cold Trivy first-scan (DB download takes ~20s);
	//    dev-stub finishes in <1s.
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, ok := h.waitForScanResult(pollCtx, h.testScanFixture.TenantID, repoResp.GetRepoId(), manifestDigest)
	duration := time.Since(start).Milliseconds()

	if !ok {
		return &scannerv1.TestScanResponse{
			Ok:           false,
			DurationMs:   duration,
			ErrorMessage: "scan did not complete within 30s (scan_id=" + scanID + ")",
		}, nil
	}

	return &scannerv1.TestScanResponse{
		Ok:             result.GetStatus() == "complete",
		ScannerName:    result.GetScannerName(),
		ScannerVersion: result.GetScannerVersion(),
		DurationMs:     duration,
		SeverityCounts: result.GetSeverityCounts(),
		ErrorMessage:   testScanErrorFromResult(result),
	}, nil
}

// waitForScanResult polls GetScanResult every 500ms until a row appears
// with a terminal status, or ctx expires. Returns ok=false on timeout.
func (h *GRPCHandler) waitForScanResult(ctx context.Context, tenantID, repoID, manifestDigest string) (*metadatav1.ScanResult, bool) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		// First-iteration probe: there's a small chance the worker
		// already finished by the time we get here. Cheap to try first.
		r, err := h.metaClient.GetScanResult(ctx, &metadatav1.GetScanResultRequest{
			TenantId:       tenantID,
			RepoId:         repoID,
			ManifestDigest: manifestDigest,
		})
		if err == nil && (r.GetStatus() == "complete" || r.GetStatus() == "failed") {
			return r, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-ticker.C:
		}
	}
}

// testScanErrorFromResult yields a friendly error message when the scan
// finished but reported failed; empty when status is complete.
func testScanErrorFromResult(r *metadatav1.ScanResult) string {
	if r.GetStatus() == "complete" {
		return ""
	}
	return "scan finished with status=" + r.GetStatus()
}

// GetScannerHealth returns liveness + recent-job stats sourced from the
// worker pool's in-memory counters. No DB I/O, no gRPC fan-out — safe
// to poll at high frequency from the dashboard.
func (h *GRPCHandler) GetScannerHealth(_ context.Context, _ *emptypb.Empty) (*scannerv1.ScannerHealthResponse, error) {
	if h.pool == nil {
		return nil, status.Error(codes.FailedPrecondition, "worker pool not configured")
	}
	resp := &scannerv1.ScannerHealthResponse{
		Healthy:       true, // process is up + serving gRPC by definition
		QueueDepth:    int64(h.pool.QueueDepth()),
		InFlightCount: h.pool.InFlightCount(),
	}
	if last := h.pool.LastSuccessAt(); !last.IsZero() {
		resp.LastSuccessfulScanAt = timestamppb.New(last)
	}
	if h.adapterReg != nil {
		if a := h.adapterReg.Active(); a != nil {
			resp.ActiveAdapterName = a.Name
			resp.ActiveAdapterVersion = a.Version
		}
	}
	return resp, nil
}
