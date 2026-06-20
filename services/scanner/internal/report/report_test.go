// Package report — unit tests for the SBOM + PDF renderers. No I/O or DB
// dependency; just assert the byte shape is valid enough for downstream
// tools (a JSON decoder and a PDF reader header check).
package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestRenderSBOM_isValidJSON verifies the SBOM bytes parse as JSON with the
// minimum SPDX fields populated.
func TestRenderSBOM_isValidJSON(t *testing.T) {
	doc := Document{
		TenantID:    "11111111-1111-1111-1111-111111111111",
		GeneratedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
		Findings: []Finding{
			{CVEID: "CVE-2024-1234", Severity: "HIGH", PackageName: "openssl", Version: "1.0.0", FixedIn: "1.0.1"},
		},
	}
	b, err := RenderSBOM(doc)
	if err != nil {
		t.Fatalf("RenderSBOM: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("SBOM not valid JSON: %v", err)
	}
	// Minimal SPDX shape — every field below is required by the spec.
	for _, k := range []string{"spdxVersion", "dataLicense", "SPDXID", "documentName", "packages"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing required SPDX field %q", k)
		}
	}
	if m["spdxVersion"] != "SPDX-2.3" {
		t.Errorf("spdxVersion: got %v, want SPDX-2.3", m["spdxVersion"])
	}
}

// TestRenderPDF_startsWithMagic verifies the rendered PDF begins with the
// standard %PDF- header — every reader sniffs the first 5 bytes.
func TestRenderPDF_startsWithMagic(t *testing.T) {
	doc := Document{
		TenantID:    "tenant-x",
		GeneratedAt: time.Now().UTC(),
	}
	sbom := []byte(`{"spdxVersion":"SPDX-2.3"}`)
	pdf, err := RenderPDF(doc, sbom)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("PDF header missing — got %q", string(pdf[:8]))
	}
	if !strings.HasSuffix(string(pdf), "%%EOF\n") {
		t.Error("PDF trailer missing")
	}
}

// TestSanitizeSPDXID_replacesInvalidChars verifies the helper drops chars
// outside the SPDXID allowlist.
func TestSanitizeSPDXID_replacesInvalidChars(t *testing.T) {
	out := sanitizeSPDXID("abc/def!ghi")
	for _, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '.', r == '-':
			// allowed
		default:
			t.Errorf("invalid char %q remained in output %q", r, out)
		}
	}
}
