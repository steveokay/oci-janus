// Package service — oidc_jwks_test.go covers the in-process JWKS cache
// via an httptest.Server stub IdP. Tests assert:
//   - cold fetch hits the network and populates the cache
//   - warm fetch within TTL serves from cache (no extra network hit)
//   - expired entry triggers a refresh
//   - 16-issuer SEC-048 cap is enforced (LRU-by-fetchedAt eviction)
//   - network errors fail closed (no stale-serve past TTL)

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// startStubIdP spins up an httptest.Server that serves the OIDC
// well-known discovery + JWKS for the given RSA key. The returned
// *atomic.Int64 counts JWKS fetches so callers can assert caching.
func startStubIdP(t *testing.T, key *rsa.PrivateKey, kid string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		host := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{"jwks_uri": host + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jwk := map[string]any{
			"kty": "RSA",
			"use": "sig",
			"kid": kid,
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk}})
	})
	return httptest.NewServer(mux), &calls
}

// TestJWKSCache exercises the cache lifecycle: cold → warm → expired →
// refresh, plus the 16-issuer cap.
func TestJWKSCache(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv, calls := startStubIdP(t, key, "kid-1")
	defer srv.Close()

	cache := newJWKSCache(http.DefaultClient)
	ctx := context.Background()

	t.Run("first fetch hits network", func(t *testing.T) {
		got, err := cache.Fetch(ctx, srv.URL, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if k := got["kid-1"]; k == nil {
			t.Fatalf("kid-1 missing from cache")
		}
		if c := calls.Load(); c != 1 {
			t.Errorf("first fetch should hit network once, got %d", c)
		}
	})

	t.Run("second fetch within TTL is cached", func(t *testing.T) {
		_, err := cache.Fetch(ctx, srv.URL, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if c := calls.Load(); c != 1 {
			t.Errorf("second fetch within TTL should not hit network, got %d", c)
		}
	})

	t.Run("expired entry triggers refresh", func(t *testing.T) {
		// TTL=0 means every entry is considered expired immediately.
		_, err := cache.Fetch(ctx, srv.URL, 0)
		if err != nil {
			t.Fatal(err)
		}
		if c := calls.Load(); c != 2 {
			t.Errorf("expired entry should hit network, got %d", c)
		}
	})

	t.Run("16-issuer cap is enforced", func(t *testing.T) {
		// Spin up 20 additional issuer stubs and fetch from each. The
		// cache size must never exceed 16; eviction is by oldest
		// fetchedAt so the original srv (the warmest) gets evicted as
		// later issuers fill the slots.
		for i := 0; i < 20; i++ {
			extra, _ := startStubIdP(t, key, "kid-extra")
			defer extra.Close()
			_, _ = cache.Fetch(ctx, extra.URL, time.Hour)
		}
		if size := cache.size(); size > maxJWKSCacheSize {
			t.Errorf("cache size = %d, want <= %d (SEC-048 analog)", size, maxJWKSCacheSize)
		}
	})
}

// TestJWKSCacheFailsClosedOnNetworkError verifies the security-critical
// property: once an entry's TTL expires, a network failure must NOT
// silently serve the stale entry. The CI runner must learn about the
// IdP outage so it can retry (or surface the failure to the operator).
func TestJWKSCacheFailsClosedOnNetworkError(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := startStubIdP(t, key, "kid-1")

	cache := newJWKSCache(http.DefaultClient)
	ctx := context.Background()

	// Warm the cache.
	if _, err := cache.Fetch(ctx, srv.URL, time.Hour); err != nil {
		t.Fatalf("warm-up fetch failed: %v", err)
	}

	// Tear down the server.
	srv.Close()

	// Within TTL, the cached entry should still serve.
	if _, err := cache.Fetch(ctx, srv.URL, time.Hour); err != nil {
		t.Fatalf("warm-cache fetch should succeed: %v", err)
	}

	// Past TTL, the network call must be attempted and must fail loud.
	if _, err := cache.Fetch(ctx, srv.URL, 0); err == nil {
		t.Error("expired entry + dead server must return an error (fail closed)")
	}
}

// TestJWKS_SSRF_ForeignHostRejected verifies SEC-058: a hostile discovery
// document that points jwks_uri at a DIFFERENT host (the classic SSRF
// primitive — aim it at 169.254.169.254 or an internal service) must be
// rejected before any fetch of that URL.
func TestJWKS_SSRF_ForeignHostRejected(t *testing.T) {
	// A "victim" server we do NOT want to be tricked into calling. It
	// counts hits so we can assert it was never reached.
	var victimHits atomic.Int64
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		victimHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	}))
	defer victim.Close()

	// The malicious IdP: its discovery doc redirects jwks_uri at the
	// victim host instead of its own.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jwks_uri": victim.URL + "/jwks"})
	})
	evil := httptest.NewServer(mux)
	defer evil.Close()

	cache := newJWKSCache(http.DefaultClient)
	if _, err := cache.Fetch(context.Background(), evil.URL, time.Hour); err == nil {
		t.Fatal("expected error: jwks_uri on a foreign host must be rejected")
	}
	if h := victimHits.Load(); h != 0 {
		t.Errorf("victim host was contacted %d times; SSRF guard failed", h)
	}
}

// TestJWKS_OversizeResponse_Rejected verifies SEC-059: a JWKS (or
// discovery) body larger than jwksMaxResponseBytes must error rather than
// buffer unbounded memory. The stub streams > 1 MiB on the discovery
// endpoint so the cap trips on the first fetch.
func TestJWKS_OversizeResponse_Rejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write a valid-JSON-prefix followed by megabytes of filler so the
		// LimitReader trips well before any decode could complete.
		_, _ = w.Write([]byte(`{"jwks_uri":"`))
		filler := make([]byte, 64*1024)
		for i := range filler {
			filler[i] = 'a'
		}
		for written := 0; written < (jwksMaxResponseBytes + 256*1024); written += len(filler) {
			if _, err := w.Write(filler); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cache := newJWKSCache(http.DefaultClient)
	if _, err := cache.Fetch(context.Background(), srv.URL, time.Hour); err == nil {
		t.Fatal("expected error: oversize discovery/JWKS response must be rejected")
	}
}
