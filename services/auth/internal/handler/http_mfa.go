// Package handler — this file holds the self-service TOTP MFA HTTP endpoints
// mounted under /api/v1/users/me/mfa (Tier-1 #1). They live in the auth service
// alongside /users/me because the same JWT middleware and Service value already
// used by /login and /apikeys give the cheapest path to the user record.
//
// Security posture (CLAUDE.md §7, §10):
//   - status / disable / regenerate require a normal access token (requireAuth).
//   - enroll / verify additionally accept a short-lived mfa_setup token so a
//     require-MFA-gated user who has no access token yet can still enrol.
//   - The enrol + verify responses carry secrets (the base32 TOTP secret, the
//     otpauth URI, and the one-time backup codes). They are returned to the
//     authenticated owner over HTTPS and are NEVER logged.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
	"github.com/steveokay/oci-janus/services/auth/internal/service"
)

// mfaClaims resolves the caller for the self-service MFA endpoints that must
// tolerate a forced-enrolment setup token: it first tries to validate the
// Bearer value as an mfa_setup token and, failing that, falls back to a normal
// access token. It returns the resolved claims plus whether the credential was
// a setup token (true) so the verify handler can complete login by minting a
// full access token. The Bearer token is extracted exactly the way requireAuth
// does — via the case-insensitive bearer.Extract helper (PENTEST-013) — so the
// two paths stay in lock-step.
func (h *HTTPHandler) mfaClaims(r *http.Request) (*service.Claims, bool, error) {
	tok, ok := bearer.Extract(r.Header.Get("Authorization"))
	if !ok || tok == "" {
		return nil, false, errors.New("missing bearer token")
	}
	// Prefer the mfa_setup token: it is only ever issued to un-enrolled users
	// under a require-MFA policy and authorizes exactly enroll/verify. On any
	// mismatch we fall through to normal access-token validation.
	if c, err := h.svc.ValidateMFASetupToken(r.Context(), tok); err == nil {
		return c, true, nil // forced-enrolment setup token
	}
	c, err := h.svc.ValidateToken(r.Context(), tok)
	return c, false, err
}

// loginMFA implements POST /api/v1/login/mfa — step 2 of the two-step login.
// It exchanges an mfa_challenge token (issued by POST /login when MFA is on)
// plus a TOTP code or single-use backup code for a full access token
// (amr=["pwd","otp"]). It mirrors the login handler's per-IP rate-limit and
// auth-failure recording so the second factor is brute-force-bounded exactly
// like the password step; the challenge token, code, and issued token are
// secrets and are never logged (CLAUDE.md §10).
func (h *HTTPHandler) loginMFA(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if err := h.svc.CheckIPRateLimit(r.Context(), ip); err != nil {
		writeError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS", "rate limit exceeded")
		return
	}
	var req struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChallengeToken == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "challenge_token and code are required")
		return
	}
	tok, err := h.svc.VerifyLoginMFA(r.Context(), req.ChallengeToken, req.Code)
	if err != nil {
		// Any failure (bad challenge token, wrong/replayed code) collapses to a
		// single 401 so an attacker cannot distinguish the cause. The service
		// layer already fed the account-lockout counter on a wrong code; here we
		// also bump the per-IP failure counter, matching the login handler.
		h.svc.RecordAuthFailure(r.Context(), ip)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid code")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// mfaStatus implements GET /api/v1/users/me/mfa — reports whether the caller has
// TOTP MFA enabled and, if so, when they enrolled. Requires a normal access token.
func (h *HTTPHandler) mfaStatus(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		// Malformed JWT — treat as an internal error rather than leaking the
		// raw claim value to the caller.
		slog.ErrorContext(r.Context(), "mfa status: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	st, err := h.svc.GetMFAStatus(r.Context(), userID)
	if err != nil {
		slog.ErrorContext(r.Context(), "mfa status: GetMFAStatus failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": st.Enabled, "enrolled_at": st.EnrolledAt})
}

// mfaEnroll implements POST /api/v1/users/me/mfa/enroll — mints a fresh pending
// TOTP secret and returns the base32 secret + otpauth URI so the FE can render a
// QR code. Accepts a setup token (forced enrolment) or a normal access token.
//
// The response body carries the plaintext secret + otpauth URI; both are secrets
// and are returned to the authenticated owner over HTTPS only — never logged.
func (h *HTTPHandler) mfaEnroll(w http.ResponseWriter, r *http.Request) {
	claims, _, err := h.mfaClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "mfa enroll: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	secret, uri, err := h.svc.BeginMFAEnrollment(r.Context(), userID, accountLabel(claims))
	if err != nil {
		if errors.Is(err, service.ErrMFAAlreadyEnabled) {
			// Idempotency guard: a user who already has MFA on must disable it
			// before re-enrolling, so re-enrolment is a conflict, not a silent
			// secret rotation.
			writeError(w, http.StatusConflict, "MFAALREADYENABLED", "mfa is already enabled")
			return
		}
		// Never log the error verbatim if it could carry secret material — the
		// service layer already guarantees enrolment errors are secret-free, but
		// we keep the response generic per CLAUDE.md §13.
		slog.ErrorContext(r.Context(), "mfa enroll failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret_base32": secret, "otpauth_uri": uri})
}

// mfaVerify implements POST /api/v1/users/me/mfa/verify — verifies the first TOTP
// code against the pending secret, flips MFA on, and returns the one-time backup
// codes. When the caller presented a setup token (forced enrolment) the response
// also carries a full access token (amr=["pwd","otp"]) so verification doubles as
// login completion.
//
// The backup codes (and, on the setup path, the access token) are secrets and are
// returned only to the authenticated owner over HTTPS — never logged.
func (h *HTTPHandler) mfaVerify(w http.ResponseWriter, r *http.Request) {
	claims, isSetup, err := h.mfaClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, tenantID, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "mfa verify: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "code is required")
		return
	}
	codes, err := h.svc.CompleteMFAEnrollment(r.Context(), userID, req.Code)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			// Wrong / replayed code. Distinct error code so the FE can prompt for
			// a fresh code without logging the user out.
			writeError(w, http.StatusBadRequest, "MFACODEINVALID", "invalid code")
			return
		}
		slog.ErrorContext(r.Context(), "mfa verify failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	resp := map[string]any{"backup_codes": codes}
	// Forced-enrolment: a setup-token-driven verify also completes login by
	// returning a full access token (amr=["pwd","otp"]). Exercised E2E in the
	// login step-up task. A token-issue failure here is non-fatal — the enrolment
	// already succeeded, so we still return the backup codes and let the FE fall
	// back to a normal login rather than 500-ing after the factor was enabled.
	if isSetup {
		// Resolve roles + is_global_admin from the DB (SEC-080) — the setup token
		// carries neither, so trusting its claims would silently de-privilege a
		// force-enrolled global admin for the whole session.
		access, terr := h.svc.IssueMFACompletedToken(r.Context(), userID, tenantID)
		if terr == nil {
			resp["token"] = access
		} else {
			slog.ErrorContext(r.Context(), "mfa verify: post-enrolment token issue failed", "error", terr)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// mfaDisable implements DELETE /api/v1/users/me/mfa — turns MFA off after a
// re-authentication gate (password OR a valid current OTP / unused backup code).
// The re-auth gate defends against a hijacked session silently removing the
// second factor. Returns 204 on success. Requires a normal access token.
//
// Passwords, OTPs, and backup codes in the body are never logged (CLAUDE.md §10).
func (h *HTTPHandler) mfaDisable(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "mfa disable: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	// A malformed/empty body is tolerated: reauth simply fails closed with
	// ErrInvalidCredentials (empty password + empty code prove nothing), which
	// maps to the 401 re-auth-required response below.
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := h.svc.DisableMFA(r.Context(), userID, req.Password, req.Code); err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "re-authentication required")
			return
		}
		slog.ErrorContext(r.Context(), "mfa disable failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mfaRegenerateBackupCodes implements POST /api/v1/users/me/mfa/backup-codes/regenerate
// — replaces the whole backup-code set with 8 fresh codes after the same re-auth
// gate as disable, and returns the new plaintext codes once. Requires a normal
// access token.
//
// The returned codes are secrets — surfaced to the owner over HTTPS, never logged.
func (h *HTTPHandler) mfaRegenerateBackupCodes(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "mfa regenerate: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	codes, err := h.svc.RegenerateBackupCodes(r.Context(), userID, req.Password, req.Code)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "re-authentication required")
			return
		}
		slog.ErrorContext(r.Context(), "mfa regenerate backup codes failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup_codes": codes})
}

// accountLabel returns a stable label for the otpauth URI account name. The
// subject (user id) is always available on the claims; there is no username or
// email field on service.Claims, so we use the subject. The label is not a
// secret (it identifies the account in the authenticator app), but it also must
// never expose more than the caller already knows about themselves — the user id
// satisfies both constraints.
func accountLabel(c *service.Claims) string {
	return c.Subject
}
