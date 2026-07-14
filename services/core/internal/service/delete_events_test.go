// FUT-081 — coverage for the manifest.deleted / tag.deleted publish paths.
// registry-core must emit an event on every successful delete so the audit
// trail (and deletion-subscribed webhooks) see it. Uses the bufconn-backed
// buildTestRegistry harness + its recording publisher — no broker needed.
package service

import (
	"context"
	"encoding/json"
	"testing"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

func TestDeleteManifest_tag_publishesTagDeleted(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()
	ctx := context.Background()

	// Seed a tag so the delete succeeds (fake returns NotFound otherwise).
	if _, err := tr.meta.PutTag(ctx, &metadatav1.PutTagRequest{
		RepoId: "repo-1", Name: "v1.0.0", ManifestDigest: "sha256:" + hex64("a"),
	}); err != nil {
		t.Fatalf("seed PutTag: %v", err)
	}

	if err := tr.reg.DeleteManifest(ctx, "tenant-1", "repo-1", "myorg/myrepo", "v1.0.0", "user-7"); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	if len(tr.pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(tr.pub.calls))
	}
	got := tr.pub.calls[0]
	if got.routingKey != events.RoutingTagDeleted {
		t.Errorf("routing key: got %q, want %q", got.routingKey, events.RoutingTagDeleted)
	}
	if got.event.Type != events.RoutingTagDeleted {
		t.Errorf("event.Type: got %q, want %q", got.event.Type, events.RoutingTagDeleted)
	}
	if got.event.TenantID != "tenant-1" {
		t.Errorf("TenantID: got %q, want tenant-1", got.event.TenantID)
	}
	var p events.TagDeletedPayload
	if err := json.Unmarshal(got.event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.RepositoryName != "myorg/myrepo" || p.RepoID != "repo-1" || p.Tag != "v1.0.0" || p.DeletedBy != "user-7" {
		t.Errorf("payload mismatch: %+v", p)
	}
}

func TestDeleteManifest_digest_publishesManifestDeleted(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()
	ctx := context.Background()

	digest := "sha256:" + hex64("b")
	if _, err := tr.meta.PutManifest(ctx, &metadatav1.PutManifestRequest{
		RepoId: "repo-1", TenantId: "tenant-1", Digest: digest, MediaType: "application/vnd.oci.image.manifest.v1+json",
	}); err != nil {
		t.Fatalf("seed PutManifest: %v", err)
	}

	if err := tr.reg.DeleteManifest(ctx, "tenant-1", "repo-1", "myorg/myrepo", digest, "user-9"); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	if len(tr.pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(tr.pub.calls))
	}
	got := tr.pub.calls[0]
	if got.routingKey != events.RoutingManifestDeleted {
		t.Errorf("routing key: got %q, want %q", got.routingKey, events.RoutingManifestDeleted)
	}
	var p events.ManifestDeletedPayload
	if err := json.Unmarshal(got.event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.RepositoryName != "myorg/myrepo" || p.RepoID != "repo-1" || p.Digest != digest || p.DeletedBy != "user-9" {
		t.Errorf("payload mismatch: %+v", p)
	}
}

func TestDeleteManifest_notFound_noPublish(t *testing.T) {
	tr, cleanup := buildTestRegistry(t)
	defer cleanup()
	ctx := context.Background()

	// No tag seeded → delete returns ErrNotFound and must NOT publish.
	err := tr.reg.DeleteManifest(ctx, "tenant-1", "repo-1", "myorg/myrepo", "ghost", "user-7")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if len(tr.pub.calls) != 0 {
		t.Errorf("expected zero publishes on a not-found delete, got %d", len(tr.pub.calls))
	}
}

// hex64 builds a 64-char lowercase-hex digest suffix from a single char so the
// tests don't hardcode long literals.
func hex64(c string) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = c[0]
	}
	return string(out)
}
