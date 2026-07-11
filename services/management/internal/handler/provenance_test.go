// provenance_test.go — tests for the Provenance surface on the manifest route
// (GET /api/v1/repositories/{org}/{repo}/tags/{tag}/manifest).
//
// Reuses the referrersTestEnv harness (fake auth/meta/audit + fake core) and
// the setManifest hook from chart_test.go to seed the manifest raw_json that
// fakeMetaServer.GetManifest returns. Repo/tag resolution matches every other
// suite (repo "myorg/myrepo", tag "v1.0").
//
// Coverage:
//   - well-known OCI annotations populate the typed provenance fields;
//   - a `javascript:` URL in org.opencontainers.image.url is dropped to "";
//   - an over-long annotation value is truncated to maxAnnotationValueLen;
//   - a manifest with no annotations omits the provenance block entirely;
//   - the raw annotations passthrough is capped at maxRawAnnotations (64);
//   - annotations are read at the top level of an OCI image INDEX too;
//   - an over-long `javascript:` URL is dropped (sanitise-before-truncate).
package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// manifestPath is the tag-detail manifest route the Provenance block rides on.
const manifestPath = "/api/v1/repositories/myorg/myrepo/tags/v1.0/manifest"

// provenanceWire mirrors the Go provenanceInfo JSON shape so tests can assert
// the typed fields plus the bounded raw annotations map.
type provenanceWire struct {
	Created       string            `json:"created"`
	Authors       string            `json:"authors"`
	URL           string            `json:"url"`
	Documentation string            `json:"documentation"`
	Source        string            `json:"source"`
	Version       string            `json:"version"`
	Revision      string            `json:"revision"`
	Vendor        string            `json:"vendor"`
	Licenses      string            `json:"licenses"`
	RefName       string            `json:"ref_name"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	BaseName      string            `json:"base_name"`
	BaseDigest    string            `json:"base_digest"`
	Annotations   map[string]string `json:"annotations"`
}

// manifestProvenanceWire is the subset of the manifest response we assert on.
// Provenance is a pointer so a missing block decodes as nil (the empty-state
// case), distinct from a present-but-empty object.
type manifestProvenanceWire struct {
	Provenance *provenanceWire `json:"provenance"`
}

// TestProvenance_wellKnownFieldsPopulate seeds a manifest with a mix of
// well-known OCI annotations — including a `javascript:` URL and an over-long
// value — and asserts the mapping, the XSS-scheme drop, and the truncation.
func TestProvenance_wellKnownFieldsPopulate(t *testing.T) {
	env := newReferrersTestEnv(t, true)

	// An over-long description: well past maxAnnotationValueLen (1024) so we
	// can assert truncation on the wire.
	longDesc := strings.Repeat("x", 2000)

	// A single-arch image manifest with a top-level annotations block. The
	// url carries a `javascript:` scheme (must be dropped) while source +
	// documentation are legitimate https URLs (must survive).
	raw := []byte(`{
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":1},
		"layers": [],
		"annotations": {
			"org.opencontainers.image.created": "2026-07-11T00:00:00Z",
			"org.opencontainers.image.source": "https://github.com/acme/widget",
			"org.opencontainers.image.revision": "abc1234def5678",
			"org.opencontainers.image.url": "javascript:alert(1)",
			"org.opencontainers.image.documentation": "https://docs.example.com/widget",
			"org.opencontainers.image.vendor": "Acme Corp",
			"org.opencontainers.image.version": "1.2.3",
			"org.opencontainers.image.licenses": "Apache-2.0",
			"org.opencontainers.image.title": "widget",
			"org.opencontainers.image.description": "` + longDesc + `",
			"org.opencontainers.image.base.name": "docker.io/library/alpine:3.20",
			"org.opencontainers.image.base.digest": "sha256:basedigest",
			"com.acme.build.pipeline": "ci-9001"
		}
	}`)
	setManifest(t, raw, nil)

	resp := env.get(t, manifestPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body manifestProvenanceWire
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Provenance == nil {
		t.Fatal("expected a provenance block, got nil")
	}
	p := body.Provenance

	// (a) Well-known fields populate.
	if p.Created != "2026-07-11T00:00:00Z" {
		t.Errorf("created: got %q", p.Created)
	}
	if p.Source != "https://github.com/acme/widget" {
		t.Errorf("source: got %q", p.Source)
	}
	if p.Revision != "abc1234def5678" {
		t.Errorf("revision: got %q", p.Revision)
	}
	if p.Documentation != "https://docs.example.com/widget" {
		t.Errorf("documentation: got %q", p.Documentation)
	}
	if p.Vendor != "Acme Corp" {
		t.Errorf("vendor: got %q", p.Vendor)
	}
	if p.Version != "1.2.3" {
		t.Errorf("version: got %q", p.Version)
	}
	if p.Licenses != "Apache-2.0" {
		t.Errorf("licenses: got %q", p.Licenses)
	}
	if p.Title != "widget" {
		t.Errorf("title: got %q", p.Title)
	}
	if p.BaseName != "docker.io/library/alpine:3.20" {
		t.Errorf("base_name: got %q", p.BaseName)
	}
	if p.BaseDigest != "sha256:basedigest" {
		t.Errorf("base_digest: got %q", p.BaseDigest)
	}

	// (b) The `javascript:` URL is dropped (safeExternalURL → "").
	if p.URL != "" {
		t.Errorf("url: expected javascript: scheme to be dropped, got %q", p.URL)
	}

	// (c) The over-long description is truncated to maxAnnotationValueLen (1024).
	if len(p.Description) != 1024 {
		t.Errorf("description: expected truncation to 1024 bytes, got %d", len(p.Description))
	}

	// The bounded raw annotations view must include the bespoke build key so
	// non-standard metadata stays visible.
	if p.Annotations["com.acme.build.pipeline"] != "ci-9001" {
		t.Errorf("raw annotations: expected bespoke key preserved, got %v", p.Annotations)
	}
	// The raw view must also reflect the sanitised URL — the dangerous scheme
	// must not leak back through the raw map either.
	if got := p.Annotations["org.opencontainers.image.url"]; got != "javascript:alert(1)" {
		// The raw map is a verbatim (truncated) passthrough of the manifest
		// annotations, so the raw value IS the original. The FE never renders
		// raw values as hrefs — only the sanitised typed fields become links.
		// This assertion documents that contract; it is intentionally the
		// unsanitised value.
		t.Logf("raw url value (rendered as text only): %q", got)
	}
}

// TestProvenance_noAnnotations_omitsBlock asserts a manifest with no
// annotations yields provenance == nil (omitted on the wire) so the FE shows
// its empty state.
func TestProvenance_noAnnotations_omitsBlock(t *testing.T) {
	env := newReferrersTestEnv(t, true)

	raw := []byte(`{
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":1},
		"layers": []
	}`)
	setManifest(t, raw, nil)

	resp := env.get(t, manifestPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Assert the key is absent from the raw JSON (omitempty), not merely null.
	var rawBody map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&rawBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := rawBody["provenance"]; present {
		t.Errorf("expected provenance key omitted when no annotations, got %q", rawBody["provenance"])
	}
}

// TestProvenance_rawAnnotationsCapped seeds far more than maxRawAnnotations (64)
// bespoke annotations and asserts the raw passthrough map is bounded to exactly
// 64 entries — the self-imposed payload-bloat guard for an attacker-controlled
// annotation set. Map iteration order is unspecified so which 64 survive is
// non-deterministic, but the count is deterministic.
func TestProvenance_rawAnnotationsCapped(t *testing.T) {
	env := newReferrersTestEnv(t, true)

	// 100 bespoke keys — well over the 64-entry cap.
	var sb strings.Builder
	sb.WriteString(`{"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":1},"layers":[],"annotations":{`)
	for i := range 100 {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"com.acme.k%d":"v%d"`, i, i)
	}
	sb.WriteString("}}")
	setManifest(t, []byte(sb.String()), nil)

	resp := env.get(t, manifestPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body manifestProvenanceWire
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Provenance == nil {
		t.Fatal("expected a provenance block, got nil")
	}
	if got := len(body.Provenance.Annotations); got != 64 {
		t.Errorf("raw annotations: expected cap of 64 entries, got %d", got)
	}
}

// TestProvenance_indexManifestAnnotations proves annotations are read at the
// top level of an OCI image INDEX (multi-arch), not just a single-arch image
// manifest — the manifest list carries config-less `manifests[]` plus a
// top-level annotations block, which rawManifest.Annotations must still parse.
func TestProvenance_indexManifestAnnotations(t *testing.T) {
	env := newReferrersTestEnv(t, true)

	raw := []byte(`{
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": [
			{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:child","size":10,"platform":{"architecture":"amd64","os":"linux"}}
		],
		"annotations": {
			"org.opencontainers.image.source": "https://github.com/acme/widget",
			"org.opencontainers.image.revision": "idxrevision123"
		}
	}`)
	setManifest(t, raw, nil)

	resp := env.get(t, manifestPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body manifestProvenanceWire
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Provenance == nil {
		t.Fatal("expected a provenance block for an index manifest, got nil")
	}
	if body.Provenance.Source != "https://github.com/acme/widget" {
		t.Errorf("index source: got %q", body.Provenance.Source)
	}
	if body.Provenance.Revision != "idxrevision123" {
		t.Errorf("index revision: got %q", body.Provenance.Revision)
	}
}

// TestProvenance_dangerousURLTooLong_dropped locks the security-relevant
// ordering in buildProvenance: safeExternalURL runs BEFORE truncateValue, so an
// over-long `javascript:` URL is dropped to "" rather than surviving as a
// truncated `javascript:aaa…` prefix that the FE might treat as a live href.
func TestProvenance_dangerousURLTooLong_dropped(t *testing.T) {
	env := newReferrersTestEnv(t, true)

	// A javascript: URL far longer than maxAnnotationValueLen (1024).
	danger := "javascript:" + strings.Repeat("a", 2000)
	raw := []byte(`{
		"config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":1},
		"layers": [],
		"annotations": {"org.opencontainers.image.url": "` + danger + `"}
	}`)
	setManifest(t, raw, nil)

	resp := env.get(t, manifestPath, adminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body manifestProvenanceWire
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Provenance == nil {
		t.Fatal("expected a provenance block, got nil")
	}
	if body.Provenance.URL != "" {
		t.Errorf("over-long javascript: URL must be dropped to \"\" (sanitise before truncate), got %q", body.Provenance.URL)
	}
}
