// Package handler — access_review.go
//
// FUT-004 Task 8 — BFF routes for the FE /api-keys/review surface. Two
// routes both authMW-gated, with the RBAC gate applied inside each handler
// so we can express "workspace-admin OR key-owner" per row rather than a
// coarse tenant-admin blanket:
//
//	GET  /api/v1/access/review/stale  → auth.ListStaleKeys
//	POST /api/v1/access/review/snooze → auth.SnoozeAPIKeyReview
//
// Isolation rules (CLAUDE.md §9):
//   - tenant_id ALWAYS comes from JWT claims (middleware.TenantIDFromContext),
//     never from a request body field.
//   - actor_id (the caller's user id) ALWAYS comes from the JWT sub via
//     middleware.UserIDFromContext, plumbed into
//     SnoozeAPIKeyReviewRequest.ActorID so the audit event records the
//     human who deferred the review — never accepted from the request body.
//
// Owner-vs-admin gate (per plan §Task 8):
//   - LIST: admins receive the full tenant list; non-admin callers receive
//     the subset where owner_user_id == caller.sub. Non-admins with zero
//     owned stale keys get an empty list (not a 403) — the FE renders the
//     "Nothing to review today" empty state consistently for everyone.
//   - SNOOZE: admins can snooze any key; non-admin callers can only snooze
//     a key they own. The BFF calls auth.ListStaleKeys first to resolve the
//     key's owner_user_id (there is no GetAPIKey RPC yet) and returns 404
//     when the key isn't in the tenant's stale list at all (defence against
//     enumerating other tenants' key ids) or 403 when the key exists but
//     isn't owned by the caller.
//
// Revoke is NOT implemented here — the FE uses the existing
// DELETE /api/v1/api-keys/:id route.
//
// The auth service enforces days ∈ [1, 90] and returns codes.InvalidArgument
// on failure. We revalidate at the BFF for defence-in-depth and to short-
// circuit an obviously bad request before the gRPC round-trip.
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

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// ── Wire shapes ───────────────────────────────────────────────────────

// StaleKeyResponse is the JSON wire shape for one stale-key row. Mirrors
// the proto snake_case + nullable timestamp semantics so the FE hook in
// frontend/src/lib/api/access-review.ts can decode directly.
//
// suggested_action is emitted as a short string ("REVOKE" / "KEEP" /
// "SNOOZE" / "UNSPECIFIED") — the prefix from the proto enum is stripped
// so the FE type union stays terse.
type StaleKeyResponse struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	OwnerUserID        string     `json:"owner_user_id"`
	Name               string     `json:"name"`
	LastUsedAt         *time.Time `json:"last_used_at"`
	RotationDueAt      *time.Time `json:"rotation_due_at"`
	ReviewSnoozedUntil *time.Time `json:"review_snoozed_until"`
	SuggestedAction    string     `json:"suggested_action"`
	Reason             string     `json:"reason"`
}

// ListStaleKeysResponseBody wraps the list under a `keys` key so future
// pagination fields can be added without a breaking wire change.
type ListStaleKeysResponseBody struct {
	Keys []StaleKeyResponse `json:"keys"`
}

// SnoozeAPIKeyReviewRequestBody is the JSON body accepted by POST snooze.
// tenant_id + actor_id are intentionally absent — they come from the JWT.
type SnoozeAPIKeyReviewRequestBody struct {
	KeyID string `json:"key_id"`
	Days  int32  `json:"days"`
}

// snoozeMinDays / snoozeMaxDays mirror the auth service's validation
// bounds (see services/auth/internal/service/access_review.go). The BFF
// revalidates for defence-in-depth AND so an obviously bad request
// short-circuits before the gRPC round-trip.
const (
	snoozeMinDays = 1
	snoozeMaxDays = 90
)

// ── Handlers ──────────────────────────────────────────────────────────

// handleListStaleKeys returns the tenant's stale keys. Admins receive the
// full list; non-admin callers receive only the keys they own (an empty
// list is a legitimate response — the FE renders the "Nothing to review
// today" empty state for everyone).
func (h *Handler) handleListStaleKeys(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())
	isAdmin := h.isTenantAdminOrPlatformAdmin(r)

	resp, err := h.auth.ListStaleKeys(r.Context(), &authv1.ListStaleKeysRequest{
		TenantId: tenantID,
	})
	if err != nil {
		mapAccessReviewGRPCError(w, "list stale keys", err)
		return
	}

	// Admins see the full list; non-admins see only their own keys. The
	// filter runs after the RPC because the auth service's ListStaleKeys
	// is tenant-scoped, not owner-scoped — we do the owner filter in the
	// BFF to keep the RPC surface simple and reusable by future admin UIs.
	keys := resp.GetKeys()
	out := make([]StaleKeyResponse, 0, len(keys))
	for _, k := range keys {
		if !isAdmin && k.GetOwnerUserId() != callerID {
			continue
		}
		out = append(out, toStaleKeyResponse(k))
	}
	writeJSON(w, http.StatusOK, ListStaleKeysResponseBody{Keys: out})
}

// handleSnoozeAPIKeyReview defers the next weekly review nudge for one
// key. Admins can snooze any key; non-admin callers can only snooze a key
// they own — the BFF resolves ownership via ListStaleKeys because there
// is no GetAPIKey RPC on the wire yet.
//
// Validation:
//   - days ∈ [1, 90] rejected at the BFF (400) before the RPC.
//   - Non-admin caller trying to snooze a foreign key: 403.
//   - Any caller referencing a key not in the tenant's stale list: 404
//     (defence against enumerating other tenants' key ids).
func (h *Handler) handleSnoozeAPIKeyReview(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	callerID := middleware.UserIDFromContext(r.Context())
	isAdmin := h.isTenantAdminOrPlatformAdmin(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body SnoozeAPIKeyReviewRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.KeyID == "" {
		writeError(w, http.StatusBadRequest, "key_id is required")
		return
	}
	// Defence in depth on top of the auth-service validation. The BE also
	// enforces this — the double-check exists so an obviously bad request
	// doesn't burn a round-trip AND so the failure mode is a legible 400
	// with an explicit range in the message.
	if body.Days < snoozeMinDays || body.Days > snoozeMaxDays {
		writeError(w, http.StatusBadRequest,
			"days must be in [1, 90]")
		return
	}

	// Non-admin callers must own the key. We look it up via ListStaleKeys
	// rather than a dedicated GetAPIKey RPC (which doesn't exist yet) so
	// the ownership resolution stays tenant-scoped and defence-against-
	// cross-tenant-id-enumeration falls out for free — a key that isn't
	// in the caller's tenant's stale list gets a 404 regardless of who
	// owns it in reality.
	if !isAdmin {
		listResp, err := h.auth.ListStaleKeys(r.Context(), &authv1.ListStaleKeysRequest{
			TenantId: tenantID,
		})
		if err != nil {
			mapAccessReviewGRPCError(w, "resolve key ownership", err)
			return
		}
		owner, found := ownerOfKey(listResp.GetKeys(), body.KeyID)
		if !found {
			// Either the key doesn't exist, exists in another tenant, or
			// exists in this tenant but isn't currently stale. In every
			// case, the caller has no business snoozing it — return 404
			// so an attacker can't distinguish the cases.
			writeError(w, http.StatusNotFound, "api key not found")
			return
		}
		if owner != callerID {
			writeError(w, http.StatusForbidden,
				"only the key owner or a workspace admin can snooze this key")
			return
		}
	}

	updated, err := h.auth.SnoozeAPIKeyReview(r.Context(), &authv1.SnoozeAPIKeyReviewRequest{
		KeyId:   body.KeyID,
		Days:    body.Days,
		ActorId: callerID,
	})
	if err != nil {
		mapAccessReviewGRPCError(w, "snooze api key review", err)
		return
	}
	writeJSON(w, http.StatusOK, toStaleKeyResponse(updated))
}

// ── Helpers ───────────────────────────────────────────────────────────

// ownerOfKey walks the ListStaleKeys result for the target key id and
// returns its owner_user_id when found. Returns ("", false) when the key
// isn't in the list — the caller treats that as a 404 regardless of the
// actual reason (missing / wrong tenant / not stale).
func ownerOfKey(keys []*authv1.StaleKey, keyID string) (string, bool) {
	for _, k := range keys {
		if k.GetId() == keyID {
			return k.GetOwnerUserId(), true
		}
	}
	return "", false
}

// toStaleKeyResponse converts the proto shape into the JSON wire shape.
// Nil timestamps are preserved so the FE can distinguish "never used"
// from a zero epoch. The proto SuggestedAction enum is stringified with
// its "SUGGESTED_ACTION_" prefix stripped to match the FE type union.
func toStaleKeyResponse(k *authv1.StaleKey) StaleKeyResponse {
	if k == nil {
		return StaleKeyResponse{}
	}
	out := StaleKeyResponse{
		ID:              k.GetId(),
		TenantID:        k.GetTenantId(),
		OwnerUserID:     k.GetOwnerUserId(),
		Name:            k.GetName(),
		SuggestedAction: suggestedActionString(k.GetSuggestedAction()),
		Reason:          k.GetReason(),
	}
	if ts := k.GetLastUsedAt(); ts != nil {
		t := ts.AsTime()
		out.LastUsedAt = &t
	}
	if ts := k.GetRotationDueAt(); ts != nil {
		t := ts.AsTime()
		out.RotationDueAt = &t
	}
	if ts := k.GetReviewSnoozedUntil(); ts != nil {
		t := ts.AsTime()
		out.ReviewSnoozedUntil = &t
	}
	return out
}

// suggestedActionString maps the proto enum to the short FE string
// ("REVOKE" / "KEEP" / "SNOOZE" / "UNSPECIFIED"). Kept as a switch (not
// a strings.TrimPrefix on the enum name) so an accidental proto rename
// surfaces here as a compile error rather than a silent wire change.
func suggestedActionString(a authv1.SuggestedAction) string {
	switch a {
	case authv1.SuggestedAction_SUGGESTED_ACTION_REVOKE:
		return "REVOKE"
	case authv1.SuggestedAction_SUGGESTED_ACTION_KEEP:
		return "KEEP"
	case authv1.SuggestedAction_SUGGESTED_ACTION_SNOOZE:
		return "SNOOZE"
	default:
		return "UNSPECIFIED"
	}
}

// mapAccessReviewGRPCError translates typed gRPC codes the auth service
// returns into HTTP statuses. Mirrors mapTokenPolicyGRPCError (from
// access_token_policy.go) so operators debugging failed requests get
// consistent language across the access surfaces.
//
// The auth service returns:
//   - codes.InvalidArgument for validation (days out of range, bad UUID)
//     → 400 with the original message.
//   - codes.NotFound when the key doesn't exist → 404.
//   - codes.Unimplemented when the access-review feature isn't wired at
//     startup → 501 so the FE can render a clear "not available" state
//     rather than a generic 500.
func mapAccessReviewGRPCError(w http.ResponseWriter, op string, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, st.Message())
			return
		case codes.NotFound:
			writeError(w, http.StatusNotFound, "api key not found")
			return
		case codes.PermissionDenied:
			writeError(w, http.StatusForbidden, st.Message())
			return
		case codes.Unimplemented:
			writeError(w, http.StatusNotImplemented, st.Message())
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
