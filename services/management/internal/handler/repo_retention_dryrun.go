// Package handler — FE-API-038 retention policy dry-run + preview-window
// state routes.
//
// Two routes layered onto the FE-API-037 retention CRUD family. The dry-run
// route is the safety net operators use before flipping a policy to enabled;
// the preview route powers the UI countdown banner that appears for the
// 24h window after a policy is enabled (preview_until is set by the upsert
// path in FE-API-037).
//
// Both routes share one metadata gRPC RPC (EvaluateRetention). The dry-run
// route passes the operator's draft candidate through. The preview route
// loads the saved policy via GetRepoRetentionPolicy and feeds it back into
// EvaluateRetention so the would-delete count is always live (not stale
// from when the preview window started).
//
// Authorization:
//
//   - Dry-run (POST): repo admin or owner. Same gate as PUT — dry-run is
//     the precondition for safely enabling, and exposing it to writers
//     would tell a non-admin caller exactly which manifests an admin
//     could evict. Holds the principle "preview access = write access".
//   - Preview (GET): repo reader or above. The preview banner is purely
//     informational, and a reader can already enumerate manifests via
//     the existing tag-list route, so this carries no additional
//     disclosure beyond what they could derive themselves.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// dryRunBody is the JSON shape accepted by POST /policies/retention/dry-run.
// Mirrors updateRetentionBody from repo_retention.go — kept as a separate
// struct (not aliased) so a future divergence (e.g. dry-run accepts a
// max_delete_results query/body parameter) doesn't require touching the
// PUT shape.
type dryRunBody struct {
	Enabled              bool                    `json:"enabled"`
	Rules                []RetentionRuleResponse `json:"rules"`
	ProtectedTagPatterns []string                `json:"protected_tag_patterns"`
}

// DryRunDeletionResponse is the per-manifest JSON shape inside would_delete.
// `tags` and `reasons` are always emitted as JSON arrays (never null) so the
// dashboard can iterate without a null-check.
type DryRunDeletionResponse struct {
	ManifestID     string   `json:"manifest_id"`
	ManifestDigest string   `json:"manifest_digest"`
	Tags           []string `json:"tags"`
	PushedAt       string   `json:"pushed_at"`
	SizeBytes      int64    `json:"size_bytes"`
	Reasons        []string `json:"reasons"`
}

// DryRunProtectedResponse is the per-manifest JSON shape inside
// protected_skipped. `matched_pattern` is the FIRST protected pattern that
// matched any of the manifest's tags — the UI only needs to render one.
type DryRunProtectedResponse struct {
	ManifestID     string   `json:"manifest_id"`
	ManifestDigest string   `json:"manifest_digest"`
	Tags           []string `json:"tags"`
	MatchedPattern string   `json:"matched_pattern"`
}

// DryRunResponse is the full POST /policies/retention/dry-run body.
//
// `total_count` / `total_bytes` reflect the FULL would-delete set even when
// `would_delete` was truncated — the UI relies on this to render
// "showing 1000 of 47 312 candidates" honestly. `truncated` indicates the
// would_delete array was capped; the protected_skipped truncation has no
// flag (operator-facing affordance is only on the would-delete side).
type DryRunResponse struct {
	WouldDelete      []DryRunDeletionResponse  `json:"would_delete"`
	ProtectedSkipped []DryRunProtectedResponse `json:"protected_skipped"`
	TotalCount       int64                     `json:"total_count"`
	TotalBytes       int64                     `json:"total_bytes"`
	EvaluatedAt      string                    `json:"evaluated_at"`
	Truncated        bool                      `json:"truncated"`
}

// PreviewStateResponse is the GET /policies/retention/preview shape.
//
// `in_preview_window` is the derived "is preview_until in the future"
// boolean — the dashboard banner branches on this. We keep both the raw
// `preview_until` ISO string AND the derived bool so the FE can render
// "preview ends in 4h" without re-parsing the timestamp.
//
// `would_delete_count` / `would_delete_bytes` mirror the dry-run totals so
// the banner can show "would delete N manifests now" — useful even AFTER
// the preview window expires, because the executor (FE-API-040) is a
// separate ticket and may not yet be running.
type PreviewStateResponse struct {
	Enabled          bool   `json:"enabled"`
	PreviewUntil     string `json:"preview_until,omitempty"`
	InPreviewWindow  bool   `json:"in_preview_window"`
	WouldDeleteCount int64  `json:"would_delete_count"`
	WouldDeleteBytes int64  `json:"would_delete_bytes"`
	PolicyUpdatedAt  string `json:"policy_updated_at,omitempty"`
}

// RegisterRepoRetentionDryRun mounts the FE-API-038 routes. Called from
// Handler.Register alongside RegisterRepoRetention. Two separate
// registration functions keep the FE-API-037 CRUD family and the
// FE-API-038 evaluation family editable in isolation.
func (h *Handler) RegisterRepoRetentionDryRun(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/repositories/{org}/{repo}/policies/retention/dry-run",
		authMW(http.HandlerFunc(h.handlePostRepoRetentionDryRun)))
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/policies/retention/preview",
		authMW(http.HandlerFunc(h.handleGetRepoRetentionPreview)))
}

// handlePostRepoRetentionDryRun runs the evaluator against a candidate
// policy supplied in the body. Does NOT persist anything. Repo admin gate
// matches the PUT route — see file-level comment for the rationale.
func (h *Handler) handlePostRepoRetentionDryRun(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// Admin gate — dry-run exposes which manifests an admin could evict;
	// it must NOT be reachable as a reader/writer (PENTEST-002 family).
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Larger body limit than the standard 4 KiB cap because the
	// protected_tag_patterns list can carry many regex strings. 16 KiB is
	// still tight enough to reject obvious abuse — the handler then
	// enforces a per-pattern length cap server-side.
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var body dryRunBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	candidate := dryRunBodyToCandidate(body)

	resp, err := h.meta.EvaluateRetention(r.Context(), &metadatav1.EvaluateRetentionRequest{
		TenantId:  tenantID,
		RepoId:    repo.GetRepoId(),
		Candidate: candidate,
		// Use the proto defaults — the metadata handler clamps to its own
		// caps and we don't expose the caps on the wire (yet). The FE-API-038
		// spec sets the cap at 1000 for would-delete + 100 for protected;
		// those are the metadata-side defaults.
		MaxDeleteResults:    0,
		MaxProtectedResults: 0,
	})
	if err != nil {
		switch grpcCodeOf(err) {
		case codes.InvalidArgument:
			// The metadata handler emits InvalidArgument for: bad rule kind,
			// bad regex, value out-of-range, enabled+empty rules. All of
			// these map to HTTP 400 — the body is invalid, not the route.
			writeError(w, http.StatusBadRequest, "invalid retention policy")
		case codes.NotFound:
			// Repo deleted between findRepo and EvaluateRetention. Surface
			// as 404 to match the GET/PUT/DELETE behaviour.
			writeError(w, http.StatusNotFound, "repository not found")
		default:
			slog.Error("EvaluateRetention (dry-run)", "err", err, "repo_id", repo.GetRepoId())
			writeError(w, http.StatusInternalServerError, "failed to evaluate retention policy")
		}
		return
	}
	writeJSON(w, http.StatusOK, evalResponseToDryRun(resp))
}

// handleGetRepoRetentionPreview returns the current preview-window state
// for the saved policy. Reader-or-above gate — the response is purely
// informational and the data it exposes is a subset of what the tags list
// would already reveal.
func (h *Handler) handleGetRepoRetentionPreview(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// Reader gate. 404 (not 403) on miss so non-members can't probe.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Load the saved policy. If none → 404 with the same code FE-API-037
	// uses on GET so the dashboard's banner state machine has one branch
	// for both routes.
	policy, err := h.meta.GetRepoRetentionPolicy(r.Context(), &metadatav1.GetRepoRetentionPolicyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"code":    "no-policy",
				"message": "no retention policy on this repository",
			})
			return
		}
		slog.Error("GetRepoRetentionPolicy (preview)", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to fetch retention policy")
		return
	}

	// Feed the saved policy back through the evaluator so the would-delete
	// totals are computed against TODAY'S manifest state, not whatever
	// state existed when preview_until was set. This is the load-bearing
	// "live preview" guarantee — without it the banner could show stale
	// counts after a busy day of pushes.
	evalCand := &metadatav1.RetentionPolicyCandidate{
		Enabled:              policy.GetEnabled(),
		Rules:                policy.GetRules(),
		ProtectedTagPatterns: policy.GetProtectedTagPatterns(),
	}
	eval, err := h.meta.EvaluateRetention(r.Context(), &metadatav1.EvaluateRetentionRequest{
		TenantId:  tenantID,
		RepoId:    repo.GetRepoId(),
		Candidate: evalCand,
		// We only need totals here, but the metadata RPC has no "totals-only"
		// mode. The caller pays one full evaluation cost per preview hit;
		// the dashboard polls this route at low frequency (banner countdown)
		// so the cost is acceptable. Optimise later if the frequency grows.
		MaxDeleteResults:    0,
		MaxProtectedResults: 0,
	})
	if err != nil {
		// A disabled or empty-rules policy that survived the upsert
		// validation will still pass through EvaluateRetention since the
		// evaluator accepts enabled=false. Any error here is genuine.
		slog.Error("EvaluateRetention (preview)", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to evaluate retention policy")
		return
	}

	out := PreviewStateResponse{
		Enabled:          policy.GetEnabled(),
		WouldDeleteCount: eval.GetTotalCount(),
		WouldDeleteBytes: eval.GetTotalBytes(),
	}
	if ts := policy.GetPreviewUntil(); ts != nil {
		previewUntil := ts.AsTime().UTC()
		out.PreviewUntil = previewUntil.Format(time.RFC3339)
		// in_preview_window collapses the "no preview_until" and "preview
		// already expired" cases into a single false. The dashboard then
		// renders "would delete N now" without a countdown.
		out.InPreviewWindow = previewUntil.After(time.Now().UTC())
	}
	if ts := policy.GetUpdatedAt(); ts != nil {
		out.PolicyUpdatedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// dryRunBodyToCandidate converts the request JSON shape to the proto
// RetentionPolicyCandidate. Allocates non-nil slices so the metadata
// handler's validation surface sees an empty list rather than nil.
func dryRunBodyToCandidate(body dryRunBody) *metadatav1.RetentionPolicyCandidate {
	rules := make([]*metadatav1.RetentionRule, 0, len(body.Rules))
	for _, r := range body.Rules {
		rules = append(rules, &metadatav1.RetentionRule{
			Kind:  r.Kind,
			Value: r.Value,
		})
	}
	patterns := body.ProtectedTagPatterns
	if patterns == nil {
		patterns = []string{}
	}
	return &metadatav1.RetentionPolicyCandidate{
		Enabled:              body.Enabled,
		Rules:                rules,
		ProtectedTagPatterns: patterns,
	}
}

// evalResponseToDryRun maps the proto evaluation response to the JSON wire
// shape. Always emits non-nil arrays for the would_delete / protected_skipped
// fields so the dashboard can iterate without a null-check.
func evalResponseToDryRun(resp *metadatav1.EvaluateRetentionResponse) DryRunResponse {
	wd := make([]DryRunDeletionResponse, 0, len(resp.GetWouldDelete()))
	for _, c := range resp.GetWouldDelete() {
		tags := c.GetTags()
		if tags == nil {
			tags = []string{}
		}
		reasons := c.GetReasons()
		if reasons == nil {
			reasons = []string{}
		}
		var pushedAt string
		if ts := c.GetPushedAt(); ts != nil {
			pushedAt = ts.AsTime().UTC().Format(time.RFC3339)
		}
		wd = append(wd, DryRunDeletionResponse{
			ManifestID:     c.GetManifestId(),
			ManifestDigest: c.GetManifestDigest(),
			Tags:           tags,
			PushedAt:       pushedAt,
			SizeBytes:      c.GetSizeBytes(),
			Reasons:        reasons,
		})
	}
	ps := make([]DryRunProtectedResponse, 0, len(resp.GetProtectedSkipped()))
	for _, p := range resp.GetProtectedSkipped() {
		tags := p.GetTags()
		if tags == nil {
			tags = []string{}
		}
		ps = append(ps, DryRunProtectedResponse{
			ManifestID:     p.GetManifestId(),
			ManifestDigest: p.GetManifestDigest(),
			Tags:           tags,
			MatchedPattern: p.GetMatchedPattern(),
		})
	}
	var evaluatedAt string
	if ts := resp.GetEvaluatedAt(); ts != nil {
		evaluatedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}
	return DryRunResponse{
		WouldDelete:      wd,
		ProtectedSkipped: ps,
		TotalCount:       resp.GetTotalCount(),
		TotalBytes:       resp.GetTotalBytes(),
		EvaluatedAt:      evaluatedAt,
		Truncated:        resp.GetTruncated(),
	}
}
