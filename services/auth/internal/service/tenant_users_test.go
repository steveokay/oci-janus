package service

import (
	"strings"
	"testing"
)

// FUT-012 Phase A — pure-function tests. The DB-bound paths
// (ListTenantUsers, CreateInvitedUser, SetUserStatus) live behind
// the testcontainers integration suite — only the deterministic
// helpers get vetted here.

func TestDeriveInviteUsername(t *testing.T) {
	cases := []struct {
		name  string
		email string
		want  string
	}{
		{"simple local-part", "alice@example.com", "alice"},
		{"dot collapses to dash", "alice.smith@example.com", "alice-smith"},
		{"plus-tag collapses to dash", "alice+ci@example.com", "alice-ci"},
		// "alice.+ci" → '.' and '+' both fall through to the dash
		// branch which then collapses adjacent dashes.
		{"adjacent non-allowlist chars collapse", "alice.+ci@example.com", "alice-ci"},
		{"leading and trailing dashes trimmed", ".alice.@example.com", "alice"},
		{"underscore + hyphen preserved", "ci_bot-2@example.com", "ci_bot-2"},
		// Too-short outputs (<3 chars) are rejected so we don't ship
		// a username that fails the validateUserName regex (3-64).
		{"too short rejected", "ab@example.com", ""},
		{"too short after sanitisation rejected", "a.@example.com", ""},
		// Malformed emails — no @ → empty username, caller surfaces
		// the "invalid email" error.
		{"no at sign", "alice.example.com", ""},
		{"empty input", "", ""},
		// Length cap: 64 chars max.
		{"truncated to 64", strings.Repeat("a", 70) + "@example.com", strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveInviteUsername(tc.email)
			if got != tc.want {
				t.Errorf("deriveInviteUsername(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}

func TestGenerateInviteToken_isHexAndCorrectLength(t *testing.T) {
	// 32 random bytes hex-encoded → 64-char lowercase hex string.
	// Same shape as the api-key secret so the copy-button UX renders
	// identically. Re-running the function must produce different
	// values — sanity check that crypto/rand isn't broken.
	tok1, err := generateInviteToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := len(tok1); got != inviteTokenRawLen*2 {
		t.Errorf("len: got %d, want %d", got, inviteTokenRawLen*2)
	}
	for _, c := range tok1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-lowercase-hex char %q in token", c)
			break
		}
	}
	tok2, err := generateInviteToken()
	if err != nil {
		t.Fatalf("generate 2: %v", err)
	}
	if tok1 == tok2 {
		t.Error("two consecutive tokens should differ — crypto/rand may be broken")
	}
}
