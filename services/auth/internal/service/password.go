// Package service contains the business logic for registry-auth.
// No database access or HTTP/gRPC concerns belong here — those live in
// repository and handler respectively.
package service

import "unicode"

// PasswordPolicyError is the sentinel type returned by ValidatePassword when
// the supplied password violates one of the policy requirements from CLAUDE.md §4.2.
// Using a distinct type (instead of errors.New) lets callers use errors.As to
// distinguish policy rejections from internal failures (e.g. argon2 errors)
// without fragile string-prefix matching (SEC-033).
type PasswordPolicyError struct{ msg string }

// Error implements the error interface; the message describes which rule was violated
// and is safe to forward verbatim to the API caller.
func (e *PasswordPolicyError) Error() string { return e.msg }

// PasswordPolicy requirements from CLAUDE.md §4.2.
// Enforced server-side; clients must not rely on frontend validation alone.
const (
	minPasswordLen = 12
)

// ValidatePassword returns a *PasswordPolicyError describing which policy
// requirement the password violates, or nil if it meets all requirements.
func ValidatePassword(password string) error {
	if len(password) < minPasswordLen {
		return &PasswordPolicyError{"password must be at least 12 characters"}
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, ch := range password {
		switch {
		case unicode.IsUpper(ch):
			hasUpper = true
		case unicode.IsLower(ch):
			hasLower = true
		case unicode.IsDigit(ch):
			hasDigit = true
		case unicode.IsPunct(ch) || unicode.IsSymbol(ch):
			hasSymbol = true
		}
	}
	switch {
	case !hasUpper:
		return &PasswordPolicyError{"password must contain at least one uppercase letter"}
	case !hasLower:
		return &PasswordPolicyError{"password must contain at least one lowercase letter"}
	case !hasDigit:
		return &PasswordPolicyError{"password must contain at least one digit"}
	case !hasSymbol:
		return &PasswordPolicyError{"password must contain at least one symbol"}
	}
	return nil
}
