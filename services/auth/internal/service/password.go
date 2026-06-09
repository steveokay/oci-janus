// Package service contains the business logic for registry-auth.
// No database access or HTTP/gRPC concerns belong here — those live in
// repository and handler respectively.
package service

import (
	"errors"
	"unicode"
)

// PasswordPolicy requirements from CLAUDE.md §4.2.
// Enforced server-side; clients must not rely on frontend validation alone.
const (
	minPasswordLen = 12
)

// ValidatePassword returns an error describing which policy requirement the
// password violates, or nil if it meets all requirements.
func ValidatePassword(password string) error {
	if len(password) < minPasswordLen {
		return errors.New("password must be at least 12 characters")
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
		return errors.New("password must contain at least one uppercase letter")
	case !hasLower:
		return errors.New("password must contain at least one lowercase letter")
	case !hasDigit:
		return errors.New("password must contain at least one digit")
	case !hasSymbol:
		return errors.New("password must contain at least one symbol")
	}
	return nil
}
