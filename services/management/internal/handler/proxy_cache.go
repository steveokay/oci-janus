// FUT-013 — pull-through cache visibility routes.
//
// Three workspace-admin-gated REST routes that wrap the new ProxyService
// RPCs (ListCachedManifests / GetCacheStats / DeleteCachedManifest) from
// services/proxy.
//
// All three return 404 "route disabled" when h.proxy is nil — same
// degrade-gracefully shape as the signer / scanner / webhook routes.
// The frontend probes `GET /api/v1/proxy/cache/stats` at app boot to
// decide whether to render the sidebar entry; a 404 hides it.
//
// Auth: workspace-admin (any admin/owner role grant on any org in the
// tenant). Platform-admin has implicit access via the (admin, org, '*')
// marker which the existing requireDomainAdmin helper already honours.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// RegisterProxyCache mounts the FUT-013 routes onto mux. Called from
// Handler.Register. All routes return 404 when h.proxy is nil (same
// opt-in pattern as the signer / scanner / webhook surfaces).
func (h *Handler) RegisterProxyCache(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/proxy/cache", authMW(http.HandlerFunc(h.handleListProxyCache)))
	mux.Handle("GET /api/v1/proxy/cache/stats", authMW(http.HandlerFunc(h.handleProxyCacheStats)))
	// FUT-016 — click-through detail page. The {id} catch-all sits AFTER the
	// /stats literal so net/http's pattern matcher routes /stats to the
	// aggregate handler. ServeMux disambiguates static vs wildcard
	// segments automatically; the ordering here is for the reader.
	mux.Handle("GET /api/v1/proxy/cache/{id}", authMW(http.HandlerFunc(h.handleGetProxyCacheDetail)))
	mux.Handle("DELETE /api/v1/proxy/cache/{id}", authMW(http.HandlerFunc(h.handleEvictProxyCache)))
}

// cachedManifestResponse is the JSON shape returned per row by GET
// /api/v1/proxy/cache. Field names mirror the proto field names in
// snake_case so the frontend doesn't need a translation layer.
type cachedManifestResponse struct {
	ID           string  `json:"id"`
	UpstreamID   string  `json:"upstream_id"`
	UpstreamName string  `json:"upstream_name"`
	Image        string  `json:"image"`
	Reference    string  `json:"reference"`
	Digest       string  `json:"digest"`
	MediaType    string  `json:"media_type"`
	SizeBytes    int64   `json:"size_bytes"`
	FetchedAt    string  `json:"fetched_at"`               // RFC3339Nano; never empty
	LastPulledAt *string `json:"last_pulled_at,omitempty"` // omitted when never pulled
	PullCount    int64   `json:"pull_count"`
}

type listProxyCacheResponse struct {
	Manifests     []cachedManifestResponse `json:"manifests"`
	NextPageToken string                   `json:"next_page_token,omitempty"`
}

type proxyCacheStatsResponse struct {
	TotalManifests  int64 `json:"total_manifests"`
	TotalBytes      int64 `json:"total_bytes"`
	UniqueUpstreams int64 `json:"unique_upstreams"`
	TotalPulls      int64 `json:"total_pulls"`
}

// ─── FUT-016 detail-page response shapes ──────────────────────────────────────

// proxyCacheLayer is one layer entry parsed out of an image manifest body.
// Mirrors the per-tag manifest layers (services/management.../handler.go
// `manifestLayer`) so the frontend can re-use its existing components.
type proxyCacheLayer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

// proxyCacheManifestRef is one per-platform child manifest of an index /
// manifest-list. Same projection as the per-tag manifest handler's
// `manifestEntry` — keeps the FE shape consistent so a future shared
// layer component just works.
type proxyCacheManifestRef struct {
	Digest       string `json:"digest"`
	Size         int64  `json:"size"`
	MediaType    string `json:"media_type"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
	OSVersion    string `json:"os_version,omitempty"`
}

// proxyCacheDetailResponse is the JSON shape returned by GET /api/v1/proxy/cache/{id}.
// Embeds the list-row fields verbatim + adds:
//
//   - kind         — "image" (single-arch) or "index" (multi-platform). Lets
//     the FE branch on a single string instead of sniffing
//     media types client-side.
//   - body_base64  — the manifest body bytes, base64-encoded, for the
//     "Manifest" tab raw-JSON viewer.
//   - layers / manifests — parsed projection. One of the two is populated
//     based on `kind`; the other is an empty slice
//     (we never emit `null` — stable shape for the
//     FE means no `?.length` dance).
type proxyCacheDetailResponse struct {
	cachedManifestResponse
	Kind       string                  `json:"kind"`
	BodyBase64 string                  `json:"body_base64"`
	Layers     []proxyCacheLayer       `json:"layers"`
	Manifests  []proxyCacheManifestRef `json:"manifests"`
}

// rawProxyManifest is the subset of an OCI/Docker manifest JSON we need
// to parse for the detail-page projection. Same shape as the per-tag
// manifest handler's `rawManifest` — both contracts come from OCI spec
// + Docker schema so divergence here would be a bug. Duplicated rather
// than imported because cross-handler-file types in the same package
// would couple two otherwise-independent routes; if a third caller
// shows up we'll promote it to a shared helper.
type rawProxyManifest struct {
	Config struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
	} `json:"config"`
	Layers []struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
	} `json:"layers"`
	Manifests []struct {
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
		MediaType string `json:"mediaType"`
		Platform  struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
			Variant      string `json:"variant"`
			OSVersion    string `json:"os.version"`
		} `json:"platform"`
	} `json:"manifests"`
}

// GET /api/v1/proxy/cache
//
// Query params:
//
//	upstream_id     — optional UUID, filters to a single upstream
//	image_contains  — optional substring filter (case-insensitive)
//	page_token      — caller-opaque cursor returned by the previous page
//	page_size       — 1..100; defaults to 50
func (h *Handler) handleListProxyCache(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	q := r.URL.Query()
	pageSize := int32(0)
	if s := q.Get("page_size"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "page_size must be 1..100")
			return
		}
		pageSize = int32(n) //nolint:gosec // bounded above
	}

	req := &proxyv1.ListCachedManifestsRequest{
		TenantId:      tenantID,
		UpstreamId:    q.Get("upstream_id"),
		ImageContains: q.Get("image_contains"),
		PageToken:     q.Get("page_token"),
		PageSize:      pageSize,
	}
	resp, err := h.proxy.ListCachedManifests(r.Context(), req)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, s.Message())
				return
			case codes.PermissionDenied:
				writeError(w, http.StatusForbidden, s.Message())
				return
			}
		}
		slog.Error("ListCachedManifests", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to list cached manifests")
		return
	}

	out := listProxyCacheResponse{
		Manifests:     make([]cachedManifestResponse, 0, len(resp.GetManifests())),
		NextPageToken: resp.GetNextPageToken(),
	}
	for _, m := range resp.GetManifests() {
		out.Manifests = append(out.Manifests, toCachedManifestResponse(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/v1/proxy/cache/stats — page-header aggregate.
func (h *Handler) handleProxyCacheStats(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	stats, err := h.proxy.GetCacheStats(r.Context(), &proxyv1.GetCacheStatsRequest{TenantId: tenantID})
	if err != nil {
		slog.Error("GetCacheStats", "err", err, "tenant_id", tenantID)
		writeError(w, http.StatusInternalServerError, "failed to load cache stats")
		return
	}
	writeJSON(w, http.StatusOK, proxyCacheStatsResponse{
		TotalManifests:  stats.GetTotalManifests(),
		TotalBytes:      stats.GetTotalBytes(),
		UniqueUpstreams: stats.GetUniqueUpstreams(),
		TotalPulls:      stats.GetTotalPulls(),
	})
}

// GET /api/v1/proxy/cache/{id} — FUT-016 click-through detail page.
//
// Returns the full cached row + parsed layer/per-platform projection so
// the FE can render a Layers tab (image manifests) or Platforms tab
// (image-index / manifest-list) without re-parsing the body in TS.
// The raw body bytes are also surfaced base64-encoded for the
// "Manifest" tab raw-JSON viewer.
//
// Auth: workspace-admin (same gate as the list/stats/delete siblings).
// h.proxy nil ⇒ 404 to preserve the FE's "feature unavailable" probe.
func (h *Handler) handleGetProxyCacheDetail(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	detail, err := h.proxy.GetCachedManifest(r.Context(), &proxyv1.GetCachedManifestRequest{
		TenantId: tenantID,
		Id:       id,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "cached manifest not found")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, s.Message())
				return
			case codes.PermissionDenied:
				writeError(w, http.StatusForbidden, s.Message())
				return
			}
		}
		slog.Error("GetCachedManifest", "err", err, "tenant_id", tenantID, "id", id)
		writeError(w, http.StatusInternalServerError, "failed to load cached manifest")
		return
	}

	out := proxyCacheDetailResponse{
		cachedManifestResponse: toCachedManifestResponse(detail.GetManifest()),
		BodyBase64:             base64.StdEncoding.EncodeToString(detail.GetBody()),
		// Pre-allocate empty slices so the JSON never emits `null` — keeps
		// the FE's `.map()` and `.length` calls safe without ?? fallbacks.
		Layers:    []proxyCacheLayer{},
		Manifests: []proxyCacheManifestRef{},
	}

	// Parse the manifest body. If the body is missing or malformed we
	// still return the row + an "image" kind with empty layers — the FE
	// will show "No layers recorded" rather than an error page, which is
	// the right behaviour for an artifact / attestation manifest that
	// genuinely has no layers. A malformed body is rare enough that
	// failing the whole request would be more confusing than helpful.
	var parsed rawProxyManifest
	if len(detail.GetBody()) > 0 {
		if err := json.Unmarshal(detail.GetBody(), &parsed); err != nil {
			slog.Warn("proxy cache detail: manifest body parse",
				"err", err, "id", id, "digest", detail.GetManifest().GetDigest())
		}
	}

	// Discriminator. An image index carries `manifests[]`; an image
	// manifest carries `config` + `layers[]`. We pick on `manifests[]`
	// presence (not media type) so both Docker (manifest.list) and OCI
	// (image.index) shapes route the same way without a media-type
	// switch.
	if len(parsed.Manifests) > 0 {
		out.Kind = "index"
		for _, m := range parsed.Manifests {
			out.Manifests = append(out.Manifests, proxyCacheManifestRef{
				Digest:       m.Digest,
				Size:         m.Size,
				MediaType:    m.MediaType,
				Architecture: m.Platform.Architecture,
				OS:           m.Platform.OS,
				Variant:      m.Platform.Variant,
				OSVersion:    m.Platform.OSVersion,
			})
		}
	} else {
		out.Kind = "image"
		for _, l := range parsed.Layers {
			out.Layers = append(out.Layers, proxyCacheLayer{
				Digest:    l.Digest,
				Size:      l.Size,
				MediaType: l.MediaType,
			})
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// DELETE /api/v1/proxy/cache/{id} — evict a single cached manifest row.
// The underlying layer blobs are NOT freed here; the existing GC
// mark-sweep handles them. See the proto comment on
// DeleteCachedManifestRequest for the row-id-not-digest decision.
func (h *Handler) handleEvictProxyCache(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	if !h.requireDomainAdmin(r) {
		writeError(w, http.StatusForbidden, "workspace admin role required")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	_, err := h.proxy.DeleteCachedManifest(r.Context(), &proxyv1.DeleteCachedManifestRequest{
		TenantId: tenantID,
		Id:       id,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "cached manifest not found")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, s.Message())
				return
			}
		}
		slog.Error("DeleteCachedManifest", "err", err, "tenant_id", tenantID, "id", id)
		writeError(w, http.StatusInternalServerError, "failed to evict cached manifest")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toCachedManifestResponse projects a proto CachedManifest into the
// JSON shape the frontend consumes. Timestamps are RFC3339Nano because
// that's what the rest of the management REST API emits (audit feed,
// webhook deliveries, retention runs) — keeping the format consistent
// means the FE's existing date helpers work without per-route shims.
func toCachedManifestResponse(m *proxyv1.CachedManifest) cachedManifestResponse {
	out := cachedManifestResponse{
		ID:           m.GetId(),
		UpstreamID:   m.GetUpstreamId(),
		UpstreamName: m.GetUpstreamName(),
		Image:        m.GetImage(),
		Reference:    m.GetReference(),
		Digest:       m.GetDigest(),
		MediaType:    m.GetMediaType(),
		SizeBytes:    m.GetSizeBytes(),
		PullCount:    m.GetPullCount(),
	}
	if ft := m.GetFetchedAt(); ft != nil {
		out.FetchedAt = ft.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if lp := m.GetLastPulledAt(); lp != nil {
		s := lp.AsTime().UTC().Format(time.RFC3339Nano)
		out.LastPulledAt = &s
	}
	return out
}

// Silence the unused-import lint when none of the response helpers
// happen to take this — keeps the file's intent obvious. (json import
// is used implicitly by writeJSON; this is just a belt-and-suspenders
// guard for the package's existing pattern.)
var _ = json.Marshal
