// Package events_test tests that all event types marshal/unmarshal correctly
// and that routing-key constants are stable. No broker connection is needed.
package events

import (
	"encoding/json"
	"testing"
	"time"
)

// TestEvent_marshalRoundTrip verifies that an Event envelope survives a JSON
// encode → decode cycle without data loss.
func TestEvent_marshalRoundTrip(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"key": "value"})
	evt := Event{
		ID:         "evt-001",
		Type:       RoutingPushCompleted,
		TenantID:   "tenant-abc",
		OccurredAt: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		Version:    "1.0",
		Payload:    payload,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal Event: %v", err)
	}

	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal Event: %v", err)
	}

	if got.ID != evt.ID {
		t.Errorf("ID: got %q, want %q", got.ID, evt.ID)
	}
	if got.Type != evt.Type {
		t.Errorf("Type: got %q, want %q", got.Type, evt.Type)
	}
	if got.TenantID != evt.TenantID {
		t.Errorf("TenantID: got %q, want %q", got.TenantID, evt.TenantID)
	}
	if !got.OccurredAt.Equal(evt.OccurredAt) {
		t.Errorf("OccurredAt: got %v, want %v", got.OccurredAt, evt.OccurredAt)
	}
}

// TestPushCompletedPayload_marshalRoundTrip verifies that PushCompletedPayload
// survives JSON round-trip with all fields intact.
func TestPushCompletedPayload_marshalRoundTrip(t *testing.T) {
	orig := PushCompletedPayload{
		RepositoryName: "myorg/myimage",
		RepoID:         "repo-uuid-123",
		Tag:            "v1.0.0",
		ManifestDigest: "sha256:abc123",
		PushedBy:       "user-xyz",
		SizeBytes:      1024 * 1024 * 500,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PushCompletedPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

// TestScanCompletedPayload_marshalRoundTrip verifies ScanCompletedPayload
// preserves severity counts across JSON marshal/unmarshal.
func TestScanCompletedPayload_marshalRoundTrip(t *testing.T) {
	orig := ScanCompletedPayload{
		ManifestDigest: "sha256:def456",
		RepositoryName: "myorg/myimage",
		ScannerName:    "trivy",
		SeverityCounts: map[string]int{
			"CRITICAL": 2,
			"HIGH":     5,
			"MEDIUM":   10,
			"LOW":      3,
		},
		PolicyViolation: true,
		Blocked:         true,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ScanCompletedPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ManifestDigest != orig.ManifestDigest {
		t.Errorf("ManifestDigest: got %q, want %q", got.ManifestDigest, orig.ManifestDigest)
	}
	if got.SeverityCounts["CRITICAL"] != orig.SeverityCounts["CRITICAL"] {
		t.Errorf("CRITICAL count: got %d, want %d",
			got.SeverityCounts["CRITICAL"], orig.SeverityCounts["CRITICAL"])
	}
	if !got.Blocked {
		t.Error("Blocked should be true")
	}
}

// TestGCRunCompletedPayload_marshalRoundTrip verifies GCRunCompletedPayload
// survives JSON round-trip.
func TestGCRunCompletedPayload_marshalRoundTrip(t *testing.T) {
	orig := GCRunCompletedPayload{
		Mode:             "full",
		ManifestsDeleted: 3,
		BlobsDeleted:     12,
		BytesFreed:       1024 * 1024 * 200,
		DryRun:           false,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got GCRunCompletedPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
}

// TestRoutingConstants_nonEmpty verifies that none of the routing key constants
// are accidentally set to empty strings (which would cause silent routing failures).
func TestRoutingConstants_nonEmpty(t *testing.T) {
	keys := []string{
		RoutingPushCompleted,
		RoutingPushFailed,
		RoutingManifestDeleted,
		RoutingTagDeleted,
		RoutingScanQueued,
		RoutingScanCompleted,
		RoutingScanPolicyBlocked,
		RoutingWebhookQueued,
		RoutingWebhookDelivered,
		RoutingWebhookFailed,
		RoutingGCRunStarted,
		RoutingGCRunCompleted,
		RoutingImageSigned,
		RoutingTenantCreated,
		RoutingTenantDomainVerified,
		RoutingStoreQueued,
	}
	for _, k := range keys {
		if k == "" {
			t.Errorf("routing key constant is empty string")
		}
	}
}

// TestExchangeConstants_nonEmpty verifies the exchange name constants are non-empty.
func TestExchangeConstants_nonEmpty(t *testing.T) {
	if ExchangeEvents == "" {
		t.Error("ExchangeEvents is empty")
	}
	if ExchangeDLX == "" {
		t.Error("ExchangeDLX is empty")
	}
}

// TestStoreQueuedPayload_omitemptyDigests verifies that optional digest fields
// are omitted from JSON when empty (reduces message size for partial events).
func TestStoreQueuedPayload_omitemptyDigests(t *testing.T) {
	p := StoreQueuedPayload{
		TenantID:     "tenant-1",
		UpstreamName: "dockerhub",
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// blob_digest, image, manifest_digest, repository_name, tag should all be absent.
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, field := range []string{"blob_digest", "image", "manifest_digest", "repository_name", "tag"} {
		if _, ok := m[field]; ok {
			t.Errorf("field %q should be omitted when empty, but found in JSON", field)
		}
	}
}
