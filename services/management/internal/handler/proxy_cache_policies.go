// FUT-017 — per-upstream scan + sign policy routes for the pull-through
// cache. Six workspace-admin-gated REST routes that wrap the new
// services/scanner and services/signer RPCs:
//
//	GET  /api/v1/proxy/upstreams/{name}/scan-policy   → ProxyCacheScanPolicy
//	PUT  /api/v1/proxy/upstreams/{name}/scan-policy   → ProxyCacheScanPolicy
//	GET  /api/v1/proxy/cache/scan-policies            → list all
//
//	GET  /api/v1/proxy/upstreams/{name}/sign-policy   → ProxyCacheSignPolicy
//	PUT  /api/v1/proxy/upstreams/{name}/sign-policy   → ProxyCacheSignPolicy
//	GET  /api/v1/proxy/cache/sign-policies            → list all
//
// Route shape:
//   - The single-policy routes are /upstreams/{name}/<kind>-policy because
//     a policy is one-per-upstream and named in the URL path.
//   - The list routes are /cache/<kind>-policies because they're a
//     tenant-wide read, not scoped to one upstream.
//
// Each route 404s with "route disabled" when its backing gRPC client is
// nil (scanner unwired → scan policy routes 404, signer unwired → sign
// policy routes 404). Same opt-in shape as the FUT-013 proxy.cache routes.
// Workspace-admin (any admin/owner role grant on any org in the tenant).
// Platform-admin trumps via the existing (admin, org, '*') marker.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// upstreamNameRe matches the operator-chosen upstream handle used as the
// per-upstream policy key. Lowercase letters, digits, hyphens, dots, max
// 64 chars. Mirrors the validation the scanner + signer services apply
// server-side, surfaced at the BFF so a bad name fails fast with a clear
// 400 instead of bouncing off a gRPC InvalidArgument.
var upstreamNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validSeverityThresholds is the closed enum the FUT-017 scan policy
// accepts. Anything else surfaces as 400. The empty string and "none"
// both mean "never block" and are kept as separate input values because
// the FE form may surface them as distinct radio options.
var validSeverityThresholds = map[string]struct{}{
	"":         {},
	"none":     {},
	"low":      {},
	"medium":   {},
	"high":     {},
	"critical": {},
}

// RegisterProxyCachePolicies mounts the FUT-017 routes. Called from
// Handler.Register alongside RegisterProxyCache.
func (h *Handler) RegisterProxyCachePolicies(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/proxy/upstreams/{name}/scan-policy", authMW(http.HandlerFunc(h.handleGetProxyCacheScanPolicy)))
	mux.Handle("PUT /api/v1/proxy/upstreams/{name}/scan-policy", authMW(http.HandlerFunc(h.handlePutProxyCacheScanPolicy)))
	mux.Handle("GET /api/v1/proxy/cache/scan-policies", authMW(http.HandlerFunc(h.handleListProxyCacheScanPolicies)))

	mux.Handle("GET /api/v1/proxy/upstreams/{name}/sign-policy", authMW(http.HandlerFunc(h.handleGetProxyCacheSignPolicy)))
	mux.Handle("PUT /api/v1/proxy/upstreams/{name}/sign-policy", authMW(http.HandlerFunc(h.handlePutProxyCacheSignPolicy)))
	mux.Handle("GET /api/v1/proxy/cache/sign-policies", authMW(http.HandlerFunc(h.handleListProxyCacheSignPolicies)))
}

// ── JSON shapes ─────────────────────────────────────────────────────

// proxyCacheScanPolicyResponse is the JSON shape returned by the
// scan-policy routes. Field names mirror the proto in snake_case so the
// frontend doesn't need a translation layer.
type proxyCacheScanPolicyResponse struct {
	UpstreamName      string  `json:"upstream_name"`
	AutoScan          bool    `json:"auto_scan"`
	SeverityThreshold string  `json:"severity_threshold"`
	UpdatedAt         *string `json:"updated_at,omitempty"`
	UpdatedBy         string  `json:"updated_by,omitempty"`
}

type proxyCacheScanPolicyPutBody struct {
	AutoScan          bool   `json:"auto_scan"`
	SeverityThreshold string `json:"severity_threshold"`
}

type listProxyCacheScanPoliciesResponse struct {
	Policies []proxyCacheScanPolicyResponse `json:"policies"`
}

type proxyCacheSignPolicyResponse struct {
	UpstreamName string  `json:"upstream_name"`
	AutoSign     bool    `json:"auto_sign"`
	KeyID        string  `json:"key_id,omitempty"`
	CreatedAt    *string `json:"created_at,omitempty"`
	UpdatedAt    *string `json:"updated_at,omitempty"`
}

type proxyCacheSignPolicyPutBody struct {
	AutoSign bool   `json:"auto_sign"`
	KeyID    string `json:"key_id"`
}

type listProxyCacheSignPoliciesResponse struct {
	Policies []proxyCacheSignPolicyResponse `json:"policies"`
}

// ── Scan policy routes ──────────────────────────────────────────────

func (h *Handler) handleGetProxyCacheScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	name := r.PathValue("name")
	if !upstreamNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid upstream name")
		return
	}

	resp, err := h.scanner.GetProxyCacheScanPolicy(r.Context(), &scannerv1.GetProxyCacheScanPolicyRequest{
		TenantId:     tenantID,
		UpstreamName: name,
	})
	if err != nil {
		slog.Error("GetProxyCacheScanPolicy", "err", err, "tenant_id", tenantID, "upstream", name)
		writeError(w, http.StatusInternalServerError, "failed to load scan policy")
		return
	}
	writeJSON(w, http.StatusOK, toScanPolicyResponse(resp))
}

func (h *Handler) handlePutProxyCacheScanPolicy(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	name := r.PathValue("name")
	if !upstreamNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid upstream name")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body proxyCacheScanPolicyPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := validSeverityThresholds[body.SeverityThreshold]; !ok {
		writeError(w, http.StatusBadRequest, "severity_threshold must be one of: none, low, medium, high, critical (or empty)")
		return
	}

	updatedBy := middleware.UserIDFromContext(r.Context())

	resp, err := h.scanner.SetProxyCacheScanPolicy(r.Context(), &scannerv1.SetProxyCacheScanPolicyRequest{
		TenantId:          tenantID,
		UpstreamName:      name,
		AutoScan:          body.AutoScan,
		SeverityThreshold: body.SeverityThreshold,
		UpdatedBy:         updatedBy,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
			writeError(w, http.StatusBadRequest, s.Message())
			return
		}
		slog.Error("SetProxyCacheScanPolicy", "err", err, "tenant_id", tenantID, "upstream", name)
		writeError(w, http.StatusInternalServerError, "failed to save scan policy")
		return
	}
	writeJSON(w, http.StatusOK, toScanPolicyResponse(resp))
}

func (h *Handler) handleListProxyCacheScanPolicies(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	stream, err := h.scanner.ListProxyCacheScanPolicies(r.Context(), &scannerv1.ListProxyCacheScanPoliciesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("ListProxyCacheScanPolicies", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list scan policies")
		return
	}
	out := listProxyCacheScanPoliciesResponse{Policies: []proxyCacheScanPolicyResponse{}}
	for {
		row, recvErr := stream.Recv()
		if recvErr != nil {
			if recvErr.Error() == "EOF" || recvErr.Error() == "<nil>" {
				// io.EOF — graceful end of stream. Some gRPC client versions
				// stringify EOF differently; comparing on Code() == OK is
				// the standard pattern.
				break
			}
			if s, ok := status.FromError(recvErr); ok && s.Code() == codes.OK {
				break
			}
			// Real failure: log + truncate the response.
			slog.Error("ListProxyCacheScanPolicies stream recv", "err", recvErr, "tenant_id", tenantID)
			break
		}
		out.Policies = append(out.Policies, toScanPolicyResponse(row))
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Sign policy routes ──────────────────────────────────────────────

func (h *Handler) handleGetProxyCacheSignPolicy(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	name := r.PathValue("name")
	if !upstreamNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid upstream name")
		return
	}

	resp, err := h.signer.GetProxyCacheSignPolicy(r.Context(), &signerv1.GetProxyCacheSignPolicyRequest{
		TenantId:     tenantID,
		UpstreamName: name,
	})
	if err != nil {
		slog.Error("GetProxyCacheSignPolicy", "err", err, "tenant_id", tenantID, "upstream", name)
		writeError(w, http.StatusInternalServerError, "failed to load sign policy")
		return
	}
	writeJSON(w, http.StatusOK, toSignPolicyResponse(resp))
}

func (h *Handler) handlePutProxyCacheSignPolicy(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}
	name := r.PathValue("name")
	if !upstreamNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid upstream name")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body proxyCacheSignPolicyPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// auto_sign + empty key_id is a no-op consumer-side per the agent's
	// design decision (the consumer treats key_id="" as disabled). We
	// reject it at the BFF anyway — saving "auto-sign on, no key picked"
	// is almost certainly a UI bug, not the operator's intent.
	if body.AutoSign && body.KeyID == "" {
		writeError(w, http.StatusBadRequest, "key_id is required when auto_sign is true")
		return
	}

	resp, err := h.signer.SetProxyCacheSignPolicy(r.Context(), &signerv1.SetProxyCacheSignPolicyRequest{
		TenantId:     tenantID,
		UpstreamName: name,
		AutoSign:     body.AutoSign,
		KeyId:        body.KeyID,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
			writeError(w, http.StatusBadRequest, s.Message())
			return
		}
		slog.Error("SetProxyCacheSignPolicy", "err", err, "tenant_id", tenantID, "upstream", name)
		writeError(w, http.StatusInternalServerError, "failed to save sign policy")
		return
	}
	writeJSON(w, http.StatusOK, toSignPolicyResponse(resp))
}

func (h *Handler) handleListProxyCacheSignPolicies(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	stream, err := h.signer.ListProxyCacheSignPolicies(r.Context(), &signerv1.ListProxyCacheSignPoliciesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		slog.Error("ListProxyCacheSignPolicies", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list sign policies")
		return
	}
	out := listProxyCacheSignPoliciesResponse{Policies: []proxyCacheSignPolicyResponse{}}
	for {
		row, recvErr := stream.Recv()
		if recvErr != nil {
			if recvErr.Error() == "EOF" {
				break
			}
			if s, ok := status.FromError(recvErr); ok && s.Code() == codes.OK {
				break
			}
			slog.Error("ListProxyCacheSignPolicies stream recv", "err", recvErr, "tenant_id", tenantID)
			break
		}
		out.Policies = append(out.Policies, toSignPolicyResponse(row))
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Projections ─────────────────────────────────────────────────────

func toScanPolicyResponse(p *scannerv1.ProxyCacheScanPolicy) proxyCacheScanPolicyResponse {
	out := proxyCacheScanPolicyResponse{
		UpstreamName:      p.GetUpstreamName(),
		AutoScan:          p.GetAutoScan(),
		SeverityThreshold: p.GetSeverityThreshold(),
		UpdatedBy:         p.GetUpdatedBy(),
	}
	if ts := p.GetUpdatedAt(); ts != nil {
		s := ts.AsTime().UTC().Format(rfc3339Nano)
		out.UpdatedAt = &s
	}
	return out
}

func toSignPolicyResponse(p *signerv1.ProxyCacheSignPolicy) proxyCacheSignPolicyResponse {
	out := proxyCacheSignPolicyResponse{
		UpstreamName: p.GetUpstreamName(),
		AutoSign:     p.GetAutoSign(),
		KeyID:        p.GetKeyId(),
	}
	if ts := p.GetCreatedAt(); ts != nil {
		s := ts.AsTime().UTC().Format(rfc3339Nano)
		out.CreatedAt = &s
	}
	if ts := p.GetUpdatedAt(); ts != nil {
		s := ts.AsTime().UTC().Format(rfc3339Nano)
		out.UpdatedAt = &s
	}
	return out
}

// rfc3339Nano is a constant local to this file rather than `time.RFC3339Nano`
// so we never import "time" just for a string. Same format the other
// management routes (audit feed, webhook deliveries, retention runs) emit
// so the FE's date helpers don't need per-route shims.
const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
