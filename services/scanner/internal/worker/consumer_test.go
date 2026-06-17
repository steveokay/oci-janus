// Package worker_test (continued) exercises event-consumer helpers in worker.go.
// Tests focus on payload parsing, store interactions, and routing-key contracts.
// No real gRPC connections or RabbitMQ brokers are required.
package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"

	"google.golang.org/grpc"
)

// buildMinimalPool returns a Pool with a real store and nil gRPC connections.
// The pool is configured with a large timeout and worker count of 1.
// Only methods that interact with the store (not the gRPC clients) can be
// safely called on this pool — the background goroutine will fail silently
// when it tries to use nil clients.
func buildMinimalPool(sc *store.Store) *Pool {
	return &Pool{
		scanner:     nil,
		metaClient:  nil,
		storageConn: (*grpc.ClientConn)(nil),
		pub:         nil,
		scanStore:   sc,
		jobs:        make(chan scanJob, 64), // large buffer so Enqueue never blocks inline
		timeout:     time.Second,
	}
}

// TestHandlePushCompleted_validPayload verifies that a well-formed push.completed
// event creates a scan record in the store with StatusPending.
func TestHandlePushCompleted_validPayload(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	payload, _ := json.Marshal(events.PushCompletedPayload{
		RepositoryName: "org/myimage",
		RepoID:         "repo-123",
		Tag:            "v1.0.0",
		ManifestDigest: "sha256:deadbeef",
		PushedBy:       "alice",
		SizeBytes:      1024,
	})

	err := p.HandlePushCompleted(context.Background(), events.Event{
		ID:       "event-1",
		TenantID: "tenant-aaa",
		Type:     events.RoutingPushCompleted,
		Payload:  payload,
	})
	if err != nil {
		t.Fatalf("HandlePushCompleted: unexpected error: %v", err)
	}

	// A scan record should have been created. Find it by scanning the store.
	// Since we don't know the scan ID (it's a UUID), we can check via TriggerScanJob
	// which also uses Create — but HandlePushCompleted creates directly.
	// Drain the jobs channel to get the scan_id.
	select {
	case job := <-p.jobs:
		rec, ok := sc.Get(job.scanID)
		if !ok {
			t.Fatalf("scan record %q not found in store after HandlePushCompleted", job.scanID)
		}
		if rec.Status != store.StatusPending {
			t.Errorf("Status: got %q, want %q", rec.Status, store.StatusPending)
		}
		if rec.ManifestDigest != "sha256:deadbeef" {
			t.Errorf("ManifestDigest: got %q, want sha256:deadbeef", rec.ManifestDigest)
		}
		if rec.RepositoryName != "org/myimage" {
			t.Errorf("RepositoryName: got %q, want org/myimage", rec.RepositoryName)
		}
		if rec.TenantID != "tenant-aaa" {
			t.Errorf("TenantID: got %q, want tenant-aaa", rec.TenantID)
		}
	default:
		t.Fatal("expected a job to be enqueued after HandlePushCompleted")
	}
}

// TestHandlePushCompleted_invalidPayload verifies that a malformed JSON payload
// returns an error so the message is NACKed by the consumer.
func TestHandlePushCompleted_invalidPayload(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	err := p.HandlePushCompleted(context.Background(), events.Event{
		ID:       "event-bad",
		TenantID: "tenant-aaa",
		Type:     events.RoutingPushCompleted,
		Payload:  []byte("NOT_JSON"),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
}

// TestHandleScanQueued_validPayload verifies that a scan.queued event creates a
// scan record in the store.
func TestHandleScanQueued_validPayload(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	payload, _ := json.Marshal(events.ScanQueuedPayload{
		TenantID:       "tenant-bbb",
		RepositoryName: "org/repo",
		RepoID:         "repo-456",
		TagName:        "latest",
		ManifestDigest: "sha256:cafebabe",
	})

	err := p.HandleScanQueued(context.Background(), events.Event{
		ID:       "event-2",
		TenantID: "tenant-bbb",
		Type:     events.RoutingScanQueued,
		Payload:  payload,
	})
	if err != nil {
		t.Fatalf("HandleScanQueued: unexpected error: %v", err)
	}

	select {
	case job := <-p.jobs:
		rec, ok := sc.Get(job.scanID)
		if !ok {
			t.Fatalf("scan record %q not found in store", job.scanID)
		}
		if rec.ManifestDigest != "sha256:cafebabe" {
			t.Errorf("ManifestDigest: got %q, want sha256:cafebabe", rec.ManifestDigest)
		}
		if rec.RepositoryName != "org/repo" {
			t.Errorf("RepositoryName: got %q, want org/repo", rec.RepositoryName)
		}
	default:
		t.Fatal("expected a job to be enqueued after HandleScanQueued")
	}
}

// TestHandleScanQueued_invalidPayload verifies that a malformed JSON payload
// returns an error from HandleScanQueued.
func TestHandleScanQueued_invalidPayload(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	err := p.HandleScanQueued(context.Background(), events.Event{
		ID:       "event-bad2",
		TenantID: "tenant-bbb",
		Type:     events.RoutingScanQueued,
		Payload:  []byte("{invalid_json"),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON payload in HandleScanQueued")
	}
}

// TestTriggerScanJob_returnsScanID verifies that TriggerScanJob returns a
// non-empty scan_id and registers the record in the store.
func TestTriggerScanJob_returnsScanID(t *testing.T) {
	sc := store.New()
	p := buildMinimalPool(sc)

	scanID := p.TriggerScanJob("tenant-xyz", "repo-789", "org/myrepo", "sha256:1234")
	if scanID == "" {
		t.Fatal("expected non-empty scan_id from TriggerScanJob")
	}

	rec, ok := sc.Get(scanID)
	if !ok {
		t.Fatalf("scan record %q not found in store", scanID)
	}
	if rec.ManifestDigest != "sha256:1234" {
		t.Errorf("ManifestDigest: got %q, want sha256:1234", rec.ManifestDigest)
	}
	if rec.TenantID != "tenant-xyz" {
		t.Errorf("TenantID: got %q, want tenant-xyz", rec.TenantID)
	}
}

// TestScanQueuedConsumerConfig_queueName verifies the scan.queued consumer queue
// name is stable — a rename would silently break RabbitMQ routing.
func TestScanQueuedConsumerConfig_queueName(t *testing.T) {
	cfg := ScanQueuedConsumerConfig()
	const wantQueue = "scanner.scan.queued"
	if cfg.Queue != wantQueue {
		t.Errorf("ScanQueuedConsumerConfig().Queue = %q, want %q", cfg.Queue, wantQueue)
	}
}

// TestScanQueuedConsumerConfig_routingKey verifies the routing key matches the
// canonical events constant.
func TestScanQueuedConsumerConfig_routingKey(t *testing.T) {
	cfg := ScanQueuedConsumerConfig()
	if cfg.RoutingKey != events.RoutingScanQueued {
		t.Errorf("ScanQueuedConsumerConfig().RoutingKey = %q, want %q",
			cfg.RoutingKey, events.RoutingScanQueued)
	}
}

// TestConsumerConfig_maxRetries verifies that the push.completed consumer is
// configured for 3 retries per CLAUDE.md §4.7.
func TestConsumerConfig_maxRetries(t *testing.T) {
	cfg := ConsumerConfig()
	const wantRetries = 3
	if cfg.MaxRetries != wantRetries {
		t.Errorf("ConsumerConfig().MaxRetries = %d, want %d", cfg.MaxRetries, wantRetries)
	}
}

// TestConsumerConfig_interface verifies that ConsumerConfig returns a
// consumer.Config value (not a pointer) with correct type.
func TestConsumerConfig_interface(t *testing.T) {
	cfg := ConsumerConfig()
	var _ consumer.Config = cfg // compile-time type check
	if cfg.Queue == "" {
		t.Error("ConsumerConfig().Queue must not be empty")
	}
}
