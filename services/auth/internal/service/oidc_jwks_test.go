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
