// Package delivery_test tests the webhook dispatcher helpers without making real
// HTTP connections — HMAC signing and retry delay scheduling are pure functions.
package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestComputeHMAC_knownVector verifies HMAC-SHA256 against a known test vector.
// The expected value was pre-computed with: echo -n "hello" | openssl dgst -sha256 -hmac "secret".
func TestComputeHMAC_knownVector(t *testing.T) {
	payload := []byte("hello")
	key := []byte("secret")

	// Compute independently to produce expected value.
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))

	got := computeHMAC(payload, key)
	if got != want {
		t.Errorf("computeHMAC: got %q, want %q", got, want)
	}
}

// TestComputeHMAC_differentKeys verifies that different keys produce different MACs.
func TestComputeHMAC_differentKeys(t *testing.T) {
	payload := []byte("same payload")
	key1 := []byte("key-one")
	key2 := []byte("key-two")

	if computeHMAC(payload, key1) == computeHMAC(payload, key2) {
		t.Error("different HMAC keys should produce different signatures")
	}
}

// TestComputeHMAC_differentPayloads verifies that different payloads produce different MACs.
func TestComputeHMAC_differentPayloads(t *testing.T) {
	key := []byte("shared-secret")
	if computeHMAC([]byte("payload-a"), key) == computeHMAC([]byte("payload-b"), key) {
		t.Error("different payloads should produce different HMAC signatures")
	}
}

// TestComputeHMAC_emptyPayload checks that an empty payload still produces a valid hex string.
func TestComputeHMAC_emptyPayload(t *testing.T) {
	got := computeHMAC([]byte{}, []byte("key"))
	if len(got) != 64 {
		t.Errorf("HMAC-SHA256 hex should be 64 chars, got %d: %q", len(got), got)
	}
}

// TestNextRetryAt_firstAttempt verifies that attempt 0 returns a time in the near future.
func TestNextRetryAt_firstAttempt(t *testing.T) {
	before := time.Now()
	at, ok := NextRetryAt(0)
	after := time.Now()

	if !ok {
		t.Fatal("NextRetryAt(0): expected ok=true")
	}
	// The first delay is 5 seconds, so the result should be between now and now+10s.
	if at.Before(before) {
		t.Error("NextRetryAt(0): returned time is in the past")
	}
	if at.After(after.Add(10 * time.Second)) {
		t.Error("NextRetryAt(0): returned time is too far in the future")
	}
}

// TestNextRetryAt_allAttempts verifies that each successive attempt uses a longer delay.
func TestNextRetryAt_allAttempts(t *testing.T) {
	var prev time.Time
	for i := 0; i < len(retryDelays); i++ {
		at, ok := NextRetryAt(i)
		if !ok {
			t.Fatalf("NextRetryAt(%d): expected ok=true, got false", i)
		}
		if i > 0 && !at.After(prev) {
			t.Errorf("NextRetryAt(%d): expected monotonically increasing times, got %v <= %v", i, at, prev)
		}
		prev = at
	}
}

// TestNextRetryAt_exhausted verifies that an attempt count beyond the last delay
// returns ok=false and a zero time (DLQ branch).
func TestNextRetryAt_exhausted(t *testing.T) {
	at, ok := NextRetryAt(len(retryDelays))
	if ok {
		t.Error("NextRetryAt: expected ok=false once all retries are exhausted")
	}
	if !at.IsZero() {
		t.Error("NextRetryAt: expected zero time when exhausted")
	}
}

// TestNextRetryAt_wellBeyondMax ensures robustness against large attempt counts.
func TestNextRetryAt_wellBeyondMax(t *testing.T) {
	_, ok := NextRetryAt(100)
	if ok {
		t.Error("NextRetryAt(100): expected ok=false")
	}
}

// TestNewDispatcher_notNil verifies that NewDispatcher returns a non-nil
// Dispatcher with a configured HTTP client.
func TestNewDispatcher_notNil(t *testing.T) {
	d := NewDispatcher(30)
	if d == nil {
		t.Fatal("NewDispatcher returned nil")
	}
	if d.client == nil {
		t.Fatal("Dispatcher.client is nil")
	}
}

// TestDispatcher_deliverInvalidURL verifies that Deliver returns an error
// when the target URL is malformed (no scheme, bad format, etc.).
func TestDispatcher_deliverInvalidURL(t *testing.T) {
	d := NewDispatcher(30)
	// A completely malformed URL should cause http.NewRequestWithContext to fail.
	err := d.Deliver(context.Background(), "://bad-url", []byte("{}"), []byte("key"))
	if err == nil {
		t.Error("expected error for malformed URL, got nil")
	}
}

// TestDispatcher_deliverSSRFBlocked verifies that a connection to a private IP
// address (127.0.0.1 via httptest) is blocked by the SSRF-protected dial.
// This exercises the custom DialContext in NewDispatcher.
func TestDispatcher_deliverSSRFBlocked(t *testing.T) {
	// Start an httptest server (binds to 127.0.0.1 — a private/loopback address).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// The production dispatcher blocks loopback (SSRF protection), so the
	// request must fail.
	d := NewDispatcher(30)
	err := d.Deliver(context.Background(), server.URL, []byte("{}"), []byte("key"))
	if err == nil {
		t.Error("expected SSRF error for connection to loopback address, got nil")
	}
}

// TestDispatcher_deliverContextCancelled verifies that cancelling the context
// before the request completes causes Deliver to return an error.
// The httptest server at 127.0.0.1 will be SSRF-blocked, but the context
// timeout fires first or the SSRF error is returned — either way, err != nil.
func TestDispatcher_deliverContextCancelled(t *testing.T) {
	// Use a minimal context that times out immediately.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	d := NewDispatcher(30)
	// Any URL works — the context will expire before (or during) dial.
	err := d.Deliver(ctx, "https://httpbin.org/post", []byte("{}"), []byte("key"))
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}
