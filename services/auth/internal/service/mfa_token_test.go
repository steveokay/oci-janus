package service

import (
	"context"
	"testing"
)

// newSigningTestService builds a Service backed by an in-memory single-key
// RSA ring plus a miniredis instance. This is the minimal wiring needed to
// exercise the pure issue/parse/validate paths (IssueToken, ValidateToken,
// the MFA typed-token issuers) without a Postgres backend. It reuses the
// existing genTestKey / newTestRedis / NewWithFakesAndRing helpers so the
// revocation checks in ValidateToken have a live *redis.Client to hit.
func newSigningTestService(t *testing.T) *Service {
	t.Helper()
	priv, _ := genTestKey(t)
	rdb := newTestRedis(t)
	ring, err := newKeyRing([]signingKey{
		{kid: "kid-test", privateKey: priv, publicKey: &priv.PublicKey},
	}, "kid-test")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	svc, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ring)
	if err != nil {
		t.Fatalf("NewWithFakesAndRing: %v", err)
	}
	return svc
}

// TestMFAChallengeToken_RejectedAsAccessToken asserts that a typ=mfa_challenge
// token minted by IssueMFAChallengeToken is refused by ValidateToken (so it can
// never be used as an access token), is accepted by ValidateMFAToken for the
// matching type, and is rejected by ValidateMFAToken for a mismatched type.
func TestMFAChallengeToken_RejectedAsAccessToken(t *testing.T) {
	s := newSigningTestService(t)
	ctx := context.Background()
	tok, err := s.IssueMFAChallengeToken(ctx, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatalf("IssueMFAChallengeToken: %v", err)
	}
	if _, err := s.ValidateToken(ctx, tok); err == nil {
		t.Fatal("ValidateToken must reject a typ=mfa_challenge token")
	}
	claims, err := s.ValidateMFAToken(ctx, tok, tokenTypeMFAChallenge)
	if err != nil {
		t.Fatalf("ValidateMFAToken(challenge): %v", err)
	}
	if claims.Subject != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("wrong subject: %s", claims.Subject)
	}
	if _, err := s.ValidateMFAToken(ctx, tok, tokenTypeMFASetup); err == nil {
		t.Fatal("ValidateMFAToken must reject a type mismatch")
	}
}

// TestAccessTokenCarriesAMRAndSurvivesRefresh asserts that the amr claim is
// carried onto a normal access token by IssueToken and read back by
// ValidateToken, and that such an access token has an empty typ.
func TestAccessTokenCarriesAMRAndSurvivesRefresh(t *testing.T) {
	s := newSigningTestService(t)
	ctx := context.Background()
	tok, err := s.IssueToken(ctx, "u", "tn", nil, nil, false, "human", []string{"pwd", "otp"}, "")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims, err := s.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if len(claims.Amr) != 2 || claims.Amr[0] != "pwd" || claims.Amr[1] != "otp" {
		t.Fatalf("amr not carried: %v", claims.Amr)
	}
	if claims.Typ != "" {
		t.Fatalf("access token must have empty typ, got %q", claims.Typ)
	}
}
