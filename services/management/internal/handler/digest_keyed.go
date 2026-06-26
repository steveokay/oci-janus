// FUT-018 — digest-keyed scan + signature BFF routes.
//
// The existing per-tag scan + signature routes
// (`/api/v1/repositories/{org}/{repo}/tags/{tag}/scan|signature`) resolve
// the tag → manifest_digest via metadata before calling the scanner /
// signer. That works for owned repositories but not for the proxy cache —
// cached manifests live in services/proxy and carry only
// (tenant_id, upstream_name, image, reference, digest). There is no
// matching `org/repo/tag` row.
//
// These four routes wrap the same gRPC RPCs the per-tag routes use but
// take the digest directly:
//
//   GET   /api/v1/scan-by-digest/{digest}        → metadata.GetScanResult
//   POST  /api/v1/scan-by-digest/{digest}        → scanner.TriggerScan
//   GET   /api/v1/signatures-by-digest/{digest}  → signer.ListSignatures
//   POST  /api/v1/sign-by-digest/{digest}        → signer.SignManifest
//
// Auth:
//   • GET routes: any authenticated caller in the tenant (read).
//   • POST routes: writer or above on at least one org in the tenant
//     (mirrors handleTriggerScan's writer-on-repo gate; we can't check
//     repo-level access for a bare digest, so we collapse to
//     workspace-writer).
//
// Each scan route 404s with "route disabled" when h.scanner is nil; each
// signature route 404s when h.signer is nil. Same opt-in shape as the
// FUT-013 / FUT-017 routes above.
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

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// digestRe matches OCI sha256 digests. Validated at the BFF so a typo'd
// path returns 400 cleanly rather than bouncing off a metadata
// InvalidArgument deeper in the stack.
var digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// RegisterDigestKeyedScanAndSignatures mounts the FUT-018 routes.
// Called from Handler.Register alongside the other proxy-cache routes.
func (h *Handler) RegisterDigestKeyedScanAndSignatures(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/scan-by-digest/{digest}", authMW(http.HandlerFunc(h.handleGetScanByDigest)))
	mux.Handle("POST /api/v1/scan-by-digest/{digest}", authMW(http.HandlerFunc(h.handleTriggerScanByDigest)))
	mux.Handle("GET /api/v1/signatures-by-digest/{digest}", authMW(http.HandlerFunc(h.handleGetSignaturesByDigest)))
	mux.Handle("POST /api/v1/sign-by-digest/{digest}", authMW(http.HandlerFunc(h.handleSignByDigest)))
}

// ── Scan ────────────────────────────────────────────────────────────

// triggerScanByDigestResponse mirrors the per-tag scan-trigger response.
// scan_id is the workflow id the scanner returns; consumers can poll
// GET /scan-by-digest/{digest} to read the result.
type triggerScanByDigestResponse struct {
	ScanID         string `json:"scan_id"`
	ManifestDigest string `json:"manifest_digest"`
	Status         string `json:"status"` // queued
}

func (h *Handler) handleGetScanByDigest(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	digest := r.PathValue("digest")
	if !digestRe.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "invalid digest")
		return
	}

	// metadata.GetScanResult is keyed on (tenant_id, manifest_digest) in
	// the repo layer; the gRPC handler ignores RepoId. So passing empty
	// repo_id is safe + matches the cached-manifest case where there is
	// no metadata.repositories row.
	result, err := h.meta.GetScanResult(r.Context(), &metadatav1.GetScanResultRequest{
		ManifestDigest: digest,
		RepoId:         "",
		TenantId:       tenantID,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "no scan recorded")
			return
		}
		slog.Error("GetScanResult by digest", "err", err, "digest", digest)
		writeError(w, http.StatusInternalServerError, "failed to fetch scan result")
		return
	}

	writeJSON(w, http.StatusOK, ScanResponse{
		ScanID:         result.GetScanId(),
		Status:         result.GetStatus(),
		ScannerName:    result.GetScannerName(),
		ScannerVersion: result.GetScannerVersion(),
		SeverityCounts: result.GetSeverityCounts(),
		FindingsJSON:   result.GetFindingsJson(),
		StartedAt:      result.GetStartedAt().AsTime(),
		CompletedAt:    result.GetCompletedAt().AsTime(),
	})
}

// handleTriggerScanByDigest publishes a scan.queued event the scanner
// consumer picks up. Same envelope handleTriggerScan emits — keeps the
// scanner side branch-free.
//
// `repository_name` in the event payload is set to "cache:<digest>" so
// downstream surfaces (audit feed, scan-result table) can tell which
// trigger path produced the scan without needing a separate event type.
// The scanner doesn't dispatch on repository_name; it's purely a label
// the worker logs.
func (h *Handler) handleTriggerScanByDigest(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	if h.pub == nil {
		writeError(w, http.StatusServiceUnavailable, "scan trigger not available — broker not configured")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	digest := r.PathValue("digest")
	if !digestRe.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "invalid digest")
		return
	}

	// Writer-or-above gate. We don't have a repo to scope on (the digest
	// could be a cached manifest from any upstream), so collapse to
	// "writer on any org in the tenant" — same logical level as the
	// per-tag scan trigger.
	if !h.hasAnyWriterRole(r) {
		writeError(w, http.StatusForbidden, "writer role required")
		return
	}

	scanID := uuid.NewString()
	payload, _ := json.Marshal(events.ScanQueuedPayload{
		TenantID:       tenantID,
		RepositoryName: "cache:" + digest,
		RepoID:         "",
		TagName:        "",
		ManifestDigest: digest,
	})
	evt := events.Event{
		ID:         scanID,
		Type:       events.RoutingScanQueued,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(r.Context(), events.RoutingScanQueued, evt); err != nil {
		slog.Error("publish scan.queued by digest", "err", err, "digest", digest)
		writeError(w, http.StatusInternalServerError, "failed to queue scan")
		return
	}

	writeJSON(w, http.StatusAccepted, triggerScanByDigestResponse{
		ScanID:         scanID,
		ManifestDigest: digest,
		Status:         "queued",
	})
}

// ── Signatures ──────────────────────────────────────────────────────

func (h *Handler) handleGetSignaturesByDigest(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	digest := r.PathValue("digest")
	if !digestRe.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "invalid digest")
		return
	}

	sigs, err := h.signer.ListSignatures(r.Context(), &signerv1.ListSignaturesRequest{
		ManifestDigest: digest,
		TenantId:       tenantID,
	})
	if err != nil {
		// signer's ListSignatures returns NotFound when nothing has
		// signed this manifest yet. Collapse to the unsigned shape
		// rather than 500 — same convention handleGetSignature uses.
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.NotFound || errors.Is(err, status.Error(codes.NotFound, ""))) {
			writeJSON(w, http.StatusOK, SignatureResponse{
				ManifestDigest: digest,
				Signed:         false,
				Signatures:     []signatureRecord{},
			})
			return
		}
		slog.Error("ListSignatures by digest", "err", err, "digest", digest)
		writeError(w, http.StatusInternalServerError, "failed to fetch signatures")
		return
	}

	resp := SignatureResponse{
		ManifestDigest: digest,
		Signed:         len(sigs.GetSignatures()) > 0,
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
	writeJSON(w, http.StatusOK, resp)
}

// signByDigestRequest is the inline payload for POST /sign-by-digest.
// Mirrors the per-tag handleSignManifest body shape.
type signByDigestRequest struct {
	SignerID string `json:"signer_id,omitempty"` // optional; empty defaults to workspace key
}

func (h *Handler) handleSignByDigest(w http.ResponseWriter, r *http.Request) {
	if h.signer == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	digest := r.PathValue("digest")
	if !digestRe.MatchString(digest) {
		writeError(w, http.StatusBadRequest, "invalid digest")
		return
	}

	// Same writer-or-above gate as the trigger-scan route. Signing a
	// cached manifest is a workspace-level write, not a per-repo write
	// (there's no repo to scope on).
	if !h.hasAnyWriterRole(r) {
		writeError(w, http.StatusForbidden, "writer role required")
		return
	}

	// Optional body. Empty body → use the workspace default key.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body signByDigestRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	resp, err := h.signer.SignManifest(r.Context(), &signerv1.SignManifestRequest{
		ManifestDigest: digest,
		TenantId:       tenantID,
		SignerId:       body.SignerID,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
			writeError(w, http.StatusBadRequest, s.Message())
			return
		}
		slog.Error("SignManifest by digest", "err", err, "digest", digest)
		writeError(w, http.StatusInternalServerError, "failed to sign manifest")
		return
	}

	sig := resp.GetSignature()
	writeJSON(w, http.StatusOK, map[string]any{
		"manifest_digest":  digest,
		"signer_id":        sig.GetSignerId(),
		"key_id":           sig.GetKeyId(),
		"signature_digest": sig.GetSignatureDigest(),
		"signed_at":        sig.GetSignedAt().AsTime().Format(rfc3339Nano),
	})
}

// ── Auth helpers ────────────────────────────────────────────────────

// hasAnyWriterRole returns true when the caller carries an admin / owner
// / writer grant on any org or repo in the tenant. Mirrors the per-tag
// scan-trigger gate but collapsed because the digest doesn't tell us
// which repo it belongs to.
//
// TODO Phase 5.4: This gate is intentionally left as a coarse writer-level
// check for now (Review §A1 #4). Because digest-keyed sign-manifest
// operations lack a repo anchor, scoping them to a specific org or repo is
// a non-trivial design problem — it requires resolving the digest to its
// repo(s) before checking the caller's scope on those repos. That
// resolver + scope intersection is a separate task and out of scope for the
// Phase 5.2 tenant-admin gate hardening PR.
func (h *Handler) hasAnyWriterRole(r *http.Request) bool {
	for _, a := range h.getUserAssignments(r) {
		switch a.GetRole() {
		case "writer", "admin", "owner":
			return true
		}
	}
	return false
}
