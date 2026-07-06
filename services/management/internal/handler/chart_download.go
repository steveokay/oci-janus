// Package handler — chart_download.go
//
// Chart download — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart/download
//
// Streams a Helm chart's packaged content layer (.tgz) to the browser, byte-
// identical to `helm pull`. Resolves the tag -> manifest -> Helm content-layer
// digest (reusing chartparse.go), then streams registry-core's GetBlobStream
// straight to the HTTP response. Auth + repo gate match the chart route. Lives
// in its own file so concurrent edits don't collide with chart.go.
package handler

import (
	"io"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// handleDownloadChart streams the tag's Helm chart .tgz to the client.
func (h *Handler) handleDownloadChart(w http.ResponseWriter, r *http.Request) {
	if h.core == nil {
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

	mf, err := h.meta.GetManifest(r.Context(), &metadatav1.GetManifestRequest{
		RepoId:    repo.GetRepoId(),
		TenantId:  tenantID,
		Reference: tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	_, cfgMediaType, contentDigest, err := parseManifestConfigAndLayer(mf.GetRawJson())
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable manifest")
		return
	}
	if cfgMediaType != helmConfigMediaType || contentDigest == "" {
		writeError(w, http.StatusBadRequest, "not a Helm chart")
		return
	}

	// Stream with the request context directly. Do NOT wrap in a short timeout:
	// GetBlobStream is a server stream, the client DeadlineInterceptor is
	// unary-only, and a large chart download must not be cut off.
	stream, err := h.core.GetBlobStream(r.Context(), &corev1.GetBlobRequest{
		TenantId: tenantID,
		Digest:   contentDigest,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to download chart")
		return
	}

	// Read the first chunk before writing headers so a NotFound / error maps to
	// a clean HTTP status instead of a truncated 200.
	first, err := stream.Recv()
	if err != nil && err != io.EOF {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "chart blob not found")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, "invalid download request")
				return
			}
		}
		writeError(w, http.StatusBadGateway, "failed to download chart")
		return
	}

	// repoName + tagName are already allowlist-validated, so the header value
	// can't carry CR/LF or quotes.
	filename := repoName + "-" + tagName + ".tgz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)

	if first != nil {
		if _, werr := w.Write(first.GetData()); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err == io.EOF {
		return // empty single-shot stream
	}

	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Headers already sent — can't change the status; log + truncate.
			slog.Error("chart download: mid-stream read failed", "err", rerr, "digest", contentDigest)
			return
		}
		if _, werr := w.Write(chunk.GetData()); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
