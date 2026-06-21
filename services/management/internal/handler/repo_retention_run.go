// Package handler — FE-API-040 retention sweep trigger.
//
// Two routes, both scoped under /api/v1/repositories/{org}/{repo}/policies/retention:
//
//	POST .../run            — repo admin / owner; queues a retention sweep.
//	GET  .../runs/{run_id}  — repo reader; returns a single retention run row.
//
// Auth posture mirrors the PUT side of FE-API-037: PUT and the POST trigger
// here are gated on repo admin or owner because both are destructive
// primitives (retention eventually deletes manifests via the FE-API-040
// soft-delete + grace flow). The GET is reader-grade since it surfaces
// existing run state rather than mutating anything.
//
// All three routes return 404 "route disabled" when GC_GRPC_ADDR is unset on
// the management deployment (mirrors admin_gc.go). This way a deployment
// running registry-gc in cron-only mode still serves every other endpoint.
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

// retentionRunTriggerResponse is the 202 body returned by POST .../run.
type retentionRunTriggerResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// retentionRunStatusResponse is the GET .../runs/{run_id} body. Timestamps
// use RFC3339 strings so the dashboard parses them without a custom
// converter.
type retentionRunStatusResponse struct {
	RunID            string `json:"run_id"`
	RepoID           string `json:"repo_id,omitempty"`
	Mode             string `json:"mode"`
	Status           string `json:"status"`
	RequestedAt      string `json:"requested_at,omitempty"`
	StartedAt        string `json:"started_at,omitempty"`
	CompletedAt      string `json:"completed_at,omitempty"`
	ManifestsMarked  int64  `json:"manifests_marked"`
	ManifestsDeleted int64  `json:"manifests_deleted"`
	BlobsFreed       int64  `json:"blobs_freed"`
	BytesFreed       int64  `json:"bytes_freed"`
	ErrorMessage     string `json:"error_message,omitempty"`
	TriggeredBy      string `json:"triggered_by,omitempty"`
}

// RegisterRepoRetentionRun mounts the FE-API-040 routes.
func (h *Handler) RegisterRepoRetentionRun(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/policies/retention/run",
		authMW(http.HandlerFunc(h.handleTriggerRepoRetentionRun)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/policies/retention/runs/{run_id}",
		authMW(http.HandlerFunc(h.handleGetRepoRetentionRun)))
}

// handleTriggerRepoRetentionRun queues a retention sweep for the addressed
// repository. Returns 202 + {run_id, status:"queued"}. The dashboard polls
// the GET sibling for progression.
func (h *Handler) handleTriggerRepoRetentionRun(w http.ResponseWriter, r *http.Request) {
	if h.gc == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// Retention is destructive — repo admin / owner only. Writer is NOT
	// enough; that mirrors the PUT-on-policy gate.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	if userID == "" {
		// RequireAuth should have populated this — defensive guard so the
		// gc service's UUID parse on triggered_by doesn't fail with a
		// less-clear message.
		writeError(w, http.StatusForbidden, "user id missing from context")
		return
	}

	resp, err := h.gc.TriggerRetentionRun(r.Context(), &gcv1.TriggerRetentionRunRequest{
		TenantId:    tenantID,
		RepoId:      repo.GetRepoId(),
		TriggeredBy: userID,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, st.Message())
				return
			case codes.FailedPrecondition:
				// gc service is configured without the dispatcher (e.g.
				// DB_DSN unset). The dashboard treats this the same as
				// GC_GRPC_ADDR being unset — 404 "route disabled".
				writeError(w, http.StatusNotFound, "route disabled")
				return
			}
		}
		slog.Error("retention: TriggerRetentionRun", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to queue retention run")
		return
	}

	writeJSON(w, http.StatusAccepted, retentionRunTriggerResponse{
		RunID:  resp.GetRunId(),
		Status: resp.GetStatus(),
	})
}

// handleGetRepoRetentionRun returns the status of one retention run. Reader
// is sufficient since the response is read-only — but we still scope to the
// caller's tenant so a malformed run_id can't accidentally surface another
// tenant's run.
func (h *Handler) handleGetRepoRetentionRun(w http.ResponseWriter, r *http.Request) {
	if h.gc == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")
	runID := r.PathValue("run_id")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing run_id")
		return
	}

	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// findRepo already 404s when the repo is missing — no need to repeat
	// the check before calling gc.
	if _, err := h.findRepo(r, tenantID, org, repoName); err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	resp, err := h.gc.GetRetentionRunStatus(r.Context(), &gcv1.GetRetentionRunStatusRequest{
		RunId:    runID,
		TenantId: tenantID,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, st.Message())
				return
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "retention run not found")
				return
			}
		}
		slog.Error("retention: GetRetentionRunStatus", "err", err, "run_id", runID)
		writeError(w, http.StatusInternalServerError, "failed to fetch retention run")
		return
	}

	out := retentionRunStatusResponse{
		RunID:            resp.GetRunId(),
		RepoID:           resp.GetRepoId(),
		Mode:             resp.GetMode(),
		Status:           resp.GetStatus(),
		ManifestsMarked:  resp.GetManifestsMarked(),
		ManifestsDeleted: resp.GetManifestsDeleted(),
		BlobsFreed:       resp.GetBlobsFreed(),
		BytesFreed:       resp.GetBytesFreed(),
		ErrorMessage:     resp.GetErrorMessage(),
		TriggeredBy:      resp.GetTriggeredBy(),
	}
	if t := resp.GetRequestedAt(); t != nil {
		out.RequestedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := resp.GetStartedAt(); t != nil {
		out.StartedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	if t := resp.GetCompletedAt(); t != nil {
		out.CompletedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// retentionRunRequestBody is reserved for future extensions to the POST .../run
// body (e.g. forcing a dry-run mode without touching the policy preview
// window). Today the route takes no body — the policy already encodes
// everything the executor needs.
type retentionRunRequestBody struct{}

// unused suppress (we keep the body type defined so a future extension
// doesn't have to rename it in flight).
var _ = json.Unmarshal
var _ = retentionRunRequestBody{}
