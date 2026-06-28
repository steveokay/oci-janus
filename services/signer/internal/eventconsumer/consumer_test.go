// Package eventconsumer tests cover the FUT-017 cache.populated consumer's
// policy gate, idempotency guard, and the happy-path sign side effect.
//
// All tests use hand-rolled fakes so no broker or DB is required.
package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/signer/internal/repository"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

// fakeSigner records each SignPayload call so tests can assert on what
// was (and was not) signed.
type fakeSigner struct {
	mu      sync.Mutex
	calls   []signCall
	sigB64  string
	signErr error
	keyID   string
}

type signCall struct {
	tenantID       string
	repositoryName string
	manifestDigest string
}

func (f *fakeSigner) SignPayload(tenantID, repositoryName, manifestDigest string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, signCall{tenantID, repositoryName, manifestDigest})
	return f.sigB64, f.signErr
}

func (f *fakeSigner) VerifyPayload(_, _, _, _ string) (bool, error) { return true, nil }
func (f *fakeSigner) KeyID() string                                 { return f.keyID }

func (f *fakeSigner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// fakePolicyLookup serves a single policy row keyed by upstream name.
// Returns (nil, nil) when no entry exists so we can also exercise the
// "no policy row" branch.
type fakePolicyLookup struct {
	policies map[string]*repository.ProxyCacheSignPolicy
	err      error
}

func (f *fakePolicyLookup) GetProxyCacheSignPolicy(_ context.Context, _, upstreamName string) (*repository.ProxyCacheSignPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.policies[upstreamName], nil
}

// fixedSigB64 is a base64-encoded test signature. The exact value
// doesn't matter — SignatureDigest just hashes the bytes and the test
// only asserts that the record landed in the store.
const fixedSigB64 = "dGVzdC1zaWduYXR1cmU="

// newTestHandler wires the smallest viable handler for the consumer
// tests. The real sigstore.Store is used because it's already
// goroutine-safe + lightweight; tests pass nil for the DB repo path so
// only the in-memory cache leg runs.
func newTestHandler(t *testing.T, lookup PolicyLookup, signer *fakeSigner) (*Handler, *sigstore.Store) {
	t.Helper()
	store := sigstore.New()
	h := NewHandler(signer, store, lookup)
	return h, store
}

// makeEvent assembles an events.Event with a CachePopulatedPayload body
// for the tests to feed into Handle.
func makeEvent(t *testing.T, payload events.CachePopulatedPayload) events.Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return events.Event{
		ID:         "evt-1",
		Type:       events.RoutingCachePopulated,
		TenantID:   payload.TenantID,
		OccurredAt: time.Now(),
		Version:    "1.0",
		Payload:    body,
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestHandle_NoPolicyRow proves the consumer treats a missing policy row
// as "feature disabled" — no sign call is made and no error returned.
func TestHandle_NoPolicyRow(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{policies: map[string]*repository.ProxyCacheSignPolicy{}}
	h, _ := newTestHandler(t, lookup, signer)

	err := h.Handle(context.Background(), makeEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-1",
		UpstreamName:   "dockerhub",
		ManifestDigest: "sha256:aaaa",
	}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := signer.callCount(); got != 0 {
		t.Fatalf("expected 0 sign calls when no policy, got %d", got)
	}
}

// TestHandle_PolicyDisabled exercises the auto_sign=false branch.
func TestHandle_PolicyDisabled(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{policies: map[string]*repository.ProxyCacheSignPolicy{
		"dockerhub": {TenantID: "tenant-1", UpstreamName: "dockerhub", AutoSign: false, KeyID: "key-1"},
	}}
	h, _ := newTestHandler(t, lookup, signer)

	err := h.Handle(context.Background(), makeEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-1",
		UpstreamName:   "dockerhub",
		ManifestDigest: "sha256:bbbb",
	}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := signer.callCount(); got != 0 {
		t.Fatalf("expected 0 sign calls when auto_sign=false, got %d", got)
	}
}

// TestHandle_EmptyKeyID covers the safety net: an operator flipped
// auto_sign on without choosing a key. We must not sign with an empty
// signer id — that would land a meaningless record in the store.
func TestHandle_EmptyKeyID(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{policies: map[string]*repository.ProxyCacheSignPolicy{
		"dockerhub": {TenantID: "tenant-1", UpstreamName: "dockerhub", AutoSign: true, KeyID: ""},
	}}
	h, _ := newTestHandler(t, lookup, signer)

	err := h.Handle(context.Background(), makeEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-1",
		UpstreamName:   "dockerhub",
		ManifestDigest: "sha256:cccc",
	}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := signer.callCount(); got != 0 {
		t.Fatalf("expected 0 sign calls when key_id is empty, got %d", got)
	}
}

// TestHandle_HappyPath proves an enabled policy causes exactly one
// SignPayload call AND that the signature record is persisted to the
// store with the expected key id.
func TestHandle_HappyPath(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{policies: map[string]*repository.ProxyCacheSignPolicy{
		"dockerhub": {TenantID: "tenant-1", UpstreamName: "dockerhub", AutoSign: true, KeyID: "key-1"},
	}}
	h, store := newTestHandler(t, lookup, signer)

	err := h.Handle(context.Background(), makeEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-1",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		Reference:      "latest",
		ManifestDigest: "sha256:dddd",
	}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if got := signer.callCount(); got != 1 {
		t.Fatalf("expected 1 sign call, got %d", got)
	}

	// Verify the signature record made it into the store under the
	// expected (tenant, manifest) key with signer_id == policy.KeyID.
	rec := store.FindRec(context.Background(), "tenant-1", "sha256:dddd", "key-1")
	if rec == nil {
		t.Fatalf("expected signature record to be stored, got nil")
	}
	if rec.KeyID != "key-1" {
		t.Fatalf("expected KeyID=key-1, got %q", rec.KeyID)
	}
	if rec.RepositoryName != "proxy/dockerhub/library/ubuntu" {
		t.Fatalf("expected repository_name=proxy/dockerhub/library/ubuntu, got %q", rec.RepositoryName)
	}
}

// TestHandle_Idempotent proves a second cache.populated event for the
// same (tenant, manifest, key) does NOT trigger a re-sign. This is the
// guard that protects against re-publishes from the proxy under retry.
func TestHandle_Idempotent(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{policies: map[string]*repository.ProxyCacheSignPolicy{
		"dockerhub": {TenantID: "tenant-1", UpstreamName: "dockerhub", AutoSign: true, KeyID: "key-1"},
	}}
	h, _ := newTestHandler(t, lookup, signer)

	ev := makeEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-1",
		UpstreamName:   "dockerhub",
		Image:          "library/ubuntu",
		ManifestDigest: "sha256:eeee",
	})

	if err := h.Handle(context.Background(), ev); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := h.Handle(context.Background(), ev); err != nil {
		t.Fatalf("second Handle: %v", err)
	}

	if got := signer.callCount(); got != 1 {
		t.Fatalf("expected 1 sign call across two redeliveries, got %d", got)
	}
}

// TestHandle_PolicyLookupError verifies a DB error during the policy
// fetch swallows the event (returns nil) so the consumer doesn't
// retry-loop on a broken DB. The log line is the operator signal.
func TestHandle_PolicyLookupError(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{err: errors.New("db down")}
	h, _ := newTestHandler(t, lookup, signer)

	err := h.Handle(context.Background(), makeEvent(t, events.CachePopulatedPayload{
		TenantID:       "tenant-1",
		UpstreamName:   "dockerhub",
		ManifestDigest: "sha256:ffff",
	}))
	if err != nil {
		t.Fatalf("expected nil error to ACK the event, got %v", err)
	}
	if got := signer.callCount(); got != 0 {
		t.Fatalf("expected 0 sign calls on policy lookup failure, got %d", got)
	}
}

// TestHandle_UnparseablePayload ensures malformed payloads are ACKed
// (logged + dropped) rather than NACKed into the DLX. A broken
// publisher should not be able to wedge the consumer.
func TestHandle_UnparseablePayload(t *testing.T) {
	signer := &fakeSigner{sigB64: fixedSigB64, keyID: "key-1"}
	lookup := &fakePolicyLookup{}
	h, _ := newTestHandler(t, lookup, signer)

	bad := events.Event{
		ID:      "evt-bad",
		Type:    events.RoutingCachePopulated,
		Version: "1.0",
		Payload: []byte(`{not valid json`),
	}
	if err := h.Handle(context.Background(), bad); err != nil {
		t.Fatalf("expected nil to ACK unparseable event, got %v", err)
	}
	if got := signer.callCount(); got != 0 {
		t.Fatalf("expected 0 sign calls on bad payload, got %d", got)
	}
}
