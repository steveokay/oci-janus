// Package handler — FE-API-037 per-repo retention policy CRUD.
//
// Three routes mounted under /api/v1/repositories/{org}/{repo}/policies/retention:
//
//	GET    — any user with repo `reader` or above. NotFound (404 with code
//	         "no-policy") when no per-repo override exists; the dashboard then
//	         renders the inherited-from-org-default state once FE-API-039 ships.
//	PUT    — repo admin or owner only. Writer is not enough — retention is a
//	         destructive primitive (it eventually deletes manifests).
//	DELETE — repo admin or owner only. Resets the repo back to "no override",
//	         so the repo falls through to the org default (FE-API-039).
//
// updated_by is taken from the JWT, not the body — the client cannot
// impersonate another user. preview_until is server-set; the client may read
// it but cannot write it.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RetentionRuleResponse mirrors the proto RetentionRule on the wire.
type RetentionRuleResponse struct {
	Kind  string `json:"kind"`
	Value int64  `json:"value"`
}

// RetentionPolicyResponse is the JSON body returned by GET/PUT.
//
// preview_until / created_at / updated_at use RFC3339 strings rather than
// time.Time so they round-trip cleanly through the dashboard's JSON parser
// and are omitted (rather than "0001-01-01") when zero.
//
// FE-API-039 additions:
//
//   - OrgID is set when the policy represents the org default (per-org PUT
//     returns the upserted row with OrgID populated; the per-repo GET
//     returns OrgID populated when it falls back to the org default).
//     Empty for a pure per-repo policy.
//   - InheritedFrom is "repo" when this response carries the per-repo row,
//     "org" when it carries the org default returned via inheritance.
//     The per-org PUT response sets it to "org" so the UI can render the
//     source consistently regardless of which endpoint was hit.
//     Existing clients that ignore the field still work; the response shape
//     stays backwards-compatible.
type RetentionPolicyResponse struct {
	RepoID               string                  `json:"repo_id"`
	OrgID                string                  `json:"org_id,omitempty"`
	TenantID             string                  `json:"tenant_id"`
	Enabled              bool                    `json:"enabled"`
	Rules                []RetentionRuleResponse `json:"rules"`
	ProtectedTagPatterns []string                `json:"protected_tag_patterns"`
	PreviewUntil         string                  `json:"preview_until,omitempty"`
	CreatedAt            string                  `json:"created_at"`
	UpdatedAt            string                  `json:"updated_at"`
	UpdatedBy            string                  `json:"updated_by,omitempty"`
	InheritedFrom        string                  `json:"inherited_from,omitempty"`
}

// updateRetentionBody is the PUT shape. updated_by is intentionally absent —
// the handler reads it from the JWT context.
type updateRetentionBody struct {
	Enabled              bool                    `json:"enabled"`
	Rules                []RetentionRuleResponse `json:"rules"`
	ProtectedTagPatterns []string                `json:"protected_tag_patterns"`
}

// RegisterRepoRetention mounts the FE-API-037 routes.
func (h *Handler) RegisterRepoRetention(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/policies/retention",
		authMW(http.HandlerFunc(h.handleGetRepoRetention)))
	mux.Handle("PUT /api/v1/repositories/{org}/{repo}/policies/retention",
		authMW(http.HandlerFunc(h.handlePutRepoRetention)))
	mux.Handle("DELETE /api/v1/repositories/{org}/{repo}/policies/retention",
		authMW(http.HandlerFunc(h.handleDeleteRepoRetention)))
}

// handleGetRepoRetention returns the per-repo policy. NotFound is the
// load-bearing case: the BFF must NOT synthesize a fake "empty policy"
// because the absence of a row is meaningful — FE-API-039 falls back to
// the org default in that branch.
func (h *Handler) handleGetRepoRetention(w http.ResponseWriter, r *http.Request) {
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

	// Reader on the repo (or parent org) is sufficient to view retention.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		// 404 (not 403) so non-members cannot confirm the repo exists.
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	policy, err := h.meta.GetRepoRetentionPolicy(r.Context(), &metadatav1.GetRepoRetentionPolicyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			// FE-API-039: no per-repo row → try the org-default fallback.
			// We call GetEffectiveRetentionPolicy rather than a separate
			// per-org GET so the disabled-default-doesn't-propagate rule
			// is enforced in exactly one place (the metadata SQL). If the
			// effective lookup also returns NotFound, we surface the
			// existing "no-policy" body unchanged so existing clients keep
			// working.
			eff, effErr := h.meta.GetEffectiveRetentionPolicy(r.Context(), &metadatav1.GetEffectiveRetentionPolicyRequest{
				TenantId: tenantID,
				RepoId:   repo.GetRepoId(),
			})
			if effErr != nil {
				if grpcCodeOf(effErr) == codes.NotFound {
					// Neither per-repo nor org default — same body shape as
					// before FE-API-039 so existing clients still parse.
					writeJSON(w, http.StatusNotFound, map[string]string{
						"code":    "no-policy",
						"message": "no retention policy on this repository and no org default",
					})
					return
				}
				slog.Error("GetEffectiveRetentionPolicy", "err", effErr, "repo_id", repo.GetRepoId())
				writeError(w, http.StatusInternalServerError, "failed to fetch retention policy")
				return
			}
			// Inheritance hit. Echo the inherited_from label through to the
			// JSON so the UI can render "(inherited from org default)"
			// without a second round-trip.
			writeJSON(w, http.StatusOK, retentionPolicyToResponseWith(eff.GetPolicy(), eff.GetInheritedFrom()))
			return
		}
		slog.Error("GetRepoRetentionPolicy", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to fetch retention policy")
		return
	}
	// Per-repo policy hit: emit inherited_from="repo" so the UI labels the
	// source explicitly even when there's no fallback in play.
	writeJSON(w, http.StatusOK, retentionPolicyToResponseWith(policy, "repo"))
}

// handlePutRepoRetention writes or replaces the policy. The JWT user_id is
// forwarded to the metadata service as updated_by — the client cannot
// override it.
func (h *Handler) handlePutRepoRetention(w http.ResponseWriter, r *http.Request) {
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

	// Retention is a destructive primitive — eventually deletes manifests.
	// Writer-on-repo is not enough; require repo admin (or parent-org admin).
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body updateRetentionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Map the wire shape to proto. We let the metadata gRPC handler enforce
	// the rule-kind allowlist, value caps, and regex validation so the
	// authoritative validation lives in one place (also exercised by the
	// metadata-handler unit tests).
	protoRules := make([]*metadatav1.RetentionRule, 0, len(body.Rules))
	for _, rule := range body.Rules {
		protoRules = append(protoRules, &metadatav1.RetentionRule{
			Kind:  rule.Kind,
			Value: rule.Value,
		})
	}
	patterns := body.ProtectedTagPatterns
	if patterns == nil {
		patterns = []string{}
	}

	policy, err := h.meta.UpsertRepoRetentionPolicy(r.Context(), &metadatav1.UpsertRepoRetentionPolicyRequest{
		TenantId:             tenantID,
		RepoId:               repo.GetRepoId(),
		Enabled:              body.Enabled,
		Rules:                protoRules,
		ProtectedTagPatterns: patterns,
		UpdatedBy:            userID,
	})
	if err != nil {
		switch grpcCodeOf(err) {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, "invalid retention policy")
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "repository not found")
		default:
			slog.Error("UpsertRepoRetentionPolicy", "err", err, "repo_id", repo.GetRepoId())
			writeError(w, http.StatusInternalServerError, "failed to update retention policy")
		}
		return
	}
	writeJSON(w, http.StatusOK, retentionPolicyToResponse(policy))
}

// handleDeleteRepoRetention removes the override. 204 on success; 404 when no
// policy exists (idempotent semantics are intentionally NOT applied — callers
// expect to know whether they actually cleared something).
func (h *Handler) handleDeleteRepoRetention(w http.ResponseWriter, r *http.Request) {
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

	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	if _, err := h.meta.DeleteRepoRetentionPolicy(r.Context(), &metadatav1.DeleteRepoRetentionPolicyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	}); err != nil {
		if grpcCodeOf(err) == codes.NotFound {
			writeError(w, http.StatusNotFound, "no retention policy on this repository")
			return
		}
		slog.Error("DeleteRepoRetentionPolicy", "err", err, "repo_id", repo.GetRepoId())
		writeError(w, http.StatusInternalServerError, "failed to delete retention policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// retentionPolicyToResponse converts the proto policy into the JSON wire shape.
// Always emits non-nil rules / protected_tag_patterns slices so the dashboard
// can iterate without a null-check.
//
// FE-API-039: the proto carries both RepoId and OrgId; we map both through so
// the per-repo response always has RepoID set and the per-org response
// always has OrgID set. The inherited_from label is set by callers via
// retentionPolicyToResponseWith — pass "" through this helper for the
// implicit "repo" default.
func retentionPolicyToResponse(p *metadatav1.RetentionPolicy) RetentionPolicyResponse {
	return retentionPolicyToResponseWith(p, "")
}

// retentionPolicyToResponseWith is the same conversion with an explicit
// inherited_from label. Used by the per-repo GET so it can emit
// `inherited_from: "repo"` on the success path and `inherited_from: "org"`
// on the org-default-fallback path. The org PUT response calls this with
// "org" so the source label is always present on org-side writes.
func retentionPolicyToResponseWith(p *metadatav1.RetentionPolicy, inheritedFrom string) RetentionPolicyResponse {
	rules := make([]RetentionRuleResponse, 0, len(p.GetRules()))
	for _, r := range p.GetRules() {
		rules = append(rules, RetentionRuleResponse{Kind: r.GetKind(), Value: r.GetValue()})
	}
	patterns := p.GetProtectedTagPatterns()
	if patterns == nil {
		patterns = []string{}
	}
	out := RetentionPolicyResponse{
		RepoID:               p.GetRepoId(),
		OrgID:                p.GetOrgId(),
		TenantID:             p.GetTenantId(),
		Enabled:              p.GetEnabled(),
		Rules:                rules,
		ProtectedTagPatterns: patterns,
		UpdatedBy:            p.GetUpdatedBy(),
		InheritedFrom:        inheritedFrom,
	}
	if ts := p.GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := p.GetUpdatedAt(); ts != nil {
		out.UpdatedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := p.GetPreviewUntil(); ts != nil {
		out.PreviewUntil = ts.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}

// grpcCodeOf extracts the gRPC status code from an error returned by the
// gRPC client. Returns codes.Unknown when the error is not a status error.
// Helper kept in this file (not handler.go) because it is currently only
// used by the FE-API-037 routes; promote when a second caller appears.
func grpcCodeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	// errors.As lets us pick up wrapped status errors too. Some interceptors
	// (e.g. the gRPC retry middleware) wrap status errors with %w-style
	// wrappers; falling back to errors.As keeps the mapping robust.
	var stErr interface {
		GRPCStatus() *status.Status
	}
	if errors.As(err, &stErr) {
		return stErr.GRPCStatus().Code()
	}
	return codes.Unknown
}
