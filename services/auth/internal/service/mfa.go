// Package service — MFA (TOTP) enrolment business logic.
//
// This file owns the self-service two-factor enrolment flow: reporting status,
// minting + storing an encrypted pending secret, and verifying the first code
// to flip the factor on (issuing single-use backup codes in the process). The
// TOTP primitives live in internal/mfa; the encrypted-at-rest secret and the
// backup-code hashes are persisted through the userRepo. Secrets, otpauth URIs,
// and OTP codes are NEVER logged (CLAUDE.md §10).
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	aespkg "github.com/steveokay/oci-janus/libs/crypto/aes"
	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/mfa"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// mfaKEKVersion is the KEK generation stamped on freshly-encrypted MFA secrets.
// A future KEK rotation increments this so re-encryption sweeps can find rows
// still on an older key.
const mfaKEKVersion int16 = 1

// ErrMFAAlreadyEnabled is returned when enrolment is attempted for a user who
// already has MFA turned on.
var ErrMFAAlreadyEnabled = errors.New("mfa already enabled")

// ErrMFANotEnrolled is returned when a completion is attempted without a prior
// pending secret (BeginMFAEnrollment was never called).
var ErrMFANotEnrolled = errors.New("mfa not enrolled")

// now returns the current wall clock, honouring an injected clock (nowFn) when
// set. Tests pin nowFn so the TOTP time step is deterministic; production leaves
// it nil and gets time.Now.
func (s *Service) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// SetMFAKEK wires the 32-byte AES-256 key-encryption key used to encrypt TOTP
// secrets at rest. Called at startup with the decoded MFA_SECRET_KEY_HEX bytes.
// Kept as a setter (mirroring SetTokenPolicyRepo) so the JWT-posture
// constructors stay signature-stable. The value is never logged.
func (s *Service) SetMFAKEK(kek []byte) {
	s.mfaKEK = kek
}

// MFAStatus is the self-service status payload.
type MFAStatus struct {
	Enabled    bool
	EnrolledAt *string // RFC3339, nil when not enrolled
}

// GetMFAStatus reports whether the user has MFA enabled.
func (s *Service) GetMFAStatus(ctx context.Context, userID uuid.UUID) (MFAStatus, error) {
	st, err := s.users.GetMFAState(ctx, userID)
	if err != nil {
		return MFAStatus{}, err
	}
	out := MFAStatus{Enabled: st.Enabled}
	if st.EnrolledAt != nil {
		v := st.EnrolledAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		out.EnrolledAt = &v
	}
	return out, nil
}

// BeginMFAEnrollment mints a fresh secret, stores it encrypted (pending), and
// returns the base32 secret + otpauth URI for the QR. Rejects if already on.
func (s *Service) BeginMFAEnrollment(ctx context.Context, userID uuid.UUID, account string) (secretBase32, otpauthURI string, err error) {
	st, err := s.users.GetMFAState(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if st.Enabled {
		return "", "", ErrMFAAlreadyEnabled
	}
	secret, uri, err := mfa.GenerateSecret(s.mfaIssuer, account)
	if err != nil {
		return "", "", err
	}
	// Encrypt the secret before it ever touches storage. aespkg.Encrypt requires
	// a 32-byte key; a misconfigured KEK surfaces here as an error rather than
	// persisting a plaintext or short-key secret.
	enc, err := aespkg.Encrypt([]byte(secret), s.mfaKEK)
	if err != nil {
		return "", "", fmt.Errorf("encrypt mfa secret: %w", err)
	}
	if err := s.users.SetPendingMFASecret(ctx, userID, enc, mfaKEKVersion); err != nil {
		return "", "", err
	}
	return secret, uri, nil
}

// CompleteMFAEnrollment verifies the first code against the pending secret; on
// success it enables MFA, generates + stores 8 argon2-hashed backup codes, and
// returns the plaintext codes once.
func (s *Service) CompleteMFAEnrollment(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	st, err := s.users.GetMFAState(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(st.SecretEnc) == 0 {
		return nil, ErrMFANotEnrolled
	}
	ok, err := s.verifyTOTP(ctx, userID, st, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if err := s.users.EnableMFA(ctx, userID); err != nil {
		return nil, err
	}
	return s.regenerateBackupCodes(ctx, userID)
}

// verifyTOTP decrypts the secret, validates the code within the skew window,
// enforces replay prevention (counter must strictly advance), and persists it.
func (s *Service) verifyTOTP(ctx context.Context, userID uuid.UUID, st *repository.MFAState, code string) (bool, error) {
	secret, err := aespkg.Decrypt(st.SecretEnc, s.mfaKEK)
	if err != nil {
		return false, fmt.Errorf("decrypt mfa secret: %w", err)
	}
	ok, counter := mfa.ValidateCode(string(secret), code, s.now())
	if !ok {
		return false, nil
	}
	// Replay guard: reject any code whose time-step counter is at or below the
	// last one we accepted, so a code (or an earlier one in the skew window)
	// cannot be spent twice.
	if st.LastUsedCounter != nil && counter <= *st.LastUsedCounter {
		return false, nil // replay of an already-used code
	}
	if err := s.users.AdvanceMFACounter(ctx, userID, counter); err != nil {
		return false, err
	}
	return true, nil
}

// DisableMFA requires re-auth (a valid password, TOTP, or unused backup code),
// then clears all MFA state + backup codes. The re-auth gate defends against a
// hijacked session silently turning the second factor off. Backup codes live in
// a separate table, so they are cleared alongside the user-row MFA columns to
// leave no orphaned recovery codes behind.
func (s *Service) DisableMFA(ctx context.Context, userID uuid.UUID, password, code string) error {
	if err := s.reauth(ctx, userID, password, code); err != nil {
		return err
	}
	if err := s.users.DisableMFA(ctx, userID); err != nil {
		return err
	}
	return s.users.DeleteBackupCodes(ctx, userID)
}

// RegenerateBackupCodes requires re-auth, then replaces the whole code set with
// 8 fresh codes and returns the new plaintext once. Re-issuing atomically
// invalidates every previously-issued code (InsertBackupCodes is delete-then-
// insert), so a leaked prior code cannot be redeemed after a regenerate.
func (s *Service) RegenerateBackupCodes(ctx context.Context, userID uuid.UUID, password, code string) ([]string, error) {
	if err := s.reauth(ctx, userID, password, code); err != nil {
		return nil, err
	}
	return s.regenerateBackupCodes(ctx, userID)
}

// reauth accepts EITHER the account password OR a valid current OTP / unused
// backup code as proof the caller controls the account. It returns
// ErrInvalidCredentials when neither proves control, so callers surface a single
// uniform failure regardless of which factor was attempted. The password branch
// is skipped when password is empty (and vice-versa) so an empty submission for
// one factor never short-circuits the other. Passwords, OTPs, and backup codes
// are never logged (CLAUDE.md §10).
func (s *Service) reauth(ctx context.Context, userID uuid.UUID, password, code string) error {
	if password != "" {
		u, err := s.users.GetByID(ctx, userID)
		if err != nil {
			return err
		}
		// argon2pkg.Verify returns (false, err) on a malformed stored hash; we
		// treat any non-match (error or false) as "password did not prove
		// control" and fall through to the code branch.
		ok, _ := argon2pkg.Verify(password, u.PasswordHash)
		if ok {
			return nil
		}
	}
	if code != "" {
		ok, err := s.ConsumeMFACode(ctx, userID, code)
		if err == nil && ok {
			return nil
		}
	}
	return ErrInvalidCredentials
}

// ConsumeMFACode validates a submitted code as EITHER a TOTP (with the same
// replay-prevention as enrolment) OR an unused single-use backup code. It
// returns true on success. A matched backup code is marked consumed before
// success is reported; if the mark loses a race to a concurrent redemption the
// method reports failure (false) so a code can never be spent twice. Returns
// ErrMFANotEnrolled when the user has no enabled factor.
func (s *Service) ConsumeMFACode(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	st, err := s.users.GetMFAState(ctx, userID)
	if err != nil {
		return false, err
	}
	if !st.Enabled || len(st.SecretEnc) == 0 {
		return false, ErrMFANotEnrolled
	}
	// TOTP first — the common case and the cheapest check.
	ok, err := s.verifyTOTP(ctx, userID, st, code)
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	// Then backup codes: argon2-compare the submission against each unused hash.
	codes, err := s.users.ListUnusedBackupCodes(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, bc := range codes {
		match, verr := argon2pkg.Verify(code, bc.CodeHash)
		if verr == nil && match {
			// Single-use: mark consumed. A concurrent double-spend loses the
			// race — MarkBackupCodeUsed returns ErrNotFound once the row is
			// already stamped, and we report failure rather than success.
			if merr := s.users.MarkBackupCodeUsed(ctx, bc.ID); merr != nil {
				return false, nil // already used by a racing request
			}
			return true, nil
		}
	}
	return false, nil
}

// regenerateBackupCodes mints, hashes, and stores 8 fresh codes, returning the
// plaintext once. Callers must surface the plaintext to the user immediately —
// only the argon2 hashes are persisted.
func (s *Service) regenerateBackupCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	codes, err := mfa.GenerateBackupCodes()
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		h, herr := argon2pkg.Hash(c)
		if herr != nil {
			return nil, fmt.Errorf("hash backup code: %w", herr)
		}
		hashes[i] = h
	}
	if err := s.users.InsertBackupCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}
