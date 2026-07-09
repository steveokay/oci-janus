// Package handler — pr_registry.go
//
// FUT-023 Phase 1 — ephemeral PR-scoped registries (BFF surface). This file
// fronts the metadata PR-registry RPCs with:
//
//	POST /webhooks/scm/github/pr        (UNAUTHENTICATED) → HandlePREvent
//	GET  /api/v1/pr-registry/config     (admin)           → GetPRRegistryConfig
//	PUT  /api/v1/pr-registry/config     (admin)           → PutPRRegistryConfig
//	GET  /api/v1/pr-registry/namespaces (admin)           → ListPRNamespaces
//
// Trust boundaries:
//   - The receiver is deliberately NOT auth-gated. GitHub can't present a JWT,
//     so the only trust boundary is the X-Hub-Signature-256 HMAC, which is
//     verified DOWNSTREAM in metadata over the exact raw request bytes. The BFF
//     therefore forwards the untouched body — it never re-marshals it (a
//     round-trip through encoding/json would change whitespace/ordering and
//     break the HMAC). management resolves no tenant: it passes TenantId="" so
//     metadata's SingleTenantInjector supplies the bootstrap tenant.
//   - The three /api/v1 routes gate on the platform-admin primitive and deny
//     service-account bearers (requirePRRegistryAdmin), mirroring the email
//     transport routes — enabling ephemeral registries is a deployment-wide
//     config change.
//
// Secret handling: the webhook secret is write-only. GetPRRegistryConfig only
// ever returns the has_secret boolean; the PUT path treats an empty
// webhook_secret as "keep existing" and never echoes the value back.
package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// prRegistryConfigJSON is the wire shape for the config GET/PUT responses. It
// mirrors the proto PRRegistryConfig but carries only has_secret for the
// secret (the raw webhook secret is never serialised) and adds a derived
// webhook_url convenience field the admin pastes into GitHub.
type prRegistryConfigJSON struct {
	Enabled          bool   `json:"enabled"`
	HasSecret        bool   `json:"has_secret"`
	PromoteTargetOrg string `json:"promote_target_org"`
	// WebhookURL is the fully-qualified public receiver URL
	// (<public-base>/webhooks/scm/github/pr). Empty when PUBLIC_BASE_URL is
	// unset — the BFF renders empty rather than guessing a scheme/host.
	WebhookURL string `json:"webhook_url"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// prRegistryConfigPutBody is the JSON body for the PUT config route. The
// webhook_secret field follows the keep-existing convention: an empty string
// leaves the sealed secret untouched, a non-empty string re-seals it.
type prRegistryConfigPutBody struct {
	Enabled          bool   `json:"enabled"`
	WebhookSecret    string `json:"webhook_secret"`
	PromoteTargetOrg string `json:"promote_target_org"`
}

// prNamespaceJSON is one row in the PR-namespace inventory.
type prNamespaceJSON struct {
	Provider   string `json:"provider"`
	SourceRepo string `json:"source_repo"`
	PRNumber   int32  `json:"pr_number"`
	OrgName    string `json:"org_name"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at,omitempty"`
	TornDownAt string `json:"torn_down_at,omitempty"`
}

// prNamespacesJSON is the envelope for the namespace-list route. The slice is
// always non-nil so the FE never has to guard a null.
type prNamespacesJSON struct {
	Namespaces    []prNamespaceJSON `json:"namespaces"`
	NextPageToken string            `json:"next_page_token"`
}

// prRegistryWebhookPath is the public path the GitHub-webhook receiver is
// mounted at. Kept as a constant so the config route renders exactly the same
// path the router registers.
const prRegistryWebhookPath = "/webhooks/scm/github/pr"

// webhookReceiverURL derives the fully-qualified receiver URL from the
// configured public base URL. Returns "" when PUBLIC_BASE_URL is unset so the
// admin sees a clean empty field rather than a guessed/broken URL.
func (h *Handler) webhookReceiverURL() string {
	if h.publicBaseURL == "" {
		return ""
	}
	return h.publicBaseURL + prRegistryWebhookPath
}

// prRegistryConfigToJSON maps the proto config to its wire shape, dropping the
// raw secret (only the has_secret marker survives) and appending the derived
// webhook_url.
func (h *Handler) prRegistryConfigToJSON(c *metadatav1.PRRegistryConfig) prRegistryConfigJSON {
	out := prRegistryConfigJSON{
		Enabled:          c.GetEnabled(),
		HasSecret:        c.GetHasSecret(),
		PromoteTargetOrg: c.GetPromoteTargetOrg(),
		WebhookURL:       h.webhookReceiverURL(),
	}
	if t := c.GetUpdatedAt(); t != nil {
		out.UpdatedAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}

// prNamespacesToJSON maps the proto namespace list to its wire shape. The
// slice is pre-allocated (never nil) so an empty inventory serialises as `[]`.
func prNamespacesToJSON(resp *metadatav1.ListPRNamespacesResponse) prNamespacesJSON {
	out := prNamespacesJSON{
		Namespaces:    make([]prNamespaceJSON, 0, len(resp.GetNamespaces())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for _, n := range resp.GetNamespaces() {
		row := prNamespaceJSON{
			Provider:   n.GetProvider(),
			SourceRepo: n.GetSourceRepo(),
			PRNumber:   n.GetPrNumber(),
			OrgName:    n.GetOrgName(),
			Status:     n.GetStatus(),
		}
		if t := n.GetCreatedAt(); t != nil {
			row.CreatedAt = t.AsTime().UTC().Format(time.RFC3339)
		}
		if t := n.GetTornDownAt(); t != nil {
			row.TornDownAt = t.AsTime().UTC().Format(time.RFC3339)
		}
		out.Namespaces = append(out.Namespaces, row)
	}
	return out
}

// requirePRRegistryAdmin gates the config + namespace routes to platform admins
// and blocks service-account bearers. Returns false (and writes the response)
// when the caller is denied — the handler must return immediately. Mirrors
// requireEmailAdmin: enabling ephemeral PR registries is a deployment-wide
// config change, so an SA token whose owner is an admin must not clear the gate
// (Decision #24).
func (h *Handler) requirePRRegistryAdmin(w http.ResponseWriter, r *http.Request) bool {
	if middleware.PrincipalKindFromContext(r.Context()) == middleware.PrincipalKindServiceAccount {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	if !h.effectiveGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// POST /webhooks/scm/github/pr  (UNAUTHENTICATED)
// ---------------------------------------------------------------------------

// handleGitHubPRWebhook is the inbound GitHub PR-webhook receiver. It is NOT
// auth-gated: the HMAC signature (verified downstream in metadata over the
// exact raw bytes) is the sole trust boundary. The handler reads the raw body
// verbatim and forwards it — with the signature + event headers — to
// metadata.HandlePREvent, then maps the outcome to an HTTP status.
//
// Tenant resolution is deliberately delegated to metadata: TenantId is passed
// empty so metadata's SingleTenantInjector supplies the bootstrap tenant. The
// X-GitHub-Delivery header is read for correlation logging only — the body and
// signature are never logged.
func (h *Handler) handleGitHubPRWebhook(w http.ResponseWriter, r *http.Request) {
	// Cap the body — a webhook payload is small; anything larger is abuse.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	// Correlation id for logging only. Never logged alongside the body/sig.
	deliveryID := r.Header.Get("X-GitHub-Delivery")

	// Read the exact raw bytes: metadata computes the HMAC over these, so the
	// body must not be re-marshalled.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// Oversized or truncated body. Bland 400 — never echo internal detail.
		slog.Warn("pr webhook: read body", "delivery_id", deliveryID)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	signature := r.Header.Get("X-Hub-Signature-256")
	event := r.Header.Get("X-GitHub-Event")

	resp, err := h.meta.HandlePREvent(r.Context(), &metadatav1.HandlePREventRequest{
		// TenantId empty on purpose: metadata's SingleTenantInjector fills the
		// x-tenant-id with the bootstrap tenant in single mode.
		TenantId:  "",
		Provider:  "github",
		RawBody:   body,
		Signature: signature,
		Event:     event,
	})
	if err != nil {
		switch status.Code(err) {
		case codes.PermissionDenied:
			// Bad / missing signature. Do not confirm which — bland 401.
			writeError(w, http.StatusUnauthorized, "invalid signature")
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, "invalid request")
		default:
			// Internal failure. Log with the delivery id for correlation; never
			// echo the gRPC status or any internal detail to the caller.
			slog.Error("pr webhook: HandlePREvent", "delivery_id", deliveryID, "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	switch resp.GetOutcome() {
	case metadatav1.HandlePREventResponse_OUTCOME_DISABLED:
		// Feature off / no config. The endpoint's existence must not be a probe
		// oracle, so return a bland 404 rather than a 200 "disabled".
		writeError(w, http.StatusNotFound, "not found")
	case metadatav1.HandlePREventResponse_OUTCOME_IGNORED:
		// Ping, non-PR event, or an unhandled action — nothing to do.
		w.WriteHeader(http.StatusNoContent)
	case metadatav1.HandlePREventResponse_OUTCOME_PROVISIONED,
		metadatav1.HandlePREventResponse_OUTCOME_TORN_DOWN,
		metadatav1.HandlePREventResponse_OUTCOME_PROMOTED_AND_TORN_DOWN:
		writeJSON(w, http.StatusOK, map[string]string{
			"outcome": outcomeLabel(resp.GetOutcome()),
			"org":     resp.GetOrgName(),
		})
	default:
		// OUTCOME_UNSPECIFIED or a future enum value — treat as a no-op ack so
		// GitHub doesn't retry, but log for visibility.
		slog.Warn("pr webhook: unexpected outcome", "delivery_id", deliveryID, "outcome", resp.GetOutcome())
		w.WriteHeader(http.StatusNoContent)
	}
}

// outcomeLabel renders the enum as the stable snake_case string the FE + audit
// trail key on. Only the "action taken" outcomes reach this — IGNORED /
// DISABLED are handled before the JSON body is written.
func outcomeLabel(o metadatav1.HandlePREventResponse_Outcome) string {
	switch o {
	case metadatav1.HandlePREventResponse_OUTCOME_PROVISIONED:
		return "provisioned"
	case metadatav1.HandlePREventResponse_OUTCOME_TORN_DOWN:
		return "torn_down"
	case metadatav1.HandlePREventResponse_OUTCOME_PROMOTED_AND_TORN_DOWN:
		return "promoted_and_torn_down"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/pr-registry/config  (admin)
// ---------------------------------------------------------------------------

// handleGetPRRegistryConfig serves the current PR-registry config. Admin-only;
// the response masks the webhook secret (has_secret only) and includes the
// derived webhook_url the admin pastes into GitHub.
func (h *Handler) handleGetPRRegistryConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requirePRRegistryAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.meta.GetPRRegistryConfig(r.Context(), &metadatav1.GetPRRegistryConfigRequest{
		TenantId: tenantID,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.prRegistryConfigToJSON(resp))
}

// ---------------------------------------------------------------------------
// PUT /api/v1/pr-registry/config  (admin)
// ---------------------------------------------------------------------------

// handlePutPRRegistryConfig upserts the PR-registry config. Admin-only. An
// empty webhook_secret keeps the stored value; updated_by is forced from the
// JWT user id (never trusted from the body). A FailedPrecondition from metadata
// (the KEK is unset, so the secret can't be sealed) maps to 409 so the FE can
// render the "configure the KEK first" empty state rather than an error.
func (h *Handler) handlePutPRRegistryConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requirePRRegistryAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body prRegistryConfigPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Validate the promote target against the org-name allowlist before
	// forwarding (SEC-084). Empty means "no promote target" and is allowed;
	// a non-empty value must be a valid org name (the same gate
	// handleCreateRepository uses) so a bad target is rejected with 400 at the
	// BFF rather than surfacing later at merge-promote time.
	if body.PromoteTargetOrg != "" {
		if err := validateOrgName(body.PromoteTargetOrg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid promote_target_org")
			return
		}
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	resp, err := h.meta.PutPRRegistryConfig(r.Context(), &metadatav1.PutPRRegistryConfigRequest{
		TenantId:         tenantID,
		Enabled:          body.Enabled,
		PromoteTargetOrg: body.PromoteTargetOrg,
		// Secret: empty means "keep existing"; a value re-seals it.
		WebhookSecret: body.WebhookSecret,
		UpdatedBy:     userID,
	})
	if err != nil {
		// KEK unset → FailedPrecondition → 409 (writeGRPCError already maps it).
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.prRegistryConfigToJSON(resp))
}

// ---------------------------------------------------------------------------
// GET /api/v1/pr-registry/namespaces  (admin)
// ---------------------------------------------------------------------------

// handleListPRNamespaces lists the PR-scoped registry namespaces. Admin-only.
// The status query param (default "active") narrows to active / torn_down / all
// namespaces; page_size + page_token drive keyset pagination.
func (h *Handler) handleListPRNamespaces(w http.ResponseWriter, r *http.Request) {
	if !h.requirePRRegistryAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	// status: default "active"; validate against the allowlist before forwarding.
	statusFilter := r.URL.Query().Get("status")
	if statusFilter == "" {
		statusFilter = "active"
	}
	switch statusFilter {
	case "active", "torn_down", "all":
		// ok — "all" is the wire form for "no filter".
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	// Metadata's contract uses "" for "all"; translate.
	if statusFilter == "all" {
		statusFilter = ""
	}

	// page_size: 1–100, default 25.
	pageSize := int32(25)
	if s := r.URL.Query().Get("page_size"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			pageSize = int32(n) //nolint:gosec // bounded above to [1, 100]
		}
	}

	// page_token: validate before forwarding (CLAUDE.md §7 — any user-supplied
	// string must pass an allowlist before reaching a downstream service).
	pageToken := r.URL.Query().Get("page_token")
	if pageToken != "" {
		if err := validatePageToken(pageToken); err != nil {
			writeError(w, http.StatusBadRequest, "invalid page_token")
			return
		}
	}

	resp, err := h.meta.ListPRNamespaces(r.Context(), &metadatav1.ListPRNamespacesRequest{
		TenantId:  tenantID,
		Status:    statusFilter,
		PageSize:  pageSize,
		PageToken: pageToken,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prNamespacesToJSON(resp))
}
