// chart_download_test.go — tests for the Helm chart download route
// (GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart/download).
//
// Reuses the referrersTestEnv harness (fake auth/meta/audit + a fake
// registry-core client) from referrers_test.go and the helm-manifest JSON
// style + digest globals (cfgDigest/contentDigest) from chart_test.go. The
// fake core's GetBlobStream (referrers_test.go) sends the canned blob in two
// chunks so these tests exercise multi-chunk reassembly.
package handler_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const chartDownloadPath = "/api/v1/repositories/myorg/myrepo/tags/v1.0/chart/download"

// TestHandleDownloadChart_nilCore_404 — a Handler whose core client is nil must
// 404 "route disabled", mirroring the chart-detail gate.
func TestHandleDownloadChart_nilCore_404(t *testing.T) {
	env := newReferrersTestEnv(t, false)
	setManifest(t, helmManifestJSON(), nil)

	resp := env.get(t, chartDownloadPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when core unset, got %d", resp.StatusCode)
	}
}

// TestHandleDownloadChart_notHelm_400 — an OCI image manifest (non-Helm config
// mediaType) is rejected with 400 before any blob stream is opened.
func TestHandleDownloadChart_notHelm_400(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	ociManifest := `{"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + cfgDigest +
		`"},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"` + contentDigest + `"}]}`
	setManifest(t, []byte(ociManifest), nil)

	resp := env.get(t, chartDownloadPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-Helm manifest, got %d", resp.StatusCode)
	}
}

// TestHandleDownloadChart_noContentLayer_400 — a Helm config manifest whose
// layers[] carries no Helm content layer has nothing to stream, so 400.
func TestHandleDownloadChart_noContentLayer_400(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	noLayerManifest := `{"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"` + cfgDigest + `"},"layers":[]}`
	setManifest(t, []byte(noLayerManifest), nil)

	resp := env.get(t, chartDownloadPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for manifest with no content layer, got %d", resp.StatusCode)
	}
}

// TestHandleDownloadChart_helm_streamsBytes — a well-formed Helm manifest whose
// content layer is seeded in the fake core streams back byte-identical, with
// the download headers set.
func TestHandleDownloadChart_helm_streamsBytes(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)

	tgz := []byte("PK\x03\x04this-is-a-fake-chart-tgz-payload-\x00\x01\x02\x03")
	env.core.blobs = map[string][]byte{contentDigest: tgz}

	resp := env.get(t, chartDownloadPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type: got %q, want application/gzip", ct)
	}
	// repo "myrepo" + tag "v1.0" -> filename "myrepo-v1.0.tgz".
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="myrepo-v1.0.tgz"`) {
		t.Errorf("Content-Disposition: got %q, want to contain filename=\"myrepo-v1.0.tgz\"", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, tgz) {
		t.Errorf("body bytes: got %d bytes, want %d matching the canned .tgz", len(body), len(tgz))
	}
}

// TestHandleDownloadChart_tagNotFound_404 — when metadata's GetManifest errors
// (tag/manifest does not exist), the handler maps it to a 404 before any blob
// stream is opened.
func TestHandleDownloadChart_tagNotFound_404(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, nil, status.Error(codes.NotFound, "no such tag"))

	resp := env.get(t, chartDownloadPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when tag/manifest not found, got %d", resp.StatusCode)
	}
}

// TestHandleDownloadChart_coreNotFound_404 — the content blob is unknown to
// registry-core (NotFound on the first stream recv); the handler maps it to a
// clean 404 rather than a truncated 200.
func TestHandleDownloadChart_coreNotFound_404(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)
	env.core.blobErrs = map[string]error{
		contentDigest: status.Error(codes.NotFound, "gone"),
	}

	resp := env.get(t, chartDownloadPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when content blob missing, got %d", resp.StatusCode)
	}
}
