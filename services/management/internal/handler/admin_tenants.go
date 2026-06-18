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
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// AdminTenantResponse is the JSON representation of one tenant.
type AdminTenantResponse struct {
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
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
func (h *Handler) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return false
	}
	if !hasScopedRole(h.getUserAssignments(r), "org", "*", "admin") {
		writeError(w, http.StatusForbidden, "platform-admin role required (org=*, admin)")
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
	writeJSON(w, http.StatusOK, tenantToAdminResp(t))
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
