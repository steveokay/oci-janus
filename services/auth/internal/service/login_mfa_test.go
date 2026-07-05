// Package service — tests for the two-step (MFA-gated) password login.
//
// These exercise Service.Login's three-way branch (plain token / MFA challenge /
// forced-enrolment setup) and Service.VerifyLoginMFA (challenge + OTP/backup
// code → full access token, wrong code feeds the lockout counter). They reuse
// the pinned-clock MFA harness (newMFATestService, enrolMFAUser) so no real
// Redis or PostgreSQL is needed. OTP/backup codes are never logged.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// loginMFAFixedNow is a stable clock so enrolment (and its TOTP counter) is
// deterministic across the whole test.
var loginMFAFixedNow = time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

// newLoginMFATestService mirrors newMFATestService but wires a miniredis-backed
// client so the token-issuing paths (IssueToken → recordIssuedJTI) work — the
// nil-Redis MFA harness only covers the enrol flow, which never mints an access
// token. Clock is pinned and a 32-byte KEK is set so enrolMFAUser is reusable.
func newLoginMFATestService(t *testing.T, fixedNow time.Time) (*Service, *fakeUserRepo) {
	t.Helper()
	priv, _ := genTestKey(t)
	ring, err := newKeyRing([]signingKey{
		{kid: "kid-test", privateKey: priv, publicKey: &priv.PublicKey},
	}, "kid-test")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	users := newFakeUserRepo()
	svc, err := NewWithFakesAndRing(users, nil, nil, nil, newTestRedis(t), ring)
	if err != nil {
		t.Fatalf("NewWithFakesAndRing: %v", err)
	}
	svc.SetMFAKEK([]byte("0123456789abcdef0123456789abcdef"))
	svc.nowFn = func() time.Time { return fixedNow }
	return svc, users
}

// TestLogin_mfaEnabled_returnsChallenge verifies that a correct password for an
// MFA-enabled user yields an mfa_challenge token, not an access token.
func TestLogin_mfaEnabled_returnsChallenge(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, _ := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	res, err := svc.Login(ctx, u.TenantID, u.Username, "pw", SessionMeta{})
	require.NoError(t, err)
	require.True(t, res.MFARequired, "MFA-enabled login must require the second step")
	require.NotEmpty(t, res.ChallengeToken, "a challenge token must be returned")
	require.Empty(t, res.Token, "no access token may be issued before the OTP step")
	require.False(t, res.MFASetupRequired)
}

// TestLogin_requireMFAPolicy_unenrolled_returnsSetup verifies that when the
// workspace policy forces MFA and a human user is not yet enrolled, Login hands
// back an mfa_setup token instead of an access token.
func TestLogin_requireMFAPolicy_unenrolled_returnsSetup(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	// Wire a token policy that forces MFA for this tenant.
	tenantID := uuid.New()
	polRepo := newFakeTokenPolicyRepo()
	polRepo.rows[tenantID] = repository.TokenPolicy{TenantID: tenantID, RequireMFA: true}
	svc.SetTokenPolicyRepo(polRepo)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	users.addUser(&repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "forced",
		IsActive:     true,
		PasswordHash: pwHash,
		Kind:         "human",
	})

	res, err := svc.Login(ctx, tenantID, "forced", "pw", SessionMeta{})
	require.NoError(t, err)
	require.True(t, res.MFASetupRequired, "policy-forced un-enrolled human must be sent to setup")
	require.NotEmpty(t, res.SetupToken, "a setup token must be returned")
	require.Empty(t, res.Token)
	require.False(t, res.MFARequired)
}

// TestLogin_noMFA_returnsToken verifies the plain path: no MFA and no forcing
// policy → a full access token.
func TestLogin_noMFA_returnsToken(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	tenantID := uuid.New()
	users.addUser(&repository.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Username:     "plain",
		IsActive:     true,
		PasswordHash: pwHash,
		Kind:         "human",
	})

	res, err := svc.Login(ctx, tenantID, "plain", "pw", SessionMeta{})
	require.NoError(t, err)
	require.NotEmpty(t, res.Token, "plain login must return an access token")
	require.False(t, res.MFARequired)
	require.False(t, res.MFASetupRequired)
}

// TestVerifyLoginMFA_validBackupCode_returnsToken verifies the full two-step
// completion: a valid challenge token plus a correct single-use backup code
// mints a full access token.
func TestVerifyLoginMFA_validBackupCode_returnsToken(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, backupCodes := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	ct, err := svc.IssueMFAChallengeToken(ctx, userID.String(), u.TenantID.String())
	require.NoError(t, err)

	tok, err := svc.VerifyLoginMFA(ctx, ct, backupCodes[0])
	require.NoError(t, err)
	require.NotEmpty(t, tok, "a full access token must be returned")

	// The issued token must be a normal access token carrying amr=["pwd","otp"].
	claims, err := svc.ValidateToken(ctx, tok)
	require.NoError(t, err)
	require.Equal(t, []string{"pwd", "otp"}, claims.Amr)
}

// TestVerifyLoginMFA_wrongCode_feedsLockout verifies a wrong OTP is rejected
// with ErrInvalidCredentials AND increments the same account-lockout counter
// AuthenticateUser uses, so the OTP step cannot be brute-forced past lockout.
func TestVerifyLoginMFA_wrongCode_feedsLockout(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, _ := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	ct, err := svc.IssueMFAChallengeToken(ctx, userID.String(), u.TenantID.String())
	require.NoError(t, err)

	_, err = svc.VerifyLoginMFA(ctx, ct, "000000")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Equal(t, 1, users.failedLogins[userID], "a wrong OTP must feed the lockout counter")
}

// TestIssueMFACompletedToken_resolvesGlobalAdminFromDB locks SEC-080: the
// post-second-factor access token must take is_global_admin from the user row,
// never from a caller-supplied claim. Both the login step-up (VerifyLoginMFA)
// and the forced-enrolment completion (mfaVerify setup path) route through this
// method — before the fix the handler stamped it from the always-false setup
// token, silently de-privileging a force-enrolled global admin for the session.
func TestIssueMFACompletedToken_resolvesGlobalAdminFromDB(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	tenantID := uuid.New()
	userID := uuid.New()
	users.addUser(&repository.User{
		ID:            userID,
		TenantID:      tenantID,
		Username:      "admin",
		IsActive:      true,
		Kind:          "human",
		IsGlobalAdmin: true,
	})

	tok, err := svc.IssueMFACompletedToken(ctx, userID, tenantID)
	require.NoError(t, err)

	claims, err := svc.ValidateToken(ctx, tok)
	require.NoError(t, err)
	require.True(t, claims.IsGlobalAdmin, "token must carry is_global_admin from the DB row, not a claim")
	require.Equal(t, []string{"pwd", "otp"}, claims.Amr)
}

// TestVerifyLoginMFA_badChallengeToken_rejected verifies that a non-challenge
// token (here an access token) cannot be spent at the /login/mfa step.
func TestVerifyLoginMFA_badChallengeToken_rejected(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, backupCodes := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	// A normal access token has typ="" and must be refused by VerifyLoginMFA.
	access, err := svc.IssueToken(ctx, userID.String(), u.TenantID.String(), nil, nil, false, "human", []string{"pwd"}, "")
	require.NoError(t, err)

	_, err = svc.VerifyLoginMFA(ctx, access, backupCodes[0])
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

// TestVerifyLoginMFA_lockedAccount_rejected verifies SEC-079: a locked account
// is refused at the OTP step before the code is checked, so minting fresh
// challenge tokens cannot be used to keep probing a locked account.
func TestVerifyLoginMFA_lockedAccount_rejected(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, backupCodes := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	// Lock the account (the lock check uses real wall-clock, like AuthenticateUser).
	require.NoError(t, users.LockUntil(ctx, userID, time.Now().Add(time.Hour)))

	ct, err := svc.IssueMFAChallengeToken(ctx, userID.String(), u.TenantID.String())
	require.NoError(t, err)

	// Even a correct backup code must be refused while locked.
	_, err = svc.VerifyLoginMFA(ctx, ct, backupCodes[0])
	require.ErrorIs(t, err, ErrAccountLocked)
}

// TestVerifyLoginMFA_challengeAttemptCap verifies SEC-079's defence-in-depth:
// a single challenge token is burned after maxMFAChallengeAttempts submissions,
// even when the account-lockout write is unavailable — so the token cannot be
// replayed for unbounded guessing within its 5-minute window.
func TestVerifyLoginMFA_challengeAttemptCap(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, backupCodes := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	// Simulate the lockout write being unavailable so the per-token cap is the
	// only bound in play — proving it limits a single challenge independently.
	users.recordFailErr = errors.New("lockout store unavailable")

	ct, err := svc.IssueMFAChallengeToken(ctx, userID.String(), u.TenantID.String())
	require.NoError(t, err)

	// Exhaust the cap with wrong codes.
	for i := 0; i < maxMFAChallengeAttempts; i++ {
		_, verr := svc.VerifyLoginMFA(ctx, ct, "000000")
		require.ErrorIs(t, verr, ErrInvalidCredentials)
	}
	// The token is now burned: even a correct backup code is refused (the cap is
	// checked before the code is consumed).
	_, err = svc.VerifyLoginMFA(ctx, ct, backupCodes[0])
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

// TestVerifyLoginMFA_success_resetsFailedLogins verifies a successful OTP step
// clears the failed-login counter, mirroring the password path (SEC-079).
func TestVerifyLoginMFA_success_resetsFailedLogins(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginMFATestService(t, loginMFAFixedNow)

	pwHash, err := argon2pkg.Hash("pw")
	require.NoError(t, err)
	userID, backupCodes := enrolMFAUser(t, svc, users, loginMFAFixedNow, pwHash)
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)

	ct, err := svc.IssueMFAChallengeToken(ctx, userID.String(), u.TenantID.String())
	require.NoError(t, err)

	// One wrong code bumps the counter...
	_, err = svc.VerifyLoginMFA(ctx, ct, "000000")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Equal(t, 1, users.failedLogins[userID])

	// ...a correct backup code succeeds and clears it.
	tok, err := svc.VerifyLoginMFA(ctx, ct, backupCodes[0])
	require.NoError(t, err)
	require.NotEmpty(t, tok)
	require.Equal(t, 0, users.failedLogins[userID], "success must reset the failed-login counter")
}
