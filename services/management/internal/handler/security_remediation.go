// Package handler — remediation suggestions feed (FE-API-017).
//
// Mounted by security.go via the single registration line below to keep
// handler.go thin. Pure pass-through to registry-metadata: aggregation,
// grouping, ordering, cursor logic, and the affected-count cap live in the
// SQL repository.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// RemediationAffectedResponse is the JSON representation of one (repo, tag,
// digest) tuple where a remediation grouping applies.
type RemediationAffectedResponse struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

// RemediationResponse is the JSON representation of one upgrade grouping
// from GET /api/v1/security/remediation. `Affected` is capped at 10 entries
// server-side; `AffectedCount` reports the true total so the dashboard can
// render "N affected (showing 10)".
type RemediationResponse struct {
	PackageName    string                        `json:"package_name"`
	FromVersion    string                        `json:"from_version"`
	ToVersion      string                        `json:"to_version"`
	CVEsFixed      []string                      `json:"cves_fixed"`
	CVEsFixedCount int32                         `json:"cves_fixed_count"`
	MaxSeverity    string                        `json:"max_severity"`
	Affected       []RemediationAffectedResponse `json:"affected"`
	AffectedCount  int32                         `json:"affected_count"`
}

// RemediationListResponse is the JSON body for GET /api/v1/security/remediation.
type RemediationListResponse struct {
	Remediations  []RemediationResponse `json:"remediations"`
	NextPageToken string                `json:"next_page_token"`
}

// handleListRemediations backs GET /api/v1/security/remediation.
//
// Query params:
//
//	limit      optional, 1..200; default 50
//	page_token optional, opaque cursor from a previous response
//
// Auth: any authenticated tenant user — same surface as FE-API-014. Tenant
// isolation is enforced upstream; the handler only forwards tenant_id from
// the authenticated context.
func (h *Handler) handleListRemediations(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	pageToken := r.URL.Query().Get("page_token")
	if pageToken != "" {
		// Reject obviously unsafe tokens before paying the gRPC round-trip.
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}

	// Default 50, hard cap 200 — matches FE-API-017 spec.
	limit := int32(50)
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			if n > 200 {
				n = 200
			}
			limit = int32(n)
		}
	}

	resp, err := h.meta.ListTenantRemediations(r.Context(), &metadatav1.ListTenantRemediationsRequest{
		TenantId:  tenantID,
		PageToken: pageToken,
		PageSize:  limit,
	})
	if err != nil {
		// Don't leak the gRPC status / tenant_id in the user-facing message;
		// the structured log captures both for operators.
		slog.Error("ListTenantRemediations", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list remediations")
		return
	}

	out := RemediationListResponse{
		Remediations:  make([]RemediationResponse, 0, len(resp.GetRemediations())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for _, rem := range resp.GetRemediations() {
		// Always allocate `affected` (even when empty) so the JSON wire shape
		// stays stable for the dashboard.
		affected := make([]RemediationAffectedResponse, 0, len(rem.GetAffected()))
		for _, a := range rem.GetAffected() {
			affected = append(affected, RemediationAffectedResponse{
				Repo:   a.GetRepo(),
				Tag:    a.GetTag(),
				Digest: a.GetDigest(),
			})
		}
		// Allocate `cves_fixed` so an empty list serialises as `[]` not null.
		cves := make([]string, 0, len(rem.GetCvesFixed()))
		cves = append(cves, rem.GetCvesFixed()...)
		out.Remediations = append(out.Remediations, RemediationResponse{
			PackageName:    rem.GetPackageName(),
			FromVersion:    rem.GetFromVersion(),
			ToVersion:      rem.GetToVersion(),
			CVEsFixed:      cves,
			CVEsFixedCount: rem.GetCvesFixedCount(),
			MaxSeverity:    rem.GetMaxSeverity(),
			Affected:       affected,
			AffectedCount:  rem.GetAffectedCount(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
