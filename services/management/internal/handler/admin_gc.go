// Package handler — FE-API-032 GC status visibility endpoints.
//
// Three platform-admin-only routes that mirror the gcv1.GCService gRPC
// surface in REST shape so the dashboard can render the GC operations
// card without speaking gRPC.
//
// Authorization model — same as the admin_tenants routes:
//
//  1. h.gc must be non-nil (GC_GRPC_ADDR was set at startup), otherwise
//     return 404 "route disabled".
//  2. The caller holds the platform-admin marker grant: hasScopedRole(
//     _, "org", "*", "admin"). The literal "*" cannot collide with a
//     real org name (validateOrgName rejects it), so this is the same
//     unambiguous marker used by admin_tenants.go for FE-API-028/029.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gcv1 "github.com/steveokay/oci-janus/proto/gen/go/gc/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// gcModeAllowlist mirrors the gc_run_mode SQL enum exposed by the gc
// service. Centralising the values here means a malformed body fails
// at the BFF before consuming a gRPC round-trip.
var gcModeAllowlist = map[string]struct{}{
	"dry-run":   {},
	"manifests": {},
	"blobs":     {},
	"full":      {},
}

// GCStatusResponse is the JSON shape of GET /api/v1/admin/gc/status.
// Timestamps are RFC3339 strings (omitempty so a missing value
// surfaces as the JSON field absent rather than `"0001-01-01..."`).
type GCStatusResponse struct {
	LastRunID               string `json:"last_run_id,omitempty"`
	LastRunMode             string `json:"last_run_mode,omitempty"`
	LastRunStatus           string `json:"last_run_status,omitempty"`
	LastRunStartedAt        string `json:"last_run_started_at,omitempty"`
	LastRunCompletedAt      string `json:"last_run_completed_at,omitempty"`
	LastRunDurationMS       int64  `json:"last_run_duration_ms"`
	LastRunBlobsFreed       int64  `json:"last_run_blobs_freed"`
	LastRunManifestsDeleted int64  `json:"last_run_manifests_deleted"`
	LastRunBytesFreed       int64  `json:"last_run_bytes_freed"`
	LastRunError            string `json:"last_run_error,omitempty"`
	LastRunTriggeredBy      string `json:"last_run_triggered_by,omitempty"`
	NextScheduledAt         string `json:"next_scheduled_at,omitempty"`
}

// GCRunResponse is one entry in the GC run history list. Mirrors the
// proto GCRun message with timestamps formatted as RFC3339.
type GCRunResponse struct {
	RunID            string `json:"run_id"`
	Mode             string `json:"mode"`
	Status           string `json:"status"`
	RequestedAt      string `json:"requested_at,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	CompletedAt      string `json:"completed_at,omitempty"`
	DurationMS       int64  `json:"duration_ms"`
	BlobsFreed       int64  `json:"blobs_freed"`
	ManifestsDeleted int64  `json:"manifests_deleted"`
	BytesFreed       int64  `json:"bytes_freed"`
	ErrorMessage     string `json:"error_message,omitempty"`
	TriggeredBy      string `json:"triggered_by,omitempty"`
}

// gcRunsListResponse is the JSON envelope for GET /api/v1/admin/gc/runs.
type gcRunsListResponse struct {
	Runs          []GCRunResponse `json:"runs"`
	NextPageToken string          `json:"next_page_token,omitempty"`
}

// gcRunNowResponse is the 202 body returned by POST /api/v1/admin/gc/run.
type gcRunNowResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// gcRunNowBody is the expected JSON body for POST /api/v1/admin/gc/run.
type gcRunNowBody struct {
	Mode string `json:"mode"`
}

// requireGCAdmin gates a route on (a) the gc gRPC client being wired
// and (b) the caller holding the platform-admin authority. The platform-admin
// check delegates to h.effectiveGlobalAdmin (REDESIGN-001 Phase 5.1) which
// reads users.is_global_admin instead of the legacy (admin, org, '*') marker.
//
// REDESIGN-001 Phase 5.4 / Decision #24: service-account principals are
// denied. Triggering GC destroys blobs, so the gate must refuse any
// non-human bearer — the shadow user's inherited roles are not an
// attestable signal that the SA itself should be allowed to delete data.
func (h *Handler) requireGCAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.gc == nil {
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

// handleAdminGCStatus returns the latest GC run snapshot. Read-only;
// the platform-admin gate still applies because the GC surface
// otherwise reveals when sweeps run (a fingerprint a malicious tenant
// could use to time exfiltration around the cleanup window).
func (h *Handler) handleAdminGCStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireGCAdmin(w, r) {
		return
	}

	st, err := h.gc.GetStatus(r.Context(), &gcv1.GetStatusRequest{})
	if err != nil {
		slog.Error("admin: GCService.GetStatus", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch gc status")
		return
	}
	writeJSON(w, http.StatusOK, gcStatusToResponse(st))
}

// handleAdminGCRuns returns the paginated run history. limit clamps to
// [1, 200] with a default of 50; page_token is forwarded opaquely and
// the gc service rejects malformed values with INVALID_ARGUMENT (which
// maps to 400 here).
func (h *Handler) handleAdminGCRuns(w http.ResponseWriter, r *http.Request) {
	if !h.requireGCAdmin(w, r) {
		return
	}

	limit := int32(50)
	if s := r.URL.Query().Get("limit"); s != "" {
		// Reuse fmtSscan from admin_tenants.go so the file stays free
		// of a strconv import (the rest of this file doesn't need it).
		var n int32
		_, _ = fmtSscan(s, &n)
		switch {
		case n <= 0:
			limit = 50
		case n > 200:
			limit = 200
		default:
			limit = n
		}
	}
	pageToken := r.URL.Query().Get("page_token")
	// Pre-validate the page_token shape — the gc service does its own
	// base64 decode, but a malformed value should fail fast here too.
	if pageToken != "" {
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}

	// S-MAINT-1 F2 — forward the optional search params straight through.
	// The gc service handler validates the RFC3339 timestamps and the
	// substring is treated as plain text (no allowlist gate needed —
	// it's an ILIKE input, fed as a parameterised bind).
	triggeredBy := r.URL.Query().Get("triggered_by")
	dateFrom := r.URL.Query().Get("date_from")
	dateTo := r.URL.Query().Get("date_to")

	resp, err := h.gc.ListRuns(r.Context(), &gcv1.ListRunsRequest{
		PageSize:    limit,
		PageToken:   pageToken,
		TriggeredBy: triggeredBy,
		DateFrom:    dateFrom,
		DateTo:      dateTo,
	})
	if err != nil {
		// The gc service maps a bad page_token to INVALID_ARGUMENT;
		// surface that distinctly so the UI can show a "invalid
		// cursor" warning rather than a generic 500.
		if st, ok := status.FromError(err); ok && st.Code() == codes.InvalidArgument {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
		slog.Error("admin: GCService.ListRuns", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch gc runs")
		return
	}

	out := make([]GCRunResponse, 0, len(resp.GetRuns()))
	for _, run := range resp.GetRuns() {
		out = append(out, gcRunToResponse(run))
	}
	writeJSON(w, http.StatusOK, gcRunsListResponse{
		Runs:          out,
		NextPageToken: resp.GetNextPageToken(),
	})
}

// handleAdminGCRun enqueues an immediate sweep. The caller's user_id
// goes through as `triggered_by` so the audit trail records the
// operator behind every manual run.
//
// Body: {"mode": "dry-run|manifests|blobs|full"}. Returns 202 +
// {run_id, status:"queued"} on success.
func (h *Handler) handleAdminGCRun(w http.ResponseWriter, r *http.Request) {
	if !h.requireGCAdmin(w, r) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body gcRunNowBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := gcModeAllowlist[body.Mode]; !ok {
		writeError(w, http.StatusBadRequest, "mode must be one of dry-run, manifests, blobs, full")
		return
	}

	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		// RequireAuth should have populated this — defensive guard.
		writeError(w, http.StatusForbidden, "user id missing from context")
		return
	}

	resp, err := h.gc.RunNow(r.Context(), &gcv1.RunNowRequest{
		Mode:        body.Mode,
		TriggeredBy: userID,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, st.Message())
				return
			case codes.FailedPrecondition:
				// The gc service surfaces FailedPrecondition when its
				// persistence layer or dispatcher isn't wired. From the
				// dashboard's perspective the route may as well be
				// disabled — return 404 so the UI matches the
				// "GC_GRPC_ADDR unset" case.
				writeError(w, http.StatusNotFound, "route disabled")
				return
			}
		}
		slog.Error("admin: GCService.RunNow", "err", err, "mode", body.Mode)
		writeError(w, http.StatusInternalServerError, "failed to queue gc run")
		return
	}

	writeJSON(w, http.StatusAccepted, gcRunNowResponse{
		RunID:  resp.GetRunId(),
		Status: resp.GetStatus(),
	})
}

// ---------------------------------------------------------------------------
// converters
// ---------------------------------------------------------------------------

// gcStatusToResponse converts the proto status message to the REST
// shape. Empty fields (no runs yet) propagate as empty strings so the
// JSON omitempty tags hide them from the wire.
//
// similar but proto field names differ (LastRun* vs Retention*); collapsing
// them would require a typed wrapper that defeats the proto contract.
//
//nolint:dupl // Sibling retentionStatusToResponse below is structurally
func gcStatusToResponse(s *gcv1.GCStatus) GCStatusResponse {
	out := GCStatusResponse{
		LastRunID:               s.GetLastRunId(),
		LastRunMode:             s.GetLastRunMode(),
		LastRunStatus:           s.GetLastRunStatus(),
		LastRunDurationMS:       s.GetLastRunDurationMs(),
		LastRunBlobsFreed:       s.GetLastRunBlobsFreed(),
		LastRunManifestsDeleted: s.GetLastRunManifestsDeleted(),
		LastRunBytesFreed:       s.GetLastRunBytesFreed(),
		LastRunError:            s.GetLastRunError(),
		LastRunTriggeredBy:      s.GetLastRunTriggeredBy(),
	}
	if t := s.GetLastRunStartedAt(); t != nil {
		out.LastRunStartedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := s.GetLastRunCompletedAt(); t != nil {
		out.LastRunCompletedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := s.GetNextScheduledAt(); t != nil {
		out.NextScheduledAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}

// gcRunToResponse converts a single GCRun proto to its REST shape.
//
// pattern; collapsing requires a typed wrapper.
//
//nolint:dupl // See gcStatusToResponse above — same proto-to-REST mapping
func gcRunToResponse(r *gcv1.GCRun) GCRunResponse {
	out := GCRunResponse{
		RunID:            r.GetRunId(),
		Mode:             r.GetMode(),
		Status:           r.GetStatus(),
		DurationMS:       r.GetDurationMs(),
		BlobsFreed:       r.GetBlobsFreed(),
		ManifestsDeleted: r.GetManifestsDeleted(),
		BytesFreed:       r.GetBytesFreed(),
		ErrorMessage:     r.GetErrorMessage(),
		TriggeredBy:      r.GetTriggeredBy(),
	}
	if t := r.GetRequestedAt(); t != nil {
		out.RequestedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := r.GetStartedAt(); t != nil {
		out.StartedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := r.GetCompletedAt(); t != nil {
		out.CompletedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}
