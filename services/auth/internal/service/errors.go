// Package service — this file contains error classification helpers that
// HTTP and gRPC handlers use to decide which errors are safe to surface to
// callers and which must be replaced with an opaque internal-error message.
package service

import "errors"

// IsPasswordPolicyError reports whether err originated from ValidatePassword
// (i.e. it is safe to return the message to the caller) rather than from an
// internal failure such as argon2id hashing.
//
// It uses errors.As on the *PasswordPolicyError sentinel so the check is
// type-safe rather than relying on fragile string-prefix matching (SEC-033).
// Returning argon2 error details to clients would leak internal library
// version information — those are wrapped by CreateUser and must be logged and
// replaced with a generic response instead.
func IsPasswordPolicyError(err error) bool {
	// errors.As unwraps through any wrapping layers, so this correctly handles
	// future callers that wrap a *PasswordPolicyError with fmt.Errorf("%w", ...).
	return errors.As(err, new(*PasswordPolicyError))
}
