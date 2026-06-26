// Package handler — super-admin tenant CRUD endpoints.
//
// These routes wrap registry-tenant gRPC behind a REST shim consumable from
// the React dashboard. Every endpoint is gated by TWO checks:
//
//  1. h.tenant is non-nil (TENANT_GRPC_ADDR was set at startup) — otherwise
//     return 404 "route disabled". Same opt-in pattern as handleSetTenantQuota.
//
//  2. The caller holds the platform-admin marker grant —
//     hasScopedRole(_, "org", "*", "admin"). The literal "*" string can never
//     collide with a real org name (validateOrgName rejects it) so this is
//     unambiguous (PENTEST-024).
//
// Successful create / delete publishes a tenant.created / tenant.deleted event
// to RabbitMQ so registry-audit records the change without a direct gRPC
// dependency.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// tenantNameRE mirrors the validator the registry-tenant gRPC server uses to
// gate CreateTenant and UpdateTenant (lowercase, 2–64 chars, [a-z0-9-]). We
// run it here as well so empty/malformed names fail fast at the REST layer
// with a 400 instead of reaching gRPC just to bounce back InvalidArgument.
var tenantNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// adminTenantPlans is the allowlist of plan values accepted by FE-API-029.
// Kept identical to registry-tenant's validTenantPlan; drift here would
// produce confusing 500-after-200 behaviour where the BFF lets a value
// through that the gRPC layer rejects.
var adminTenantPlans = map[string]struct{}{
	"free":       {},
	"pro":        {},
	"enterprise": {},
}

// AdminTenantResponse is the JSON representation of one tenant returned by
// the list endpoint. The detail endpoint returns AdminTenantDetailResponse,
// which is a strict superset — the embed avoids field drift between the two.
type AdminTenantResponse struct {
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

// AdminTenantDetailResponse extends AdminTenantResponse with usage aggregates
// (FE-API-028). Composed from four gRPC calls — tenant + metadata + auth +
// audit — so the platform-admin dashboard can render the tenant-detail card
// without fanning out across services itself.
//
// last_push_at is a pointer so it serialises to JSON `null` when the tenant
// has never recorded a push. A non-nil pointer with the zero time would round
// trip as "0001-01-01T00:00:00Z" and force the frontend to guard against
// epoch-vs-null specifically, which is a footgun.
type AdminTenantDetailResponse struct {
	AdminTenantResponse
	// Slug and Host mirror the workspace endpoint (FE-API-007) so
	// the admin card can show how the tenant reaches the registry without an
	// extra round trip. Host is always the wildcard subdomain after
	// REDESIGN-001 RM-001 removed the per-tenant custom-domain feature.
	Slug string `json:"slug"`
	Host string `json:"host"`

	StorageUsedBytes  int64 `json:"storage_used_bytes"`
	StorageQuotaBytes int64 `json:"storage_quota_bytes"`
	RepositoryCount   int64 `json:"repository_count"`
	OrganizationCount int64 `json:"organization_count"`
	UserCount         int64 `json:"user_count"`

	LastPushAt *time.Time `json:"last_push_at"`
}

func tenantToAdminResp(t *tenantv1.Tenant) AdminTenantResponse {
	return AdminTenantResponse{
		TenantID:  t.GetTenantId(),
		Name:      t.GetName(),
		Plan:      t.GetPlan(),
		CreatedAt: t.GetCreatedAt().AsTime(),
	}
}

// ── shared gate ──────────────────────────────────────────────────────────────

// requirePlatformAdmin enforces both prerequisites for every admin-tenant
// route. Returns false (and writes the response) when the caller is denied,
// in which case the handler must return immediately.
//
// REDESIGN-001 Phase 5.1: the platform-admin check now delegates to
// h.effectiveGlobalAdmin which reads users.is_global_admin (the typed
// primitive) instead of the (admin, org, '*') legacy marker string.
func (h *Handler) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return false
	}
	if !h.effectiveGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	return true
}

// ── routes ───────────────────────────────────────────────────────────────────

// GET /api/v1/admin/tenants
type adminListTenantsResponse struct {
	Tenants       []AdminTenantResponse `json:"tenants"`
	NextPageToken string                `json:"next_page_token,omitempty"`
}

func (h *Handler) handleAdminListTenants(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}

	pageSize := int32(0)
	if s := r.URL.Query().Get("page_size"); s != "" {
		// Parse but cap at the server-enforced 200 in the tenant service.
		var n int32
		_, _ = fmtSscan(s, &n)
		pageSize = n
	}
	pageToken := r.URL.Query().Get("page_token")

	resp, err := h.tenant.ListTenants(r.Context(), &tenantv1.ListTenantsRequest{
		PageSize:  pageSize,
		PageToken: pageToken,
	})
	if err != nil {
		slog.Error("admin: ListTenants", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}

	out := make([]AdminTenantResponse, 0, len(resp.GetTenants()))
	for _, t := range resp.GetTenants() {
		out = append(out, tenantToAdminResp(t))
	}
	writeJSON(w, http.StatusOK, adminListTenantsResponse{
		Tenants:       out,
		NextPageToken: resp.GetNextPageToken(),
	})
}

// POST /api/v1/admin/tenants
type adminCreateTenantBody struct {
	Name string `json:"name"`
	Plan string `json:"plan"`
}

func (h *Handler) handleAdminCreateTenant(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body adminCreateTenantBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	t, err := h.tenant.CreateTenant(r.Context(), &tenantv1.CreateTenantRequest{
		Name: body.Name,
		Plan: body.Plan,
	})
	if err != nil {
		// Surface AlreadyExists / InvalidArgument distinctly so the UI can
		// show a friendly "name already taken" message; everything else maps
		// to 500 with a generic body so we don't leak driver detail.
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.AlreadyExists:
				writeError(w, http.StatusConflict, "tenant name already in use")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, st.Message())
				return
			}
		}
		slog.Error("admin: CreateTenant", "err", err, "name", body.Name)
		writeError(w, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	// Best-effort audit. RabbitMQ publish failures must not block the response
	// because the create already committed; registry-audit's RabbitMQ consumer
	// is the same path that processes every other platform event.
	h.publishTenantEvent(r, events.RoutingTenantCreated, t.GetTenantId(), body.Name)

	writeJSON(w, http.StatusCreated, tenantToAdminResp(t))
}

// GET /api/v1/admin/tenants/{tenantID}
//
// FE-API-028 composition: registry-tenant gives identity (name/plan/slug/host),
// registry-metadata gives storage + repo + org counts, registry-auth gives
// user count, registry-audit gives the last push timestamp. Failures from the
// usage probes degrade to zero values rather than 500 — the platform admin
// still needs to see the tenant's identity and act on it even if one of the
// downstream services is briefly unavailable.
func (h *Handler) handleAdminGetTenant(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	tenantID := r.PathValue("tenantID")
	if _, err := uuid.Parse(tenantID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	t, err := h.tenant.GetTenant(r.Context(), &tenantv1.GetTenantRequest{TenantId: tenantID})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		slog.Error("admin: GetTenant", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to fetch tenant")
		return
	}

	writeJSON(w, http.StatusOK, h.buildAdminTenantDetail(r, t))
}

// buildAdminTenantDetail performs the FE-API-028 four-way composition. Pulled
// out of the GET handler so handleAdminUpdateTenant can reuse the same shape
// after a successful patch.
//
// Each downstream call is best-effort with logged warnings — the response is
// still meaningful when some of the counts are zero (e.g. a freshly created
// tenant has no metadata row yet, no users yet, no pushes yet).
func (h *Handler) buildAdminTenantDetail(r *http.Request, t *tenantv1.Tenant) AdminTenantDetailResponse {
	tenantID := t.GetTenantId()

	resp := AdminTenantDetailResponse{
		AdminTenantResponse: tenantToAdminResp(t),
		Slug:                t.GetSlug(),
		Host:                t.GetHost(),
	}

	if usage, err := h.meta.GetTenantUsage(r.Context(), &metadatav1.GetTenantUsageRequest{
		TenantId: tenantID,
	}); err == nil {
		resp.StorageUsedBytes = usage.GetStorageUsedBytes()
		resp.StorageQuotaBytes = usage.GetStorageQuotaBytes()
		resp.RepositoryCount = usage.GetRepositoryCount()
		resp.OrganizationCount = usage.GetOrganizationCount()
	} else {
		slog.WarnContext(r.Context(), "admin: GetTenantUsage", "err", err, "tenant_id", tenantID)
	}

	if uc, err := h.auth.CountTenantUsers(r.Context(), &authv1.CountTenantUsersRequest{
		TenantId: tenantID,
	}); err == nil {
		resp.UserCount = uc.GetCount()
	} else {
		slog.WarnContext(r.Context(), "admin: CountTenantUsers", "err", err, "tenant_id", tenantID)
	}

	if lp, err := h.audit.GetLastTenantPush(r.Context(), &auditv1.GetLastTenantPushRequest{
		TenantId: tenantID,
	}); err == nil {
		if ts := lp.GetLastPushAt(); ts != nil {
			at := ts.AsTime()
			resp.LastPushAt = &at
		}
	} else {
		slog.WarnContext(r.Context(), "admin: GetLastTenantPush", "err", err, "tenant_id", tenantID)
	}

	return resp
}

// PATCH /api/v1/admin/tenants/{tenantID}
//
// FE-API-029. Accepts optional `name` and `plan` fields; at least one must be
// present. Name changes also recompute the slug (atomic inside the tenant
// repo). Successful patches publish tenant.renamed and/or tenant.plan_changed
// events so registry-audit records the change.
func (h *Handler) handleAdminUpdateTenant(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	tenantID := r.PathValue("tenantID")
	if _, err := uuid.Parse(tenantID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	// Use pointers so the handler can tell "field absent" from "field present
	// with empty string". Same trick the auth /users/me PATCH uses.
	var body struct {
		Name *string `json:"name"`
		Plan *string `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == nil && body.Plan == nil {
		writeError(w, http.StatusBadRequest, "at least one of name or plan is required")
		return
	}

	// Validate at the BFF layer too. Returning a precise 400 from here saves
	// a gRPC round-trip and keeps the JSON error message stable.
	if body.Name != nil {
		if !tenantNameRE.MatchString(*body.Name) {
			writeError(w, http.StatusBadRequest, "name must match ^[a-z0-9][a-z0-9-]{1,63}$")
			return
		}
	}
	if body.Plan != nil {
		if _, ok := adminTenantPlans[*body.Plan]; !ok {
			writeError(w, http.StatusBadRequest, "plan must be one of free, pro, enterprise")
			return
		}
	}

	// Build the gRPC request — the optional fields propagate via pointer.
	req := &tenantv1.UpdateTenantRequest{TenantId: tenantID}
	req.Name = body.Name
	req.Plan = body.Plan

	t, err := h.tenant.UpdateTenant(r.Context(), req)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "tenant not found")
				return
			case codes.AlreadyExists:
				writeError(w, http.StatusConflict, "tenant name already in use")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, st.Message())
				return
			}
		}
		slog.Error("admin: UpdateTenant", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to update tenant")
		return
	}

	// Publish per-mutation events so audit logs distinguish "renamed" from
	// "plan changed" — useful when both are touched in one PATCH.
	if body.Name != nil {
		h.publishTenantEvent(r, events.RoutingTenantRenamed, t.GetTenantId(), t.GetName())
	}
	if body.Plan != nil {
		h.publishTenantPlanChanged(r, t.GetTenantId(), *body.Plan)
	}

	writeJSON(w, http.StatusOK, h.buildAdminTenantDetail(r, t))
}

// publishTenantPlanChanged emits a tenant.plan_changed RabbitMQ event so
// registry-audit can record the plan transition. Failures are logged, not
// returned — see publishTenantEvent for the same rationale.
func (h *Handler) publishTenantPlanChanged(r *http.Request, tenantID, newPlan string) {
	if h.pub == nil {
		return
	}
	actor := middleware.UserIDFromContext(r.Context())
	payload, err := json.Marshal(map[string]string{
		"tenant_id": tenantID,
		"plan":      newPlan,
		"actor_id":  actor,
	})
	if err != nil {
		slog.WarnContext(r.Context(), "admin: marshal tenant.plan_changed payload", "err", err)
		return
	}
	evt := events.Event{
		ID:         uuid.NewString(),
		Type:       events.RoutingTenantPlanChanged,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(r.Context(), events.RoutingTenantPlanChanged, evt); err != nil {
		slog.WarnContext(r.Context(), "admin: publish tenant.plan_changed", "err", err, "tenant_id", tenantID)
	}
}

// DELETE /api/v1/admin/tenants/{tenantID}
func (h *Handler) handleAdminDeleteTenant(w http.ResponseWriter, r *http.Request) {
	if !h.requirePlatformAdmin(w, r) {
		return
	}
	tenantID := r.PathValue("tenantID")
	if _, err := uuid.Parse(tenantID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}

	if _, err := h.tenant.DeleteTenant(r.Context(), &tenantv1.DeleteTenantRequest{TenantId: tenantID}); err != nil {
		slog.Error("admin: DeleteTenant", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to delete tenant")
		return
	}

	h.publishTenantEvent(r, events.RoutingTenantDeleted, tenantID, "")
	w.WriteHeader(http.StatusNoContent)
}

// publishTenantEvent emits a tenant.created / tenant.deleted RabbitMQ event so
// registry-audit can record the change. Failures are logged, not returned —
// the DB write already committed, so failing the response would leave the
// caller confused about whether the action took effect.
func (h *Handler) publishTenantEvent(r *http.Request, routingKey, tenantID, name string) {
	if h.pub == nil {
		return
	}
	actor := middleware.UserIDFromContext(r.Context())
	payload, err := json.Marshal(map[string]string{
		"tenant_id": tenantID,
		"name":      name,
		"actor_id":  actor,
	})
	if err != nil {
		slog.WarnContext(r.Context(), "admin: marshal tenant event payload", "err", err)
		return
	}
	evt := events.Event{
		ID:         uuid.NewString(),
		Type:       routingKey,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(r.Context(), routingKey, evt); err != nil {
		slog.WarnContext(r.Context(), "admin: publish tenant event", "err", err, "tenant_id", tenantID)
	}
}

// fmtSscan is a tiny wrapper around fmt.Sscan so the body of handleAdminListTenants
// stays free of a top-level fmt import (the rest of this file doesn't need fmt).
// Returning errors is intentional but unused here — pagination treats malformed
// page_size as "use server default".
func fmtSscan(s string, v *int32) (int, error) {
	// Inline the standard library call rather than a real wrapper so this file
	// remains self-contained; importing fmt for one Sscan elsewhere is fine.
	n, err := fmtSscanInt32(s)
	*v = int32(n)
	return 1, err
}

func fmtSscanInt32(s string) (int32, error) {
	var n int32
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return n, errors.New("not a number")
		}
		n = n*10 + int32(c-'0')
		if n > 1_000_000 {
			return n, errors.New("too large")
		}
	}
	return n, nil
}
