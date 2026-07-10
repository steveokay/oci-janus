package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// makeChartTGZ builds a gzip+tar chart archive with the given files
// (path -> content) for the extractValuesYAML tests.
func makeChartTGZ(t *testing.T, files map[string]string) []byte {
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

func TestParseManifestConfigAndLayer_helm(t *testing.T) {
	raw := []byte(`{
		"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":100},
		"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip","digest":"sha256:` + strings.Repeat("b", 64) + `","size":2048}]
	}`)
	cfgDigest, cfgMT, contentDigest, err := parseManifestConfigAndLayer(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfgMT != helmConfigMediaType {
		t.Fatalf("cfgMT=%q", cfgMT)
	}
	if cfgDigest != "sha256:"+strings.Repeat("a", 64) || contentDigest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("digests: %q %q", cfgDigest, contentDigest)
	}
}

func TestParseManifestConfigAndLayer_notHelm(t *testing.T) {
	raw := []byte(`{"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `"},"layers":[]}`)
	_, cfgMT, _, err := parseManifestConfigAndLayer(raw)
	if err != nil {
		t.Fatalf("parse should not error on non-helm: %v", err)
	}
	if cfgMT == helmConfigMediaType {
		t.Fatal("expected non-helm mediaType")
	}
}

func TestParseChartMetadata_full(t *testing.T) {
	cfg := []byte(`{"name":"myapp","version":"1.2.3","appVersion":"2.0.0","description":"d","apiVersion":"v2","type":"application","kubeVersion":">=1.24.0","home":"https://h","keywords":["web"],"sources":["https://s"],"maintainers":[{"name":"Ada","email":"a@x.io"}],"dependencies":[{"name":"pg","version":"12.x","repository":"oci://r"}],"annotations":{"category":"DB"}}`)
	m, err := parseChartMetadata(cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "myapp" || m.Version != "1.2.3" || m.AppVersion != "2.0.0" {
		t.Fatalf("meta: %+v", m)
	}
	if len(m.Maintainers) != 1 || m.Maintainers[0].Name != "Ada" {
		t.Fatalf("maintainers: %+v", m.Maintainers)
	}
	if len(m.Dependencies) != 1 || m.Dependencies[0].Repository != "oci://r" {
		t.Fatalf("deps: %+v", m.Dependencies)
	}
}

func TestParseChartMetadata_garbage(t *testing.T) {
	if _, err := parseChartMetadata([]byte("not json")); err == nil {
		t.Fatal("expected error on garbage config")
	}
}

func TestExtractValuesYAML_root(t *testing.T) {
	tgz := makeChartTGZ(t, map[string]string{
		"myapp/Chart.yaml":             "name: myapp",
		"myapp/values.yaml":            "replicaCount: 1\n",
		"myapp/charts/sub/values.yaml": "subchart: true\n",
	})
	got, truncated, err := extractValuesYAML(tgz)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if truncated || got != "replicaCount: 1\n" {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}

func TestExtractValuesYAML_subchartOnly_notFound(t *testing.T) {
	tgz := makeChartTGZ(t, map[string]string{
		"myapp/charts/sub/values.yaml": "subchart: true\n",
	})
	_, _, err := extractValuesYAML(tgz)
	if err == nil {
		t.Fatal("expected not-found when only a subchart values.yaml exists")
	}
}

func TestExtractValuesYAML_truncated(t *testing.T) {
	big := strings.Repeat("a: 1\n", 100000) // > valuesCap
	tgz := makeChartTGZ(t, map[string]string{"myapp/values.yaml": big})
	got, truncated, err := extractValuesYAML(tgz)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !truncated || len(got) != valuesCap {
		t.Fatalf("truncated=%v len=%d", truncated, len(got))
	}
}

func TestExtractValuesYAML_badGzip(t *testing.T) {
	if _, _, err := extractValuesYAML([]byte("not gzip")); err == nil {
		t.Fatal("expected error on non-gzip input")
	}
}

// TestParseChartMetadata_dropsUnsafeURLs verifies that non-http(s) URL fields
// (javascript:/data:) are stripped so they never reach the FE as anchor hrefs.
func TestParseChartMetadata_dropsUnsafeURLs(t *testing.T) {
	cfg := []byte(`{"home":"javascript:alert(1)","icon":"http://ok/i.png","sources":["javascript:evil","https://ok"],"maintainers":[{"name":"A","url":"data:x"}]}`)
	m, err := parseChartMetadata(cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Home != "" {
		t.Errorf("home: got %q, want empty (javascript: stripped)", m.Home)
	}
	if m.Icon != "http://ok/i.png" {
		t.Errorf("icon: got %q, want http://ok/i.png", m.Icon)
	}
	if len(m.Sources) != 1 || m.Sources[0] != "https://ok" {
		t.Errorf("sources: got %v, want [https://ok]", m.Sources)
	}
	if len(m.Maintainers) != 1 || m.Maintainers[0].URL != "" {
		t.Errorf("maintainer url: got %q, want empty (data: stripped)", m.Maintainers[0].URL)
	}
}

// TestExtractValuesYAML_decompressionBounded verifies the maxDecompressedBytes
// bound truncates the gzip stream before an oversized leading entry is skipped,
// surfacing an error rather than decompressing unboundedly.
func TestExtractValuesYAML_decompressionBounded(t *testing.T) {
	orig := maxDecompressedBytes
	defer func() { maxDecompressedBytes = orig }()
	maxDecompressedBytes = 32

	tgz := makeChartTGZ(t, map[string]string{
		// A leading entry whose body exceeds the 32-byte bound, so the tar
		// reader hits the truncated gzip stream before reaching values.yaml.
		"myapp/big.txt":     strings.Repeat("x", 4096),
		"myapp/values.yaml": "replicaCount: 1\n",
	})
	if _, _, err := extractValuesYAML(tgz); err == nil {
		t.Fatal("expected error when the decompression bound truncates the stream")
	}
}

// TestExtractValuesYAML_traversalIgnored verifies a directory-traversal entry
// ("../evil/values.yaml") is skipped and the chart-root values.yaml is used.
func TestExtractValuesYAML_traversalIgnored(t *testing.T) {
	tgz := makeChartTGZ(t, map[string]string{
		"../evil/values.yaml": "pwned: true\n",
		"myapp/values.yaml":   "replicaCount: 1\n",
	})
	got, _, err := extractValuesYAML(tgz)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "replicaCount: 1\n" {
		t.Fatalf("got %q, want the chart-root values.yaml", got)
	}
}
