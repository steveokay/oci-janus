// Tests for the JWT principal_kind extraction helper used by RequireAuth
// to inject the caller's principal kind into request context
// (REDESIGN-001 Phase 5.4).
//
// The helper deliberately skips signature verification — see the comment
// on parsePrincipalKindFromJWT for why that is safe in this codepath.
// These tests therefore focus on the structural decoding contract: a
// well-formed JWT body yields the embedded claim; malformed inputs return
// the empty string so RequireAuth falls back to the legacy default
// ("human") rather than producing an undefined kind.
package middleware

import (
	"encoding/base64"
	"strings"
	"testing"
)

// encodeJWT builds a 3-segment unsigned JWT-shape: header.payload.signature.
// The signature segment is filled with placeholder bytes so the structural
// shape matches what golang-jwt produces; parsePrincipalKindFromJWT only
// reads the payload segment, so the signature contents are irrelevant.
func encodeJWT(payload string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString([]byte("placeholder-signature"))
	return header + "." + body + "." + sig
}

// TestParsePrincipalKindFromJWT_ServiceAccount verifies the helper extracts
// the principal_kind claim verbatim when it is set to "service_account".
// This is the load-bearing case for the Phase 5.4 admin-gate deny.
func TestParsePrincipalKindFromJWT_ServiceAccount(t *testing.T) {
	tok := encodeJWT(`{"sub":"shadow-user","tenant_id":"t1","principal_kind":"service_account"}`)
	got := parsePrincipalKindFromJWT(tok)
	if got != "service_account" {
		t.Errorf("parsePrincipalKindFromJWT = %q, want %q", got, "service_account")
	}
}

// TestParsePrincipalKindFromJWT_Human verifies the human case round-trips
// through the helper. Existing human sessions must keep their kind so
// admin gates can distinguish them from SA bearers.
func TestParsePrincipalKindFromJWT_Human(t *testing.T) {
	tok := encodeJWT(`{"sub":"alice","tenant_id":"t1","principal_kind":"human"}`)
	got := parsePrincipalKindFromJWT(tok)
	if got != "human" {
		t.Errorf("parsePrincipalKindFromJWT = %q, want %q", got, "human")
	}
}

// TestParsePrincipalKindFromJWT_LegacyEmpty verifies that a JWT without the
// principal_kind claim returns "" so the middleware falls back to the
// human default. Existing sessions issued before Phase 5.4 lack the field
// and must continue to work.
func TestParsePrincipalKindFromJWT_LegacyEmpty(t *testing.T) {
	tok := encodeJWT(`{"sub":"alice","tenant_id":"t1"}`)
	got := parsePrincipalKindFromJWT(tok)
	if got != "" {
		t.Errorf("legacy claim: parsePrincipalKindFromJWT = %q, want \"\"", got)
	}
}

// TestParsePrincipalKindFromJWT_Malformed covers each structural failure
// mode. Every malformed input must return "" so RequireAuth treats it as
// a legacy token and applies the human default rather than crashing.
func TestParsePrincipalKindFromJWT_Malformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"single segment", "notajwt"},
		{"two segments only", "abc.def"},
		// Body is not valid base64url — DecodeString fails.
		{"invalid base64 body", "eyJhbGciOiJSUzI1NiJ9.@@@@.sig"},
		// Body decodes but is not valid JSON.
		{"non-JSON body", "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePrincipalKindFromJWT(tc.in)
			if got != "" {
				t.Errorf("got %q, want \"\" (malformed token must fall back to human default)", got)
			}
		})
	}
}

// TestEncodeJWT_HasThreeSegments is a sanity check on the test helper
// itself — the parser counts dots, so a regression in encodeJWT would
// silently break every other test in this file.
func TestEncodeJWT_HasThreeSegments(t *testing.T) {
	tok := encodeJWT(`{"x":1}`)
	if n := strings.Count(tok, "."); n != 2 {
		t.Fatalf("encodeJWT produced %d dots, want 2", n)
	}
}
