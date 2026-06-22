// Package handler — scan policy endpoints (FE-API-018).
//
// Two routes:
//
//	GET /api/v1/security/policies   — any authenticated tenant user
//	PUT /api/v1/security/policies   — admin/owner on any org in the tenant
//	                                  (same gate as webhooks; see PENTEST-002)
//
// All validation runs at the BFF level before the gRPC call. The scanner
// service's `scan_policies` CHECK constraint is a defence-in-depth backstop;
// the BFF allowlist is the source of truth for "what shapes are accepted".
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ScanPolicyResponse is the JSON wire form of a scan policy.
//
// FE-API-049 extension: OrgID + RepoID identify the scope when this
// response carries an org-default or per-repo override; both empty when
// returned via the per-tenant GET. Enabled lets the operator suspend a
// policy without losing config; the per-tenant row defaults to true
// (the original FE-API-018 schema has no enabled column, so backend
// emits true unconditionally for tenant-scoped reads).
type ScanPolicyResponse struct {
	TenantID          string   `json:"tenant_id"`
	OrgID             string   `json:"org_id,omitempty"`
	RepoID            string   `json:"repo_id,omitempty"`
	AutoScanOnPush    bool     `json:"auto_scan_on_push"`
	BlockOnSeverity   string   `json:"block_on_severity"`
	ExemptCVEs        []string `json:"exempt_cves"`
	ScannerPlugin     string   `json:"scanner_plugin"`
	ScannerVersionPin string   `json:"scanner_version_pin"`
	Enabled           bool     `json:"enabled"`
	UpdatedAt         string   `json:"updated_at,omitempty"`
	UpdatedBy         string   `json:"updated_by,omitempty"`
	// InheritedFrom is populated only on GET /repositories/.../policies/scan
	// when the response represents an inherited policy (set to "org",
	// "tenant", or "default"). The per-org and per-tenant direct GETs
	// leave it empty.
	InheritedFrom string `json:"inherited_from,omitempty"`
}

// updateScanPolicyBody is the JSON body for PUT /api/v1/security/policies.
// All fields are required — PUT is the full replace shape. Use PATCH if
// partial updates are needed in a follow-up.
type updateScanPolicyBody struct {
	AutoScanOnPush    bool     `json:"auto_scan_on_push"`
	BlockOnSeverity   string   `json:"block_on_severity"`
	ExemptCVEs        []string `json:"exempt_cves"`
	ScannerPlugin     string   `json:"scanner_plugin"`
	ScannerVersionPin string   `json:"scanner_version_pin"`
}

// reCVEID matches valid CVE identifiers: CVE-YYYY-N where N is 4–7 digits.
// Anchored to prevent log/header injection via embedded control characters.
var reCVEID = regexp.MustCompile(`^CVE-\d{4}-\d{4,7}$`)

// allowedBlockSeverities is the closed set the BFF accepts. The empty
// string means "never block". Matches the CHECK constraint on
// scan_policies.block_on_severity.
var allowedBlockSeverities = map[string]struct{}{
	"":         {},
	"CRITICAL": {},
	"HIGH":     {},
	"MEDIUM":   {},
	"LOW":      {},
}

// allowedScannerPlugins is the closed allowlist of scanner backends. New
// entries here require code review — scanner_plugin is consumed by
// registry-scanner to choose a plugin process, so an unbounded value
// would be a code-execution risk.
var allowedScannerPlugins = map[string]struct{}{
	"trivy": {},
	"grype": {},
}

// requireScanPolicyAdmin gates PUT. Same shape as requireWebhookAdmin —
// tenant-scoped resources have no "tenant admin" role today, so we accept
// any admin/owner grant at org scope.
func (h *Handler) requireScanPolicyAdmin(r *http.Request) bool {
	for _, a := range h.getUserAssignments(r) {
		if roleIndex(a.GetRole()) < roleIndex("admin") {
			continue
		}
		if a.GetScopeType() == "org" {
			return true
		}
	}
	return false
}

// RegisterSecurityPolicies mounts the FE-API-018 policy routes.
func (h *Handler) RegisterSecurityPolicies(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/security/policies", authMW(http.HandlerFunc(h.handleGetScanPolicy)))
	mux.Handle("PUT /api/v1/security/policies", authMW(http.HandlerFunc(h.handleUpdateScanPolicy)))
}

// handleGetScanPolicy returns the active policy for the caller's tenant.
// Scanner returns a default policy on cache miss — the BFF forwards
// whatever it gets.
func (h *Handler) handleGetScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	p, err := h.scanner.GetScanPolicy(r.Context(), &scannerv1.GetScanPolicyRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("GetScanPolicy", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to fetch scan policy")
		return
	}
	writeJSON(w, http.StatusOK, scanPolicyToResponse(p))
}

// handleUpdateScanPolicy validates the body then upserts via gRPC.
func (h *Handler) handleUpdateScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireScanPolicyAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body updateScanPolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Allowlist validation. Order: cheap string-compare first, then iterate
	// over the CVE list. Rejecting at the BFF means the scanner DB never
	// sees a malformed value.
	if _, ok := allowedBlockSeverities[body.BlockOnSeverity]; !ok {
		writeError(w, http.StatusBadRequest, "block_on_severity must be empty or one of CRITICAL,HIGH,MEDIUM,LOW")
		return
	}
	if _, ok := allowedScannerPlugins[body.ScannerPlugin]; !ok {
		writeError(w, http.StatusBadRequest, "scanner_plugin must be one of trivy,grype")
		return
	}
	for _, cve := range body.ExemptCVEs {
		if !reCVEID.MatchString(cve) {
			writeError(w, http.StatusBadRequest, "exempt_cves entries must match ^CVE-\\d{4}-\\d{4,7}$")
			return
		}
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())

	// nil-safe exempt list — the scanner handler expects a non-nil slice
	// for the text[] column.
	exempts := body.ExemptCVEs
	if exempts == nil {
		exempts = []string{}
	}

	p, err := h.scanner.UpdateScanPolicy(r.Context(), &scannerv1.UpdateScanPolicyRequest{
		TenantId:          tenantID,
		AutoScanOnPush:    body.AutoScanOnPush,
		BlockOnSeverity:   body.BlockOnSeverity,
		ExemptCves:        exempts,
		ScannerPlugin:     body.ScannerPlugin,
		ScannerVersionPin: body.ScannerVersionPin,
		UpdatedBy:         userID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		// Map known scanner status codes; default to 500 so internal gRPC
		// detail stays out of the response body.
		switch st.Code() {
		case codes.InvalidArgument:
			slog.Warn("UpdateScanPolicy invalid argument", "detail", st.Message())
			writeError(w, http.StatusBadRequest, "invalid request")
		default:
			slog.Error("UpdateScanPolicy", "err", err, "tenant_id", tenantID)
			writeError(w, http.StatusInternalServerError, "failed to update scan policy")
		}
		return
	}
	writeJSON(w, http.StatusOK, scanPolicyToResponse(p))
}

// scanPolicyToResponse converts proto to the JSON wire form. Always emits
// a non-nil ExemptCVEs slice so the frontend's array helpers don't have
// to guard against null.
func scanPolicyToResponse(p *scannerv1.ScanPolicy) ScanPolicyResponse {
	cves := p.GetExemptCves()
	if cves == nil {
		cves = []string{}
	}
	out := ScanPolicyResponse{
		TenantID:          p.GetTenantId(),
		OrgID:             p.GetOrgId(),
		RepoID:            p.GetRepoId(),
		AutoScanOnPush:    p.GetAutoScanOnPush(),
		BlockOnSeverity:   p.GetBlockOnSeverity(),
		ExemptCVEs:        cves,
		ScannerPlugin:     p.GetScannerPlugin(),
		ScannerVersionPin: p.GetScannerVersionPin(),
		Enabled:           p.GetEnabled(),
		UpdatedBy:         p.GetUpdatedBy(),
	}
	// Tenant-scoped reads come back with Enabled=false from the pre-FE-API-049
	// schema where the column doesn't exist. We surface true for those so
	// the FE doesn't render every legacy row as "disabled" — the inheritance
	// helper already treats tenant rows as always-on.
	if out.OrgID == "" && out.RepoID == "" && !out.Enabled {
		out.Enabled = true
	}
	if t := p.GetUpdatedAt(); t != nil {
		out.UpdatedAt = t.AsTime().UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}
