// Package service — this file contains error classification helpers that
// HTTP and gRPC handlers use to decide which errors are safe to surface to
// callers and which must be replaced with an opaque internal-error message.
package service

import "strings"

// IsPasswordPolicyError reports whether err originated from ValidatePassword
// (i.e. it is safe to return the message to the caller) rather than from an
// internal failure such as argon2id hashing.
//
// The heuristic relies on the wrapping convention used in CreateUser:
//   - ValidatePassword returns bare errors.New("password must ...") messages.
//   - argon2 failures are wrapped as fmt.Errorf("hash password: %w", ...).
//
// Returning argon2 error details to clients would leak internal library
// version information and stack context — those must be logged and replaced
// with a generic "unable to create user" response instead.
func IsPasswordPolicyError(err error) bool {
	// A nil error is never a policy error.
	if err == nil {
		return false
	}

	// Hash failures produced by CreateUser carry the "hash password: " prefix.
	// Any error without that prefix came from ValidatePassword and is safe to
	// surface verbatim to the caller.
	return !strings.HasPrefix(err.Error(), "hash password:")
}
