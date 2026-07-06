// chart_test.go — tests for the Helm chart-detail route
// (GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart).
//
// These reuse the referrersTestEnv harness (fake auth/meta/audit + a fake
// registry-core client) from referrers_test.go so tenant/token/repo
// resolution matches every other suite (repo "myorg/myrepo"). The metadata
// fake's GetManifest is defined here (fakeMetaServer otherwise inherits the
// UnimplementedMetadataServiceServer stub) and driven per-case through the
// getManifestRawJSON / getManifestErr package globals, mirroring the
// getTagErr / scanSBOMOverride hook pattern used elsewhere in the suite.
package handler_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// buildChartTGZ builds a gzip+tar chart archive (path -> content). A local copy
// lives here because makeChartTGZ sits in the internal `package handler` test
// files and isn't visible from this external `package handler_test` suite.
func buildChartTGZ(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return gzBuf.Bytes()
}

// getManifestRawJSON / getManifestErr drive the fakeMetaServer.GetManifest
// response for a single test. Set them in-test and reset via t.Cleanup so
// cases don't leak canned manifests into one another.
var (
	getManifestRawJSON []byte
	getManifestErr     error
)

// GetManifest is the chart route's tag -> manifest resolution point. It returns
// the raw manifest JSON seeded by the running test (or getManifestErr when set).
func (s *fakeMetaServer) GetManifest(_ context.Context, _ *metadatav1.GetManifestRequest) (*metadatav1.Manifest, error) {
	if getManifestErr != nil {
		return nil, getManifestErr
	}
	return &metadatav1.Manifest{RawJson: getManifestRawJSON}, nil
}

// setManifest seeds the GetManifest canned response for one test and registers
// cleanup that clears both hooks.
func setManifest(t *testing.T, rawJSON []byte, err error) {
	t.Helper()
	getManifestRawJSON = rawJSON
	getManifestErr = err
	t.Cleanup(func() {
		getManifestRawJSON = nil
		getManifestErr = nil
	})
}

const chartPath = "/api/v1/repositories/myorg/myrepo/tags/v1.0/chart"

// Valid sha256 digests for the config + content blobs (64 hex chars each).
var (
	cfgDigest     = "sha256:" + strings.Repeat("a", 64)
	contentDigest = "sha256:" + strings.Repeat("b", 64)
)

// helmManifestJSON builds a Helm-on-OCI image manifest referencing the config +
// content-layer digests above.
func helmManifestJSON() []byte {
	return []byte(`{"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"` + cfgDigest +
		`"},"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip","digest":"` + contentDigest + `"}]}`)
}

// TestHandleGetChart_nilCore_404 — a Handler whose core client is nil must 404
// "route disabled" so the FE hides the Chart tab, mirroring the referrers gate.
func TestHandleGetChart_nilCore_404(t *testing.T) {
	env := newReferrersTestEnv(t, false)
	setManifest(t, helmManifestJSON(), nil)

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when core unset, got %d", resp.StatusCode)
	}
}

// TestHandleGetChart_notHelm_400 — an OCI image manifest (non-Helm config
// mediaType) is rejected with 400 before any blob fetch.
func TestHandleGetChart_notHelm_400(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	ociManifest := `{"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + cfgDigest +
		`"},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"` + contentDigest + `"}]}`
	setManifest(t, []byte(ociManifest), nil)

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-Helm manifest, got %d", resp.StatusCode)
	}
}

// TestHandleGetChart_helm_happyPath — a well-formed Helm manifest with a config
// blob + a content-layer .tgz yields 200 with parsed metadata + values.yaml.
func TestHandleGetChart_helm_happyPath(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)

	env.core.blobs = map[string][]byte{
		cfgDigest:     []byte(`{"name":"web","version":"1.0.0","appVersion":"2.0.0"}`),
		contentDigest: buildChartTGZ(t, map[string]string{"web/values.yaml": "replicaCount: 1\n"}),
	}

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.ChartResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Metadata == nil {
		t.Fatalf("expected metadata, got nil (err=%q)", body.MetadataError)
	}
	if body.Metadata.Name != "web" {
		t.Errorf("metadata name: got %q, want web", body.Metadata.Name)
	}
	if body.Metadata.AppVersion != "2.0.0" {
		t.Errorf("metadata app_version: got %q, want 2.0.0", body.Metadata.AppVersion)
	}
	if !strings.Contains(body.Values, "replicaCount: 1") {
		t.Errorf("values: got %q, want to contain replicaCount: 1", body.Values)
	}
	if body.ValuesError != "" {
		t.Errorf("unexpected values_error: %q", body.ValuesError)
	}
}

// TestHandleGetChart_malformedConfig_metadataError — a garbage config blob but a
// valid content .tgz: metadata fails independently (nil + error string) while
// values still populate. The two halves fail independently, so overall 200.
func TestHandleGetChart_malformedConfig_metadataError(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)

	env.core.blobs = map[string][]byte{
		cfgDigest:     []byte("this is not json"),
		contentDigest: buildChartTGZ(t, map[string]string{"web/values.yaml": "replicaCount: 3\n"}),
	}

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.ChartResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Metadata != nil {
		t.Errorf("expected nil metadata for malformed config, got %+v", body.Metadata)
	}
	if body.MetadataError == "" {
		t.Errorf("expected non-empty metadata_error")
	}
	if !strings.Contains(body.Values, "replicaCount: 3") {
		t.Errorf("values: got %q, want to contain replicaCount: 3", body.Values)
	}
}

// TestHandleGetChart_coreDown_500 — when the config-blob fetch hits a transport
// error (core Unavailable) both halves are unreadable, so the route hard-fails
// with 500. Uses the global blobErr so every GetBlob call fails.
func TestHandleGetChart_coreDown_500(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)
	env.core.blobErr = status.Error(codes.Unavailable, "down")

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 when core is down, got %d", resp.StatusCode)
	}
}

// TestHandleGetChart_valuesFails_metadataOK — the config blob parses fine but
// the content-layer fetch fails (non-FailedPrecondition): metadata still
// populates while values fail independently, so overall 200.
func TestHandleGetChart_valuesFails_metadataOK(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)

	env.core.blobs = map[string][]byte{
		cfgDigest: []byte(`{"name":"web","version":"1.0.0"}`),
	}
	env.core.blobErrs = map[string]error{
		contentDigest: status.Error(codes.Internal, "boom"),
	}

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.ChartResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Metadata == nil {
		t.Fatalf("expected metadata, got nil (err=%q)", body.MetadataError)
	}
	if body.ValuesError == "" {
		t.Errorf("expected non-empty values_error")
	}
	if body.Values != "" {
		t.Errorf("expected empty values, got %q", body.Values)
	}
}

// TestHandleGetChart_contentTooLarge_truncated — a FailedPrecondition on the
// content-layer fetch (blob exceeds cap) marks values as truncated with an
// error note, still returning 200 with the metadata half intact.
func TestHandleGetChart_contentTooLarge_truncated(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	setManifest(t, helmManifestJSON(), nil)

	env.core.blobs = map[string][]byte{
		cfgDigest: []byte(`{"name":"web","version":"1.0.0"}`),
	}
	env.core.blobErrs = map[string]error{
		contentDigest: status.Error(codes.FailedPrecondition, "too big"),
	}

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.ChartResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.ValuesTruncated {
		t.Errorf("expected values_truncated=true")
	}
	if body.ValuesError == "" {
		t.Errorf("expected non-empty values_error")
	}
}

// TestHandleGetChart_noContentLayer — a Helm manifest with no helm content
// layer yields "chart has no content layer" for values while metadata renders.
func TestHandleGetChart_noContentLayer(t *testing.T) {
	env := newReferrersTestEnv(t, true)
	noLayerManifest := `{"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"` + cfgDigest + `"},"layers":[]}`
	setManifest(t, []byte(noLayerManifest), nil)

	env.core.blobs = map[string][]byte{
		cfgDigest: []byte(`{"name":"web","version":"1.0.0"}`),
	}

	resp := env.get(t, chartPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.ChartResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Metadata == nil {
		t.Fatalf("expected metadata, got nil (err=%q)", body.MetadataError)
	}
	if body.ValuesError != "chart has no content layer" {
		t.Errorf("values_error: got %q, want %q", body.ValuesError, "chart has no content layer")
	}
}
