// Package handler — REM-011 Phase 2 platform-admin scanner adapter routes.
//
// Five REST routes wrap the scannerv1 adapter-management RPCs so the
// dashboard can discover installed adapters, swap the active one, fire
// a known-good test scan, and surface worker-pool liveness.
//
// FE-API numbering:
//
//	FE-API-044  GET    /api/v1/admin/scanners            ListInstalledAdapters
//	FE-API-044  GET    /api/v1/admin/scanners/active     GetActiveAdapter
//	FE-API-045  PATCH  /api/v1/admin/scanners/active     SetActiveAdapter
//	FE-API-046  POST   /api/v1/admin/scanners/test       RunTestScan
//	FE-API-047  GET    /api/v1/admin/scanners/health     GetScannerHealth
//
// Authorization model — same as admin_tenants.go / admin_gc.go:
//
//  1. h.scanner must be non-nil (SCANNER_GRPC_ADDR was set at startup),
//     otherwise return 404 "route disabled".
//  2. The caller holds the platform-admin marker grant —
//     hasScopedRole(_, "org", "*", "admin"). Picking the active scanner
//     adapter is a deployment-wide choice (it affects every tenant's
//     scans), so tenant-scoped admin grants are intentionally not
//     enough.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// AdminAdapterResponse is the JSON wire form for one scanner adapter row.
// Mirrors the proto Adapter message — fields are renamed only where the
// JSON convention diverges from the proto field name.
type AdminAdapterResponse struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Path      string   `json:"path"`
	Checksum  string   `json:"checksum"`
	SizeBytes int64    `json:"size_bytes"`
	EnvKeys   []string `json:"env_keys"`
	Active    bool     `json:"active"`
}

// adminAdapterListResponse is the envelope for GET /api/v1/admin/scanners.
type adminAdapterListResponse struct {
	Adapters          []AdminAdapterResponse `json:"adapters"`
	ActiveAdapterPath string                 `json:"active_adapter_path,omitempty"`
}

// adminSetActiveAdapterBody is the JSON body for PATCH /admin/scanners/active.
// Only the path is operator-supplied; actor_user_id is filled from the
// auth context so a client cannot impersonate another user's swap.
type adminSetActiveAdapterBody struct {
	AdapterPath string `json:"adapter_path"`
}

// AdminTestScanResponse mirrors the proto TestScanResponse.
type AdminTestScanResponse struct {
	OK             bool             `json:"ok"`
	ScannerName    string           `json:"scanner_name,omitempty"`
	ScannerVersion string           `json:"scanner_version,omitempty"`
	DurationMS     int64            `json:"duration_ms"`
	SeverityCounts map[string]int32 `json:"severity_counts,omitempty"`
	ErrorMessage   string           `json:"error_message,omitempty"`
}

// AdminScannerHealthResponse mirrors the proto ScannerHealthResponse.
// LastSuccessfulScanAt is RFC3339 with omitempty so "never" surfaces as
// an absent field rather than the epoch — same convention as admin_gc.go.
type AdminScannerHealthResponse struct {
	Healthy              bool   `json:"healthy"`
	LastSuccessfulScanAt string `json:"last_successful_scan_at,omitempty"`
	QueueDepth           int64  `json:"queue_depth"`
	InFlightCount        int64  `json:"in_flight_count"`
	ActiveAdapterName    string `json:"active_adapter_name,omitempty"`
	ActiveAdapterVersion string `json:"active_adapter_version,omitempty"`
	// ActiveAdapterEngineReachable/Detail surface REM engine-sidecar
	// reachability (e.g. trivy-engine) distinct from a generic scan
	// error — see scanner.proto ScannerHealthResponse fields 7/8.
	ActiveAdapterEngineReachable bool   `json:"active_adapter_engine_reachable"`
	ActiveAdapterEngineDetail    string `json:"active_adapter_engine_detail,omitempty"`
}

// requireScannerAdmin is the shared platform-admin gate. Returns false
// (and writes the response) when the caller is denied — the handler
// must return immediately.
//
// REDESIGN-001 Phase 5.1: delegates to h.effectiveGlobalAdmin which reads
// users.is_global_admin (typed primitive) instead of (admin, org, '*').
//
// REDESIGN-001 Phase 5.4 / Decision #24: deny SA principals before the
// role lookup. Swapping the active scanner adapter is a platform-wide
// configuration change; SA bearers must never be able to clear this gate
// just because their owner happens to be an admin.
func (h *Handler) requireScannerAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return false
	}
	if middleware.PrincipalKindFromContext(r.Context()) == middleware.PrincipalKindServiceAccount {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	if !h.effectiveGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	return true
}

// adapterToAdminResp converts a proto adapter to its JSON wire shape.
func adapterToAdminResp(a *scannerv1.Adapter) AdminAdapterResponse {
	return AdminAdapterResponse{
		Name:      a.GetName(),
		Version:   a.GetVersion(),
		Path:      a.GetPath(),
		Checksum:  a.GetChecksum(),
		SizeBytes: a.GetSizeBytes(),
		EnvKeys:   a.GetEnvKeys(),
		Active:    a.GetActive(),
	}
}

// GET /api/v1/admin/scanners
//
// FE-API-044 list half. The active flag on each adapter and the envelope's
// active_adapter_path are duplicates intentionally — the UI can render
// the active card without iterating the list, while bulk views still
// have a per-row flag.
func (h *Handler) handleAdminListScanners(w http.ResponseWriter, r *http.Request) {
	if !h.requireScannerAdmin(w, r) {
		return
	}
	resp, err := h.scanner.ListInstalledAdapters(r.Context(), &emptypb.Empty{})
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: ListInstalledAdapters", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list scanner adapters")
		return
	}
	out := make([]AdminAdapterResponse, 0, len(resp.GetAdapters()))
	for _, a := range resp.GetAdapters() {
		out = append(out, adapterToAdminResp(a))
	}
	writeJSON(w, http.StatusOK, adminAdapterListResponse{
		Adapters:          out,
		ActiveAdapterPath: resp.GetActiveAdapterPath(),
	})
}

// GET /api/v1/admin/scanners/active
//
// FE-API-044 active half. NotFound from the scanner (no adapter selected
// at boot — only possible in a misconfigured deployment) bubbles up as a
// 404 so the UI can show a "no adapter selected" state distinct from a
// gRPC failure.
func (h *Handler) handleAdminGetActiveScanner(w http.ResponseWriter, r *http.Request) {
	if !h.requireScannerAdmin(w, r) {
		return
	}
	a, err := h.scanner.GetActiveAdapter(r.Context(), &emptypb.Empty{})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "no active scanner adapter")
			return
		}
		slog.ErrorContext(r.Context(), "admin: GetActiveAdapter", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch active scanner adapter")
		return
	}
	writeJSON(w, http.StatusOK, adapterToAdminResp(a))
}

// PATCH /api/v1/admin/scanners/active
//
// FE-API-045. Body carries the target adapter path; actor_user_id is
// filled from the auth context. The gRPC layer validates that the path
// is in the registry and rejects unknowns with InvalidArgument, which
// surfaces here as a 400 with the message verbatim — the registry is
// the source of truth for what's allowed and the user needs the exact
// failure reason to recover.
func (h *Handler) handleAdminSetActiveScanner(w http.ResponseWriter, r *http.Request) {
	if !h.requireScannerAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body adminSetActiveAdapterBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.AdapterPath == "" {
		writeError(w, http.StatusBadRequest, "adapter_path is required")
		return
	}
	actor := middleware.UserIDFromContext(r.Context())
	a, err := h.scanner.SetActiveAdapter(r.Context(), &scannerv1.SetActiveAdapterRequest{
		AdapterPath: body.AdapterPath,
		ActorUserId: actor,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, st.Message())
				return
			case codes.FailedPrecondition:
				writeError(w, http.StatusServiceUnavailable, "scanner adapter registry not ready")
				return
			}
		}
		slog.ErrorContext(r.Context(), "admin: SetActiveAdapter", "err", err, "adapter_path", body.AdapterPath)
		writeError(w, http.StatusInternalServerError, "failed to set active scanner adapter")
		return
	}
	writeJSON(w, http.StatusOK, adapterToAdminResp(a))
}

// POST /api/v1/admin/scanners/test
//
// FE-API-046. Fires a test scan against the deployment's configured
// fixture. Body is currently empty — the fixture lives in scanner env
// vars (SCANNER_TEST_TENANT_ID/REPOSITORY/MANIFEST_REF) so the caller
// cannot point the test scan at arbitrary other tenants' images.
func (h *Handler) handleAdminTestScanner(w http.ResponseWriter, r *http.Request) {
	if !h.requireScannerAdmin(w, r) {
		return
	}
	resp, err := h.scanner.RunTestScan(r.Context(), &emptypb.Empty{})
	if err != nil {
		// gRPC error is distinct from "scan ran but failed" — the
		// latter still returns ok=200 with ok:false in the body so the
		// UI can show the timing and error_message together.
		slog.ErrorContext(r.Context(), "admin: RunTestScan", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to run test scan")
		return
	}
	writeJSON(w, http.StatusOK, AdminTestScanResponse{
		OK:             resp.GetOk(),
		ScannerName:    resp.GetScannerName(),
		ScannerVersion: resp.GetScannerVersion(),
		DurationMS:     resp.GetDurationMs(),
		SeverityCounts: resp.GetSeverityCounts(),
		ErrorMessage:   resp.GetErrorMessage(),
	})
}

// GET /api/v1/admin/scanners/health
//
// FE-API-047. Read-only liveness snapshot the dashboard can poll to
// replace the Phase 1 90s client-side stuck-pending heuristic. Cheap:
// the scanner serves this from in-memory atomic counters, no DB or
// fan-out RPCs.
func (h *Handler) handleAdminScannerHealth(w http.ResponseWriter, r *http.Request) {
	if !h.requireScannerAdmin(w, r) {
		return
	}
	resp, err := h.scanner.GetScannerHealth(r.Context(), &emptypb.Empty{})
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: GetScannerHealth", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch scanner health")
		return
	}
	out := AdminScannerHealthResponse{
		Healthy:                      resp.GetHealthy(),
		QueueDepth:                   resp.GetQueueDepth(),
		InFlightCount:                resp.GetInFlightCount(),
		ActiveAdapterName:            resp.GetActiveAdapterName(),
		ActiveAdapterVersion:         resp.GetActiveAdapterVersion(),
		ActiveAdapterEngineReachable: resp.GetActiveAdapterEngineReachable(),
		ActiveAdapterEngineDetail:    resp.GetActiveAdapterEngineDetail(),
	}
	if ts := resp.GetLastSuccessfulScanAt(); ts != nil {
		// RFC3339 keeps parity with the gc + tenant admin routes —
		// both use the same human-readable timestamp format so the UI
		// has one date-rendering code path across all admin surfaces.
		out.LastSuccessfulScanAt = ts.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	writeJSON(w, http.StatusOK, out)
}

// RegisterAdminScanners mounts the five REM-011 Phase 2 routes onto mux.
// All five are wrapped by authMW (RequireAuth) and gated by the
// platform-admin marker inside each handler via requireScannerAdmin.
func (h *Handler) RegisterAdminScanners(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/admin/scanners", authMW(http.HandlerFunc(h.handleAdminListScanners)))
	mux.Handle("GET /api/v1/admin/scanners/active", authMW(http.HandlerFunc(h.handleAdminGetActiveScanner)))
	mux.Handle("PATCH /api/v1/admin/scanners/active", authMW(http.HandlerFunc(h.handleAdminSetActiveScanner)))
	mux.Handle("POST /api/v1/admin/scanners/test", authMW(http.HandlerFunc(h.handleAdminTestScanner)))
	mux.Handle("GET /api/v1/admin/scanners/health", authMW(http.HandlerFunc(h.handleAdminScannerHealth)))
}
