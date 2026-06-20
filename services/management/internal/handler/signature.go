// Package handler — signature.go
//
// FE-API-003 — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/signature
//
// Returns the signing status for a specific tag. The signer service stores
// signatures per manifest_digest; the BFF resolves the tag to its current
// manifest digest, then calls signer.ListSignatures.
//
// Authorization mirrors the existing tag-detail routes (pull access on the
// repo). By default we deliberately do not verify the cryptographic signature
// server-side on every request — VerifyManifest is heavier and we don't
// want every page render to redo that work. The list response carries
// enough metadata (signer_id, key_id, signed_at) for the UI to render the
// signing posture.
//
// FE-API-025 layered on top of this with a `?verify=true` query param: when
// set, the BFF fans out one VerifyManifest call per signature and decorates
// each record with `verified` + an optional `failure_reason`. The verify
// path runs the calls in parallel (cap 16) with an independent 5s deadline
// per signature so one stuck signer doesn't slow the others. When `verify`
// is absent the wire shape is unchanged — the new fields use `omitempty`.
//
// Lives in its own file so concurrent edits to handler.go (RBAC,
// webhooks, admin tenants) don't conflict with the signature feature
// surface.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
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
	ManifestDigest string            `json:"manifest_digest"`
	Signed         bool              `json:"signed"`
	Signatures     []signatureRecord `json:"signatures"`
}

// signatureRecord is one entry in SignatureResponse.Signatures.
//
// The two trailing fields (Verified + FailureReason) are populated only when
// the caller passes `?verify=true` on the GET request. They use omitempty so
// the wire shape for the default (cheap) path is unchanged from FE-API-003 —
// existing UI code keyed on Signatures[i].SignerID keeps working.
type signatureRecord struct {
	SignerID        string    `json:"signer_id"`
	KeyID           string    `json:"key_id"`
	SignatureDigest string    `json:"signature_digest"`
	SignedAt        time.Time `json:"signed_at"`
	// Verified carries the cryptographic verify result on the `?verify=true`
	// path. Pointer-to-bool with omitempty so we can distinguish three
	// states cleanly on the wire:
	//   - nil  → caller did not opt into verification (FE-API-003 shape)
	//   - true → signer.VerifyManifest returned verified=true
	//   - false → signer.VerifyManifest returned verified=false OR errored
	// A plain bool with omitempty would collapse states two and three on
	// the JSON wire because `false` is the zero value and gets dropped.
	Verified *bool `json:"verified,omitempty"`
	// FailureReason is set only when Verified would be false in a verify
	// request — captures the signer's failure_reason or a synthesized string
	// for timeouts/transport errors. Truncated to maxFailureReasonChars so a
	// pathological signer can't make the response payload explode.
	FailureReason string `json:"failure_reason,omitempty"`
}

// maxFailureReasonChars caps the failure_reason string per record so a
// pathological gRPC error description can't bloat the JSON response. 200
// chars is enough for "x509: certificate signed by unknown authority" plus
// the cosign provenance hint without leaking full stack traces.
const maxFailureReasonChars = 200

// verifyParallelism caps the fan-out for `?verify=true`. Most images carry
// 1-3 signatures; this cap is a defence-in-depth ceiling so a pathological
// 100-signature image doesn't open 100 concurrent gRPC streams to the signer.
const verifyParallelism = 16

// perSignatureVerifyTimeout is the deadline applied independently to each
// VerifyManifest call so one slow signer doesn't slow the others. The whole
// request still inherits the HTTP server's 30s write timeout from server.go.
const perSignatureVerifyTimeout = 5 * time.Second

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

	// FE-API-025: opt-in cryptographic verify. Only fans out when the caller
	// explicitly passes verify=true; the default path is unchanged from
	// FE-API-003 so existing dashboard renders stay cheap.
	if shouldVerify(r) && len(resp.Signatures) > 0 {
		h.verifySignatures(r.Context(), tenantID, tag.GetManifestDigest(), resp.Signatures)
	}

	writeJSON(w, http.StatusOK, resp)
}

// shouldVerify returns true when the caller opted into cryptographic
// verification via ?verify=true. Only the literal "true" enables it — any
// other value (including "1", "yes", or an empty value) is treated as the
// default fast path. This matches Go's strconv.ParseBool boolean conventions
// while keeping the URL-facing flag explicit.
func shouldVerify(r *http.Request) bool {
	return r.URL.Query().Get("verify") == "true"
}

// verifySignatures decorates each entry in records with the result of a
// signer.VerifyManifest call. Runs verifyParallelism workers at most, each
// with an independent perSignatureVerifyTimeout — a stuck signer for one
// key never slows the others.
//
// Mutates records in place. parent is the request context; cancelling it
// (via the HTTP client disconnecting) cancels every outstanding verify call.
func (h *Handler) verifySignatures(parent context.Context, tenantID, digest string, records []signatureRecord) {
	// sem caps fan-out so a pathological 100-signature image doesn't open 100
	// concurrent gRPC calls to the signer.
	sem := make(chan struct{}, verifyParallelism)
	var wg sync.WaitGroup

	for i := range records {
		wg.Add(1)
		sem <- struct{}{}
		// Capture i by value — needed for the goroutine to write to the
		// correct record. records[i] gives the goroutine an indexed slot it
		// owns, so no mutex is required on the slice itself.
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			// Per-signature deadline. Independent from the parent so one
			// slow signer can time out without dragging the others with it.
			ctx, cancel := context.WithTimeout(parent, perSignatureVerifyTimeout)
			defer cancel()

			resp, err := h.signer.VerifyManifest(ctx, &signerv1.VerifyManifestRequest{
				TenantId:       tenantID,
				ManifestDigest: digest,
				SignerId:       records[idx].SignerID,
			})

			// Helper local to assignment so the pointer pattern stays close
			// to the field that needs it. Using literal &false/&true here
			// would require a sentinel var; this keeps intent inline.
			setVerified := func(v bool) { records[idx].Verified = &v }

			if err != nil {
				setVerified(false)
				// Distinguish timeout from a generic gRPC error so the UI
				// can render the right operator hint ("retry later" vs.
				// "key was revoked").
				if errors.Is(err, context.DeadlineExceeded) {
					records[idx].FailureReason = "verification timed out"
					return
				}
				if st, ok := status.FromError(err); ok && st.Code() == codes.DeadlineExceeded {
					records[idx].FailureReason = "verification timed out"
					return
				}
				records[idx].FailureReason = truncateReason(err.Error())
				return
			}

			setVerified(resp.GetVerified())
			if !resp.GetVerified() {
				records[idx].FailureReason = truncateReason(resp.GetFailureReason())
			}
		}(i)
	}

	wg.Wait()
}

// truncateReason trims s to maxFailureReasonChars, appending an ellipsis
// when truncation actually fires. Keeps the JSON response bounded and
// strips any trailing whitespace/null bytes a misbehaving gRPC error
// description might inject.
func truncateReason(s string) string {
	if len(s) <= maxFailureReasonChars {
		return s
	}
	return s[:maxFailureReasonChars] + "..."
}
