package upstream

import "testing"

// TestParseBearerChallenge_simple covers the common Docker Hub style header
// and exercises the unquoted/quoted parsing baseline.
func TestParseBearerChallenge_simple(t *testing.T) {
	header := `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`
	got := parseBearerChallenge(header)

	want := map[string]string{
		"realm":   "https://auth.docker.io/token",
		"service": "registry.docker.io",
		"scope":   "repository:library/alpine:pull",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

// TestParseBearerChallenge_commaInsideQuotedScope — PENTEST-009: a comma
// inside a quoted value must NOT split the segment. The naive splitter
// (strings.Split on ",") would have produced scope="repository:foo" and
// then a stray :bar:pull" segment.
func TestParseBearerChallenge_commaInsideQuotedScope(t *testing.T) {
	header := `Bearer realm="https://auth.example/token",scope="repository:foo,bar:pull"`
	got := parseBearerChallenge(header)

	if got["realm"] != "https://auth.example/token" {
		t.Errorf("realm: got %q", got["realm"])
	}
	if got["scope"] != "repository:foo,bar:pull" {
		t.Errorf("scope: got %q, want %q (PENTEST-009 quoted-comma)", got["scope"], "repository:foo,bar:pull")
	}
}

// TestParseBearerChallenge_escapedQuotes exercises the RFC 7230 quoted-string
// backslash escapes inside a value. The parser must honour `\"` so it does
// not terminate the quote prematurely.
func TestParseBearerChallenge_escapedQuotes(t *testing.T) {
	header := `Bearer realm="https://auth.example/token",service="weird\"name"`
	got := parseBearerChallenge(header)

	if got["service"] != `weird"name` {
		t.Errorf("service: got %q, want %q", got["service"], `weird"name`)
	}
}

// TestParseBearerChallenge_extraWhitespaceAndMissingValue tolerates the kinds
// of malformed headers we may see in the wild without crashing.
func TestParseBearerChallenge_extraWhitespaceAndMissingValue(t *testing.T) {
	header := `Bearer realm =  "https://x"  ,   ,no-equals-sign,scope="x"`
	got := parseBearerChallenge(header)

	if got["realm"] != "https://x" {
		t.Errorf("realm: got %q, want %q", got["realm"], "https://x")
	}
	if got["scope"] != "x" {
		t.Errorf("scope: got %q, want %q", got["scope"], "x")
	}
	if _, ok := got["no-equals-sign"]; ok {
		t.Errorf("malformed segment should be ignored, got entry")
	}
}
