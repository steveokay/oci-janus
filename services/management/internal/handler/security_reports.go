// Package handler — compliance report endpoints (FE-API-019).
//
// All five routes are gated on `h.scanner != nil`; when SCANNER_GRPC_ADDR
// is unset they return 404 "route disabled" so a deployment without the
// scanner service still serves every other surface.
//
// Auth posture:
//
//   - POST /generate, GET /reports, GET /reports/{id}, GET /reports/{id}/download/*
//     all require an authenticated tenant user. Reports are tenant-scoped so
//     a regular reader can request and pull their own tenant's report.
//
// Download routes stream the file directly from disk under
// REPORT_OUTPUT_DIR. The on-disk path is fetched from the scanner via the
// gRPC `download_*_url` fields. Before opening the file the BFF re-validates
// the path with filepath.Clean + a startsWith check against the configured
// root would normally apply; for v1 we trust the scanner's path because
// the scanner constructed it itself from a uuid.New() and never accepts a
// caller-supplied path. We still guard against path traversal in the
// extension check so a malformed row can't leak random files.
package handler

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ComplianceReportResponse is the JSON wire form for a single report row.
type ComplianceReportResponse struct {
	ReportID        string `json:"report_id"`
	TenantID        string `json:"tenant_id"`
	RequestedBy     string `json:"requested_by"`
	RequestedAt     string `json:"requested_at,omitempty"`
	StartedAt       string `json:"started_at,omitempty"`
	CompletedAt     string `json:"completed_at,omitempty"`
	Status          string `json:"status"`
	ErrorMessage    string `json:"error_message,omitempty"`
	DownloadPDFURL  string `json:"download_pdf_url,omitempty"`
	DownloadSBOMURL string `json:"download_sbom_url,omitempty"`
}

// generateReportResponse is the JSON body returned by POST /generate.
type generateReportResponse struct {
	ReportID string `json:"report_id"`
	Status   string `json:"status"`
}

// RegisterSecurityReports mounts the FE-API-019 report routes.
func (h *Handler) RegisterSecurityReports(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/security/reports/generate", authMW(http.HandlerFunc(h.handleGenerateReport)))
	mux.Handle("GET /api/v1/security/reports", authMW(http.HandlerFunc(h.handleListReports)))
	mux.Handle("GET /api/v1/security/reports/{id}", authMW(http.HandlerFunc(h.handleGetReport)))
	mux.Handle("GET /api/v1/security/reports/{id}/download/pdf", authMW(http.HandlerFunc(h.handleDownloadPDF)))
	mux.Handle("GET /api/v1/security/reports/{id}/download/sbom", authMW(http.HandlerFunc(h.handleDownloadSBOM)))
}

// handleGenerateReport kicks off an async job.
func (h *Handler) handleGenerateReport(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	resp, err := h.scanner.GenerateComplianceReport(r.Context(), &scannerv1.GenerateComplianceReportRequest{
		TenantId:    tenantID,
		RequestedBy: userID,
	})
	if err != nil {
		slog.Error("GenerateComplianceReport", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to queue report")
		return
	}
	writeJSON(w, http.StatusAccepted, generateReportResponse{
		ReportID: resp.GetReportId(),
		Status:   resp.GetStatus(),
	})
}

// handleListReports returns recent reports for the caller's tenant.
func (h *Handler) handleListReports(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	// page_size: default 50, max 200. Matches the scanner-side cap so the
	// BFF never sees an oversized response stream.
	pageSize := int32(50)
	if s := r.URL.Query().Get("per_page"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
			pageSize = int32(n)
		}
	}
	pageToken := r.URL.Query().Get("page_token")
	if pageToken != "" {
		// Use the same allowlist guard as other paginated routes.
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}
	statusFilter := r.URL.Query().Get("status")
	switch statusFilter {
	case "", "pending", "running", "succeeded", "failed":
	default:
		writeError(w, http.StatusBadRequest, "status must be one of pending|running|succeeded|failed or empty")
		return
	}

	resp, err := h.scanner.ListComplianceReports(r.Context(), &scannerv1.ListComplianceReportsRequest{
		TenantId:  tenantID,
		Status:    statusFilter,
		PageSize:  pageSize,
		PageToken: pageToken,
	})
	if err != nil {
		slog.Error("ListComplianceReports", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list reports")
		return
	}
	reports := make([]ComplianceReportResponse, 0, len(resp.GetReports()))
	for _, p := range resp.GetReports() {
		reports = append(reports, reportProtoToResponse(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reports":         reports,
		"next_page_token": resp.GetNextPageToken(),
	})
}

// handleGetReport fetches one report by id.
func (h *Handler) handleGetReport(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	rec, err := h.scanner.GetComplianceReport(r.Context(), &scannerv1.GetComplianceReportRequest{
		TenantId: tenantID,
		ReportId: id,
	})
	if err != nil {
		st, _ := status.FromError(err)
		if st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "report not found")
			return
		}
		slog.Error("GetComplianceReport", "err", err, "report_id", id)
		writeError(w, http.StatusInternalServerError, "failed to fetch report")
		return
	}
	writeJSON(w, http.StatusOK, reportProtoToResponse(rec))
}

// handleDownloadPDF streams the rendered PDF for a succeeded report.
func (h *Handler) handleDownloadPDF(w http.ResponseWriter, r *http.Request) {
	h.serveReportFile(w, r, kindPDF)
}

// handleDownloadSBOM streams the rendered SPDX JSON for a succeeded report.
func (h *Handler) handleDownloadSBOM(w http.ResponseWriter, r *http.Request) {
	h.serveReportFile(w, r, kindSBOM)
}

// reportKind selects which artifact to stream.
type reportKind int

const (
	kindPDF reportKind = iota
	kindSBOM
)

// serveReportFile is the shared body of both download routes.
//
// REM-012 — the BFF no longer opens the on-disk artifact directly.
// Instead it calls the scanner's streaming DownloadComplianceReport RPC,
// which sends the file content_type on the first chunk + the bytes on
// subsequent chunks. We forward those bytes straight into the HTTP
// response writer with no buffering of the full payload. This removes
// the shared-volume dependency between scanner + management and makes
// cross-node K8s deployments work without surprises (per the proper-fix
// plan logged on REM-012 in status.md).
func (h *Handler) serveReportFile(w http.ResponseWriter, r *http.Request, kind reportKind) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report id")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	// Pick the format string the scanner RPC accepts + the filename
	// extension we'll suggest in Content-Disposition. The scanner's
	// allowlist (pdf|sbom) is mirrored here so a future kind addition
	// has one obvious place to update.
	var format, expectedExt string
	switch kind {
	case kindPDF:
		format = "pdf"
		expectedExt = ".pdf"
	case kindSBOM:
		format = "sbom"
		expectedExt = ".json"
	}

	stream, err := h.scanner.DownloadComplianceReport(r.Context(), &scannerv1.DownloadComplianceReportRequest{
		TenantId: tenantID,
		ReportId: id,
		Format:   format,
	})
	if err != nil {
		// Failure during stream setup — the RPC errored before any
		// chunks arrived. Map gRPC codes to HTTP semantics; everything
		// unmapped surfaces as 500 with the report_id logged for
		// triage.
		mapDownloadErr(w, err, id)
		return
	}

	// First chunk carries content_type so we can commit headers BEFORE
	// any bytes flow into the response. Once Write is called the
	// headers are sealed; this ordering matters.
	first, err := stream.Recv()
	if err != nil {
		// Includes io.EOF (server closed without sending anything),
		// which we treat the same as an unexpected absence of bytes.
		mapDownloadErr(w, err, id)
		return
	}
	contentType := first.GetContentType()
	if contentType == "" {
		// Defence-in-depth — the scanner contract puts content_type on
		// the first chunk; if a future server breaks the contract we
		// fall back to a sensible default rather than emitting an empty
		// Content-Type header.
		switch kind {
		case kindPDF:
			contentType = "application/pdf"
		case kindSBOM:
			contentType = "application/json"
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+id+expectedExt+"\"")

	// First chunk may also carry data (operator convenience — saves a
	// round-trip on small reports). Write it before draining the
	// remainder of the stream.
	if data := first.GetData(); len(data) > 0 {
		if _, werr := w.Write(data); werr != nil {
			// Client likely disconnected; nothing we can recover. Log
			// and let the defer-less stream cleanup run via context
			// cancellation on return.
			slog.Warn("stream report write", "err", werr, "report_id", id)
			return
		}
	}

	// Drain the rest of the stream. We deliberately don't io.Copy from
	// an adapter — the gRPC streaming Recv loop is the natural shape
	// here, and stops cleanly on io.EOF.
	for {
		chunk, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return
		}
		if recvErr != nil {
			// Mid-stream error — we've already committed headers, so
			// we can't change the HTTP status code. Best we can do is
			// stop writing + log; the client sees a truncated body.
			slog.Warn("stream report recv", "err", recvErr, "report_id", id)
			return
		}
		if _, werr := w.Write(chunk.GetData()); werr != nil {
			slog.Warn("stream report write", "err", werr, "report_id", id)
			return
		}
	}
}

// mapDownloadErr translates a gRPC error from the scanner stream into an
// HTTP response. Used by serveReportFile during stream setup (before
// any bytes have been written) so we can still pick a status code.
func mapDownloadErr(w http.ResponseWriter, err error, reportID string) {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.NotFound:
		writeError(w, http.StatusNotFound, "report not found")
	case codes.FailedPrecondition:
		// Report exists but isn't succeeded yet (still pending/running)
		// — 409 Conflict matches the pre-REM-012 semantics so any
		// existing frontend handling keeps working.
		writeError(w, http.StatusConflict, "report not ready")
	case codes.InvalidArgument:
		writeError(w, http.StatusBadRequest, st.Message())
	default:
		slog.Error("DownloadComplianceReport stream", "err", err, "report_id", reportID)
		writeError(w, http.StatusInternalServerError, "failed to stream report")
	}
}

// reportProtoToResponse converts proto → JSON wire shape, formatting
// timestamps as RFC3339 strings and skipping zero values.
func reportProtoToResponse(p *scannerv1.ComplianceReport) ComplianceReportResponse {
	out := ComplianceReportResponse{
		ReportID:        p.GetReportId(),
		TenantID:        p.GetTenantId(),
		RequestedBy:     p.GetRequestedBy(),
		Status:          p.GetStatus(),
		ErrorMessage:    p.GetErrorMessage(),
		DownloadPDFURL:  p.GetDownloadPdfUrl(),
		DownloadSBOMURL: p.GetDownloadSbomUrl(),
	}
	if t := p.GetRequestedAt(); t != nil {
		out.RequestedAt = t.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if t := p.GetStartedAt(); t != nil {
		out.StartedAt = t.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if t := p.GetCompletedAt(); t != nil {
		out.CompletedAt = t.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

