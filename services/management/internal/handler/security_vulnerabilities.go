// Package handler — workspace-wide vulnerabilities list (FE-API-014).
//
// Mounted by handler.Register via the single registration line below to
// keep handler.go thin. The handler is a straight pass-through to the
// metadata service: validation lives here, aggregation lives in the SQL
// CTE on registry-metadata.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// AffectedTagResponse is the JSON representation of one (repo, tag, digest)
// tuple where a CVE was observed in the latest scan.
type AffectedTagResponse struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

// VulnerabilityResponse is the JSON representation of one rolled-up CVE
// from GET /api/v1/security/vulnerabilities.
type VulnerabilityResponse struct {
	CVEID          string                `json:"cve_id"`
	Severity       string                `json:"severity"`
	Title          string                `json:"title"`
	Description    string                `json:"description"`
	FixedIn        string                `json:"fixed_in"`
	PackageName    string                `json:"package_name"`
	PackageVersion string                `json:"package_version"`
	Affected       []AffectedTagResponse `json:"affected"`
	FirstSeen      time.Time             `json:"first_seen"`
	LastSeen       time.Time             `json:"last_seen"`
}

// VulnerabilityListResponse is the JSON body for GET /api/v1/security/vulnerabilities.
type VulnerabilityListResponse struct {
	Vulnerabilities []VulnerabilityResponse `json:"vulnerabilities"`
	NextPageToken   string                  `json:"next_page_token"`
}

// validSeverity is the same allowlist used by registry-metadata. Defined
// here so the BFF can return 400 before any gRPC call.
func validSeverity(s string) bool {
	switch s {
	case "", "CRITICAL", "HIGH", "MEDIUM", "LOW", "NEGLIGIBLE":
		return true
	}
	return false
}

// handleListVulnerabilities backs GET /api/v1/security/vulnerabilities.
//
// Query params:
//
//	severity   optional, one of CRITICAL|HIGH|MEDIUM|LOW|NEGLIGIBLE
//	page_token optional, opaque cursor from a previous response
//	limit      optional, 1..200; default 50
//
// Tenant isolation is enforced upstream — the handler only forwards
// tenant_id from the authenticated context.
func (h *Handler) handleListVulnerabilities(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	severity := strings.ToUpper(r.URL.Query().Get("severity"))
	if !validSeverity(severity) {
		writeError(w, http.StatusBadRequest, "invalid severity filter")
		return
	}

	pageToken := r.URL.Query().Get("page_token")
	if pageToken != "" {
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}

	// Default 50, hard cap 200 — matches FE-API-014 spec.
	limit := int32(50)
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			if n > 200 {
				n = 200
			}
			limit = int32(n)
		}
	}

	resp, err := h.meta.ListTenantVulnerabilities(r.Context(), &metadatav1.ListTenantVulnerabilitiesRequest{
		TenantId:  tenantID,
		Severity:  severity,
		PageToken: pageToken,
		PageSize:  limit,
	})
	if err != nil {
		slog.Error("ListTenantVulnerabilities", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list vulnerabilities")
		return
	}

	out := VulnerabilityListResponse{
		Vulnerabilities: make([]VulnerabilityResponse, 0, len(resp.GetVulnerabilities())),
		NextPageToken:   resp.GetNextPageToken(),
	}
	for _, v := range resp.GetVulnerabilities() {
		// Affected slice is always allocated (even when empty) so the wire
		// shape stays stable for the dashboard.
		affected := make([]AffectedTagResponse, 0, len(v.GetAffected()))
		for _, a := range v.GetAffected() {
			affected = append(affected, AffectedTagResponse{
				Repo:   a.GetRepo(),
				Tag:    a.GetTag(),
				Digest: a.GetDigest(),
			})
		}
		out.Vulnerabilities = append(out.Vulnerabilities, VulnerabilityResponse{
			CVEID:          v.GetCveId(),
			Severity:       v.GetSeverity(),
			Title:          v.GetTitle(),
			Description:    v.GetDescription(),
			FixedIn:        v.GetFixedIn(),
			PackageName:    v.GetPackageName(),
			PackageVersion: v.GetPackageVersion(),
			Affected:       affected,
			FirstSeen:      v.GetFirstSeen().AsTime(),
			LastSeen:       v.GetLastSeen().AsTime(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
