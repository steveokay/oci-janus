// Package handler — promote_tag.go
//
// FUT-020 — image promotion (atomic tag copy).
//
// Two routes:
//   POST /api/v1/repositories/{org}/{repo}/tags/{tag}/promote
//       Copies the current manifest of the source tag onto a destination
//       {org}/{repo}:{tag}. Auth: repo `writer` on BOTH source AND
//       destination — the load-bearing security invariant is that someone
//       with pull-only access on `prod/*` cannot promote INTO it. Success
//       returns 201 + the persisted Promotion JSON and fires an
//       image.promoted event on RabbitMQ for the audit consumer.
//
//   GET /api/v1/repositories/{org}/{repo}/promotions
//       Returns recent promotions that touch this repo (src OR dst side).
//       Auth: repo `reader` — the history is not a secret.
//
// A promotion is a metadata-level primitive. No blobs are copied; both
// tags reference the same manifest digest so storage stays deduplicated.
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// promoteRequest is the JSON body of POST …/promote. Every field is
// validated at the handler layer before touching the gRPC surface;
// unvalidated strings never reach the metadata service.
type promoteRequest struct {
	DstOrg  string `json:"dst_org"`
	DstRepo string `json:"dst_repo"`
	DstTag  string `json:"dst_tag"`
	Note    string `json:"note,omitempty"`
	// CreateIfMissing (REM-030) — when true, the metadata surface creates
	// the destination repository if it does not exist. The BFF still
	// requires writer role on the destination scope; the promotion path
	// does not exempt auto-creation from RBAC. Default false preserves
	// the original 404-on-missing-dst behaviour.
	CreateIfMissing bool `json:"create_if_missing,omitempty"`
}

// promoteMaxNote caps operator-supplied notes at 256 chars. Kept small
// enough to fit inside a single audit metadata blob without inflating the
// promotions.note column bytes budget.
const promoteMaxNote = 256

// promotionResponse mirrors the wire shape of the metadata.Promotion proto
// but with idiomatic Go JSON tags (snake_case) so the FE hooks don't have
// to translate. Also flattens the promoted_at timestamp to time.Time so
// it JSON-encodes as RFC3339 like every other timestamp on this surface.
type promotionResponse struct {
	ID          string    `json:"id"`
	SrcOrg      string    `json:"src_org"`
	SrcRepo     string    `json:"src_repo"`
	SrcTag      string    `json:"src_tag"`
	SrcDigest   string    `json:"src_digest"`
	DstOrg      string    `json:"dst_org"`
	DstRepo     string    `json:"dst_repo"`
	DstTag      string    `json:"dst_tag"`
	DstDigest   string    `json:"dst_digest"`
	ActorUserID string    `json:"actor_user_id,omitempty"`
	Note        string    `json:"note,omitempty"`
	PromotedAt  time.Time `json:"promoted_at"`
}

// toPromotionResponse converts a proto Promotion to its JSON wire form.
func toPromotionResponse(p *metadatav1.Promotion) promotionResponse {
	return promotionResponse{
		ID:          p.GetId(),
		SrcOrg:      p.GetSrcOrg(),
		SrcRepo:     p.GetSrcRepo(),
		SrcTag:      p.GetSrcTag(),
		SrcDigest:   p.GetSrcDigest(),
		DstOrg:      p.GetDstOrg(),
		DstRepo:     p.GetDstRepo(),
		DstTag:      p.GetDstTag(),
		DstDigest:   p.GetDstDigest(),
		ActorUserID: p.GetActorUserId(),
		Note:        p.GetNote(),
		PromotedAt:  p.GetPromotedAt().AsTime(),
	}
}

// handlePromoteTag copies the source tag's current manifest onto a
// destination tag. The BOTH-sides write-role gate is the load-bearing
// security invariant — see the file docstring.
func (h *Handler) handlePromoteTag(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	srcOrg, srcRepoName, srcTagName := r.PathValue("org"), r.PathValue("repo"), r.PathValue("tag")

	// Validate source identifiers before reading the body — cheap defence
	// against a hostile path (e.g. `../` traversal via URL encoding).
	if err := validateOrgName(srcOrg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid src org name")
		return
	}
	if err := validateRepoName(srcRepoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid src repository name")
		return
	}
	if err := validateTagName(srcTagName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid src tag name")
		return
	}

	// Parse the body first so we know the destination identifiers before
	// the (both-sides) role check.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body promoteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateOrgName(body.DstOrg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dst org name")
		return
	}
	if err := validateRepoName(body.DstRepo); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dst repository name")
		return
	}
	if err := validateTagName(body.DstTag); err != nil {
		writeError(w, http.StatusBadRequest, "invalid dst tag name")
		return
	}
	if len(body.Note) > promoteMaxNote {
		writeError(w, http.StatusBadRequest, "note exceeds 256 characters")
		return
	}

	// Load the caller's role assignments once so we can gate on BOTH
	// source and destination without a second RPC round-trip.
	assignments := h.getUserAssignments(r)
	srcScope := srcOrg + "/" + srcRepoName
	dstScope := body.DstOrg + "/" + body.DstRepo
	// Writer OR above required on the source — otherwise a pull-only
	// reader could enumerate anything they wanted, but they could ALSO
	// use the source as a laundering channel to push into any repo they
	// separately have write on. Requiring writer on the source makes the
	// audit trail meaningful ("this operator had write access here").
	if !hasScopedRole(assignments, "repo", srcScope, "writer") {
		writeError(w, http.StatusForbidden, "insufficient permissions on source repository")
		return
	}
	// Writer on the destination — the load-bearing gate. A read-only
	// user on prod/* must not be able to push a stale image in via a
	// promotion.
	if !hasScopedRole(assignments, "repo", dstScope, "writer") {
		writeError(w, http.StatusForbidden, "insufficient permissions on destination repository")
		return
	}

	// Actor id from JWT sub. Empty when the caller is on a raw API key
	// belonging to a service account with no shadow user id — the metadata
	// handler treats empty as "anonymous" and passes nil into the tx.
	actorID := middleware.UserIDFromContext(r.Context())

	prom, err := h.meta.PromoteTag(r.Context(), &metadatav1.PromoteTagRequest{
		TenantId:        tenantID,
		SrcOrg:          srcOrg,
		SrcRepo:         srcRepoName,
		SrcTag:          srcTagName,
		DstOrg:          body.DstOrg,
		DstRepo:         body.DstRepo,
		DstTag:          body.DstTag,
		ActorUserId:     actorID,
		Note:            body.Note,
		CreateIfMissing: body.CreateIfMissing,
	})
	if err != nil {
		// Map the metadata-side error codes onto HTTP. Anything else falls
		// through to 500 so a transport blip doesn't masquerade as a
		// client error.
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "source tag or destination repository not found")
				return
			case codes.FailedPrecondition:
				writeError(w, http.StatusConflict, "destination tag is immutable")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, "invalid promotion request")
				return
			}
		}
		slog.Error("PromoteTag", "err", err,
			"src", srcScope+":"+srcTagName,
			"dst", dstScope+":"+body.DstTag)
		writeError(w, http.StatusInternalServerError, "failed to promote tag")
		return
	}

	// Publish image.promoted so the audit consumer + webhook receiver see
	// the event. Publish AFTER the response is prepared, so a publisher
	// blip logs but doesn't roll back the durable promotion state.
	if h.pub != nil {
		payload, _ := json.Marshal(events.ImagePromotedPayload{
			TenantID:    tenantID,
			SrcOrg:      prom.GetSrcOrg(),
			SrcRepo:     prom.GetSrcRepo(),
			SrcTag:      prom.GetSrcTag(),
			SrcDigest:   prom.GetSrcDigest(),
			DstOrg:      prom.GetDstOrg(),
			DstRepo:     prom.GetDstRepo(),
			DstTag:      prom.GetDstTag(),
			DstDigest:   prom.GetDstDigest(),
			ActorUserID: prom.GetActorUserId(),
			Note:        prom.GetNote(),
		})
		evt := events.Event{
			ID:         uuid.New().String(),
			Type:       events.RoutingImagePromoted,
			TenantID:   tenantID,
			OccurredAt: time.Now(),
			Version:    "1.0",
			Payload:    payload,
		}
		if err := h.pub.Publish(r.Context(), events.RoutingImagePromoted, evt); err != nil {
			// The promotion is already durable in the promotions table;
			// audit replay can rebuild the missing event. Log loudly so
			// an operator can spot a broker outage.
			slog.Error("publish image.promoted", "err", err,
				"promotion_id", prom.GetId())
		}
	}

	writeJSON(w, http.StatusCreated, toPromotionResponse(prom))
}

// handleListPromotions returns recent promotions touching the given repo
// (src OR dst). Reader role suffices — promotion history is not sensitive.
func (h *Handler) handleListPromotions(w http.ResponseWriter, r *http.Request) {
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

	if !hasScopedRole(h.getUserAssignments(r), "repo", org+"/"+repoName, "reader") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// The metadata RPC caps limit at 200 internally; we pass the requested
	// value through unchanged (default 50 when zero).
	resp, err := h.meta.ListPromotions(r.Context(), &metadatav1.ListPromotionsRequest{
		TenantId: tenantID,
		Org:      org,
		Repo:     repoName,
		Limit:    50,
	})
	if err != nil {
		slog.Error("ListPromotions", "err", err, "repo", org+"/"+repoName)
		writeError(w, http.StatusInternalServerError, "failed to list promotions")
		return
	}

	// Materialise an empty slice rather than nil so the JSON envelope
	// always contains `promotions: []` — the FE table treats null as an
	// error but an empty array as an empty state.
	out := make([]promotionResponse, 0, len(resp.GetPromotions()))
	for _, p := range resp.GetPromotions() {
		out = append(out, toPromotionResponse(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"promotions": out})
}
