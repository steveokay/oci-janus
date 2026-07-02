// Package handler — http_workload_token_test.go covers the public
// POST /auth/token/workload endpoint:
//
//   - Feature-off when the OIDCTrustService isn't wired (503).
//   - 400 when neither body nor header carries a JWT.
//   - 401 when the JWT is malformed.
//   - 429 when the per-(iss, sub) Redis bucket is exhausted.
//   - Per-(iss, sub) isolation: a different subject gets its own bucket.
//
// The happy-path exchange is exercised end-to-end in the service-layer
// tests (oidc_trust_test.go); duplicating it here would require booting
// an httptest IdP per case, which adds little. The handler-level test
// focuses on the HTTP wiring.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// newWorkloadHandler builds an HTTPHandler with only the workload
// exchange wired — the rest of the service surface is left nil so the
// test focuses on the rate-limit + dispatch logic.
//
// oidc is nil so the exchange short-circuits to 503; tests that need a
// real exchange would have to spin up a full service stack which is
// already covered at the service layer.
func newWorkloadHandler(t *testing.T) (*HTTPHandler, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() { mr.Close() })

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	h := &HTTPHandler{}
	h.workloadRedis = rdb
	// oidc deliberately left nil — tests below assert the 503 short-circuit.
	return h, mr
}

// mkUnsignedJWT builds a syntactically valid but unsigned JWT. The
// signature is garbage; peekIssuerAndSubject succeeds (it only reads
// the claims) so the rate-limit gate fires, but the downstream exchange
// (when wired) would reject the signature. Used to test rate-limit
// gating without booting a real IdP.
func mkUnsignedJWT(iss, sub string) string {
	claims := jwt.MapClaims{
		"iss": iss,
		"sub": sub,
		"aud": "registry.example.com",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	// SigningMethodNone needs the "none" sentinel; we don't care about
	// signature validity here because the OIDC service is nil in these
	// tests.
	s, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	return s
}

// TestWorkloadHandler_FeatureOff_503 verifies the 503 short-circuit
// when no OIDCTrustService is wired.
func TestWorkloadHandler_FeatureOff_503(t *testing.T) {
	h, _ := newWorkloadHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/auth/token/workload",
		strings.NewReader(`{"oidc_jwt":"x.y.z"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleWorkloadTokenExchange(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Contains(t, body["error"], "not configured")
}

// TestWorkloadHandler_MissingJWT_400 verifies the "no JWT" branch.
func TestWorkloadHandler_MissingJWT_400(t *testing.T) {
	h, _ := newWorkloadHandler(t)
	// Wire a non-nil oidc placeholder so we get past the 503 gate. We
	// use a non-nil pointer to the zero value — the handler dereferences
	// h.oidc only when it calls ExchangeWorkloadToken, which won't
	// happen on this path (missing JWT short-circuits first).
	h.oidc = workloadPlaceholder

	req := httptest.NewRequest(http.MethodPost, "/auth/token/workload",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleWorkloadTokenExchange(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestWorkloadHandler_MalformedJWT_401 verifies the unparsable-JWT
// branch returns 401 (the same shape a signature-failed JWT would).
func TestWorkloadHandler_MalformedJWT_401(t *testing.T) {
	h, _ := newWorkloadHandler(t)
	h.oidc = workloadPlaceholder

	req := httptest.NewRequest(http.MethodPost, "/auth/token/workload",
		strings.NewReader(`{"oidc_jwt":"not-a-jwt"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleWorkloadTokenExchange(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestWorkloadHandler_RateLimit429 verifies that once a single
// (iss, sub) pair has burned its 100/min budget, the 101st request
// returns 429 with a Retry-After header.
func TestWorkloadHandler_RateLimit429(t *testing.T) {
	h, _ := newWorkloadHandler(t)
	h.oidc = workloadPlaceholder

	jwtStr := mkUnsignedJWT("https://gh.io", "repo:org/r:ref:refs/heads/main")

	ctx := context.Background()
	for i := 0; i < workloadRateLimitPerMin; i++ {
		exceeded, _, err := h.checkWorkloadRateLimit(ctx, "https://gh.io", "repo:org/r:ref:refs/heads/main")
		require.NoError(t, err)
		require.False(t, exceeded, "request %d should not exceed", i+1)
	}
	// The 101st must trip the limit.
	exceeded, retryAfter, err := h.checkWorkloadRateLimit(ctx, "https://gh.io", "repo:org/r:ref:refs/heads/main")
	require.NoError(t, err)
	require.True(t, exceeded, "101st request must be rate-limited")
	require.Equal(t, 60, retryAfter)

	// Round-trip via the actual HTTP handler to confirm the response
	// shape (status, Retry-After header, JSON error body).
	req := httptest.NewRequest(http.MethodPost, "/auth/token/workload",
		strings.NewReader(`{"oidc_jwt":"`+jwtStr+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleWorkloadTokenExchange(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "60", w.Header().Get("Retry-After"))
}

// TestWorkloadHandler_RateLimitIsolation verifies that two different
// subjects get their own buckets — a noisy CI runner on one subject
// must NOT starve a quiet CI runner on a different subject.
func TestWorkloadHandler_RateLimitIsolation(t *testing.T) {
	h, _ := newWorkloadHandler(t)
	h.oidc = workloadPlaceholder

	ctx := context.Background()
	// Burn the budget for subject A.
	for i := 0; i < workloadRateLimitPerMin; i++ {
		exceeded, _, err := h.checkWorkloadRateLimit(ctx, "https://gh.io", "subject-A")
		require.NoError(t, err)
		require.False(t, exceeded)
	}
	exceeded, _, err := h.checkWorkloadRateLimit(ctx, "https://gh.io", "subject-A")
	require.NoError(t, err)
	require.True(t, exceeded, "subject-A should be exhausted")

	// Subject B must still be allowed.
	exceeded, _, err = h.checkWorkloadRateLimit(ctx, "https://gh.io", "subject-B")
	require.NoError(t, err)
	require.False(t, exceeded, "subject-B must have its own bucket")
}

// TestExtractWorkloadJWT_BodyAndHeader covers the two extraction shapes.
func TestExtractWorkloadJWT_BodyAndHeader(t *testing.T) {
	t.Run("body takes precedence", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x",
			strings.NewReader(`{"oidc_jwt":"from-body"}`))
		req.Header.Set("Authorization", "Bearer from-header")
		got := extractWorkloadJWT(req)
		require.Equal(t, "from-body", got)
	})

	t.Run("header fallback when body empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer from-header")
		got := extractWorkloadJWT(req)
		require.Equal(t, "from-header", got)
	})

	t.Run("nothing returns empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
		got := extractWorkloadJWT(req)
		require.Equal(t, "", got)
	})

	t.Run("oversized body is rejected", func(t *testing.T) {
		big := strings.Repeat("a", workloadRequestBodyLimit+100)
		req := httptest.NewRequest(http.MethodPost, "/x",
			strings.NewReader(`{"oidc_jwt":"`+big+`"}`))
		// Decode should fail or return empty because the body is too long
		// for the configured limit. We don't assert WHICH happens —
		// either is safe (we don't want to mint a token from a hostile
		// multi-MB payload).
		got := extractWorkloadJWT(req)
		// Either empty (decode aborted) or shorter than the original —
		// in any case, MUST NOT be the full payload.
		require.True(t, len(got) < len(big), "oversized body must not be accepted intact")
	})
}

// TestPeekIssuerAndSubject covers the unsigned-JWT parse path.
func TestPeekIssuerAndSubject(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		j := mkUnsignedJWT("https://gh.io", "repo:org/r:ref:refs/heads/main")
		iss, sub, err := peekIssuerAndSubject(j)
		require.NoError(t, err)
		require.Equal(t, "https://gh.io", iss)
		require.Equal(t, "repo:org/r:ref:refs/heads/main", sub)
	})

	t.Run("malformed", func(t *testing.T) {
		_, _, err := peekIssuerAndSubject("not.a.jwt")
		require.Error(t, err)
	})

	t.Run("missing claims", func(t *testing.T) {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{})
		s, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		_, _, err := peekIssuerAndSubject(s)
		require.Error(t, err)
	})
}

// TestWorkloadRateLimitKey covers SEC-061: the Redis bucket key is a
// fixed-length hash of the (iss, sub) tuple, so an attacker cannot bloat
// Redis memory with a multi-megabyte `sub` claim, and the NUL separator
// keeps otherwise-colliding tuples distinct.
func TestWorkloadRateLimitKey(t *testing.T) {
	const prefix = "workload:rate:"

	t.Run("length is bounded regardless of claim size", func(t *testing.T) {
		huge := strings.Repeat("A", 5<<20) // 5 MiB subject
		key := workloadRateLimitKey("https://gh.io", huge)
		// prefix + 64 hex chars, never proportional to the input.
		require.Equal(t, len(prefix)+64, len(key))
		require.True(t, strings.HasPrefix(key, prefix))
	})

	t.Run("separator ambiguity resolved", func(t *testing.T) {
		// ("a", "b:c") and ("a:b", "c") must NOT collide — the old
		// `iss + ":" + sub` form mapped both to "a:b:c".
		require.NotEqual(t,
			workloadRateLimitKey("a", "b:c"),
			workloadRateLimitKey("a:b", "c"),
		)
	})

	t.Run("deterministic for the same tuple", func(t *testing.T) {
		require.Equal(t,
			workloadRateLimitKey("https://gh.io", "repo:org/r"),
			workloadRateLimitKey("https://gh.io", "repo:org/r"),
		)
	})
}

// workloadPlaceholder is a non-nil but zero-valued *service.OIDCTrustService
// pointer that test cases assign to h.oidc to escape the 503 short-circuit
// without booting a real service. The pointed-to memory is never
// dereferenced because the assertions short-circuit on earlier branches
// (missing JWT, malformed JWT, rate-limit). If a test inadvertently
// invokes ExchangeWorkloadToken on this placeholder, it will panic on the
// internal nil deps — that's a feature, not a bug; deeper integration
// tests live at the service layer.
var workloadPlaceholder = &service.OIDCTrustService{}
