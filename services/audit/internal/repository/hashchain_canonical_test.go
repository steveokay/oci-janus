// Unit tests for the canonical JSON serialiser that feeds the audit hash
// chain (SEC-052). The chain's tamper-evidence depends on canonicaliseJSON
// producing byte-identical output for the inserter and the verifier regardless
// of key order, whitespace, or number magnitude. These tests are pure (no DB,
// no build tag) so the standard `go test ./...` CI job exercises them.
package repository

import (
	"strings"
	"testing"
)

// TestCanonicaliseJSON_SortsKeysAtEveryDepth pins the recursive key sort and
// the preservation of array order.
func TestCanonicaliseJSON_SortsKeysAtEveryDepth(t *testing.T) {
	in := []byte(`{"b":1,"a":{"d":2,"c":3},"z":[3,1,2]}`)
	got, err := canonicaliseJSON(in)
	if err != nil {
		t.Fatalf("canonicaliseJSON: %v", err)
	}
	// Keys sorted a,b,z; nested object sorted c,d; array order untouched.
	want := `{"a":{"c":3,"d":2},"b":1,"z":[3,1,2]}`
	if string(got) != want {
		t.Fatalf("canonical form mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestCanonicaliseJSON_LargeIntegerKeepsPrecision proves the UseNumber path
// carries integers past the float64 exact-integer boundary. 9007199254740993 =
// 2^53 + 1 is the first integer float64 cannot represent; without UseNumber the
// decoder would round it to ...992 and two payloads differing only above the
// boundary would hash identically — a silent chain collision.
func TestCanonicaliseJSON_LargeIntegerKeepsPrecision(t *testing.T) {
	got, err := canonicaliseJSON([]byte(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatalf("canonicaliseJSON: %v", err)
	}
	if !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("large integer lost precision: got %s", got)
	}
	// And the sibling value that float64 WOULD collapse it to must stay
	// distinct — different input → different canonical bytes.
	other, err := canonicaliseJSON([]byte(`{"n":9007199254740992}`))
	if err != nil {
		t.Fatalf("canonicaliseJSON sibling: %v", err)
	}
	if string(got) == string(other) {
		t.Fatalf("2^53+1 and 2^53 canonicalised to the same bytes: %s", got)
	}
}

// TestCanonicaliseJSON_WhitespaceInsensitiveAndIdempotent proves the output is
// invariant under input whitespace + key order, and that re-canonicalising the
// output is a no-op (the inserter and verifier converge on one form).
func TestCanonicaliseJSON_WhitespaceInsensitiveAndIdempotent(t *testing.T) {
	spaced := []byte("{ \"a\" : 1 ,\n\t\"b\" : [ 1 , 2 ] }")
	tight := []byte(`{"b":[1,2],"a":1}`)

	c1, err := canonicaliseJSON(spaced)
	if err != nil {
		t.Fatalf("canonicaliseJSON spaced: %v", err)
	}
	c2, err := canonicaliseJSON(tight)
	if err != nil {
		t.Fatalf("canonicaliseJSON tight: %v", err)
	}
	if string(c1) != string(c2) {
		t.Fatalf("whitespace/key-order changed the canonical form:\n %s\n %s", c1, c2)
	}
	c3, err := canonicaliseJSON(c1)
	if err != nil {
		t.Fatalf("canonicaliseJSON idempotent: %v", err)
	}
	if string(c3) != string(c1) {
		t.Fatalf("canonicaliseJSON not idempotent:\n %s\n %s", c1, c3)
	}
}

// TestCanonicaliseJSON_RejectsInvalidJSON confirms the serialiser errors rather
// than emitting non-deterministic or non-JSON hash input. NaN / Infinity are
// not valid JSON number tokens (the edge case named in SEC-052), and truncated
// / empty input must also fail loudly.
func TestCanonicaliseJSON_RejectsInvalidJSON(t *testing.T) {
	for _, bad := range []string{`{"n":NaN}`, `{"n":Infinity}`, `{"n":-Inf}`, `{`, ``} {
		if _, err := canonicaliseJSON([]byte(bad)); err == nil {
			t.Errorf("canonicaliseJSON(%q) should error, got nil", bad)
		}
	}
}
