// Package handler — sign_manifest.go
//
// FE-API-026 — POST /api/v1/repositories/{org}/{repo}/tags/{tag}/sign
//
// Lets a repo admin sign the current manifest of a tag from the dashboard.
// The signer service owns key material (Cosign / Notary v2 backed by Vault
// in production); management never touches a private key. We only
// translate "sign this tag with this signer_id" into a SignManifest gRPC
// call, then publish image.signed on success so audit/webhook consumers see
// the event.
//
// The signer service does not currently publish image.signed on its own
// successful sign (audited via grep across services/signer). When that
// changes, drop this publisher call here and let signer own the event
// surface — consumers are keyed on (tenant_id, manifest_digest, signer_id)
// so a one-day overlap of double-publish is safe.
//
// Authorization: repo `admin` (or higher) on this repo OR its parent org,
// the same gate used by handleDeleteRepository — signing leaves a
// permanent record so we treat it as a destructive write.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
	"unicode"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// signManifestBody is the JSON body for POST …/sign.
//
// Two mutually-exclusive ways to name the signing identity (FUT-009):
//
//   - signer_id          — the legacy free-form string. Key material lives
//     in registry-signer's Vault backend keyed by this
//     value. Kept for backward compat and the cosign CLI
//     path (CI bots that already POST a raw signer_id).
//   - service_account_id — a workspace service account chosen from the
//     dashboard Select. When set, the BFF resolves it
//     to the SA's shadow user_id and records THAT as the
//     signature's signer_id, so the signing identity is a
//     managed principal rather than an opaque string.
//
// Exactly one of the two must be provided. Supplying both is rejected 400
// so the caller's intent is unambiguous — we never silently prefer one.
//
// Note (FUT-009): service_account_id carries the SA's *shadow user_id*, not
// the service_accounts.id primary key. registry-auth exposes no gRPC RPC
// that maps an SA primary key → shadow user_id, and FUT-009 explicitly
// forbids a proto change. The shadow user_id is the identifier the BFF can
// both validate (via ListTenantUsers, which returns SA-kind shadow users)
// and record as signer_id (a UUID the tag-detail render resolves back to
// the SA display name). The dashboard Select sends the SA's shadow_user_id
// as this field's value.
type signManifestBody struct {
	SignerID string `json:"signer_id"`
	// ServiceAccountID is the SA shadow user_id chosen from the dashboard
	// Select. Mutually exclusive with SignerID. See the type doc for why
	// this is the shadow user_id and not the SA primary key.
	ServiceAccountID string `json:"service_account_id"`
}

// maxSignerIDLen caps signer_id at 256 chars — long enough for a
// fully-qualified KMS key ARN, short enough that an attacker can't smuggle
// a huge payload through this validated field.
const maxSignerIDLen = 256

// validateSignerID enforces the input-validation contract from CLAUDE.md §7:
// non-empty, max 256 chars, ASCII printable only. ASCII printable rules
// out NUL bytes, control characters, and any non-ASCII text that could
// confuse the signer's key lookup.
func validateSignerID(s string) bool {
	if s == "" || len(s) > maxSignerIDLen {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// handleSignManifest signs the manifest currently tagged by {tag}.
//
// Flow mirrors handleGetSignature for resolution (find repo → GetTag for
// digest) so a typo in the URL surfaces a clean 404 before the
// signer.SignManifest gRPC call is made.
func (h *Handler) handleSignManifest(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		// Same "route disabled" gate as handleGetSignature so a management
		// deployment without registry-signer renders a clean 404 instead
		// of a confusing 5xx when the user hits the sign button.
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName, tagName := r.PathValue("org"), r.PathValue("repo"), r.PathValue("tag")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if err := validateTagName(tagName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag name")
		return
	}

	// PENTEST-002: repo admin (or admin on the parent org) may sign.
	// Signing produces an immutable audit record, so we treat it the same
	// way we treat delete/update — admin-only, not writer.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// Body must be small JSON. MaxBytesReader caps it before json.Decode
	// touches anything, defending against a 1GB JSON bomb.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body signManifestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// FUT-009 — resolve the signing identity. Exactly one of signer_id /
	// service_account_id must be provided. When the SA path is used we
	// resolve + validate the SA server-side and record its shadow user_id
	// as the signer_id; the free-form path is unchanged from FE-API-026.
	signerID, herr := h.resolveSignerIdentity(r.Context(), tenantID, &body)
	if herr != nil {
		writeError(w, herr.status, herr.msg)
		return
	}

	// Resolve the repo + tag → digest. SignManifest is keyed by digest
	// (manifests are immutable; tags are pointers), so this lookup pins
	// the operation to whatever the tag points at *right now*.
	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}
	tag, err := h.meta.GetTag(r.Context(), &metadatav1.GetTagRequest{
		RepoId:   repo.GetRepoId(),
		TenantId: tenantID,
		Name:     tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	signResp, err := h.signer.SignManifest(r.Context(), &signerv1.SignManifestRequest{
		TenantId:       tenantID,
		RepositoryName: org + "/" + repoName,
		ManifestDigest: tag.GetManifestDigest(),
		SignerId:       signerID,
	})
	if err != nil {
		// Map a small set of signer-side errors back to HTTP. The default
		// is 500 so a transport blip doesn't masquerade as a client error.
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.AlreadyExists:
				// signer.SignManifest rejects re-signing the same digest with
				// the same signer_id — surface as 409 so the UI can render
				// "already signed by this key" instead of a generic error.
				writeError(w, http.StatusConflict, "manifest already signed by this signer")
				return
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "manifest not found")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, "signer rejected request")
				return
			}
		}
		slog.Error("SignManifest", "err", err, "digest", tag.GetManifestDigest(), "signer_id", signerID)
		writeError(w, http.StatusInternalServerError, "failed to sign manifest")
		return
	}

	sig := signResp.GetSignature()
	record := signatureRecord{
		SignerID:        sig.GetSignerId(),
		KeyID:           sig.GetKeyId(),
		SignatureDigest: sig.GetSignatureDigest(),
		SignedAt:        sig.GetSignedAt().AsTime(),
	}

	// Publish image.signed so audit + webhook + any future consumer can
	// react to the sign. We publish even when the publisher is nil to
	// keep the test surface deterministic, but only when pub is non-nil
	// (the dev/test path).
	if h.pub != nil {
		callerID := middleware.UserIDFromContext(r.Context())
		payload, _ := json.Marshal(events.ImageSignedPayload{
			TenantID:        tenantID,
			RepositoryName:  org + "/" + repoName,
			Tag:             tagName,
			ManifestDigest:  tag.GetManifestDigest(),
			SignerID:        sig.GetSignerId(),
			KeyID:           sig.GetKeyId(),
			SignatureDigest: sig.GetSignatureDigest(),
			SignedBy:        callerID,
		})
		evt := events.Event{
			ID:         uuid.New().String(),
			Type:       events.RoutingImageSigned,
			TenantID:   tenantID,
			OccurredAt: time.Now(),
			Version:    "1.0",
			Payload:    payload,
		}
		if err := h.pub.Publish(r.Context(), events.RoutingImageSigned, evt); err != nil {
			// A publish failure does not roll back the signature — the
			// signer state is already mutated. Log loudly so an operator
			// can replay the missing event from the signer's audit table.
			slog.Error("publish image.signed", "err", err, "digest", tag.GetManifestDigest())
		}
	}

	writeJSON(w, http.StatusCreated, record)
}

// signIdentityError bundles an HTTP status + message so resolveSignerIdentity
// can return a typed failure the handler maps straight onto writeError. Using
// a small struct (rather than a bare error) keeps the status code intent
// explicit at the call site without a second lookup table.
type signIdentityError struct {
	status int
	msg    string
}

// saListPageSize is the page size used when paging ListTenantUsers to resolve
// a service account. 200 is the server-side clamp ceiling — a single page
// covers the vast majority of tenants; we still follow next_page_token so a
// large tenant resolves correctly.
const saListPageSize = 200

// saResolveMaxPages bounds the ListTenantUsers pagination loop so a
// pathological tenant (or a buggy server that never clears next_page_token)
// can't spin this resolution forever. 50 pages × 200 rows = 10k users, well
// beyond any realistic single-tenant SA count.
const saResolveMaxPages = 50

// resolveSignerIdentity determines the signer_id to record for this sign,
// enforcing the FUT-009 "exactly one identity" contract:
//
//   - Neither field set                → 400 (a signer must be named).
//   - Both fields set                  → 400 (ambiguous intent).
//   - signer_id only                   → validated free-form string, returned
//     as-is (backward-compatible FE-API-026 path). A UUID-shaped value is
//     rejected 400 (SEC-330-A) so free-form identities can never be confused
//     with a service account's shadow user_id.
//   - service_account_id only          → resolved to the SA's shadow user_id
//     after confirming the SA exists, is
//     enabled, and belongs to tenantID.
//
// The SA is validated against ListTenantUsers (already tenant-scoped): the
// resolved row must be kind='service_account' and status='active'. On any
// mismatch we return a clear 4xx per CLAUDE.md §7 (reject unknown / disabled /
// cross-tenant) rather than forwarding an unvalidated identifier to the signer.
func (h *Handler) resolveSignerIdentity(ctx context.Context, tenantID string, body *signManifestBody) (string, *signIdentityError) {
	hasSignerID := body.SignerID != ""
	hasSA := body.ServiceAccountID != ""

	switch {
	case hasSignerID && hasSA:
		// Ambiguous — refuse rather than guessing which the caller meant.
		return "", &signIdentityError{http.StatusBadRequest, "provide either signer_id or service_account_id, not both"}
	case !hasSignerID && !hasSA:
		return "", &signIdentityError{http.StatusBadRequest, "signer_id or service_account_id is required"}
	case hasSignerID:
		// Legacy free-form path — validate and pass through unchanged.
		if !validateSignerID(body.SignerID) {
			return "", &signIdentityError{http.StatusBadRequest, "invalid signer_id"}
		}
		// SEC-330-A: reject a UUID-shaped free-form signer_id. The SA path
		// records a service account's shadow user_id (a UUID) as the signer_id,
		// and the tag-detail Signing panel badges any UUID signer_id as a
		// validated "Service account". Allowing a bare UUID down the free-form
		// path — which skips all SA existence/active/kind/tenant checks — would
		// let a repo-admin forge that managed-identity provenance (and could
		// collide with a real SA's later legitimate signature). Keep the two
		// namespaces disjoint: free-form identities must not be UUIDs.
		if _, err := uuid.Parse(body.SignerID); err == nil {
			return "", &signIdentityError{http.StatusBadRequest, "signer_id must not be a UUID; use service_account_id to sign as a service account"}
		}
		return body.SignerID, nil
	default:
		// Service-account path — resolve + validate server-side.
		return h.resolveServiceAccountSigner(ctx, tenantID, body.ServiceAccountID)
	}
}

// resolveServiceAccountSigner validates that saShadowUserID names an enabled
// service account in tenantID and returns the shadow user_id to record as the
// signer_id. See resolveSignerIdentity for the surrounding contract.
//
// Validation steps (CLAUDE.md §7):
//  1. The id must be a well-formed UUID (rejects free-form smuggling).
//  2. ListTenantUsers (tenant-scoped) must return a row for it whose kind is
//     'service_account' — a human user_id or an unknown id is rejected 400.
//  3. The row's status must be 'active' — a disabled SA is rejected 400.
func (h *Handler) resolveServiceAccountSigner(ctx context.Context, tenantID, saShadowUserID string) (string, *signIdentityError) {
	// (1) Shape check — the shadow user_id is a UUID; anything else is a
	// client error we surface before any RPC round-trip.
	if _, err := uuid.Parse(saShadowUserID); err != nil {
		return "", &signIdentityError{http.StatusBadRequest, "invalid service_account_id"}
	}

	// (2) + (3) Resolve against the caller's tenant. ListTenantUsers is
	// tenant-scoped server-side, so a cross-tenant SA simply never appears in
	// the page set and falls through to the not-found branch — the BFF can
	// never leak another tenant's SA existence.
	//
	// Cost note: this is an O(tenant users) linear scan per sign because auth
	// exposes no shadow-user_id-keyed lookup RPC (only ListTenantUsers returns
	// SA-kind rows). Accepted at current scale (single-tenant, small SA counts)
	// and bounded by saResolveMaxPages. If a GetServiceAccount / LookupUser RPC
	// ever lands, switch this to a direct O(1) lookup.
	pageToken := ""
	for page := 0; page < saResolveMaxPages; page++ {
		resp, err := h.auth.ListTenantUsers(ctx, &authv1.ListTenantUsersRequest{
			TenantId:  tenantID,
			PageSize:  saListPageSize,
			PageToken: pageToken,
		})
		if err != nil {
			// A transport / auth-service failure is a 500 — we can't confirm
			// the SA is valid, so we fail closed rather than sign with an
			// unresolved identity.
			slog.Error("ListTenantUsers (SA sign resolution)", "err", err, "tenant_id", tenantID)
			return "", &signIdentityError{http.StatusInternalServerError, "failed to resolve service account"}
		}

		for _, u := range resp.GetUsers() {
			if u.GetUserId() != saShadowUserID {
				continue
			}
			// Found the row. Enforce the kind + status gates.
			if u.GetKind() != "service_account" {
				// A valid tenant user_id that isn't an SA — treat as unknown SA
				// so callers can't repurpose the field to sign as a human.
				return "", &signIdentityError{http.StatusBadRequest, "service_account_id does not name a service account"}
			}
			if u.GetStatus() != "active" {
				return "", &signIdentityError{http.StatusBadRequest, "service account is disabled"}
			}
			// Record the shadow user_id as the signer_id. The tag-detail
			// render resolves this UUID back to the SA display name.
			return u.GetUserId(), nil
		}

		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}

	// Walked every page without a match — unknown (or cross-tenant) SA.
	return "", &signIdentityError{http.StatusBadRequest, "service account not found"}
}
