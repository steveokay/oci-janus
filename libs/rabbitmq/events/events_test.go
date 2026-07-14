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
		RoutingStoreQueued,
		RoutingPullImage,
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

// TestPullImagePayload_marshalRoundTrip verifies the FE-API-042 payload survives
// a JSON round-trip with all fields intact — the optional fields (manifest_id,
// tag, actor_id) carry omitempty so they only round-trip cleanly when populated.
func TestPullImagePayload_marshalRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 21, 10, 30, 0, 0, time.UTC)
	orig := PullImagePayload{
		TenantID:       "tenant-abc",
		RepositoryID:   "repo-uuid-123",
		RepositoryName: "myorg/myimage",
		ManifestDigest: "sha256:abc123",
		ManifestID:     "manifest-uuid-456",
		Tag:            "v1.0.0",
		ActorID:        "user-xyz",
		PulledAt:       now,
		Via:            "proxy",
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PullImagePayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TenantID != orig.TenantID || got.RepositoryID != orig.RepositoryID ||
		got.RepositoryName != orig.RepositoryName || got.ManifestDigest != orig.ManifestDigest ||
		got.ManifestID != orig.ManifestID || got.Tag != orig.Tag || got.ActorID != orig.ActorID ||
		got.Via != orig.Via {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, orig)
	}
	if !got.PulledAt.Equal(orig.PulledAt) {
		t.Errorf("PulledAt: got %v, want %v", got.PulledAt, orig.PulledAt)
	}
}

// TestPullImagePayload_omitemptyOptionals verifies that the optional fields are
// elided from JSON when empty. An anonymous pull (no JWT) must not embed an
// empty actor_id; a digest-direct pull must not embed an empty tag.
func TestPullImagePayload_omitemptyOptionals(t *testing.T) {
	p := PullImagePayload{
		TenantID:       "tenant-1",
		RepositoryID:   "repo-1",
		RepositoryName: "org/repo",
		ManifestDigest: "sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
		PulledAt:       time.Now().UTC(),
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, field := range []string{"manifest_id", "tag", "actor_id", "via"} {
		if _, ok := m[field]; ok {
			t.Errorf("field %q should be omitted when empty, but found in JSON", field)
		}
	}
}

// TestRoutingPullImage_constant verifies the FE-API-042 routing key matches the
// documented wire shape so consumers (audit, metadata) and the webhook catalog
// stay aligned without runtime lookup.
func TestRoutingPullImage_constant(t *testing.T) {
	if RoutingPullImage != "pull.image" {
		t.Errorf("RoutingPullImage: got %q, want %q", RoutingPullImage, "pull.image")
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
