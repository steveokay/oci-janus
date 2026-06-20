// Package handler — signature.go
//
// FE-API-003 — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/signature
//
// Returns the signing status for a specific tag. The signer service stores
// signatures per manifest_digest; the BFF resolves the tag to its current
// manifest digest, then calls signer.ListSignatures.
//
// Authorization mirrors the existing tag-detail routes (pull access on the
// repo). We deliberately do not verify the cryptographic signature
// server-side on every request — VerifyManifest is heavier and we don't
// want every page render to redo that work. The list response carries
// enough metadata (signer_id, key_id, signed_at) for the UI to render the
// signing posture; a future `?verify=true` query param can opt into the
// expensive cryptographic check.
//
// Lives in its own file so concurrent edits to handler.go (RBAC,
// webhooks, admin tenants) don't conflict with the signature feature
// surface.
package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// SignatureResponse is the JSON body for GET …/tags/{tag}/signature.
//
// `Signed` is the one signal the UI most often branches on (signed vs
// unsigned). When `Signed` is false the `Signatures` slice is empty and
// the rest of the fields are zero-valued. The full list of signatures is
// returned so a tag signed by multiple parties (the typical pattern in a
// compliance environment) renders every signer.
type SignatureResponse struct {
	ManifestDigest string             `json:"manifest_digest"`
	Signed         bool               `json:"signed"`
	Signatures     []signatureRecord  `json:"signatures"`
}

type signatureRecord struct {
	SignerID        string    `json:"signer_id"`
	KeyID           string    `json:"key_id"`
	SignatureDigest string    `json:"signature_digest"`
	SignedAt        time.Time `json:"signed_at"`
}

func (h *Handler) handleGetSignature(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		// Same pattern as the webhook + tenant gates: 404 "route disabled"
		// so the frontend can render an unsigned/disabled state instead of
		// surfacing a generic 5xx.
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

	sigs, err := h.signer.ListSignatures(r.Context(), &signerv1.ListSignaturesRequest{
		ManifestDigest: tag.GetManifestDigest(),
		TenantId:       tenantID,
	})
	if err != nil {
		// signer's ListSignatures returns NotFound when nothing has signed
		// this manifest yet. That's the normal "unsigned" state, not an
		// error — collapse it into Signed:false rather than 500.
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.NotFound || errors.Is(err, status.Error(codes.NotFound, ""))) {
			writeJSON(w, http.StatusOK, SignatureResponse{
				ManifestDigest: tag.GetManifestDigest(),
				Signed:         false,
				Signatures:     []signatureRecord{},
			})
			return
		}
		slog.Error("ListSignatures", "err", err, "digest", tag.GetManifestDigest())
		writeError(w, http.StatusInternalServerError, "failed to fetch signatures")
		return
	}

	resp := SignatureResponse{
		ManifestDigest: tag.GetManifestDigest(),
		Signatures:     make([]signatureRecord, 0, len(sigs.GetSignatures())),
	}
	for _, s := range sigs.GetSignatures() {
		resp.Signatures = append(resp.Signatures, signatureRecord{
			SignerID:        s.GetSignerId(),
			KeyID:           s.GetKeyId(),
			SignatureDigest: s.GetSignatureDigest(),
			SignedAt:        s.GetSignedAt().AsTime(),
		})
	}
	resp.Signed = len(resp.Signatures) > 0

	writeJSON(w, http.StatusOK, resp)
}
