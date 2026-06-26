// Package handler — workspace custom-domain CRUD (FE-API-027).
//
// These five routes translate REST calls into TenantService gRPC for tenant
// admins who manage their own custom domains. Authentication is the standard
// RequireAuth middleware; authorization requires admin/owner on any org-scoped
// grant in the active tenant (mirrors requireWebhookAdmin from webhooks.go).
//
// The verification token rides along on the JSON responses (registration AND
// list) so the dashboard can re-display the TXT challenge after the register
// dialog has been dismissed — see DSGN-021 in the design review. Disclosure
// is bounded by the admin/owner gate; any caller who can read the list can
// already mint a fresh token by re-registering, so re-surfacing the existing
// one adds no privilege.
//
// Verify-now strategy: option (a) — synchronous DNS check via
// TenantService.VerifyDomainNow. The worker continues to poll on its own
// cadence; the inline check just shortcuts the "I just published the record"
// path so the user sees the green check immediately.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// reDomain matches lowercase RFC 1123 hostnames per the FE-API-027 spec:
// at least two labels, no underscores, no trailing dot, no IP literals.
// Length cap of 253 chars is enforced separately so the regex stays small.
var reDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// maxDomainLen is the RFC 1035 length cap. Enforced in validateDomain to
// short-circuit pathological inputs before the regex backtracks.
const maxDomainLen = 253

// platformWildcardSuffix is the env-derived dot-prefixed suffix used to reject
// custom registrations under the wildcard zone (e.g. ".registry.example.com").
// Populated via Handler.WithPlatformBaseDomain; empty disables the check
// (useful in tests so the wildcard guard isn't a surprise).
//
// We keep this as Handler state rather than a package global so multi-tenant
// test runs don't bleed config between environments.
//
// The same guard lives inside registry-tenant (defense in depth). The BFF
// version exists to fail fast with a 400 before a wasted gRPC round trip.

// validateDomain returns nil when s is a syntactically valid registerable
// hostname. The error message is generic so it can be passed through to the
// HTTP response without leaking validator internals.
func validateDomain(s string) error {
	if len(s) == 0 || len(s) > maxDomainLen {
		return errInvalidDomain
	}
	if !reDomain.MatchString(s) {
		return errInvalidDomain
	}
	return nil
}

// errInvalidDomain is the singular validation error for the domain field —
// returning the same value keeps comparisons in tests cheap.
var errInvalidDomain = &domainErr{msg: "invalid domain"}

type domainErr struct{ msg string }

func (e *domainErr) Error() string { return e.msg }

// WorkspaceDomainResponse is the FE-API-027 JSON shape — a wider DomainEntry
// that includes scheduling + notification state.
//
// `verification_token` + `txt_record_name` are surfaced on unverified rows so
// the dashboard can re-display the TXT challenge after the register dialog has
// been closed (DSGN-021). Disclosure is bounded by the same admin/owner gate
// that protects the register route — any caller who can read this list can
// already re-register and mint a fresh token, so re-surfacing the current one
// adds no new privilege escalation surface. Once `verified` flips to true the
// token loses operational meaning; we keep returning it for symmetry rather
// than carving a verified-only branch in the marshaller.
type WorkspaceDomainResponse struct {
	Domain            string     `json:"domain"`
	Verified          bool       `json:"verified"`
	IsPrimary         bool       `json:"is_primary"`
	RegisteredAt      time.Time  `json:"registered_at"`
	VerifiedAt        *time.Time `json:"verified_at"`
	NextPollAfter     *time.Time `json:"next_poll_after"`
	Notified24h       bool       `json:"notified_24h"`
	Notified48h       bool       `json:"notified_48h"`
	VerificationToken string     `json:"verification_token,omitempty"`
	TXTRecordName     string     `json:"txt_record_name,omitempty"`
}

// registerDomainResponse is the POST /workspace/me/domains body. It expands
// the bare token with friendly instructions + the fully-qualified TXT record
// name so the user can copy-paste straight into their DNS provider's UI.
type registerDomainResponse struct {
	Domain            string `json:"domain"`
	VerificationToken string `json:"verification_token"`
	TXTRecordName     string `json:"txt_record_name"`
	Instructions      string `json:"instructions"`
}

// registerDomainBody mirrors the public schema. Only `domain` is read; any
// extra fields are ignored by the decoder.
type registerDomainBody struct {
	Domain string `json:"domain"`
}

// setPrimaryBody is the PATCH body. `is_primary:true` is the only supported
// mutation today — `false` returns 400 so the caller learns to use DELETE +
// re-register to clear primary state.
type setPrimaryBody struct {
	IsPrimary *bool `json:"is_primary"`
}

// requireDomainAdmin gates every domain mutation route.
//
// Custom domain registration is a tenant-wide operation — a domain bound to
// the tenant resolves traffic for ALL orgs within it. An org-A admin must NOT
// be able to register or delete custom domains that affect org-B's routing
// (Review §A1, Top-5 #2 fix).
//
// Note: domains are slated for removal (Phase 0 RM-001, Phase 2.1). Until
// the routes are removed this gate must remain correct.
//
// Valid callers:
//   - Platform-admin marker (admin, org, "*")
//   - Tenant-scoped admin (admin, tenant, <tenant_id>)
func (h *Handler) requireDomainAdmin(r *http.Request) bool {
	tenantID := middleware.TenantIDFromContext(r.Context())
	return effectiveTenantAdmin(h.getUserAssignments(r), tenantID)
}

// RegisterWorkspaceDomains mounts the FE-API-027 routes onto mux. Called from
// Handler.Register. All routes return 404 when the TenantService client is
// unwired — same opt-in pattern as WebhookClient/SignerClient.
func (h *Handler) RegisterWorkspaceDomains(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/workspace/me/domains", authMW(http.HandlerFunc(h.handleListWorkspaceDomains)))
	mux.Handle("POST /api/v1/workspace/me/domains", authMW(http.HandlerFunc(h.handleRegisterWorkspaceDomain)))
	mux.Handle("POST /api/v1/workspace/me/domains/{domain}/verify", authMW(http.HandlerFunc(h.handleVerifyWorkspaceDomain)))
	mux.Handle("PATCH /api/v1/workspace/me/domains/{domain}", authMW(http.HandlerFunc(h.handlePatchWorkspaceDomain)))
	mux.Handle("DELETE /api/v1/workspace/me/domains/{domain}", authMW(http.HandlerFunc(h.handleDeleteWorkspaceDomain)))
}

// ---------------------------------------------------------------------------
// GET /api/v1/workspace/me/domains
// ---------------------------------------------------------------------------

// handleListWorkspaceDomains returns every registered domain for the caller's
// tenant. Read is gated by the same admin check as the mutations — the list
// includes notification timestamps and the next-poll cursor, both of which
// leak operational state we don't want a tenant reader to see (FE-API-027).
func (h *Handler) handleListWorkspaceDomains(w http.ResponseWriter, r *http.Request) {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	resp, err := h.tenant.ListTenantDomains(r.Context(), &tenantv1.ListTenantDomainsRequest{
		TenantId: tenantID,
	})
	if err != nil {
		mapDomainGRPCError(w, err, "list domains")
		return
	}

	out := make([]WorkspaceDomainResponse, 0, len(resp.GetDomains()))
	for _, d := range resp.GetDomains() {
		out = append(out, domainEntryToResponse(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": out})
}

// ---------------------------------------------------------------------------
// POST /api/v1/workspace/me/domains
// ---------------------------------------------------------------------------

// handleRegisterWorkspaceDomain validates the input, calls RegisterDomain on
// the tenant service, and wraps the returned token with copy-pasteable DNS
// instructions. The wildcard guard is enforced again on the BFF side so
// callers see a 400 immediately rather than a generic gRPC InvalidArgument.
func (h *Handler) handleRegisterWorkspaceDomain(w http.ResponseWriter, r *http.Request) {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body registerDomainBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	d := strings.ToLower(strings.TrimSpace(body.Domain))
	if err := validateDomain(d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	resp, err := h.tenant.RegisterDomain(r.Context(), &tenantv1.RegisterDomainRequest{
		TenantId: tenantID,
		Domain:   d,
	})
	if err != nil {
		mapDomainGRPCError(w, err, "register domain")
		return
	}
	writeJSON(w, http.StatusCreated, registerDomainResponse{
		Domain:            d,
		VerificationToken: resp.GetVerificationToken(),
		TXTRecordName:     "_registry-verify." + d,
		Instructions:      "Add the TXT record above; verification polls every 5–20m (24h notify, 48h cutoff).",
	})
}

// ---------------------------------------------------------------------------
// POST /api/v1/workspace/me/domains/{domain}/verify
// ---------------------------------------------------------------------------

// handleVerifyWorkspaceDomain triggers an immediate DNS check. Returns the
// updated DomainEntry so the dashboard can flip the "verified" badge without
// re-listing. Option (a) of the FE-API-027 spec.
func (h *Handler) handleVerifyWorkspaceDomain(w http.ResponseWriter, r *http.Request) {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	d := strings.ToLower(r.PathValue("domain"))
	if err := validateDomain(d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	entry, err := h.tenant.VerifyDomainNow(r.Context(), &tenantv1.VerifyDomainNowRequest{
		TenantId: tenantID,
		Domain:   d,
	})
	if err != nil {
		mapDomainGRPCError(w, err, "verify domain")
		return
	}
	writeJSON(w, http.StatusOK, domainEntryToResponse(entry))
}

// ---------------------------------------------------------------------------
// PATCH /api/v1/workspace/me/domains/{domain}
// ---------------------------------------------------------------------------

// handlePatchWorkspaceDomain currently only supports `{"is_primary": true}`.
// The atomic demote-then-promote happens in the tenant repository inside a
// single transaction so we never observe two primaries (or none).
func (h *Handler) handlePatchWorkspaceDomain(w http.ResponseWriter, r *http.Request) {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	d := strings.ToLower(r.PathValue("domain"))
	if err := validateDomain(d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body setPrimaryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.IsPrimary == nil || !*body.IsPrimary {
		// `is_primary: false` would orphan the tenant's host (the wildcard
		// fallback then wins) — refuse so the operator instead deletes the
		// row (or promotes another verified domain). Documenting the
		// constraint in the error string so the dashboard can surface it.
		writeError(w, http.StatusBadRequest, "is_primary must be true; delete the domain to clear primary")
		return
	}

	entry, err := h.tenant.SetPrimaryDomain(r.Context(), &tenantv1.SetPrimaryDomainRequest{
		TenantId: tenantID,
		Domain:   d,
	})
	if err != nil {
		mapDomainGRPCError(w, err, "set primary domain")
		return
	}
	writeJSON(w, http.StatusOK, domainEntryToResponse(entry))
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/workspace/me/domains/{domain}
// ---------------------------------------------------------------------------

// handleDeleteWorkspaceDomain removes the domain. When the deleted row was
// primary, the tenant gRPC layer sets the `x-janus-was-primary` response
// header; we surface that to API clients as `X-Janus-Warning` so the UI can
// toast about the host losing its custom domain.
func (h *Handler) handleDeleteWorkspaceDomain(w http.ResponseWriter, r *http.Request) {
	if h.tenant == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())

	d := strings.ToLower(r.PathValue("domain"))
	if err := validateDomain(d); err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain")
		return
	}

	var hdr metadata.MD
	if _, err := h.tenant.DeleteDomain(r.Context(),
		&tenantv1.DeleteDomainRequest{TenantId: tenantID, Domain: d},
		grpc.Header(&hdr),
	); err != nil {
		mapDomainGRPCError(w, err, "delete domain")
		return
	}
	if vals := hdr.Get("x-janus-was-primary"); len(vals) > 0 && vals[0] == "true" {
		w.Header().Set("X-Janus-Warning", "primary-domain-removed")
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// domainEntryToResponse converts the gRPC DomainEntry to the public REST
// shape. The verification_token + derived TXT record name are surfaced so the
// dashboard can re-display the challenge after the register dialog has been
// dismissed (DSGN-021). Auth is the same admin/owner gate that protects
// registration — anyone who can read this list can already mint a fresh
// token, so re-surfacing the existing one adds no privilege.
func domainEntryToResponse(d *tenantv1.DomainEntry) WorkspaceDomainResponse {
	out := WorkspaceDomainResponse{
		Domain:            d.GetDomain(),
		Verified:          d.GetVerified(),
		IsPrimary:         d.GetIsPrimary(),
		Notified24h:       d.GetNotified_24H(),
		Notified48h:       d.GetNotified_48H(),
		VerificationToken: d.GetVerificationToken(),
		TXTRecordName:     "_registry-verify." + d.GetDomain(),
	}
	if ts := d.GetRegisteredAt(); ts != nil {
		out.RegisteredAt = ts.AsTime()
	}
	if ts := d.GetVerifiedAt(); ts != nil {
		t := ts.AsTime()
		out.VerifiedAt = &t
	}
	if ts := d.GetNextPollAfter(); ts != nil {
		t := ts.AsTime()
		out.NextPollAfter = &t
	}
	return out
}

// mapDomainGRPCError translates the gRPC status codes the tenant service may
// return for FE-API-027 operations into HTTP responses. The error string is
// always generic so internal validation messages don't leak to the client.
func mapDomainGRPCError(w http.ResponseWriter, err error, opLabel string) {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.InvalidArgument:
		// AlreadyExists rides on the gRPC InvalidArgument when the underlying
		// pgconn 23505 surfaces via MapDBError → ResourceExhausted. We handle
		// AlreadyExists explicitly below; the catch-all here is "your input
		// was malformed", which the BFF maps to a flat 400.
		slog.Warn(opLabel+" invalid argument", "detail", st.Message())
		// Distinguish the wildcard guard so the UI can render a tailored
		// message. The detail string is matched verbatim — registry-tenant
		// owns the exact wording.
		if strings.Contains(st.Message(), "platform-managed wildcard") {
			writeError(w, http.StatusBadRequest, "cannot register domain within the platform-managed wildcard space")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request")
	case codes.NotFound:
		writeError(w, http.StatusNotFound, "domain not found")
	case codes.AlreadyExists:
		// Same-tenant duplicate registration. The tenant service collapses
		// "exists on another tenant" into a generic AlreadyExists too so we
		// don't disclose cross-tenant existence — mirroring webhook behaviour.
		writeError(w, http.StatusConflict, "domain already registered")
	case codes.FailedPrecondition:
		// Unverified target on PATCH, or DNS check failure on POST verify.
		// 409 conveys "we understood you but the resource isn't in the right
		// state". Use the gRPC message text when it's safe (no internal IPs).
		writeError(w, http.StatusConflict, st.Message())
	case codes.PermissionDenied:
		writeError(w, http.StatusForbidden, "permission denied")
	default:
		slog.Error(opLabel, "err", err)
		writeError(w, http.StatusInternalServerError, opLabel+" failed")
	}
}
