// Package repository tests focus on the pure helpers — the page token
// codec and the nullableTenant adapter. The SQL paths are exercised
// end-to-end by the handler tests against a fake Repository
// implementation; running pgxpool against a real Postgres is out of
// scope for this unit-test suite.
package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPageToken_roundTrip — encode → decode preserves both fields for
// a concrete completed_at + run_id pair.
func TestPageToken_roundTrip(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	completed := time.Date(2026, 6, 21, 12, 34, 56, 789_000_000, time.UTC)

	tok := encodePageToken(completed, id)
	if tok == "" {
		t.Fatal("encoded token should not be empty")
	}

	gotCompleted, gotID, err := decodePageToken(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotID != id {
		t.Errorf("run_id: got %v, want %v", gotID, id)
	}
	if gotCompleted == nil {
		t.Fatal("completed_at: got nil, want non-nil")
	}
	if !gotCompleted.Equal(completed) {
		t.Errorf("completed_at: got %v, want %v", *gotCompleted, completed)
	}
}

// TestPageToken_nullCompletedAt — encoding a zero/epoch timestamp
// produces a token whose decoded completed_at is nil, the sentinel
// used when paging through in-flight (NULL completed_at) rows.
func TestPageToken_nullCompletedAt(t *testing.T) {
	id := uuid.New()
	tok := encodePageToken(time.Time{}, id)
	gotCompleted, gotID, err := decodePageToken(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotID != id {
		t.Errorf("run_id: got %v, want %v", gotID, id)
	}
	if gotCompleted != nil {
		t.Errorf("expected nil completed_at for zero input, got %v", *gotCompleted)
	}
}

// TestPageToken_malformedBase64 — a non-base64url string surfaces as
// an error so the handler can return INVALID_ARGUMENT.
func TestPageToken_malformedBase64(t *testing.T) {
	_, _, err := decodePageToken("@@@@not-base64@@@@")
	if err == nil {
		t.Error("expected decode error for malformed base64")
	}
}

// TestPageToken_missingSeparator — base64 that decodes to a string
// without the `|` separator must fail decoding rather than silently
// emitting a zero UUID.
func TestPageToken_missingSeparator(t *testing.T) {
	// "no separator here" base64url-encoded.
	encoded := "bm8gc2VwYXJhdG9yIGhlcmU"
	_, _, err := decodePageToken(encoded)
	if err == nil {
		t.Error("expected decode error for missing separator")
	}
}

// TestPageToken_invalidUUID — the run_id portion must parse as UUID.
func TestPageToken_invalidUUID(t *testing.T) {
	// "|not-a-uuid" base64url-encoded.
	encoded := "fG5vdC1hLXV1aWQ"
	_, _, err := decodePageToken(encoded)
	if err == nil {
		t.Error("expected decode error for invalid uuid portion")
	}
}

// TestNullableTenant_zeroUUIDToNil verifies the SQL helper that maps
// uuid.Nil → SQL NULL so cross-tenant cron rows land with NULL in
// tenant_id rather than the zero UUID literal.
func TestNullableTenant_zeroUUIDToNil(t *testing.T) {
	if v := nullableTenant(uuid.Nil); v != nil {
		t.Errorf("nullableTenant(Nil): got %v, want nil", v)
	}
}

// TestNullableTenant_realUUIDPassthrough verifies that a non-nil UUID
// surfaces unchanged — we don't want SQL NULL for tenant-scoped rows.
func TestNullableTenant_realUUIDPassthrough(t *testing.T) {
	id := uuid.New()
	v := nullableTenant(id)
	if v == nil {
		t.Fatal("nullableTenant(real uuid): got nil")
	}
	if got, ok := v.(uuid.UUID); !ok || got != id {
		t.Errorf("nullableTenant pass-through: got %v (%T), want %v", v, v, id)
	}
}
