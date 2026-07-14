// FUT-081 — coverage for RecordPushFailed, the push.failed publish path the
// handler wires into the manifest-push error branches (quota, immutability,
// internal error). Best-effort publish; a broker error must not propagate.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

func TestRecordPushFailed_publishesEvent(t *testing.T) {
	pub := &recordingPublisher{}
	reg := newTestRegistry(pub, func() bool { return true })

	reg.RecordPushFailed(context.Background(), "tenant-1", "repo-1", "myorg/myrepo", "v1.0.0", "user-3", "quota_exceeded")

	if len(pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.calls))
	}
	got := pub.calls[0]
	if got.routingKey != events.RoutingPushFailed {
		t.Errorf("routing key: got %q, want %q", got.routingKey, events.RoutingPushFailed)
	}
	if got.event.Type != events.RoutingPushFailed || got.event.TenantID != "tenant-1" {
		t.Errorf("envelope mismatch: type=%q tenant=%q", got.event.Type, got.event.TenantID)
	}
	var p events.PushFailedPayload
	if err := json.Unmarshal(got.event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.RepositoryName != "myorg/myrepo" || p.RepoID != "repo-1" || p.Tag != "v1.0.0" ||
		p.PushedBy != "user-3" || p.Reason != "quota_exceeded" {
		t.Errorf("payload mismatch: %+v", p)
	}

	// The payload must also unmarshal into PushCompletedPayload (the audit
	// consumer reuses that type for push.failed) with the shared fields intact.
	var pc events.PushCompletedPayload
	if err := json.Unmarshal(got.event.Payload, &pc); err != nil {
		t.Fatalf("PushCompletedPayload unmarshal: %v", err)
	}
	if pc.RepositoryName != "myorg/myrepo" || pc.Tag != "v1.0.0" || pc.PushedBy != "user-3" {
		t.Errorf("audit-compat fields lost: %+v", pc)
	}
}

func TestRecordPushFailed_publisherError_doesNotPanic(t *testing.T) {
	pub := &recordingPublisher{returnErr: errors.New("amqp closed")}
	reg := newTestRegistry(pub, func() bool { return true })

	reg.RecordPushFailed(context.Background(), "t", "r", "org/repo", "v1", "u", "internal_error")
	if len(pub.calls) != 1 {
		t.Errorf("expected 1 attempted publish even on failure, got %d", len(pub.calls))
	}
}
