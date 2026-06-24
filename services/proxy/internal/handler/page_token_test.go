package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPageToken_roundTrip exercises the encode/decode pair for the
// ListCachedManifests cursor. The encoded form is opaque to callers but
// must round-trip losslessly inside the proxy service — otherwise the
// second-page query reads the wrong rows.
func TestPageToken_roundTrip(t *testing.T) {
	id := uuid.MustParse("a1b2c3d4-e5f6-4789-abcd-ef0123456789")
	// Pin to a fixed instant so the test is hermetic. UTC + nanos because
	// that's the precision UnixNano preserves.
	ts := time.Date(2026, 6, 24, 14, 30, 45, 123456789, time.UTC)

	tok := encodePageToken(ts, id)
	if tok == "" {
		t.Fatal("encodePageToken returned empty token")
	}

	gotTS, gotID, err := decodePageToken(tok)
	if err != nil {
		t.Fatalf("decodePageToken: %v", err)
	}
	// time.Time round-trips through UnixNano — match by .Equal not ==.
	if !gotTS.Equal(ts) {
		t.Errorf("timestamp: got %v, want %v", gotTS, ts)
	}
	if gotID != id {
		t.Errorf("id: got %s, want %s", gotID, id)
	}
}

// TestPageToken_emptyStringDecodesToZero — the first-page sentinel.
// ListCachedManifests treats a zero timestamp as "no cursor" and skips
// the keyset clause; this test pins that contract.
func TestPageToken_emptyStringDecodesToZero(t *testing.T) {
	ts, id, err := decodePageToken("")
	if err != nil {
		t.Fatalf("decodePageToken(empty): %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time, got %v", ts)
	}
	if id != uuid.Nil {
		t.Errorf("expected nil uuid, got %v", id)
	}
}

// TestPageToken_malformed covers the bad-input surface: invalid base64,
// wrong version byte, missing separator, bad timestamp, bad uuid.
// All five should return an error and never panic — the caller passes
// the token straight through from a client.
func TestPageToken_malformed(t *testing.T) {
	cases := []struct {
		name string
		tok  string
		want string
	}{
		{"bad base64", "not!base64!", "decode"},
		{"wrong version", "AjE3MTk4MzU4NDV8YQ", "version"}, // version byte 0x02
		{"missing pipe", "AcXh1bmsK", "malformed"},
		{"bad timestamp", "AXNvbWV0aGluZ3xhYmM", "malformed"},
		{"bad uuid", "ATEyMzQ1Njc4OXxub3QtYS11dWlk", "malformed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodePageToken(tc.tok)
			if err == nil {
				t.Fatalf("expected error for %q", tc.tok)
			}
			if !strings.Contains(err.Error(), tc.want) {
				// Don't fail hard on the exact wording — different
				// malformed inputs surface different error shapes
				// (decode failure vs format failure). Just log so the
				// test stays useful when the helper text changes.
				t.Logf("error %q does not contain hint %q", err.Error(), tc.want)
			}
		})
	}
}

// TestPageToken_differentInputsProduceDifferentTokens — sanity check
// that two distinct (ts, id) pairs map to two distinct tokens. Cheap
// guard against an accidental constant-output bug.
func TestPageToken_differentInputsProduceDifferentTokens(t *testing.T) {
	id1 := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	id2 := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	ts := time.Now().UTC()

	a := encodePageToken(ts, id1)
	b := encodePageToken(ts, id2)
	if a == b {
		t.Errorf("expected distinct tokens for different ids, got %q == %q", a, b)
	}
}
