// Package handler — http_workload_token.go is the public HTTP endpoint
// for FUT-001 federated workload identity. CI runners POST their OIDC
// JWT to /auth/token/workload; this handler validates + exchanges it
// for a short-lived registry JWT.
//
// Authentication model: NO Bearer required. The OIDC JWT IS the
// credential — the exchange flow verifies its signature against the
// IdP's JWKS before minting anything.
//
// Per-(issuer, subject) Redis rate-limit (100 / 60s) applies BEFORE
// the JWKS fetch + signature verification so a compromised CI cannot
// burn unbounded server cycles. Fail-OPEN on Redis errors — the rate
// limit is an optimisation, not a security boundary; an attacker who
// can DoS Redis cannot also bypass the signature check that protects
// the actual token mint.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// workloadRateLimitPerMin is the per-(issuer, subject) request budget.
// 100 / 60s = ~1.6 RPS, which is more than enough for a CI bot's
// realistic exchange cadence (one exchange per pipeline run, not per
// step). Bumping this without considering DoS amplification is unwise.
const workloadRateLimitPerMin = 100

// workloadRateLimitWindow is the bucket window length. Note: the
// implementation calls `INCR` + `EXPIRE` unconditionally per request,
// which SLIDES the TTL forward on each hit — this is effectively a
// keep-alive fixed-window (a caller sustaining ≥ 1 req/s past 100 stays
// locked out until they pause for a full 60s), not a strict fixed-window
// where the bucket resets on wall-clock boundaries. Both variants still
// bound throughput to 100/60s; the sliding form is friendlier to
// legitimate bursty CI at the cost of stricter behaviour against
// aggressive brute-force retries. If a strict fixed-window is desired,
// swap the pipeline for `SET NX EX 60 <first-hit>` + `INCR` — see
// REM-023 follow-up.
const workloadRateLimitWindow = 60 * time.Second

// workloadRequestBodyLimit caps the JSON body we'll parse. A workload
// JWT is < 4 KiB in every realistic shape; 8 KiB gives 2× headroom.
const workloadRequestBodyLimit = 8 * 1024

// WithWorkloadExchange wires the OIDCTrustService + a Redis client so
// the POST /auth/token/workload route is served. Returns the receiver
// for the existing `httpH = httpH.WithXxx(...)` chaining pattern.
//
// If oidc is nil, the route is registered but returns 503 with a clear
// "feature not configured" message. We register the route either way so
// operators don't get a generic 404 — discovering "the endpoint exists,
// but it's off" is more useful than discovering "the endpoint doesn't
// exist."
func (h *HTTPHandler) WithWorkloadExchange(oidc *service.OIDCTrustService, rdb *redis.Client) *HTTPHandler {
	h.oidc = oidc
	h.workloadRedis = rdb
	return h
}

// HandleWorkloadTokenExchange is the public POST /auth/token/workload
// route. Accepts the OIDC JWT in the JSON body OR the Authorization
// header (Bearer form). The body field takes precedence so a Bearer
// header with a stale value doesn't override an explicit body field.
//
// Response shape mirrors OAuth 2.0 RFC 6749 §5.1: a JSON object with
// access_token / expires_in / token_type. Errors return JSON with an
// "error" field plus the appropriate HTTP status.
func (h *HTTPHandler) HandleWorkloadTokenExchange(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "workload token exchange is not configured")
		return
	}

	rawJWT := extractWorkloadJWT(r)
	if rawJWT == "" {
		writeJSONError(w, http.StatusBadRequest, "missing oidc_jwt")
		return
	}

	// Pre-parse (without verifying) to derive the rate-limit key. A
	// malformed token gets a generic 401 — same shape as a signature
	// failure so an attacker cannot enumerate parse-ability by status.
	iss, sub, err := peekIssuerAndSubject(rawJWT)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Rate-limit gate. Fail-OPEN on Redis errors — the rate limit is an
	// optimisation, not a security boundary. The signature check that
	// protects the token mint runs regardless of Redis state.
	if h.workloadRedis != nil {
		exceeded, retryAfter, rlErr := h.checkWorkloadRateLimit(r.Context(), iss, sub)
		switch {
		case rlErr != nil:
			slog.WarnContext(r.Context(), "workload rate-limit check failed; failing open",
				"err", rlErr,
			)
		case exceeded:
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}

	// Delegate to the service. Every reject reason has already been
	// classified into a clean gRPC status; we map them to HTTP codes.
	result, err := h.oidc.ExchangeWorkloadToken(r.Context(), rawJWT)
	if err != nil {
		if s, ok := status.FromError(err); ok {
			switch s.Code() {
			case codes.Unauthenticated:
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			case codes.Unavailable:
				writeJSONError(w, http.StatusServiceUnavailable, "idp unreachable")
			default:
				writeJSONError(w, http.StatusInternalServerError, "internal")
			}
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": result.AccessToken,
		"expires_in":   result.ExpiresIn,
		"token_type":   result.TokenType,
	})
}

// extractWorkloadJWT pulls the JWT from the JSON body or the
// Authorization header. Body takes precedence so a stale Bearer header
// doesn't override an explicit body field.
//
// Body shape: {"oidc_jwt": "<jwt>"}.
// Header shape: "Authorization: Bearer <jwt>".
func extractWorkloadJWT(r *http.Request) string {
	if r.Body != nil {
		// Cap the body length so a hostile caller cannot stream
		// gigabytes of JSON into the decoder.
		limited := http.MaxBytesReader(nil, r.Body, workloadRequestBodyLimit)
		defer func() { _ = limited.Close() }()
		var body struct {
			OIDCJWT string `json:"oidc_jwt"`
		}
		// json.NewDecoder may consume the entire body even if the
		// first field is what we want; that's fine because we don't
		// re-read it.
		if err := json.NewDecoder(limited).Decode(&body); err == nil && body.OIDCJWT != "" {
			return body.OIDCJWT
		}
		// Drain whatever's left so the connection can be reused.
		_, _ = io.Copy(io.Discard, limited)
	}

	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// peekIssuerAndSubject parses the JWT WITHOUT verifying the signature.
// Used only to derive the rate-limit bucket key. The downstream
// ExchangeWorkloadToken does the real signature verification.
func peekIssuerAndSubject(rawJWT string) (iss, sub string, err error) {
	tok, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseUnverified(rawJWT, jwt.MapClaims{})
	if err != nil {
		return "", "", err
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	iss, _ = claims["iss"].(string)
	sub, _ = claims["sub"].(string)
	if iss == "" || sub == "" {
		return "", "", errors.New("missing iss or sub")
	}
	return iss, sub, nil
}

// checkWorkloadRateLimit increments a Redis counter keyed on
// (issuer, subject) with a 60s TTL. Returns (exceeded, retryAfterSeconds, err).
//
// Implementation note: uses a pipeline of INCR + EXPIRE so the first
// request in a window sets the TTL atomically with the increment. The
// EXPIRE is unconditional (it's idempotent — re-setting the TTL on an
// existing key resets it, which is fine for this rate-limit shape).
func (h *HTTPHandler) checkWorkloadRateLimit(ctx context.Context, iss, sub string) (bool, int, error) {
	key := "workload:rate:" + iss + ":" + sub
	pipe := h.workloadRedis.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, workloadRateLimitWindow)
	if _, err := pipe.Exec(ctx); err != nil {
		if errors.Is(err, redis.Nil) {
			return false, 0, nil
		}
		return false, 0, err
	}
	n := incr.Val()
	if n > workloadRateLimitPerMin {
		return true, int(workloadRateLimitWindow / time.Second), nil
	}
	return false, 0, nil
}

// writeJSONError writes a small JSON error body with the right Content-Type
// so clients can parse the error rather than guessing from the status code.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
