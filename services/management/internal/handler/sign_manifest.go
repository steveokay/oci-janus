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
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
	"unicode"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// signManifestBody is the JSON body for POST …/sign.
//
// Only signer_id is accepted — key material lives in registry-signer's
// Vault backend keyed by signer_id, and management never touches private
// keys. Future extension could carry a free-form annotation; for now the
// shape is intentionally minimal so we don't accidentally accept
// security-sensitive overrides from the dashboard.
type signManifestBody struct {
	SignerID string `json:"signer_id"`
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
	if !validateSignerID(body.SignerID) {
		writeError(w, http.StatusBadRequest, "invalid signer_id")
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
		SignerId:       body.SignerID,
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
		slog.Error("SignManifest", "err", err, "digest", tag.GetManifestDigest(), "signer_id", body.SignerID)
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
