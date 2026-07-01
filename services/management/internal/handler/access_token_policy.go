// Package handler — access_token_policy.go
//
// FUT-003 Task 10 — BFF admin routes for per-tenant token policy (max TTL,
// rotation interval, idle-revoke). All three limits are stored as
// google.protobuf.Int32Value so "unset" (null on the wire) is distinguishable
// from an explicit zero, and PUT can partial-update.
//
// 2 routes, both authMW-gated and tenant-admin gated:
//
//	GET /api/v1/access/token-policy → auth.GetTokenPolicy
//	PUT /api/v1/access/token-policy → auth.PutTokenPolicy
//
// Isolation rules (CLAUDE.md §9):
//   - tenant_id ALWAYS comes from JWT claims (middleware.TenantIDFromContext),
//     never from a request body field.
//   - actor_id (the caller's user id) ALWAYS comes from the JWT sub via
//     middleware.UserIDFromContext, never from a request body field. It gets
//     recorded in updated_by_user_id + in the auth.token_policy.changed
//     audit event on the auth service.
//
// Validation lives entirely on the auth service:
//   - each limit must be 0 < N <= 3650
//   - idle_revoke_days must be >= 7
//
// InvalidArgument errors surface here as 400 with the auth service's original
// message.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// ── Wire shapes ───────────────────────────────────────────────────────

// TokenPolicyResponse is the JSON representation of one auth.v1.TokenPolicy
// row. The three limits are *int32 so the JSON serialiser emits `null` for
// unset values — the FE needs to distinguish "policy row exists but this
// limit is not set" (null) from an explicit zero (which is not a legal value
// anyway, but the wire distinction still matters for consistency).
type TokenPolicyResponse struct {
	TenantID             string    `json:"tenant_id"`
	MaxTTLDays           *int32    `json:"max_ttl_days"`
	RotationIntervalDays *int32    `json:"rotation_interval_days"`
	IdleRevokeDays       *int32    `json:"idle_revoke_days"`
	UpdatedAt            time.Time `json:"updated_at"`
	UpdatedByUserID      string    `json:"updated_by_user_id"`
}

// PutTokenPolicyRequestBody is the JSON body accepted by PUT. All three
// fields are optional and nullable — an omitted or explicitly-null field
// means "preserve existing value" (partial update semantics; the auth
// repository uses COALESCE in its UPSERT). tenant_id + actor_id are
// intentionally absent — they come from the JWT.
type PutTokenPolicyRequestBody struct {
	MaxTTLDays           *int32 `json:"max_ttl_days,omitempty"`
	RotationIntervalDays *int32 `json:"rotation_interval_days,omitempty"`
	IdleRevokeDays       *int32 `json:"idle_revoke_days,omitempty"`
}

// ── Handlers ──────────────────────────────────────────────────────────

// handleGetTokenPolicy returns the current policy row for the caller's
// tenant. A tenant with no configured policy still returns 200 with all
// limit fields null.
func (h *Handler) handleGetTokenPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	policy, err := h.auth.GetTokenPolicy(r.Context(), &authv1.GetTokenPolicyRequest{
		TenantId: tenantID,
	})
	if err != nil {
		mapTokenPolicyGRPCError(w, "get token policy", err)
		return
	}
	writeJSON(w, http.StatusOK, toTokenPolicyResponse(policy))
}

// handlePutTokenPolicy applies the partial update to the caller's tenant.
// Nil/absent JSON fields translate to nil *wrapperspb.Int32Value on the
// wire, which the auth repository interprets as "preserve existing" via
// COALESCE. The actor id is plumbed from the JWT sub so audit records the
// admin who made the change.
func (h *Handler) handlePutTokenPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.isTenantAdminOrPlatformAdmin(r) {
		writeError(w, http.StatusForbidden, "tenant-admin role required")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	actorID := middleware.UserIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body PutTokenPolicyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	policy, err := h.auth.PutTokenPolicy(r.Context(), &authv1.PutTokenPolicyRequest{
		TenantId:             tenantID,
		MaxTtlDays:           intPtrToWrapper(body.MaxTTLDays),
		RotationIntervalDays: intPtrToWrapper(body.RotationIntervalDays),
		IdleRevokeDays:       intPtrToWrapper(body.IdleRevokeDays),
		ActorId:              actorID,
	})
	if err != nil {
		mapTokenPolicyGRPCError(w, "put token policy", err)
		return
	}
	writeJSON(w, http.StatusOK, toTokenPolicyResponse(policy))
}

// ── Helpers ───────────────────────────────────────────────────────────

// intPtrToWrapper lifts a nullable *int32 (JSON body) into an Int32Value
// wrapper. A nil input yields a nil wrapper — the auth repository's UPSERT
// treats nil as "preserve existing value" via COALESCE. Any non-nil value
// (including zero) is passed through verbatim so the auth service can run
// its 0 < N <= 3650 validation.
func intPtrToWrapper(v *int32) *wrapperspb.Int32Value {
	if v == nil {
		return nil
	}
	return wrapperspb.Int32(*v)
}

// wrapperToIntPtr lowers an Int32Value wrapper to a nullable *int32 for the
// JSON response shape. A nil wrapper (limit not set on this row) yields a
// nil pointer so the JSON serialiser emits `null`.
func wrapperToIntPtr(v *wrapperspb.Int32Value) *int32 {
	if v == nil {
		return nil
	}
	x := v.GetValue()
	return &x
}

// toTokenPolicyResponse converts the proto shape into the JSON wire shape.
// A tenant with no persisted policy row still returns a valid response —
// the auth service returns an empty TokenPolicy with tenant_id set, and the
// three *int32 fields become nil (JSON null).
func toTokenPolicyResponse(p *authv1.TokenPolicy) TokenPolicyResponse {
	out := TokenPolicyResponse{
		TenantID:             p.GetTenantId(),
		MaxTTLDays:           wrapperToIntPtr(p.GetMaxTtlDays()),
		RotationIntervalDays: wrapperToIntPtr(p.GetRotationIntervalDays()),
		IdleRevokeDays:       wrapperToIntPtr(p.GetIdleRevokeDays()),
		UpdatedByUserID:      p.GetUpdatedByUserId(),
	}
	if ts := p.GetUpdatedAt(); ts != nil {
		out.UpdatedAt = ts.AsTime()
	}
	return out
}

// mapTokenPolicyGRPCError translates the typed gRPC codes the auth service
// returns into HTTP statuses. Mirrors mapOIDCTrustGRPCError (from
// access_oidc_trust.go) so operators debugging failed requests get
// consistent language.
//
// The auth service returns codes.InvalidArgument for every validation
// failure (limit outside 0<N<=3650, idle_revoke_days < 7, etc.) — those
// surface as 400 with the auth service's original message.
func mapTokenPolicyGRPCError(w http.ResponseWriter, op string, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, st.Message())
			return
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "token policy not found")
			return
		case codes.PermissionDenied:
			writeError(w, http.StatusForbidden, st.Message())
			return
		case codes.FailedPrecondition:
			writeError(w, http.StatusPreconditionFailed, st.Message())
			return
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		writeError(w, http.StatusServiceUnavailable, "auth service unavailable")
		return
	}
	slog.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "failed to "+op)
}
