// Package worker — FUT-017 consumer tests for HandleCachePopulated.
//
// These tests mirror the shape of consumer_test.go: a minimal Pool with
// nil gRPC clients, a real store, a large jobs buffer, and a stub
// resolver. We never run a real Scan() — the assertions are on the
// store / channel side effects of the handler.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
)

// stubProxyResolver builds a ProxyCachePolicyResolver that returns the
// supplied policy + ok flag. err is returned when non-nil so tests can
// exercise the fail-closed branch.
func stubProxyResolver(policy ProxyCachePolicy, ok bool, err error) ProxyCachePolicyResolver {
	return func(_ context.Context, _ string, _ string) (ProxyCachePolicy, bool, error) {
		return policy, ok, err
	}
}

// makeCachePopulatedEvent assembles a canonical cache.populated event
// envelope around the supplied payload. Keeps tests focused on the
// payload-specific bits.
func makeCachePopulatedEvent(t *testing.T, p events.CachePopulatedPayload) events.Event {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal cache.populated payload: %v", err)
	}
	return events.Event{
		ID:       "evt-fut017",
		Type:     events.RoutingCachePopulated,
		TenantID: p.TenantID,
		Payload:  raw,
	}
}

// TestHandleCachePopulated_noResolver verifies a pool without a
// proxy-cache resolver wired ACKs the event without enqueueing. This is
// the boot path tests + dev environments without FUT-017 take.
func TestHandleCachePopulated_noResolver(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	evt := makeCachePopulatedEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-a",
		UpstreamID:     "u-1",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		Reference:      "latest",
		ManifestDigest: "sha256:aaaaaaaa",
	})
	if err := p.HandleCachePopulated(context.Background(), evt); err != nil {
		t.Fatalf("HandleCachePopulated: %v", err)
	}
	select {
	case job := <-p.jobs:
		t.Fatalf("expected no job enqueued without resolver, got %+v", job)
	default:
	}
}

// TestHandleCachePopulated_policyDisabled verifies a row that exists
// but has auto_scan=false is a no-op.
func TestHandleCachePopulated_policyDisabled(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)
	p.WithProxyCachePolicyResolver(stubProxyResolver(
		ProxyCachePolicy{AutoScan: false},
		true, nil,
	))

	evt := makeCachePopulatedEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-a",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		ManifestDigest: "sha256:bbbbbbbb",
	})
	if err := p.HandleCachePopulated(context.Background(), evt); err != nil {
		t.Fatalf("HandleCachePopulated: %v", err)
	}
	select {
	case job := <-p.jobs:
		t.Fatalf("expected no job enqueued for auto_scan=false, got %+v", job)
	default:
	}
}

// TestHandleCachePopulated_autoScanEnqueuesJob verifies the happy path:
// a (tenant, upstream) row with auto_scan=true enqueues a scan job
// carrying the manifest digest from the event payload + the mapped
// block_on_severity.
func TestHandleCachePopulated_autoScanEnqueuesJob(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)
	p.WithProxyCachePolicyResolver(stubProxyResolver(
		ProxyCachePolicy{AutoScan: true, SeverityThreshold: "HIGH"},
		true, nil,
	))

	evt := makeCachePopulatedEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-a",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		ManifestDigest: "sha256:cccccccc",
	})
	if err := p.HandleCachePopulated(context.Background(), evt); err != nil {
		t.Fatalf("HandleCachePopulated: %v", err)
	}

	select {
	case job := <-p.jobs:
		if job.manifestDigest != "sha256:cccccccc" {
			t.Errorf("manifestDigest: got %q, want sha256:cccccccc", job.manifestDigest)
		}
		if job.tenantID != "tenant-a" {
			t.Errorf("tenantID: got %q, want tenant-a", job.tenantID)
		}
		if job.repositoryName != "library/ubuntu" {
			t.Errorf("repositoryName: got %q, want library/ubuntu", job.repositoryName)
		}
		if job.policy.BlockOnSeverity != "HIGH" {
			t.Errorf("policy.BlockOnSeverity: got %q, want HIGH", job.policy.BlockOnSeverity)
		}
		// Confirm the store carries the scan record so dashboard polls
		// see "pending" until the worker runs.
		rec, ok := sc.Get(job.scanID)
		if !ok {
			t.Fatalf("scan record %q not found in store", job.scanID)
		}
		if rec.Status != store.StatusPending {
			t.Errorf("Status: got %q, want %q", rec.Status, store.StatusPending)
		}
	default:
		t.Fatal("expected a job to be enqueued after HandleCachePopulated with auto_scan=true")
	}
}

// TestHandleCachePopulated_idempotency verifies the in-memory dedup:
// a second cache.populated event for the same (tenant, manifest digest)
// while the first scan is still in flight does NOT enqueue another job.
func TestHandleCachePopulated_idempotency(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)
	p.WithProxyCachePolicyResolver(stubProxyResolver(
		ProxyCachePolicy{AutoScan: true},
		true, nil,
	))

	payload := events.CachePopulatedPayload{
		TenantID:       "tenant-a",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		ManifestDigest: "sha256:dddddddd",
	}
	evt := makeCachePopulatedEvent(t, payload)
	if err := p.HandleCachePopulated(context.Background(), evt); err != nil {
		t.Fatalf("first HandleCachePopulated: %v", err)
	}
	// First call enqueued one job — drain it so the store still
	// records the scan as pending (we never touch it after Drain so
	// the in-memory record remains).
	select {
	case <-p.jobs:
	default:
		t.Fatal("expected first call to enqueue a job")
	}

	// Second call for the same (tenant, digest) — should be a no-op
	// because the store still carries a Pending entry for that pair.
	if err := p.HandleCachePopulated(context.Background(), evt); err != nil {
		t.Fatalf("second HandleCachePopulated: %v", err)
	}
	select {
	case job := <-p.jobs:
		t.Fatalf("expected second call to dedup, got job %+v", job)
	default:
	}
}

// TestHandleCachePopulated_resolverError verifies the fail-closed
// posture: a resolver error returns the error so the broker NACKs and
// the message is redelivered after backoff.
func TestHandleCachePopulated_resolverError(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)
	p.WithProxyCachePolicyResolver(stubProxyResolver(
		ProxyCachePolicy{}, false, errors.New("DB unreachable"),
	))

	evt := makeCachePopulatedEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-a",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		ManifestDigest: "sha256:eeeeeeee",
	})
	err := p.HandleCachePopulated(context.Background(), evt)
	if err == nil {
		t.Fatal("expected error from resolver to propagate")
	}
	// And no job is enqueued.
	select {
	case job := <-p.jobs:
		t.Fatalf("expected no job on resolver error, got %+v", job)
	default:
	}
}

// TestHandleCachePopulated_malformedPayload verifies invalid JSON
// surfaces as an error so the broker NACKs the malformed message.
func TestHandleCachePopulated_malformedPayload(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	evt := events.Event{
		ID:       "evt-bad",
		Type:     events.RoutingCachePopulated,
		TenantID: "tenant-a",
		Payload:  []byte("{not-json"),
	}
	if err := p.HandleCachePopulated(context.Background(), evt); err == nil {
		t.Fatal("expected error for malformed JSON payload")
	}
}

// TestHandleCachePopulated_missingFieldsNoop verifies a payload missing
// required fields (e.g. blank tenant or digest) is silently skipped
// instead of crashing or enqueueing an incomplete job.
func TestHandleCachePopulated_missingFieldsNoop(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)
	// Resolver intentionally wired — we want to confirm we bail out
	// BEFORE consulting the resolver when the payload is empty.
	called := false
	p.WithProxyCachePolicyResolver(func(_ context.Context, _ string, _ string) (ProxyCachePolicy, bool, error) {
		called = true
		return ProxyCachePolicy{}, false, nil
	})

	evt := makeCachePopulatedEvent(t, events.CachePopulatedPayload{
		TenantID: "tenant-a",
		// UpstreamName + ManifestDigest both blank — invalid payload.
	})
	if err := p.HandleCachePopulated(context.Background(), evt); err != nil {
		t.Fatalf("HandleCachePopulated: %v", err)
	}
	if called {
		t.Error("resolver should not be called when payload is missing required fields")
	}
}

// TestCachePopulatedConsumerConfig_queueName verifies the consumer
// queue name is stable.
func TestCachePopulatedConsumerConfig_queueName(t *testing.T) {
	cfg := CachePopulatedConsumerConfig()
	const wantQueue = "scanner.cache.populated"
	if cfg.Queue != wantQueue {
		t.Errorf("CachePopulatedConsumerConfig().Queue = %q, want %q", cfg.Queue, wantQueue)
	}
}

// TestCachePopulatedConsumerConfig_routingKey verifies the routing key
// matches the canonical events constant.
func TestCachePopulatedConsumerConfig_routingKey(t *testing.T) {
	cfg := CachePopulatedConsumerConfig()
	if cfg.RoutingKey != events.RoutingCachePopulated {
		t.Errorf("CachePopulatedConsumerConfig().RoutingKey = %q, want %q",
			cfg.RoutingKey, events.RoutingCachePopulated)
	}
}

// TestStore_HasRecentScan_inFlightMatches verifies the in-memory dedup
// helper considers a pending or running scan a hit regardless of the
// recentWindow argument. Belt-and-braces test alongside the consumer
// idempotency test above.
func TestStore_HasRecentScan_inFlightMatches(t *testing.T) {
	sc := store.New()
	sc.Create("scan-1", "tenant-a", "sha256:zzzz", "library/ubuntu")
	if !sc.HasRecentScan("tenant-a", "sha256:zzzz", time.Minute) {
		t.Error("expected HasRecentScan to match an in-flight pending scan")
	}
	if sc.HasRecentScan("tenant-b", "sha256:zzzz", time.Minute) {
		t.Error("expected HasRecentScan to be scoped by tenant_id")
	}
}
