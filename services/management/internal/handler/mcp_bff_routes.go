// mcp_bff_routes.go — FUT-082.
//
// Three read-only BFF routes that close the gaps in the registry-mcp tool
// surface. Each MCP tool is a pure HTTP client of this BFF (CLAUDE.md §4.14);
// before FUT-082 three of the twelve tools pointed at routes that never
// existed, so they always 404'd. These handlers wrap the gRPC RPCs the tools
// need:
//
//	GET /api/v1/service-accounts   → auth.ListServiceAccounts
//	GET /api/v1/audit              → audit.ListAuditEvents   (tenant-wide)
//	GET /api/v1/promotions         → metadata.ListPromotions (tenant-wide)
//
// Authz posture: RequireAuth has already run, so the caller is authenticated
// and bound to a tenant (tenant_id is sourced from the JWT context, never the
// request). All three surfaces return TENANT-SCOPED data to any authenticated
// caller — no extra role gate. That matches the existing read-only posture of
// handleListStaleKeys (which returns tenant data to non-admins) and the
// single-tenant-by-design deployment: the operator
// who minted the MCP API key owns the whole tenant. A future multi-tenant
// hardening pass may want to gate audit/service-accounts behind tenant-admin.
package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// auditListLimitCap mirrors client.AuditLimitCap in services/mcp — the BFF
// coerces any oversized limit down so neither layer can pull the whole trail
// in one call. Defence in depth: the audit repository also clamps at 500.
const auditListLimitCap = 500

// ---------------------------------------------------------------------------
// GET /api/v1/service-accounts
// ---------------------------------------------------------------------------

// serviceAccountResponse is the JSON shape the MCP list_service_accounts tool
// consumes (services/mcp/internal/client.ServiceAccount). Disabled is a plain
// bool — the auth summary collapses the disabled_at timestamp to a flag.
type serviceAccountResponse struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	AllowedScopes  []string `json:"allowed_scopes"`
	Disabled       bool     `json:"disabled"`
	ActiveKeyCount int32    `json:"active_key_count"`
	CreatedAt      string   `json:"created_at,omitempty"`
	LastUsedAt     string   `json:"last_used_at,omitempty"`
	// Origin records how the SA was created: 'manual' (admin/API) or
	// 'mcp-connect' (the one-click MCP connect flow). MCP provenance.
	Origin string `json:"origin,omitempty"`
}

// handleListServiceAccounts proxies auth.ListServiceAccounts. include_disabled
// is always true so the inventory view shows retired bots too — the FE/LLM can
// filter on the disabled flag.
func (h *Handler) handleListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.auth.ListServiceAccounts(r.Context(), &authv1.ListServiceAccountsRequest{
		TenantId:        tenantID,
		IncludeDisabled: true,
	})
	if err != nil {
		slog.Error("ListServiceAccounts", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list service accounts")
		return
	}

	// Materialise an empty slice so the envelope is always `service_accounts: []`
	// rather than null — the tool + FE treat null as an error state.
	out := make([]serviceAccountResponse, 0, len(resp.GetServiceAccounts()))
	for _, sa := range resp.GetServiceAccounts() {
		row := serviceAccountResponse{
			ID:             sa.GetId(),
			Name:           sa.GetName(),
			Description:    sa.GetDescription(),
			AllowedScopes:  sa.GetAllowedScopes(),
			Disabled:       sa.GetDisabled(),
			ActiveKeyCount: sa.GetActiveKeyCount(),
			Origin:         sa.GetOrigin(),
		}
		if ts := sa.GetCreatedAt(); ts != nil {
			row.CreatedAt = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if ts := sa.GetLastUsedAt(); ts != nil {
			row.LastUsedAt = ts.AsTime().UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"service_accounts": out})
}

// ---------------------------------------------------------------------------
// GET /api/v1/audit
// ---------------------------------------------------------------------------

// auditEventResponse is the JSON shape the MCP list_audit_events tool consumes
// (services/mcp/internal/client.AuditEvent). actor_kind / ip_address are the
// BFF-friendly names for the proto actor_type / actor_ip fields.
type auditEventResponse struct {
	ID         string `json:"id"`
	OccurredAt string `json:"occurred_at"`
	Action     string `json:"action"`
	ActorID    string `json:"actor_id,omitempty"`
	ActorKind  string `json:"actor_kind,omitempty"`
	Resource   string `json:"resource,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	IPAddress  string `json:"ip_address,omitempty"`
}

// handleListAuditEvents proxies audit.ListAuditEvents for a tenant-wide read.
//
// Query params (all optional):
//   - action_prefix — mapped to the proto `action` filter. NOTE: the audit
//     repository matches `action` EXACTLY, so this is an exact-action filter
//     despite the client's parameter name; unmatched actions simply return no
//     rows. Kept as action_prefix on the wire for MCP-client compatibility.
//   - actor_id       — exact actor filter.
//   - limit          — capped at auditListLimitCap (500).
//
// `since` / `resource` are accepted-and-ignored: the audit ListAuditEvents RPC
// has no field for them yet (the repository applies a default 30-day window).
func (h *Handler) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	limit := int32(auditListLimitCap)
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < auditListLimitCap {
			limit = int32(n) //nolint:gosec // bounded above to (0, 500)
		}
	}

	resp, err := h.audit.ListAuditEvents(r.Context(), &auditv1.ListAuditEventsRequest{
		TenantId: tenantID,
		ActorId:  r.URL.Query().Get("actor_id"),
		Action:   r.URL.Query().Get("action_prefix"),
		Limit:    limit,
	})
	if err != nil {
		slog.Error("ListAuditEvents", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list audit events")
		return
	}

	out := make([]auditEventResponse, 0, len(resp.GetEvents()))
	for _, e := range resp.GetEvents() {
		row := auditEventResponse{
			ID:        e.GetId(),
			Action:    e.GetAction(),
			ActorID:   e.GetActorId(),
			ActorKind: e.GetActorType(),
			Resource:  e.GetResource(),
			Outcome:   e.GetOutcome(),
			IPAddress: e.GetActorIp(),
		}
		if ts := e.GetOccurredAt(); ts != nil {
			row.OccurredAt = ts.AsTime().UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// ---------------------------------------------------------------------------
// GET /api/v1/promotions (tenant-wide)
// ---------------------------------------------------------------------------

// handleListPromotionsTenantWide proxies metadata.ListPromotions with an EMPTY
// org + repo — the metadata service reads that as "return the whole tenant's
// promotion history" (FUT-082 metadata change). The per-repo variant lives in
// promote_tag.go (handleListPromotions); this route exists so the MCP
// list_promotions tool can omit org/repo for a platform-wide view.
func (h *Handler) handleListPromotionsTenantWide(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.meta.ListPromotions(r.Context(), &metadatav1.ListPromotionsRequest{
		TenantId: tenantID,
		// Org + Repo deliberately empty → tenant-wide query.
		Limit: 50,
	})
	if err != nil {
		slog.Error("ListPromotions tenant-wide", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list promotions")
		return
	}

	out := make([]promotionResponse, 0, len(resp.GetPromotions()))
	for _, p := range resp.GetPromotions() {
		out = append(out, toPromotionResponse(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"promotions": out})
}
