// Package handler — chart.go
//
// Chart tab — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart
//
// Renders a Helm chart's Chart.yaml metadata + values.yaml for the dashboard.
// The BFF resolves the tag -> manifest via registry-metadata, reads the config
// + content-layer digests out of the manifest JSON, and fetches both blobs from
// registry-core over gRPC (CoreService.GetBlob). Helm-specific parsing lives in
// chartparse.go so this file stays a thin fetch/aggregate/serve layer, mirroring
// referrers.go. Authorization matches the sibling tag-detail routes (pull access
// via RequireAuth + findRepo). Returns 404 "route disabled" when the core client
// is nil so the FE can hide the Chart tab.
package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// chartTimeout bounds each outgoing registry-core GetBlob call (CLAUDE.md §6).
const chartTimeout = 5 * time.Second

// ChartResponse is the JSON body for GET …/tags/{tag}/chart. Metadata is null
// when the config blob is missing/unparseable (MetadataError explains why);
// Values is "" with a non-empty ValuesError when the content layer can't be
// read. The two halves fail independently.
type ChartResponse struct {
	Metadata        *ChartMetadata `json:"metadata"`
	MetadataError   string         `json:"metadata_error,omitempty"`
	Values          string         `json:"values"`
	ValuesTruncated bool           `json:"values_truncated"`
	ValuesError     string         `json:"values_error,omitempty"`
}

// handleGetChart resolves the tag's manifest, then fetches + parses the Helm
// config + content-layer blobs.
func (h *Handler) handleGetChart(w http.ResponseWriter, r *http.Request) {
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

	// GetManifest resolves a tag reference at the metadata repo layer and
	// returns the raw manifest JSON we need for the config + layer digests.
	mf, err := h.meta.GetManifest(r.Context(), &metadatav1.GetManifestRequest{
		RepoId:    repo.GetRepoId(),
		TenantId:  tenantID,
		Reference: tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	cfgDigest, cfgMediaType, contentDigest, err := parseManifestConfigAndLayer(mf.GetRawJson())
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable manifest")
		return
	}
	if cfgMediaType != helmConfigMediaType {
		writeError(w, http.StatusBadRequest, "not a Helm chart")
		return
	}

	resp := ChartResponse{}

	// --- metadata half (config blob) ---
	cfgBytes, cerr := h.fetchBlob(r.Context(), tenantID, cfgDigest, configBlobCap)
	if cerr != nil {
		resp.MetadataError = "could not read chart metadata"
		slog.Warn("chart: config blob", "err", cerr, "digest", cfgDigest)
	} else if meta, perr := parseChartMetadata(cfgBytes); perr != nil {
		resp.MetadataError = "could not parse Chart.yaml"
	} else {
		resp.Metadata = &meta
	}

	// --- values half (content layer) ---
	if contentDigest == "" {
		resp.ValuesError = "chart has no content layer"
	} else if tgz, verr := h.fetchBlob(r.Context(), tenantID, contentDigest, contentBlobCap); verr != nil {
		if status.Code(verr) == codes.FailedPrecondition {
			resp.ValuesTruncated = true
			resp.ValuesError = "chart archive too large to inspect"
		} else {
			resp.ValuesError = "could not read chart archive"
		}
	} else if vals, truncated, xerr := extractValuesYAML(tgz, valuesCap); xerr != nil {
		if errors.Is(xerr, errValuesNotFound) {
			resp.ValuesError = "no values.yaml in chart"
		} else {
			resp.ValuesError = "could not extract values.yaml"
		}
	} else {
		resp.Values = vals
		resp.ValuesTruncated = truncated
	}

	// Only hard-fail when BOTH halves failed to read (likely core is down).
	if resp.Metadata == nil && resp.Values == "" && resp.MetadataError != "" && resp.ValuesError != "" {
		writeError(w, http.StatusInternalServerError, "failed to read chart")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// fetchBlob calls registry-core GetBlob with an independent deadline and cap,
// returning the raw bytes. Errors are returned verbatim so the caller can
// distinguish FailedPrecondition (too large) from the rest.
func (h *Handler) fetchBlob(ctx context.Context, tenantID, digest string, maxBytes int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, chartTimeout)
	defer cancel()
	resp, err := h.core.GetBlob(ctx, &corev1.GetBlobRequest{
		TenantId: tenantID, Digest: digest, MaxBytes: maxBytes,
	})
	if err != nil {
		return nil, err
	}
	return bytes.Clone(resp.GetData()), nil
}
