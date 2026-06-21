// FE-API-042 — coverage for RecordPull, the pull.image publish path that
// services/core wires into handleGetManifest. The tests here exercise the
// sampler gate + payload shape against an in-memory recording publisher so
// no broker is required.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// recordingPublisher captures every published event so the test can inspect
// routing key + payload shape. Satisfies the eventPublisher interface.
type recordingPublisher struct {
	calls []recordedPublish
	// returnErr (optional) makes Publish return an error to verify the
	// best-effort log path doesn't propagate failure up to the handler.
	returnErr error
}

type recordedPublish struct {
	routingKey string
	event      events.Event
}

func (p *recordingPublisher) Publish(_ context.Context, routingKey string, evt events.Event) error {
	p.calls = append(p.calls, recordedPublish{routingKey: routingKey, event: evt})
	return p.returnErr
}

// newTestRegistry builds a Registry with everything stubbed out except the
// publisher + sampler. metadata/storage clients are nil because RecordPull
// never touches them — it's a pure publish path off the handler hot path.
func newTestRegistry(pub eventPublisher, sample pullSampler) *Registry {
	return &Registry{
		publisher:  pub,
		pullSample: sample,
	}
}

// TestRecordPull_publishesEventOnHappyPath confirms a sampled pull produces
// exactly one publish with the right routing key + a well-formed PullImagePayload.
func TestRecordPull_publishesEventOnHappyPath(t *testing.T) {
	pub := &recordingPublisher{}
	reg := newTestRegistry(pub, func() bool { return true })

	reg.RecordPull(
		context.Background(),
		"tenant-1",
		"repo-1",
		"myorg/myrepo",
		"sha256:aabbcc",
		"manifest-uuid-9",
		"v1.0.0",
		"user-42",
	)

	if len(pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.calls))
	}
	got := pub.calls[0]
	if got.routingKey != events.RoutingPullImage {
		t.Errorf("routing key: got %q, want %q", got.routingKey, events.RoutingPullImage)
	}
	if got.event.Type != events.RoutingPullImage {
		t.Errorf("event.Type: got %q, want %q", got.event.Type, events.RoutingPullImage)
	}
	if got.event.TenantID != "tenant-1" {
		t.Errorf("event.TenantID: got %q, want tenant-1", got.event.TenantID)
	}
	var payload events.PullImagePayload
	if err := json.Unmarshal(got.event.Payload, &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload.RepositoryID != "repo-1" {
		t.Errorf("payload.RepositoryID: got %q, want repo-1", payload.RepositoryID)
	}
	if payload.RepositoryName != "myorg/myrepo" {
		t.Errorf("payload.RepositoryName: got %q, want myorg/myrepo", payload.RepositoryName)
	}
	if payload.ManifestDigest != "sha256:aabbcc" {
		t.Errorf("payload.ManifestDigest: got %q", payload.ManifestDigest)
	}
	if payload.ManifestID != "manifest-uuid-9" {
		t.Errorf("payload.ManifestID: got %q", payload.ManifestID)
	}
	if payload.Tag != "v1.0.0" {
		t.Errorf("payload.Tag: got %q", payload.Tag)
	}
	if payload.ActorID != "user-42" {
		t.Errorf("payload.ActorID: got %q", payload.ActorID)
	}
	if payload.PulledAt.IsZero() {
		t.Error("payload.PulledAt is zero — RecordPull must stamp the timestamp")
	}
}

// TestRecordPull_sampleRateZero_skipsPublish verifies the sampler gate
// short-circuits the publish entirely when configured for 0% sampling. This
// matches the "PULL_EVENT_SAMPLE_RATE=0.0 ⇒ analytics returns zeros" contract
// documented in the .env.example.
func TestRecordPull_sampleRateZero_skipsPublish(t *testing.T) {
	pub := &recordingPublisher{}
	reg := newTestRegistry(pub, defaultPullSampler(0.0))

	for i := 0; i < 50; i++ {
		reg.RecordPull(context.Background(), "t", "r", "org/repo", "sha256:zz", "", "", "u")
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected zero publishes at sample_rate=0.0, got %d", len(pub.calls))
	}
}

// TestRecordPull_sampleRateOne_alwaysPublishes confirms a sample rate of 1.0
// produces a publish on every call. defaultPullSampler short-circuits >=1.0
// to "always true" so we don't lose samples on the rand.Float64() in [0, 1)
// boundary.
func TestRecordPull_sampleRateOne_alwaysPublishes(t *testing.T) {
	pub := &recordingPublisher{}
	reg := newTestRegistry(pub, defaultPullSampler(1.0))

	for i := 0; i < 25; i++ {
		reg.RecordPull(context.Background(), "t", "r", "org/repo", "sha256:zz", "", "", "u")
	}
	if len(pub.calls) != 25 {
		t.Errorf("expected 25 publishes at sample_rate=1.0, got %d", len(pub.calls))
	}
}

// TestRecordPull_publisherError_doesNotPanic confirms the best-effort contract:
// a broker error is logged + swallowed so the pull response is never affected.
func TestRecordPull_publisherError_doesNotPanic(t *testing.T) {
	pub := &recordingPublisher{returnErr: errors.New("amqp closed")}
	reg := newTestRegistry(pub, func() bool { return true })

	// Should not panic, should not return any signal.
	reg.RecordPull(context.Background(), "t", "r", "org/repo", "sha256:zz", "", "", "")
	if len(pub.calls) != 1 {
		t.Errorf("expected 1 attempted publish even on failure, got %d", len(pub.calls))
	}
}

// TestRecordPull_anonymousPull_emptyActorID verifies the anonymous-pull case:
// no JWT → empty ActorID in payload. The audit consumer turns this into
// actor_id="anonymous" downstream; the wire payload itself must carry "".
func TestRecordPull_anonymousPull_emptyActorID(t *testing.T) {
	pub := &recordingPublisher{}
	reg := newTestRegistry(pub, func() bool { return true })

	reg.RecordPull(context.Background(), "t", "r", "org/repo", "sha256:zz", "", "", "")

	if len(pub.calls) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.calls))
	}
	var p events.PullImagePayload
	if err := json.Unmarshal(pub.calls[0].event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.ActorID != "" {
		t.Errorf("ActorID: got %q, want empty for anonymous pull", p.ActorID)
	}
}

// TestRecordPull_nilSampler_skipsPublish guards against a future Registry
// constructor that forgets to set pullSample — better to skip the publish
// than to nil-deref on the hot read path.
func TestRecordPull_nilSampler_skipsPublish(t *testing.T) {
	pub := &recordingPublisher{}
	reg := newTestRegistry(pub, nil)

	reg.RecordPull(context.Background(), "t", "r", "org/repo", "sha256:zz", "", "", "")
	if len(pub.calls) != 0 {
		t.Errorf("expected zero publishes when sampler is nil, got %d", len(pub.calls))
	}
}

// TestDefaultPullSampler_clamps verifies the sampler edge cases without
// invoking RecordPull. Out-of-range inputs collapse to "never" / "always" so a
// misconfigured rate can't crash later in the hot path.
func TestDefaultPullSampler_clamps(t *testing.T) {
	if defaultPullSampler(0.0)() {
		t.Error("rate=0.0 must never publish")
	}
	if defaultPullSampler(-1.0)() {
		t.Error("rate<0.0 must clamp to never-publish")
	}
	if !defaultPullSampler(1.0)() {
		t.Error("rate=1.0 must always publish")
	}
	if !defaultPullSampler(2.0)() {
		t.Error("rate>1.0 must clamp to always-publish")
	}
}
