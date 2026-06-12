// Package worker_test tests pure helper functions in the scan worker.
// No gRPC connections, databases, or RabbitMQ brokers are required.
package worker

import (
	"encoding/json"
	"testing"

	"github.com/steveokay/oci-janus/libs/scanner/plugin"
)

// TestHasPolicyViolation_criticalTriggersBlock verifies that CRITICAL findings
// cause a policy violation per CLAUDE.md §4.7.
func TestHasPolicyViolation_criticalTriggersBlock(t *testing.T) {
	result := &plugin.ScanResult{
		SeverityCounts: map[string]int{
			"CRITICAL": 1,
			"HIGH":     0,
			"MEDIUM":   3,
		},
	}
	if !hasPolicyViolation(result) {
		t.Error("expected policy violation for CRITICAL findings")
	}
}

// TestHasPolicyViolation_highTriggersBlock verifies that HIGH findings alone
// also trigger a policy violation.
func TestHasPolicyViolation_highTriggersBlock(t *testing.T) {
	result := &plugin.ScanResult{
		SeverityCounts: map[string]int{
			"CRITICAL": 0,
			"HIGH":     2,
		},
	}
	if !hasPolicyViolation(result) {
		t.Error("expected policy violation for HIGH findings")
	}
}

// TestHasPolicyViolation_mediumOnlyNoBlock verifies that MEDIUM severity alone
// does not trigger a policy block.
func TestHasPolicyViolation_mediumOnlyNoBlock(t *testing.T) {
	result := &plugin.ScanResult{
		SeverityCounts: map[string]int{
			"CRITICAL": 0,
			"HIGH":     0,
			"MEDIUM":   10,
			"LOW":      5,
		},
	}
	if hasPolicyViolation(result) {
		t.Error("MEDIUM-only findings should not trigger a policy violation")
	}
}

// TestHasPolicyViolation_emptyCountsNoBlock verifies that an empty severity
// map (clean scan) does not trigger a block.
func TestHasPolicyViolation_emptyCountsNoBlock(t *testing.T) {
	result := &plugin.ScanResult{
		SeverityCounts: map[string]int{},
	}
	if hasPolicyViolation(result) {
		t.Error("clean scan result should not trigger a policy violation")
	}
}

// TestHasPolicyViolation_nilCounts verifies robustness when SeverityCounts is nil.
func TestHasPolicyViolation_nilCounts(t *testing.T) {
	result := &plugin.ScanResult{} // SeverityCounts defaults to nil map
	if hasPolicyViolation(result) {
		t.Error("nil severity counts should not trigger a policy violation")
	}
}

// TestMarshalFindings_roundTrip verifies that marshalFindings produces valid JSON
// that can be unmarshalled back to the original slice.
func TestMarshalFindings_roundTrip(t *testing.T) {
	result := &plugin.ScanResult{
		Findings: []plugin.Finding{
			{
				CVE:      "CVE-2024-12345",
				Severity: "CRITICAL",
				Package:  "openssl",
				Version:  "1.0.2",
				FixedIn:  "1.0.3",
			},
			{
				CVE:      "CVE-2024-99999",
				Severity: "HIGH",
				Package:  "libz",
				Version:  "1.2.11",
			},
		},
	}

	data := marshalFindings(result)
	if len(data) == 0 {
		t.Fatal("marshalFindings returned empty bytes")
	}

	// Round-trip: unmarshal back and compare CVEs.
	var got []plugin.Finding
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal findings: %v", err)
	}
	if len(got) != len(result.Findings) {
		t.Fatalf("expected %d findings, got %d", len(result.Findings), len(got))
	}
	for i, f := range got {
		if f.CVE != result.Findings[i].CVE {
			t.Errorf("finding[%d].CVE: got %q, want %q", i, f.CVE, result.Findings[i].CVE)
		}
		if f.Severity != result.Findings[i].Severity {
			t.Errorf("finding[%d].Severity: got %q, want %q", i, f.Severity, result.Findings[i].Severity)
		}
	}
}

// TestMarshalFindings_emptyFindings verifies that an empty findings list marshals
// to a valid JSON array rather than null.
func TestMarshalFindings_emptyFindings(t *testing.T) {
	result := &plugin.ScanResult{Findings: []plugin.Finding{}}
	data := marshalFindings(result)
	// An empty slice should serialize to "[]", not "null".
	if string(data) != "[]" {
		t.Errorf("empty findings: expected '[]', got %q", string(data))
	}
}

// TestOciManifestParse_extractsLayers verifies that the internal ociManifest type
// correctly unmarshals a realistic OCI manifest JSON to extract layer digests.
func TestOciManifestParse_extractsLayers(t *testing.T) {
	// Minimal OCI manifest JSON with two layers.
	raw := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"layers": [
			{"digest": "sha256:aaa", "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "size": 1000},
			{"digest": "sha256:bbb", "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "size": 2000}
		]
	}`)

	var m ociManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal ociManifest: %v", err)
	}
	if len(m.Layers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(m.Layers))
	}
	if m.Layers[0].Digest != "sha256:aaa" {
		t.Errorf("layer[0].Digest: got %q, want %q", m.Layers[0].Digest, "sha256:aaa")
	}
	if m.Layers[1].Size != 2000 {
		t.Errorf("layer[1].Size: got %d, want 2000", m.Layers[1].Size)
	}
}

// TestConsumerConfig_queueName ensures the consumer queue name is stable so a
// rename doesn't silently break RabbitMQ routing.
func TestConsumerConfig_queueName(t *testing.T) {
	cfg := ConsumerConfig()
	const wantQueue = "scanner.push.completed"
	if cfg.Queue != wantQueue {
		t.Errorf("ConsumerConfig().Queue = %q, want %q", cfg.Queue, wantQueue)
	}
}
