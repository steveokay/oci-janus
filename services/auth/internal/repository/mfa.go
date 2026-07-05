package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MFAState is the per-user TOTP state, read on the login hot path and during
// enrolment. It is kept separate from the User struct (and its shared scanOne
// helper) on purpose: MFA columns are only needed on the enrolment / login-MFA
// paths, so folding them into the wide User SELECT list would tax every user
// lookup in the service for no benefit. Migration 20260705120000 added the
// backing columns.
type MFAState struct {
	Enabled          bool       // users.mfa_enabled
	SecretEnc        []byte     // users.mfa_secret_enc — AES-256-GCM ciphertext; nil when not enrolled
	SecretKEKVersion *int16     // users.mfa_secret_kek_version — rekey generation stamped on SecretEnc (SMALLINT)
	EnrolledAt       *time.Time // users.mfa_enrolled_at — set when enrolment completes
	LastUsedCounter  *int64     // users.mfa_last_used_counter — TOTP counter of the last accepted code
}

// GetMFAState loads the user's MFA columns. Returns ErrNotFound if the user id
// does not exist. Nullable columns surface as nil pointers / nil slice so the
// caller can distinguish "not enrolled" from a zero value.
func (r *UserRepository) GetMFAState(ctx context.Context, userID uuid.UUID) (*MFAState, error) {
	const q = `
		SELECT mfa_enabled, mfa_secret_enc, mfa_secret_kek_version,
		       mfa_enrolled_at, mfa_last_used_counter
		FROM   users
		WHERE  id = $1`
	var m MFAState
	err := r.pool.QueryRow(ctx, q, userID).Scan(
		&m.Enabled, &m.SecretEnc, &m.SecretKEKVersion, &m.EnrolledAt, &m.LastUsedCounter,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get mfa state: %w", err)
	}
	return &m, nil
}

// SetPendingMFASecret stores an encrypted TOTP secret with mfa_enabled left
// false (enrolment step 1). It overwrites any prior pending secret and clears
// mfa_last_used_counter so a fresh secret starts with a clean replay window.
// The row is not considered MFA-protected until EnableMFA flips the flag.
func (r *UserRepository) SetPendingMFASecret(ctx context.Context, userID uuid.UUID, secretEnc []byte, kekVersion int16) error {
	const q = `
		UPDATE users
		SET mfa_secret_enc = $1, mfa_secret_kek_version = $2,
		    mfa_enabled = false, mfa_last_used_counter = NULL, updated_at = now()
		WHERE id = $3`
	return r.execMFAAffectingOne(ctx, "set pending mfa secret", q, secretEnc, kekVersion, userID)
}

// EnableMFA flips mfa_enabled true and stamps mfa_enrolled_at (enrolment
// step 2). Called only after the user has proven possession of the secret by
// submitting a valid TOTP code against the pending secret.
func (r *UserRepository) EnableMFA(ctx context.Context, userID uuid.UUID) error {
	const q = `UPDATE users SET mfa_enabled = true, mfa_enrolled_at = now(), updated_at = now() WHERE id = $1`
	return r.execMFAAffectingOne(ctx, "enable mfa", q, userID)
}

// DisableMFA clears all MFA state on the user row. Backup codes live in a
// separate table and must be removed with DeleteBackupCodes (ideally in the
// same logical operation) so no orphaned recovery codes remain.
func (r *UserRepository) DisableMFA(ctx context.Context, userID uuid.UUID) error {
	const q = `
		UPDATE users
		SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_secret_kek_version = NULL,
		    mfa_enrolled_at = NULL, mfa_last_used_counter = NULL, updated_at = now()
		WHERE id = $1`
	return r.execMFAAffectingOne(ctx, "disable mfa", q, userID)
}

// AdvanceMFACounter atomically records counter as the last accepted TOTP
// time-step, but only when it strictly exceeds the stored value (or none is
// set yet). It returns true when the row advanced, false when a concurrent
// request had already advanced to counter or beyond. This compare-and-swap is
// the authoritative replay guard (SEC-078): the service's in-memory
// counter<=last check is a check-then-act fast path that two parallel requests
// carrying the same valid OTP can both pass, so the single-winner guarantee has
// to live in the UPDATE's WHERE clause. A false return is a replay, not an error.
func (r *UserRepository) AdvanceMFACounter(ctx context.Context, userID uuid.UUID, counter int64) (bool, error) {
	const q = `UPDATE users
		SET mfa_last_used_counter = $1, updated_at = now()
		WHERE id = $2
		  AND (mfa_last_used_counter IS NULL OR mfa_last_used_counter < $1)`
	tag, err := r.pool.Exec(ctx, q, counter, userID)
	if err != nil {
		return false, fmt.Errorf("advance mfa counter: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// InsertBackupCodes replaces the user's backup codes with the given argon2id
// hashes in a single transaction (delete-then-insert). Used on both enrol and
// regenerate so a regenerate atomically invalidates every previously-issued
// code. Only hashes are stored — never the plaintext recovery codes.
func (r *UserRepository) InsertBackupCodes(ctx context.Context, userID uuid.UUID, hashes []string) error {
	// Wrap the delete + inserts in one transaction so a partial failure never
	// leaves the user with a mix of old and new codes.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin backup codes tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit; safe to always defer.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM user_mfa_backup_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clear backup codes: %w", err)
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_mfa_backup_codes (user_id, code_hash) VALUES ($1, $2)`, userID, h); err != nil {
			return fmt.Errorf("insert backup code: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// DeleteBackupCodes removes all of the user's backup codes (on MFA disable).
// Idempotent: deleting when none exist is not an error.
func (r *UserRepository) DeleteBackupCodes(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM user_mfa_backup_codes WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete backup codes: %w", err)
	}
	return nil
}

// BackupCode is one stored recovery code: its primary key, the argon2id hash,
// and whether it has already been consumed. The plaintext code is never
// persisted, so recovery verification argon2-compares a submitted code against
// each unused hash.
type BackupCode struct {
	ID       uuid.UUID
	CodeHash string
	Used     bool
}

// ListUnusedBackupCodes returns the user's not-yet-consumed codes for the
// service to argon2-compare against a submitted recovery code. Used codes are
// excluded at the SQL layer (used_at IS NULL).
func (r *UserRepository) ListUnusedBackupCodes(ctx context.Context, userID uuid.UUID) ([]BackupCode, error) {
	const q = `SELECT id, code_hash FROM user_mfa_backup_codes WHERE user_id = $1 AND used_at IS NULL`
	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list backup codes: %w", err)
	}
	defer rows.Close()

	var out []BackupCode
	for rows.Next() {
		// Only unused rows are selected, so Used is implicitly false here.
		var c BackupCode
		if err := rows.Scan(&c.ID, &c.CodeHash); err != nil {
			return nil, fmt.Errorf("scan backup code: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkBackupCodeUsed stamps used_at on a single code, but only if it is still
// unused (the "AND used_at IS NULL" guard makes a concurrent double-spend a
// no-op). Returns ErrNotFound if the code does not exist or has already been
// consumed, so the caller can reject the redemption cleanly.
func (r *UserRepository) MarkBackupCodeUsed(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE user_mfa_backup_codes SET used_at = now() WHERE id = $1 AND used_at IS NULL`
	return r.execMFAAffectingOne(ctx, "mark backup code used", q, id)
}

// execMFAAffectingOne runs an UPDATE/DELETE expected to touch exactly one row,
// mapping the zero-rows case to ErrNotFound. Named with an MFA prefix to avoid
// colliding with any future generic exec helper in the repository package.
func (r *UserRepository) execMFAAffectingOne(ctx context.Context, label, q string, args ...any) error {
	tag, err := r.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
