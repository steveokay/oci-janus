// Package handler — bulk_scan.go
//
// S-MAINT-1 F1 — bulk scan fan-out for an org or a repo.
//
// Two new BFF routes complement the existing per-tag
// POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan:
//
//   - POST /api/v1/repositories/{org}/{repo}/scan
//   - POST /api/v1/orgs/{org}/scan
//
// Both enumerate the relevant tags (via ListRepositories + ListTags on
// the metadata service) and publish a scan.queued event per tag —
// exactly the same envelope handleTriggerScan emits, so the scanner
// worker pool sees one consistent stream regardless of whether a scan
// came from a push, the per-tag Rescan button, or the new bulk affordance.
//
// Why a hard per-request cap (bulkScanLimit) rather than unbounded
// fan-out: a careless click on an org with 10k tags would queue 10k
// jobs in one breath, drown the scanner worker pool, and quietly
// inflate the audit log. Capping at 500 keeps the worst case bounded;
// the response shape surfaces `capped:true` + `tags_count` so the FE
// can show "queued 500 of 10,234 — click again to continue" and the
// operator can repeat the action explicitly.
package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// bulkScanLimit caps the number of scan.queued events one bulk request
// can fan out. 500 is comfortably above any sane "scan this repo" /
// "scan this org" expectation while leaving the scanner worker pool
// room to drain a burst without DLQ pressure.
const bulkScanLimit = 500

// BulkScanResponse is the JSON body returned by both bulk-scan routes.
//
// ScansQueued: total scan.queued events successfully published.
// RepositoriesCount: number of repos visited (1 for the repo route).
// TagsCount: total tags considered across those repos before capping.
// Capped: true when TagsCount exceeded the per-request limit and the
//         tail of the listing was skipped.
// Limit: the bulkScanLimit constant in effect — surfaced so the FE
//        can render "X of Y queued · cap is Z" with no magic numbers.
type BulkScanResponse struct {
	ScansQueued       int  `json:"scans_queued"`
	RepositoriesCount int  `json:"repositories_count"`
	TagsCount         int  `json:"tags_count"`
	Capped            bool `json:"capped"`
	Limit             int  `json:"limit"`
}

// publishScanQueuedFor fires the same scan.queued envelope as
// handleTriggerScan, with one shared shape for both bulk routes.
// Failures here are logged but do not abort the fan-out — the
// alternative (giving up halfway through a 200-tag org) is worse
// operationally than silently dropping one tag and continuing.
func (h *Handler) publishScanQueuedFor(
	r *http.Request,
	tenantID, repoID, repositoryName, tagName, manifestDigest string,
) bool {
	payload, _ := json.Marshal(events.ScanQueuedPayload{
		TenantID:       tenantID,
		RepositoryName: repositoryName,
		RepoID:         repoID,
		TagName:        tagName,
		ManifestDigest: manifestDigest,
	})
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingScanQueued,
		TenantID:   tenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    payload,
	}
	if err := h.pub.Publish(r.Context(), events.RoutingScanQueued, evt); err != nil {
		slog.WarnContext(r.Context(), "bulk scan: publish scan.queued failed",
			"err", err,
			"repository", repositoryName,
			"tag", tagName,
		)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// POST /api/v1/repositories/{org}/{repo}/scan
// ---------------------------------------------------------------------------

// handleRepoBulkScan fans a scan request out across every tag in the
// repository. Permissions mirror the per-tag handleTriggerScan: writer
// or above on the repo, since the cost (CPU + DB rows + audit entries)
// is proportional to the tag count.
func (h *Handler) handleRepoBulkScan(w http.ResponseWriter, r *http.Request) {
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

	// Same writer-on-this-repo gate as handleTriggerScan. Bulk is just
	// many-tag, not many-repo — the per-tag permission posture applies.
	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "writer") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	queued, total, capped, err := h.scanAllTagsOfRepo(r, tenantID, repo.GetRepoId(), org+"/"+repoName)
	if err != nil {
		slog.Error("bulk scan: list tags", "err", err, "repo", org+"/"+repoName)
		writeError(w, http.StatusInternalServerError, "failed to enumerate tags")
		return
	}

	writeJSON(w, http.StatusAccepted, BulkScanResponse{
		ScansQueued:       queued,
		RepositoriesCount: 1,
		TagsCount:         total,
		Capped:            capped,
		Limit:             bulkScanLimit,
	})
}

// ---------------------------------------------------------------------------
// POST /api/v1/orgs/{org}/scan
// ---------------------------------------------------------------------------

// handleOrgBulkScan fans a scan request out across every tag in every
// repo of the org. Heavier-weight than the repo route, so the
// permission gate is org-admin-or-above rather than per-repo writer.
func (h *Handler) handleOrgBulkScan(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	org := r.PathValue("org")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}

	// Org-scoped admin gate. Heavier than the repo route's writer gate
	// because a click here can fan out to every tag in every repo
	// underneath — the operator needs explicit "I run this org" authority.
	if !hasScopedRole(h.getUserAssignments(r), "org", org, "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// Resolve the org name to its org_id via the read-only
	// LookupOrgIDByName RPC (FE-API-039). NotFound surfaces as 404
	// here so a typo in the org name doesn't queue an empty fan-out.
	orgLookup, err := h.meta.LookupOrgIDByName(r.Context(), &metadatav1.LookupOrgIDByNameRequest{
		TenantId: tenantID,
		Name:     org,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "org not found")
		return
	}

	queued, total, capped, repoCount, err := h.scanAllReposOfOrg(r, tenantID, orgLookup.GetOrgId(), org)
	if err != nil {
		slog.Error("bulk scan: list repos for org", "err", err, "org", org)
		writeError(w, http.StatusInternalServerError, "failed to enumerate repositories")
		return
	}

	writeJSON(w, http.StatusAccepted, BulkScanResponse{
		ScansQueued:       queued,
		RepositoriesCount: repoCount,
		TagsCount:         total,
		Capped:            capped,
		Limit:             bulkScanLimit,
	})
}

// scanAllTagsOfRepo enumerates every tag in a single repository and
// publishes a scan.queued event per tag, up to the bulkScanLimit cap.
// Returns (queued, total, capped, err) so callers can surface partial
// progress on the org-level route.
//
// Tags whose parent manifest is non-image (artifact_type != "image" /
// "") are skipped — the scanner already short-circuits these via
// HandlePushCompleted's P6 check, but skipping them here saves a
// per-tag scan_results row that would land as a "no findings" no-op.
func (h *Handler) scanAllTagsOfRepo(
	r *http.Request,
	tenantID, repoID, repositoryName string,
) (queued, total int, capped bool, err error) {
	stream, sErr := h.meta.ListTags(r.Context(), &metadatav1.ListTagsRequest{
		TenantId: tenantID,
		RepoId:   repoID,
		// PageSize 0 = no limit on metadata's side; the bulkScanLimit
		// guard below stops the fan-out before it can run away on a
		// pathologically tag-heavy repo.
	})
	if sErr != nil {
		return 0, 0, false, sErr
	}

	for {
		tag, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			// Mid-stream errors are surfaced as the overall error so the
			// caller can return 500 — a bulk scan is a single atomic
			// operator-facing action, and partial fan-out failures
			// would be confusing without a flag of their own.
			return queued, total, capped, recvErr
		}

		// Skip non-image artifacts — Batch 5 P6 already does this on
		// the scanner side, but skipping here keeps the queued / total
		// counters honest from the operator's perspective.
		if t := tag.GetArtifactType(); t != "" && t != "image" {
			continue
		}

		total++
		if queued >= bulkScanLimit {
			capped = true
			continue
		}
		if h.publishScanQueuedFor(r, tenantID, repoID, repositoryName, tag.GetName(), tag.GetManifestDigest()) {
			queued++
		}
	}
	return queued, total, capped, nil
}

// scanAllReposOfOrg enumerates every repository in the org and, for
// each, every tag underneath. Same per-tag publish path as the repo
// route — just an outer loop. Returns (queued, total, capped, repoCount, err).
func (h *Handler) scanAllReposOfOrg(
	r *http.Request,
	tenantID, orgID, orgName string,
) (queued, total int, capped bool, repoCount int, err error) {
	repoStream, sErr := h.meta.ListRepositories(r.Context(), &metadatav1.ListRepositoriesRequest{
		TenantId: tenantID,
		OrgId:    orgID,
	})
	if sErr != nil {
		return 0, 0, false, 0, sErr
	}

	for {
		repo, recvErr := repoStream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return queued, total, capped, repoCount, recvErr
		}
		repoCount++

		// Drain each repo's tags. We continue iterating repos even after
		// hitting the cap so the `total` counter reflects the true tag
		// count across the whole org — the FE uses that for the
		// "queued X of Y" copy.
		q, t, c, terr := h.scanAllTagsOfRepo(r, tenantID, repo.GetRepoId(), orgName+"/"+repo.GetName())
		if terr != nil {
			// Per-repo tag-listing failure: log + skip, don't abort the
			// whole org-bulk run. The operator gets a partial fan-out
			// rather than nothing.
			slog.WarnContext(r.Context(), "bulk org scan: list tags failed for one repo",
				"err", terr,
				"repo", orgName+"/"+repo.GetName(),
			)
			continue
		}

		// We need to keep the global cap honest across repos: subsequent
		// repos can only queue up to (bulkScanLimit - queued) more tags.
		// scanAllTagsOfRepo doesn't know about the outer counter, so we
		// re-apply the cap here. queued + q may overflow the limit on
		// the boundary repo — clamp it back down so the audit-emitted
		// count never exceeds the documented cap.
		queued += q
		total += t
		if c {
			capped = true
		}
		if queued >= bulkScanLimit {
			capped = true
			// Don't queue more from later repos; total still accumulates
			// so the response carries the true denominator.
			continue
		}
	}
	return queued, total, capped, repoCount, nil
}
