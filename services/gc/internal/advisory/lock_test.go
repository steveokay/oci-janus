// Package advisory_test tests the lockKey helper — a pure function that can be
// verified without a PostgreSQL connection.
package advisory

import (
	"testing"

	"github.com/google/uuid"
)

// TestLockKey_deterministic verifies that calling lockKey twice with the same
// UUID always returns the same int64 value (it must be stable across calls).
func TestLockKey_deterministic(t *testing.T) {
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	k1 := lockKey(id)
	k2 := lockKey(id)
	if k1 != k2 {
		t.Errorf("lockKey not deterministic: %d != %d", k1, k2)
	}
}

// TestLockKey_differentUUIDs verifies that two distinct UUIDs produce different
// lock keys. While collisions are theoretically possible (FNV-64a has a 64-bit
// output), they must not happen for these fixed, well-known UUIDs.
func TestLockKey_differentUUIDs(t *testing.T) {
	a := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	b := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	if lockKey(a) == lockKey(b) {
		t.Error("distinct UUIDs must produce distinct lock keys")
	}
}

// TestLockKey_zeroUUID verifies that the nil UUID does not panic and returns
// a consistent value (edge case: all-zero UUID is a valid input).
func TestLockKey_zeroUUID(t *testing.T) {
	zero := uuid.UUID{} // all-zero bytes
	k := lockKey(zero)
	if k == 0 {
		// FNV-64a of 16 zero bytes is non-zero; if it somehow returns 0 that's worth noting.
		t.Log("lockKey(zeroUUID) == 0 — check FNV output for all-zero input")
	}
	// Call twice to verify determinism.
	if lockKey(zero) != k {
		t.Error("lockKey(zeroUUID) not deterministic")
	}
}
