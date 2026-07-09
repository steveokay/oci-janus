package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPoster_signsAndPosts spins up a loopback server, posts a signed body via a
// stock-client Poster, and asserts the headers + HMAC signature the endpoint
// observed match what the test computes independently.
func TestPoster_signsAndPosts(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	secret := []byte("s3cr3t-key")

	var gotSig, gotContentType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Registry-Signature")
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPosterForTest()
	code, err := p.Post(context.Background(), srv.URL, body, secret)
	if err != nil {
		t.Fatalf("Post returned error: %v", err)
	}
	if code != 200 {
		t.Fatalf("expected status 200, got %d", code)
	}
	if gotContentType != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", gotContentType)
	}
	if string(gotBody) != string(body) {
		t.Fatalf("body not echoed: got %q want %q", gotBody, body)
	}

	// Compute the expected signature independently.
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != wantSig {
		t.Fatalf("signature mismatch: got %q want %q", gotSig, wantSig)
	}
}

// TestValidateURL_rejectsHTTPAndPrivate asserts non-HTTPS and private-IP
// destinations are both rejected.
func TestValidateURL_rejectsHTTPAndPrivate(t *testing.T) {
	if err := ValidateURL("http://example.com/x"); err == nil {
		t.Fatal("expected error for non-HTTPS URL, got nil")
	}
	if err := ValidateURL("https://127.0.0.1/x"); err == nil {
		t.Fatal("expected error for private IP URL, got nil")
	}
}

// TestBuildPayload_shape asserts the envelope carries the expected fields.
func TestBuildPayload_shape(t *testing.T) {
	got := string(buildPayload("scanner_freshness", "Subject", "Summary", "/repos/x", "tid", time.Unix(0, 0).UTC()))
	for _, want := range []string{
		`"event":"notification"`,
		`"category":"scanner_freshness"`,
		`"subject":"Subject"`,
		`"tenant_id":"tid"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("payload %q missing %q", got, want)
		}
	}
}

// TestBackoff_schedule asserts the retry schedule + clamp behaviour.
func TestBackoff_schedule(t *testing.T) {
	want := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Fatalf("Backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := Backoff(99); got != 2*time.Hour {
		t.Fatalf("Backoff(99) = %v, want 2h", got)
	}
}
