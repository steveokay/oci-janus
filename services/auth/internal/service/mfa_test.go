package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/hotp"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// newMFATestService builds a Service backed by an in-memory fakeUserRepo, a
// single-key RSA ring, a 32-byte MFA KEK, and a clock pinned to fixedNow. The
// pinned clock makes TOTP validation deterministic so the test can compute the
// exact code the service will accept. Redis is nil — the enrolment path never
// touches it.
func newMFATestService(t *testing.T, fixedNow time.Time) (*Service, *fakeUserRepo) {
	t.Helper()
	priv, _ := genTestKey(t)
	ring, err := newKeyRing([]signingKey{
		{kid: "kid-test", privateKey: priv, publicKey: &priv.PublicKey},
	}, "kid-test")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	users := newFakeUserRepo()
	svc, err := NewWithFakesAndRing(users, nil, nil, nil, nil, ring)
	if err != nil {
		t.Fatalf("NewWithFakesAndRing: %v", err)
	}
	// A deterministic 32-byte AES-256 KEK for encrypting the TOTP secret.
	svc.SetMFAKEK([]byte("0123456789abcdef0123456789abcdef"))
	// Pin the clock so the TOTP time step is stable across the whole test.
	svc.nowFn = func() time.Time { return fixedNow }
	return svc, users
}

// codeForSecret returns the 6-digit TOTP that ValidateCode will accept for the
// given base32 secret at the pinned clock. It mirrors the service's counter
// derivation (unix / 30) and uses the same pquerna/otp primitive.
func codeForSecret(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	counter := uint64(at.Unix()) / 30 //nolint:gosec // test-only, bounded
	code, err := hotp.GenerateCode(secret, counter)
	if err != nil {
		t.Fatalf("hotp.GenerateCode: %v", err)
	}
	return code
}

// TestMFAEnrollment_HappyPathAndReplay walks the full enrolment lifecycle:
// begin → verify with a valid code → complete (8 backup codes, enabled) →
// re-begin rejected → re-complete with the same code rejected (replay guard).
func TestMFAEnrollment_HappyPathAndReplay(t *testing.T) {
	ctx := context.Background()
	fixedNow := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	svc, users := newMFATestService(t, fixedNow)

	// Seed a human user for enrolment.
	userID := uuid.New()
	users.addUser(&repository.User{
		ID:       userID,
		TenantID: uuid.New(),
		Username: "alice",
		Email:    "alice@example.com",
		IsActive: true,
	})

	// 1. Begin enrolment → non-empty secret + otpauth URI; pending secret stored,
	//    not yet enabled.
	secret, uri, err := svc.BeginMFAEnrollment(ctx, userID, "alice@example.com")
	if err != nil {
		t.Fatalf("BeginMFAEnrollment: %v", err)
	}
	if secret == "" || uri == "" {
		t.Fatalf("expected non-empty secret and uri, got secret=%q uri=%q", secret, uri)
	}
	status, err := svc.GetMFAStatus(ctx, userID)
	if err != nil {
		t.Fatalf("GetMFAStatus after begin: %v", err)
	}
	if status.Enabled {
		t.Fatal("MFA must not be enabled after BeginMFAEnrollment")
	}
	if st := users.mfa[userID]; st == nil || len(st.SecretEnc) == 0 {
		t.Fatal("expected an encrypted pending secret to be stored")
	}

	// 2. Compute the correct current code for the returned secret.
	code := codeForSecret(t, secret, fixedNow)

	// 3. Complete enrolment → exactly 8 backup codes; MFA now enabled with an
	//    EnrolledAt timestamp.
	codes, err := svc.CompleteMFAEnrollment(ctx, userID, code)
	if err != nil {
		t.Fatalf("CompleteMFAEnrollment: %v", err)
	}
	if len(codes) != 8 {
		t.Fatalf("expected 8 backup codes, got %d", len(codes))
	}
	status, err = svc.GetMFAStatus(ctx, userID)
	if err != nil {
		t.Fatalf("GetMFAStatus after complete: %v", err)
	}
	if !status.Enabled {
		t.Fatal("MFA must be enabled after CompleteMFAEnrollment")
	}
	if status.EnrolledAt == nil {
		t.Fatal("EnrolledAt must be set after CompleteMFAEnrollment")
	}

	// 4. Re-begin must be rejected — MFA is already on.
	if _, _, err := svc.BeginMFAEnrollment(ctx, userID, "alice@example.com"); !errors.Is(err, ErrMFAAlreadyEnabled) {
		t.Fatalf("expected ErrMFAAlreadyEnabled on re-enrol, got %v", err)
	}

	// 5. Replaying the SAME code must fail — the counter guard rejects a code at
	//    or below the last accepted counter with ErrInvalidCredentials.
	if _, err := svc.CompleteMFAEnrollment(ctx, userID, code); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials on code replay, got %v", err)
	}
}
