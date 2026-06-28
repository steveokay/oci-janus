// Package handler — trusted_keys.go
//
// Futures.md Tier 1 #3 Phase 2 — per-repo trusted-key allowlist.
//
// Four routes that wrap the metadata trusted-key RPCs (List/Add/Remove)
// plus the BFF-orchestrated "recent signers" helper that powers the
// dashboard's "Pick from recent signers" Approve dialog:
//
//	GET    /api/v1/repositories/{org}/{repo}/trusted-keys             — list approved keys
//	POST   /api/v1/repositories/{org}/{repo}/trusted-keys             — add   (body: {key_id, display_name})
//	DELETE /api/v1/repositories/{org}/{repo}/trusted-keys/{key_id}    — remove
//	GET    /api/v1/repositories/{org}/{repo}/recent-signers           — picker-source: distinct key_ids that recently signed in this repo
//
// The allowlist itself isn't sensitive (it's the set of public-key
// identifiers the operator chose to trust — anyone with reader access
// already sees signed images and their key_ids on the tag detail
// page), so List + recent-signers are reader-accessible. Add/Remove
// are security transitions that gate pull admission, so they require
// repo admin like the Settings tab's other toggles.
//
// The recent-signers route is deliberately BFF-orchestrated rather than
// a new signer RPC: ListSignatures(manifest_digest) is enough, the call
// counts are bounded (N tags × 1 RPC = small N for any reasonable repo),
// and an extra proto field/RPC purely for picker UX would be hard to
// justify against the proto-stability cost.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// trustedKeyResponse is the JSON wire form of one allowed key. Mirrors
// the proto RepositoryTrustedKey 1:1 except `added_at` is ISO-8601
// rather than the proto Timestamp shape, matching the rest of the BFF
// responses.
type trustedKeyResponse struct {
	ID          string `json:"id"`
	KeyID       string `json:"key_id"`
	DisplayName string `json:"display_name,omitempty"`
	AddedBy     string `json:"added_by,omitempty"`
	AddedAt     string `json:"added_at"`
}

// addTrustedKeyBody is the expected JSON body for POST. key_id is
// required; display_name is optional but strongly encouraged so the
// dashboard table renders meaningful labels.
type addTrustedKeyBody struct {
	KeyID       string `json:"key_id"`
	DisplayName string `json:"display_name"`
}

// keyIDPattern matches the shape services/signer emits today: 16 hex
// chars for the Vault Transit backend (truncated SHA256). Cosign
// keyless will use full SHA256 fingerprints (64 hex). Allow both
// (8-128 chars of hex or colon-separated hex like `sha256:...`).
// Loose enough to accept future formats, strict enough to reject the
// most common copy-paste mistakes (spaces, quotes, full PEM blocks).
var keyIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_:./-]{8,256}$`)

func (h *Handler) handleListTrustedKeys(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	resp, err := h.meta.ListRepositoryTrustedKeys(r.Context(), &metadatav1.ListRepositoryTrustedKeysRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
	})
	if err != nil {
		slog.Error("ListRepositoryTrustedKeys", "err", err, "repo", org+"/"+repoName)
		writeError(w, http.StatusInternalServerError, "failed to list trusted keys")
		return
	}

	out := make([]trustedKeyResponse, 0, len(resp.GetKeys()))
	for _, k := range resp.GetKeys() {
		out = append(out, trustedKeyResponse{
			ID:          k.GetId(),
			KeyID:       k.GetKeyId(),
			DisplayName: k.GetDisplayName(),
			AddedBy:     k.GetAddedBy(),
			AddedAt:     k.GetAddedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (h *Handler) handleAddTrustedKey(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// Repo admin gate — security-relevant transition: adding a key
	// either widens (from "nothing trusted") or, more commonly,
	// changes the set of who can sign for this repo. Mirrors the
	// posture of the Settings-tab toggles.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body addTrustedKeyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !keyIDPattern.MatchString(body.KeyID) {
		writeError(w, http.StatusBadRequest, "invalid key_id (expected 8-256 chars of hex / base64-ish identifier)")
		return
	}
	if len(body.DisplayName) > 128 {
		writeError(w, http.StatusBadRequest, "display_name too long (max 128 chars)")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// added_by sources from the actor user_id injected by the auth
	// middleware so the audit chain stays intact. Empty string for
	// principals without a user_id (system-driven callers); the SQL
	// turns "" into NULL via NULLIF so the column stays type-clean.
	addedBy := middleware.UserIDFromContext(r.Context())

	k, err := h.meta.AddRepositoryTrustedKey(r.Context(), &metadatav1.AddRepositoryTrustedKeyRequest{
		TenantId:    tenantID,
		RepoId:      repo.GetRepoId(),
		KeyId:       body.KeyID,
		DisplayName: body.DisplayName,
		AddedBy:     addedBy,
	})
	if err != nil {
		slog.Error("AddRepositoryTrustedKey", "err", err, "repo", org+"/"+repoName, "key_id", body.KeyID)
		writeError(w, http.StatusInternalServerError, "failed to add trusted key")
		return
	}

	writeJSON(w, http.StatusCreated, trustedKeyResponse{
		ID:          k.GetId(),
		KeyID:       k.GetKeyId(),
		DisplayName: k.GetDisplayName(),
		AddedBy:     k.GetAddedBy(),
		AddedAt:     k.GetAddedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *Handler) handleRemoveTrustedKey(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName, keyID := r.PathValue("org"), r.PathValue("repo"), r.PathValue("key_id")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if !keyIDPattern.MatchString(keyID) {
		writeError(w, http.StatusBadRequest, "invalid key_id")
		return
	}

	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	if _, err := h.meta.RemoveRepositoryTrustedKey(r.Context(), &metadatav1.RemoveRepositoryTrustedKeyRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
		KeyId:    keyID,
	}); err != nil {
		// metadata's repository returns ErrNotFound when the key isn't
		// approved; mapErr surfaces that as codes.NotFound which we
		// translate to a clean 404 here so the FE can distinguish
		// "already removed" from a real failure.
		if s, ok := status.FromError(err); ok && s.Code() == codes.NotFound {
			writeError(w, http.StatusNotFound, "trusted key not found")
			return
		}
		if errors.Is(err, context.Canceled) {
			// Client gave up mid-flight — no point logging this loudly.
			return
		}
		slog.Error("RemoveRepositoryTrustedKey", "err", err, "repo", org+"/"+repoName, "key_id", keyID)
		writeError(w, http.StatusInternalServerError, "failed to remove trusted key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/recent-signers
//
// Powers the Approve-Trusted-Key dialog's "Pick from recent signers" mode.
// Walks the most recent N tags in this repo, calls signer.ListSignatures
// per tag, dedupes by key_id, and returns the top N distinct entries
// ordered by most-recent signed_at. Reader-accessible.
// ---------------------------------------------------------------------------

// recentSignerEntry is one row in the recent-signers picker. Mirrors the
// fields the FE needs to render the dropdown:
//
//   - key_id      → the literal value that goes into the trusted-keys POST
//   - signer_id   → auto-fill for the optional display_name input
//   - last_signed_at → drives the "5m ago" relative-time hint
//   - tag_count   → "signed N tags" helper so the operator can sanity-check
//     they're approving an actively-used key vs a one-shot accident
type recentSignerEntry struct {
	KeyID        string    `json:"key_id"`
	SignerID     string    `json:"signer_id,omitempty"`
	LastSignedAt time.Time `json:"last_signed_at"`
	TagCount     int       `json:"tag_count"`
}

// recentSignersResponse is the wire body. Always emits a non-nil slice so
// the FE can branch on `signers.length === 0` without a null check.
type recentSignersResponse struct {
	Signers []recentSignerEntry `json:"signers"`
}

// defaultRecentSignersLimit caps the response size and the upstream tag
// fetch. 20 is enough for a picker dropdown — anything beyond that and
// the operator should be using the manual-entry mode instead.
const defaultRecentSignersLimit = 20

// maxRecentSignersLimit lets an automation script pull a wider snapshot
// without rewriting the route, but caps the per-tag fan-out so a
// pathological caller can't ask for 10,000 ListSignatures calls.
const maxRecentSignersLimit = 100

func (h *Handler) handleListRecentSigners(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName := r.PathValue("org"), r.PathValue("repo")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}

	// ?limit=N — optional, defaults to 20, capped at 100 to bound the
	// downstream fan-out. Values <1 fall back to the default so a
	// `?limit=0` doesn't return an empty body silently.
	limit := defaultRecentSignersLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit (expected positive integer)")
			return
		}
		if n > maxRecentSignersLimit {
			n = maxRecentSignersLimit
		}
		limit = n
	}

	// Signer not wired (SIGNER_GRPC_ADDR unset) → empty list with 200.
	// The dialog's "Recent signer" mode degrades gracefully to "no recent
	// signatures" and the operator falls back to Manual entry. This is
	// the same posture as handleGetSignature, but here we return 200 +
	// empty rather than 404 because the picker is an enhancement on top
	// of a manual flow that still works.
	if h.signer == nil {
		writeJSON(w, http.StatusOK, recentSignersResponse{Signers: []recentSignerEntry{}})
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// Pull the most recent `limit` tags. We don't have a true "most
	// recent" ordering hint on ListTags today (the stream returns
	// repository-insertion order with a server-side page_size cap), so
	// we'll over-fetch slightly and sort client-side by tag.updated_at
	// before fanning out — see comment at the sort below. PageSize ==
	// limit keeps the upstream call cheap; for limit=20 this fetches at
	// most one page.
	stream, err := h.meta.ListTags(r.Context(), &metadatav1.ListTagsRequest{
		TenantId: tenantID,
		RepoId:   repo.GetRepoId(),
		PageSize: int32(limit),
	})
	if err != nil {
		slog.Error("ListTags (recent-signers)", "err", err, "repo", org+"/"+repoName)
		writeError(w, http.StatusInternalServerError, "failed to list tags")
		return
	}

	type recentTag struct {
		digest    string
		updatedAt time.Time
	}
	tags := make([]recentTag, 0, limit)
	for {
		tag, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			// Same posture as handleListTags — log + break rather than
			// 500 so a transient stream-close doesn't blank the
			// picker. The empty-list fallback in the FE keeps the
			// dialog usable via Manual entry.
			slog.Error("ListTags stream (recent-signers)", "err", recvErr, "repo", org+"/"+repoName)
			break
		}
		// Skip referrer-style artifacts (`signature`, `sbom`). Their
		// "signature" entries point at the parent manifest, not at the
		// referrer manifest itself — listing them as picker entries
		// would either dedupe to the same key_id we already see on the
		// parent (best case) or surface a key_id that signed the
		// signature artifact instead of the image (confusing).
		if at := tag.GetArtifactType(); at == "signature" || at == "sbom" {
			continue
		}
		digest := tag.GetManifestDigest()
		if digest == "" {
			continue
		}
		tags = append(tags, recentTag{
			digest:    digest,
			updatedAt: tag.GetUpdatedAt().AsTime(),
		})
	}

	// Sort tags by updated_at DESC so we walk the most-recently-updated
	// tags first. This matters when a repo has more than `limit` tags
	// and we want the picker biased toward the latest activity; for
	// repos with fewer tags the sort is a no-op.
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].updatedAt.After(tags[j].updatedAt)
	})
	if len(tags) > limit {
		tags = tags[:limit]
	}

	// Aggregate signatures across tags, deduping by key_id. We dedupe by
	// the manifest_digest first because two tags can point at the same
	// manifest (e.g. `latest` + `1.2.3`); we don't want their signatures
	// to count twice in tag_count.
	type signerAgg struct {
		keyID    string
		signerID string
		latest   time.Time
		// tags counts distinct manifest_digests this key has signed in
		// this repo, not distinct signatures (one tag with two sigs from
		// the same key still counts as one tag).
		tags map[string]struct{}
	}
	agg := make(map[string]*signerAgg) // key_id → aggregate
	seenDigests := make(map[string]struct{})

	for _, t := range tags {
		if _, dup := seenDigests[t.digest]; dup {
			// Two tags → same manifest. We already collected its
			// signatures in the previous iteration.
			continue
		}
		seenDigests[t.digest] = struct{}{}

		// Per-call deadline so one slow signer can't drag the whole
		// picker response past the HTTP server's write timeout. The
		// parent context still wins on client disconnect.
		callCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		resp, sigErr := h.signer.ListSignatures(callCtx, &signerv1.ListSignaturesRequest{
			ManifestDigest: t.digest,
			TenantId:       tenantID,
		})
		cancel()
		if sigErr != nil {
			// NotFound = manifest has no signatures yet, normal state.
			// Swallow + continue so an unsigned tag doesn't blank the
			// rest of the picker. For other errors (DeadlineExceeded,
			// transport failures) log + continue so a degraded signer
			// still produces a partial result.
			if st, ok := status.FromError(sigErr); ok && st.Code() == codes.NotFound {
				continue
			}
			if errors.Is(sigErr, context.Canceled) {
				// Caller gave up — abort early.
				return
			}
			slog.Warn("ListSignatures (recent-signers)", "err", sigErr, "repo", org+"/"+repoName, "digest", t.digest)
			continue
		}
		for _, s := range resp.GetSignatures() {
			kid := s.GetKeyId()
			if kid == "" {
				continue
			}
			signedAt := s.GetSignedAt().AsTime()
			entry, ok := agg[kid]
			if !ok {
				entry = &signerAgg{
					keyID:    kid,
					signerID: s.GetSignerId(),
					latest:   signedAt,
					tags:     make(map[string]struct{}),
				}
				agg[kid] = entry
			}
			// "first non-empty signer_id seen" — keeps the auto-fill
			// hint stable even if some signature rows lack the field.
			if entry.signerID == "" {
				entry.signerID = s.GetSignerId()
			}
			if signedAt.After(entry.latest) {
				entry.latest = signedAt
			}
			entry.tags[t.digest] = struct{}{}
		}
	}

	// Flatten + sort by last_signed_at DESC. We don't bother with a
	// secondary sort key — collisions on the timestamp are unlikely at
	// second-resolution + tied entries are equally good picker rows.
	out := make([]recentSignerEntry, 0, len(agg))
	for _, e := range agg {
		out = append(out, recentSignerEntry{
			KeyID:        e.keyID,
			SignerID:     e.signerID,
			LastSignedAt: e.latest,
			TagCount:     len(e.tags),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSignedAt.After(out[j].LastSignedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}

	writeJSON(w, http.StatusOK, recentSignersResponse{Signers: out})
}
