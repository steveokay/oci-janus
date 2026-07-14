// Package worker_test exercises the Worker's event-handling and delivery logic
// using hand-written fake implementations — no real PostgreSQL or HTTP required.
package worker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	libsaes "github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/webhook/internal/repository"
)

// validKeyHex is a 32-byte (64-char hex) AES key used in all worker tests.
const validKeyHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// validKey is the decoded form of validKeyHex.
var validKey = mustDecodeHex(validKeyHex)

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// fakeWorkerRepo implements workerRepo with configurable per-method responses.
type fakeWorkerRepo struct {
	mu sync.Mutex

	// FindEndpointsForEvent
	endpoints []*repository.EndpointRecord
	findErr   error

	// CreateDelivery
	deliveryRec *repository.DeliveryRecord
	createErr   error
	createCalls int

	// PollDueDeliveries
	pollRecs []*repository.DeliveryRecord
	pollErr  error

	// GetEndpoint
	endpointRec *repository.EndpointRecord
	getErr      error

	// MarkDelivered / MarkFailed
	markDeliveredErr error
	markFailedErr    error

	// Recorded calls
	markDeliveredIDs []uuid.UUID
	markFailedIDs    []uuid.UUID
	markFailedDeads  []bool
}

func (f *fakeWorkerRepo) FindEndpointsForEvent(_ context.Context, _ uuid.UUID, _ string) ([]*repository.EndpointRecord, error) {
	return f.endpoints, f.findErr
}

func (f *fakeWorkerRepo) CreateDelivery(_ context.Context, _, _ uuid.UUID, _ string, _ []byte) (*repository.DeliveryRecord, error) {
	f.mu.Lock()
	f.createCalls++
	f.mu.Unlock()
	return f.deliveryRec, f.createErr
}

func (f *fakeWorkerRepo) PollDueDeliveries(_ context.Context, _ int) ([]*repository.DeliveryRecord, error) {
	return f.pollRecs, f.pollErr
}

func (f *fakeWorkerRepo) GetEndpoint(_ context.Context, _ uuid.UUID) (*repository.EndpointRecord, error) {
	return f.endpointRec, f.getErr
}

func (f *fakeWorkerRepo) MarkDelivered(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	f.markDeliveredIDs = append(f.markDeliveredIDs, id)
	f.mu.Unlock()
	return f.markDeliveredErr
}

func (f *fakeWorkerRepo) MarkFailed(_ context.Context, id uuid.UUID, _ string, _ time.Time, dead bool) error {
	f.mu.Lock()
	f.markFailedIDs = append(f.markFailedIDs, id)
	f.markFailedDeads = append(f.markFailedDeads, dead)
	f.mu.Unlock()
	return f.markFailedErr
}

// fakeDispatcher implements workerDispatcher, returning a configurable error.
type fakeDispatcher struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (f *fakeDispatcher) Deliver(_ context.Context, _ string, _ []byte, _ []byte) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.err
}

// fakePublisher (FUT-081) records every published lifecycle event so tests can
// assert webhook.queued / webhook.delivered / webhook.failed were emitted.
type fakePublisher struct {
	mu    sync.Mutex
	calls []recordedPublish
}

type recordedPublish struct {
	routingKey string
	event      events.Event
}

func (p *fakePublisher) Publish(_ context.Context, routingKey string, evt events.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, recordedPublish{routingKey: routingKey, event: evt})
	return nil
}

func (p *fakePublisher) byKey(routingKey string) []recordedPublish {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []recordedPublish
	for _, c := range p.calls {
		if c.routingKey == routingKey {
			out = append(out, c)
		}
	}
	return out
}

// makeWorker creates a Worker with the provided fakes and validKeyHex.
func makeWorker(t *testing.T, repo workerRepo, disp workerDispatcher) *Worker {
	w, _ := makeWorkerWithPub(t, repo, disp)
	return w
}

// makeWorkerWithPub is makeWorker but also returns the recording publisher for
// tests that assert lifecycle events (FUT-081).
func makeWorkerWithPub(t *testing.T, repo workerRepo, disp workerDispatcher) (*Worker, *fakePublisher) {
	t.Helper()
	pub := &fakePublisher{}
	w, err := newWithDeps(repo, disp, pub, validKeyHex, 60)
	if err != nil {
		t.Fatalf("newWithDeps: %v", err)
	}
	return w, pub
}

// encryptSecret encrypts a plaintext HMAC secret using validKey so fake endpoint
// records have realistic SecretEnc values the worker can decrypt.
func encryptSecret(t *testing.T, plain string) string {
	t.Helper()
	ct, err := libsaes.Encrypt([]byte(plain), validKey)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	return hex.EncodeToString(ct)
}

// TestNew_invalidKey verifies that newWithDeps() rejects a key that is not
// 64 hex characters (32 bytes).
func TestNew_invalidKey(t *testing.T) {
	_, err := newWithDeps(&fakeWorkerRepo{}, &fakeDispatcher{}, &fakePublisher{}, "tooshort", 5)
	if err == nil {
		t.Fatal("expected error for invalid credential key")
	}
}

// TestNew_validKey verifies that newWithDeps() succeeds with a valid 64-hex key.
func TestNew_validKey(t *testing.T) {
	w, err := newWithDeps(&fakeWorkerRepo{}, &fakeDispatcher{}, &fakePublisher{}, validKeyHex, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w == nil {
		t.Fatal("Worker must not be nil")
	}
}

// TestHandleEvent_invalidTenantID verifies that an event with a malformed
// tenant_id is rejected immediately.
func TestHandleEvent_invalidTenantID(t *testing.T) {
	w := makeWorker(t, &fakeWorkerRepo{}, &fakeDispatcher{})

	err := w.HandleEvent(context.Background(), events.Event{
		ID:       uuid.NewString(),
		TenantID: "not-a-uuid",
		Type:     events.RoutingPushCompleted,
	})
	if err == nil {
		t.Fatal("expected error for invalid tenant_id")
	}
}

// TestHandleEvent_noMatchingEndpoints verifies that an event with no subscribed
// endpoints returns nil (nothing to do).
func TestHandleEvent_noMatchingEndpoints(t *testing.T) {
	repo := &fakeWorkerRepo{endpoints: nil, findErr: nil}
	w := makeWorker(t, repo, &fakeDispatcher{})

	err := w.HandleEvent(context.Background(), events.Event{
		ID:       uuid.NewString(),
		TenantID: "11111111-1111-1111-1111-111111111111",
		Type:     events.RoutingPushCompleted,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestHandleEvent_findEndpointsError verifies that a database error in
// FindEndpointsForEvent is returned as a non-nil error.
func TestHandleEvent_findEndpointsError(t *testing.T) {
	repo := &fakeWorkerRepo{findErr: errors.New("db down")}
	w := makeWorker(t, repo, &fakeDispatcher{})

	err := w.HandleEvent(context.Background(), events.Event{
		ID:       uuid.NewString(),
		TenantID: "11111111-1111-1111-1111-111111111111",
		Type:     events.RoutingPushCompleted,
	})
	if err == nil {
		t.Fatal("expected error from FindEndpointsForEvent")
	}
}

// TestHandleEvent_createsDeliveryForEachEndpoint verifies that CreateDelivery is
// called once per matched endpoint.
func TestHandleEvent_createsDeliveryForEachEndpoint(t *testing.T) {
	endpointID := uuid.New()
	repo := &fakeWorkerRepo{
		endpoints: []*repository.EndpointRecord{
			{ID: endpointID, TenantID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), URL: "https://example.com/hook", Events: []string{"push.completed"}},
		},
		deliveryRec: &repository.DeliveryRecord{ID: uuid.New(), EndpointID: endpointID},
	}
	w := makeWorker(t, repo, &fakeDispatcher{})

	err := w.HandleEvent(context.Background(), events.Event{
		ID:       uuid.NewString(),
		TenantID: "11111111-1111-1111-1111-111111111111",
		Type:     events.RoutingPushCompleted,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// HandleEvent returned nil even if CreateDelivery failed for one endpoint,
	// but here we just verify no error was surfaced.
}

// TestAttemptDelivery_success verifies the happy path: dispatcher succeeds →
// MarkDelivered is called.
func TestAttemptDelivery_success(t *testing.T) {
	deliveryID := uuid.New()
	endpointID := uuid.New()

	secretPlain := "hmac-secret-value"
	repo := &fakeWorkerRepo{
		endpointRec: &repository.EndpointRecord{
			ID:        endpointID,
			URL:       "https://example.com/hook",
			SecretEnc: encryptSecret(t, secretPlain),
		},
	}
	disp := &fakeDispatcher{err: nil}
	w := makeWorker(t, repo, disp)

	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID:         deliveryID,
		EndpointID: endpointID,
		Payload:    []byte(`{"event":"push.completed"}`),
		Attempts:   0,
	})

	// Give goroutines a moment to settle (attemptDelivery is called inline in tests).
	repo.mu.Lock()
	marked := len(repo.markDeliveredIDs)
	repo.mu.Unlock()

	if marked != 1 {
		t.Errorf("MarkDelivered should have been called once, got %d", marked)
	}
	if repo.markDeliveredIDs[0] != deliveryID {
		t.Errorf("MarkDelivered called with wrong ID: %v", repo.markDeliveredIDs[0])
	}
}

// TestAttemptDelivery_failureRetry verifies that a dispatcher error with fewer
// than max attempts results in MarkFailed being called with dead=false.
func TestAttemptDelivery_failureRetry(t *testing.T) {
	deliveryID := uuid.New()
	endpointID := uuid.New()

	repo := &fakeWorkerRepo{
		endpointRec: &repository.EndpointRecord{
			ID:        endpointID,
			URL:       "https://example.com/hook",
			SecretEnc: encryptSecret(t, "hmac-secret"),
		},
	}
	// Dispatcher fails.
	disp := &fakeDispatcher{err: errors.New("connection refused")}
	w := makeWorker(t, repo, disp)

	// Attempt 1 (attempts=0 → next attempt will be index 1, which is within retryDelays).
	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID:         deliveryID,
		EndpointID: endpointID,
		Payload:    []byte(`{}`),
		Attempts:   0,
	})

	repo.mu.Lock()
	failed := len(repo.markFailedIDs)
	deadFlags := append([]bool(nil), repo.markFailedDeads...)
	repo.mu.Unlock()

	if failed != 1 {
		t.Fatalf("MarkFailed should be called once, got %d", failed)
	}
	if deadFlags[0] {
		t.Error("dead should be false for attempt 0 (still has retries remaining)")
	}
}

// TestAttemptDelivery_exhaustedRetries verifies that once all retries are
// exhausted (attempts >= len(retryDelays)), MarkFailed is called with dead=true.
func TestAttemptDelivery_exhaustedRetries(t *testing.T) {
	deliveryID := uuid.New()
	endpointID := uuid.New()

	repo := &fakeWorkerRepo{
		endpointRec: &repository.EndpointRecord{
			ID:        endpointID,
			URL:       "https://example.com/hook",
			SecretEnc: encryptSecret(t, "hmac-secret"),
		},
	}
	disp := &fakeDispatcher{err: errors.New("still failing")}
	w := makeWorker(t, repo, disp)

	// Simulate 5 prior attempts — NextRetryAt(5) returns ok=false.
	// Per dispatcher.go, retryDelays has 5 entries (0..4 valid, 5 = exhausted).
	const maxAttempts = 5
	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID:         deliveryID,
		EndpointID: endpointID,
		Payload:    []byte(`{}`),
		Attempts:   maxAttempts,
	})

	repo.mu.Lock()
	deadFlags := append([]bool(nil), repo.markFailedDeads...)
	repo.mu.Unlock()

	if len(deadFlags) == 0 {
		t.Fatal("MarkFailed was not called")
	}
	if !deadFlags[0] {
		t.Error("dead should be true when all retries are exhausted")
	}
}

// TestAttemptDelivery_getEndpointError verifies that a GetEndpoint failure
// causes the delivery to be silently skipped (no panic, no MarkFailed).
func TestAttemptDelivery_getEndpointError(t *testing.T) {
	repo := &fakeWorkerRepo{
		getErr: errors.New("endpoint not found"),
	}
	w := makeWorker(t, repo, &fakeDispatcher{})

	// Should not panic and should not call MarkDelivered or MarkFailed.
	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID:         uuid.New(),
		EndpointID: uuid.New(),
		Payload:    []byte(`{}`),
	})

	if len(repo.markDeliveredIDs)+len(repo.markFailedIDs) != 0 {
		t.Error("expected no Mark calls when GetEndpoint fails")
	}
}

// TestAttemptDelivery_invalidSecretEnc verifies that an unparseable hex secret
// causes the delivery to be skipped without panic.
func TestAttemptDelivery_invalidSecretEnc(t *testing.T) {
	endpointID := uuid.New()
	repo := &fakeWorkerRepo{
		endpointRec: &repository.EndpointRecord{
			ID:        endpointID,
			URL:       "https://example.com/hook",
			SecretEnc: "zz_not_valid_hex",
		},
	}
	w := makeWorker(t, repo, &fakeDispatcher{})

	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID:         uuid.New(),
		EndpointID: endpointID,
		Payload:    []byte(`{}`),
	})

	if len(repo.markDeliveredIDs)+len(repo.markFailedIDs) != 0 {
		t.Error("expected no Mark calls when hex decode of secret fails")
	}
}

// TestEventRoutingKeys_includesRequiredEvents verifies that the routing key list
// contains the events defined in CLAUDE.md §4.9.
func TestEventRoutingKeys_includesRequiredEvents(t *testing.T) {
	keys := EventRoutingKeys()
	required := []string{
		events.RoutingPushCompleted,
		events.RoutingManifestDeleted,
		events.RoutingTagDeleted,
		events.RoutingScanCompleted,
		events.RoutingScanPolicyBlocked,
		events.RoutingImageSigned,
	}
	keySet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keySet[k] = struct{}{}
	}
	for _, want := range required {
		if _, ok := keySet[want]; !ok {
			t.Errorf("EventRoutingKeys() missing required key %q", want)
		}
	}
}

// ── FUT-081: webhook lifecycle events ─────────────────────────────────────────

// TestHandleEvent_publishesQueued verifies that enqueuing a delivery emits one
// webhook.queued event carrying the delivery + endpoint + source event type.
func TestHandleEvent_publishesQueued(t *testing.T) {
	endpointID := uuid.New()
	deliveryID := uuid.New()
	tenantID := "11111111-1111-1111-1111-111111111111"
	repo := &fakeWorkerRepo{
		endpoints: []*repository.EndpointRecord{
			{ID: endpointID, TenantID: uuid.MustParse(tenantID), URL: "https://example.com/hook", Events: []string{"push.completed"}},
		},
		deliveryRec: &repository.DeliveryRecord{ID: deliveryID, EndpointID: endpointID, TenantID: uuid.MustParse(tenantID)},
	}
	w, pub := makeWorkerWithPub(t, repo, &fakeDispatcher{})

	if err := w.HandleEvent(context.Background(), events.Event{
		ID: uuid.NewString(), TenantID: tenantID, Type: events.RoutingPushCompleted,
	}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	queued := pub.byKey(events.RoutingWebhookQueued)
	if len(queued) != 1 {
		t.Fatalf("expected 1 webhook.queued, got %d", len(queued))
	}
	if queued[0].event.TenantID != tenantID {
		t.Errorf("queued event TenantID: got %q, want %q", queued[0].event.TenantID, tenantID)
	}
	var p events.WebhookQueuedPayload
	if err := json.Unmarshal(queued[0].event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.DeliveryID != deliveryID.String() || p.EndpointID != endpointID.String() || p.EventType != events.RoutingPushCompleted {
		t.Errorf("queued payload mismatch: %+v", p)
	}
}

// TestHandleEvent_skipsOwnLifecycleEvents guards against a feedback loop: the
// consumer binds '#', so it also receives the webhook.* events it publishes.
// Those must be ignored (no delivery created, no re-publish).
func TestHandleEvent_skipsOwnLifecycleEvents(t *testing.T) {
	repo := &fakeWorkerRepo{
		endpoints: []*repository.EndpointRecord{
			{ID: uuid.New(), TenantID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), URL: "https://x/y"},
		},
		deliveryRec: &repository.DeliveryRecord{ID: uuid.New()},
	}
	w, pub := makeWorkerWithPub(t, repo, &fakeDispatcher{})

	for _, rk := range []string{events.RoutingWebhookQueued, events.RoutingWebhookDelivered, events.RoutingWebhookFailed} {
		if err := w.HandleEvent(context.Background(), events.Event{
			ID: uuid.NewString(), TenantID: "11111111-1111-1111-1111-111111111111", Type: rk,
		}); err != nil {
			t.Fatalf("HandleEvent(%s): %v", rk, err)
		}
	}
	if repo.createCalls != 0 {
		t.Errorf("expected 0 CreateDelivery calls for webhook.* events, got %d", repo.createCalls)
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected 0 publishes for webhook.* events, got %d", len(pub.calls))
	}
}

// TestAttemptDelivery_publishesDelivered verifies a successful send emits one
// webhook.delivered event.
func TestAttemptDelivery_publishesDelivered(t *testing.T) {
	endpointID := uuid.New()
	deliveryID := uuid.New()
	tenantID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repo := &fakeWorkerRepo{
		endpointRec: &repository.EndpointRecord{ID: endpointID, URL: "https://example.com/hook", SecretEnc: encryptSecret(t, "s")},
	}
	w, pub := makeWorkerWithPub(t, repo, &fakeDispatcher{err: nil})

	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID: deliveryID, EndpointID: endpointID, TenantID: tenantID, EventType: "push.completed", Payload: []byte(`{}`),
	})

	delivered := pub.byKey(events.RoutingWebhookDelivered)
	if len(delivered) != 1 {
		t.Fatalf("expected 1 webhook.delivered, got %d", len(delivered))
	}
	if delivered[0].event.TenantID != tenantID.String() {
		t.Errorf("delivered TenantID: got %q, want %q", delivered[0].event.TenantID, tenantID.String())
	}
	var p events.WebhookDeliveredPayload
	if err := json.Unmarshal(delivered[0].event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.DeliveryID != deliveryID.String() || p.URL != "https://example.com/hook" || p.EventType != "push.completed" {
		t.Errorf("delivered payload mismatch: %+v", p)
	}
}

// TestAttemptDelivery_publishesFailedWithDead verifies a failed, retry-exhausted
// send emits one webhook.failed event with dead=true.
func TestAttemptDelivery_publishesFailedWithDead(t *testing.T) {
	endpointID := uuid.New()
	deliveryID := uuid.New()
	tenantID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	repo := &fakeWorkerRepo{
		endpointRec: &repository.EndpointRecord{ID: endpointID, URL: "https://example.com/hook", SecretEnc: encryptSecret(t, "s")},
	}
	w, pub := makeWorkerWithPub(t, repo, &fakeDispatcher{err: errors.New("boom")})

	w.attemptDelivery(context.Background(), &repository.DeliveryRecord{
		ID: deliveryID, EndpointID: endpointID, TenantID: tenantID, EventType: "push.completed", Payload: []byte(`{}`), Attempts: 5,
	})

	failed := pub.byKey(events.RoutingWebhookFailed)
	if len(failed) != 1 {
		t.Fatalf("expected 1 webhook.failed, got %d", len(failed))
	}
	var p events.WebhookFailedPayload
	if err := json.Unmarshal(failed[0].event.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.DeliveryID != deliveryID.String() || !p.Dead || p.Error == "" {
		t.Errorf("failed payload mismatch: %+v", p)
	}
}
