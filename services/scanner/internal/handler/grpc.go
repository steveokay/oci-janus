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
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/scanner/internal/repository"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"
)

// GRPCHandler implements scannerv1.ScannerServiceServer.
type GRPCHandler struct {
	scannerv1.UnimplementedScannerServiceServer
	pool      *worker.Pool
	scanStore *store.Store
	// repo is optional — when nil the policy + report RPCs short-circuit
	// with FailedPrecondition. Tests that only exercise the scan lifecycle
	// can leave it unset.
	repo *repository.Repository
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
func policyToProto(p *repository.ScanPolicy) *scannerv1.ScanPolicy {
	out := &scannerv1.ScanPolicy{
		TenantId:          p.TenantID.String(),
		AutoScanOnPush:    p.AutoScanOnPush,
		BlockOnSeverity:   p.BlockOnSeverity,
		ExemptCves:        p.ExemptCVEs,
		ScannerPlugin:     p.ScannerPlugin,
		ScannerVersionPin: p.ScannerVersionPin,
		UpdatedAt:         timestamppb.New(p.UpdatedAt),
	}
	if p.UpdatedBy != uuid.Nil {
		out.UpdatedBy = p.UpdatedBy.String()
	}
	return out
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
