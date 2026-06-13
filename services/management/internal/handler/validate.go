// Package handler — validate.go
//
// Input validators for all path parameters and request-body fields accepted by
// the management REST API. Every pattern is taken verbatim from the allowlists
// defined in CLAUDE.md §7 (Input Validation). Reject at the handler layer
// before any gRPC call is made — never pass unvalidated strings downstream.
package handler

import (
	"fmt"
	"regexp"
)

var (
	// reOrgName matches valid org names: 2–64 lowercase alphanumeric characters
	// and hyphens. No leading/trailing hyphens are enforced by the pattern.
	// Source: CLAUDE.md §7 — "Org name: ^[a-z0-9-]{2,64}$"
	reOrgName = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)

	// reRepoName matches valid repository names: lowercase alphanumeric segments
	// separated by dots, underscores, or hyphens; max 128 characters total.
	// Source: CLAUDE.md §7 — "Repository name: ^[a-z0-9]+([._-][a-z0-9]+)*$"
	reRepoName = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)

	// reTagName matches valid tag names: starts with alphanumeric or underscore,
	// followed by up to 127 more alphanumeric, dot, underscore, or hyphen chars.
	// Source: CLAUDE.md §7 — "Tag name: ^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$"
	reTagName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)
)

// validateOrgName returns a non-nil error if s is not a valid org name.
func validateOrgName(s string) error {
	if !reOrgName.MatchString(s) {
		return fmt.Errorf("org name %q does not match ^[a-z0-9-]{2,64}$", s)
	}
	return nil
}

// validateRepoName returns a non-nil error if s is not a valid repository name.
func validateRepoName(s string) error {
	if len(s) > 128 {
		return fmt.Errorf("repo name exceeds 128 characters")
	}
	if !reRepoName.MatchString(s) {
		return fmt.Errorf("repo name %q does not match ^[a-z0-9]+([._-][a-z0-9]+)*$", s)
	}
	return nil
}

// validateTagName returns a non-nil error if s is not a valid tag name.
func validateTagName(s string) error {
	if !reTagName.MatchString(s) {
		return fmt.Errorf("tag name %q does not match ^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$", s)
	}
	return nil
}
