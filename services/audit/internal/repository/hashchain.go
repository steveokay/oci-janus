// Hash-chain primitives for audit_events (REDESIGN-001 Phase 6.12).
//
// This file is the single canonical source for both inserter (Insert) and
// verifier (VerifyChain). If a future verifier disagrees with the inserter
// by a single byte, the entire chain reads as tampered. Treat any change
// to canonicalRowBytes as a wire-format change — it MUST stay
// backward-compatible with rows already in the database.
//
// =============================================================================
// CANONICAL ROW SERIALISATION
// =============================================================================
//
// Each row contributes the following bytes to the chain, in exactly this
// order (writes via writeField below for length-prefix framing so two
// adjacent strings like "" and "x" cannot collide with one "" + "x"):
//
//  1. id           (UUID, 16 raw bytes — Marshal() output)
//  2. tenant_id    (UUID, 16 raw bytes)
//  3. actor_id     (string, length-prefixed UTF-8)
//  4. actor_type   (string, length-prefixed UTF-8)
//  5. actor_ip     (string, length-prefixed UTF-8)
//  6. action       (string, length-prefixed UTF-8)
//  7. resource     (string, length-prefixed UTF-8)
//  8. outcome      (string, length-prefixed UTF-8)
//  9. metadata     (raw JSON bytes, length-prefixed; we DO NOT
//     re-canonicalise the JSON because the inserter and
//     verifier both see the same byte sequence produced
//     by the consumer's json.Marshal. If a future change
//     starts re-marshalling JSON in the read path it MUST
//     do the same here to stay deterministic.)
//  10. occurred_at  (UTC time formatted as RFC3339Nano — fixed
//     timezone, fixed precision, length-prefixed.)
//
// Length prefix: 4-byte big-endian uint32 of the field byte length.
// We use a fixed-width framing (rather than newline / NUL terminators)
// to guarantee no field value can fake a frame boundary.
//
// Hash function: SHA-256.
// row_hash = sha256(prev_hash || canonical_row_bytes(this row))
//
// Genesis row: prev_hash is the single byte 0x00 (DB column DEFAULT).
//
// All bytes flow through writeField; the FRAME_VERSION constant gets
// hashed first inside canonicalRowBytes so a future incompatible change
// (e.g. switching to JSON canonicalisation) can be detected by bumping
// the version and the verifier will surface the mismatch immediately.
package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"sort"
	"time"

	"github.com/google/uuid"
)

// chainFrameVersion is hashed into every row so a future canonicalisation
// change can be detected. v1 = the layout documented in the file header.
// Bump this if the serialisation contract changes; a verifier that sees
// rows with a different frame version will (correctly) flag them.
const chainFrameVersion uint32 = 1

// genesisPrevHash is the single-byte sentinel stored on the very first
// row of a tenant's chain (matches the prev_hash column DEFAULT). Kept
// as a package-level constant so the inserter and verifier agree on the
// exact byte sequence — including length — without re-deriving it.
var genesisPrevHash = []byte{0x00}

// canonicalRowBytes assembles the deterministic byte payload for a single
// audit row, ready to be SHA-256'd together with prev_hash to produce
// row_hash. See the file-level comment for the column ordering contract.
//
// IMPORTANT: every branch of this function must produce the same bytes
// regardless of Go version, platform, or pgx driver version. No map
// iteration, no time.Local, no fmt.Sprintf-of-floats.
func canonicalRowBytes(e *AuditEvent) ([]byte, error) {
	h := newCanonHasher()

	// 1. Frame version up front so a layout change is detectable.
	var verBuf [4]byte
	binary.BigEndian.PutUint32(verBuf[:], chainFrameVersion)
	if _, err := h.Write(verBuf[:]); err != nil {
		return nil, fmt.Errorf("canonicalRowBytes write version: %w", err)
	}

	// 2. UUIDs as raw 16-byte arrays so JSON or text-form drift cannot
	//    perturb the hash. MarshalBinary is documented to return the
	//    fixed 16-byte representation.
	idBytes, err := e.ID.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("canonicalRowBytes marshal id: %w", err)
	}
	if err := writeField(h, idBytes); err != nil {
		return nil, err
	}
	tenantBytes, err := e.TenantID.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("canonicalRowBytes marshal tenant: %w", err)
	}
	if err := writeField(h, tenantBytes); err != nil {
		return nil, err
	}

	// 3-8. String fields in the exact order defined at the top of file.
	for _, s := range []string{
		e.ActorID,
		e.ActorType,
		e.ActorIP,
		e.Action,
		e.Resource,
		e.Outcome,
	} {
		if err := writeField(h, []byte(s)); err != nil {
			return nil, err
		}
	}

	// 9. Metadata: canonicalised JSON. Postgres JSONB does not preserve
	//    key order or whitespace, so hashing the raw bytes the
	//    application produced would never match the bytes pgx reads
	//    back from JSONB. We re-encode the JSON with a deterministic
	//    canonical form (sorted object keys at every depth, no
	//    whitespace) so the inserter and verifier produce identical
	//    bytes regardless of how Postgres reformatted the storage.
	//    nil metadata is treated as `{}` (the same default the
	//    inserter applies before the DB write).
	meta := e.Metadata
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	canonMeta, err := canonicaliseJSON(meta)
	if err != nil {
		return nil, fmt.Errorf("canonicalRowBytes metadata: %w", err)
	}
	if err := writeField(h, canonMeta); err != nil {
		return nil, err
	}

	// 10. occurred_at: RFC3339Nano in UTC, length-prefixed string. UTC
	//     is mandatory — a local-time format would hash differently on
	//     a verifier running in a different TZ. We TRUNCATE to
	//     microseconds before formatting because Postgres TIMESTAMPTZ
	//     only stores microsecond precision; if the inserter hashes the
	//     wall-clock nanoseconds it set on e.OccurredAt, the verifier
	//     reading the row back from Postgres will see a truncated time
	//     and recompute a different hash. Truncating here matches what
	//     the DB will durably hold for both the inserter and verifier.
	tsBytes := []byte(e.OccurredAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
	if err := writeField(h, tsBytes); err != nil {
		return nil, err
	}

	return h.Sum(nil), nil
}

// computeRowHash returns sha256(prev || canonical(e)). prev MUST be the
// previous row's row_hash (or genesisPrevHash for the first row).
func computeRowHash(prev []byte, e *AuditEvent) ([]byte, error) {
	canon, err := canonicalRowBytes(e)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	// Length-prefix both halves so prev=AB||canon=C is distinguishable
	// from prev=A||canon=BC. A single-byte prev would collide with the
	// genesis sentinel otherwise.
	if err := writeField(h, prev); err != nil {
		return nil, err
	}
	if err := writeField(h, canon); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// writeField writes a 4-byte big-endian length prefix followed by the
// raw field bytes. The fixed-width prefix makes the framing unambiguous
// regardless of field content.
func writeField(h hash.Hash, b []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	if _, err := h.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("hashchain write len: %w", err)
	}
	if len(b) == 0 {
		return nil
	}
	if _, err := h.Write(b); err != nil {
		return fmt.Errorf("hashchain write field: %w", err)
	}
	return nil
}

// newCanonHasher returns a fresh SHA-256 instance. Wrapped in a helper
// so a future migration to a different hash (BLAKE3, etc.) is one
// substitution rather than a sweep across this file.
func newCanonHasher() hash.Hash { return sha256.New() }

// tenantAdvisoryLockKey hashes a tenant UUID down to the int64 key
// pg_advisory_xact_lock(bigint) accepts. We use FNV-64a so the key is
// stable across processes and Go versions; pg_advisory locks are
// process-global so two concurrent inserters for the same tenant must
// agree on the same bigint key.
//
// Collisions across DIFFERENT tenants are harmless — at worst two
// tenants serialise their inserts unnecessarily. They never lose
// integrity. (SEC-044 redesign: an earlier draft used a separate
// audit_chain_tip table, but granting UPDATE on it to
// `registry_audit_app` defeated the tamper-evidence posture.
// We now derive the tip from `SELECT row_hash FROM audit_events
// ORDER BY chain_seq DESC LIMIT 1` so the role keeps INSERT-only
// on audit_events.)
func tenantAdvisoryLockKey(tenantID uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write(tenantID[:])
	// Reinterpret uint64 → int64 (pg_advisory_xact_lock signature). The
	// raw bit pattern survives — sign just flips for keys with the high
	// bit set, which Postgres accepts without complaint.
	return int64(h.Sum64()) //nolint:gosec // intentional reinterpret
}

// VerifyChain walks every audit_events row for tenantID by following the
// linked-list structure of the chain (start at the genesis row whose
// prev_hash is the sentinel 0x00 byte, then jump to whichever row has
// prev_hash = current.row_hash, etc.) and recomputes each row_hash from
// (prev_hash, canonical bytes). Returns the (id, occurredAt) of the
// first row whose stored hash does not match the recomputation, or a
// row that should have a successor but doesn't (broken chain).
//
// Returns (uuid.Nil, time.Time{}, nil) when the chain is intact and
// every row in the table is accounted for.
//
// LINKED-LIST WALK vs. SORT-BY-OCCURRED_AT: we deliberately do NOT walk
// rows in occurred_at order because the chain order is the order of
// Insert lock acquisition (which is the order that produced each
// prev_hash → row_hash link), not the wall-clock order of the events.
// Two events with very close occurred_at values can be inserted in
// either order depending on which goroutine grabbed the advisory lock
// first; the chain structure is the source of truth.
//
// A mismatch means either the row was UPDATEd in place after insert
// (tampering) or there's a canonicalisation bug — either way the
// chain is no longer trustworthy from that row forward.
//
// This is a read-only operation; it does not take the advisory lock.
// Running it concurrently with active inserts is safe but may flag
// "in-flight" rows whose prev_hash refers to an as-yet-uncommitted
// row — in practice operators run VerifyChain offline against a
// snapshot, so we accept the race.
func (r *Repository) VerifyChain(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, time.Time, error) {
	// Load every row for the tenant into memory keyed by hex(prev_hash).
	// Audit chains are bounded by retention (default 30 days) so an
	// in-memory walk is acceptable; for very large tenants a future
	// streaming verifier could index by prev_hash inside Postgres.
	rows, err := r.pool.Query(ctx,
		`SELECT id, tenant_id, actor_id, actor_type, actor_ip, action, resource,
		        outcome, metadata, occurred_at, prev_hash, row_hash
		 FROM audit_events
		 WHERE tenant_id = $1`,
		tenantID,
	)
	if err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("VerifyChain query: %w", err)
	}
	defer rows.Close()

	// Bucket every row by its prev_hash so we can hop from row N to
	// row N+1 in O(1) lookups. Theoretical edge case: two rows share a
	// prev_hash (a race window in concurrent inserts that defeats the
	// advisory lock). We surface that by returning the second-such row
	// as tampered — the chain has forked, which is itself a failure.
	type rowRec struct {
		ev       AuditEvent
		prevHash []byte
		rowHash  []byte
	}
	bucket := make(map[string]*rowRec)
	var total int
	for rows.Next() {
		var rec rowRec
		if err := rows.Scan(
			&rec.ev.ID, &rec.ev.TenantID, &rec.ev.ActorID, &rec.ev.ActorType, &rec.ev.ActorIP,
			&rec.ev.Action, &rec.ev.Resource, &rec.ev.Outcome, &rec.ev.Metadata, &rec.ev.OccurredAt,
			&rec.prevHash, &rec.rowHash,
		); err != nil {
			return uuid.Nil, time.Time{}, fmt.Errorf("VerifyChain scan: %w", err)
		}
		key := string(rec.prevHash)
		if _, dup := bucket[key]; dup {
			// Two rows with the same prev_hash means the chain forked
			// — the advisory lock was bypassed or a tamperer cloned a
			// row. Flag the second row.
			return rec.ev.ID, rec.ev.OccurredAt, nil
		}
		// Copy so the loop variable's storage is not reused across
		// iterations (rec is reassigned each loop and we keep a
		// pointer into bucket).
		recCopy := rec
		bucket[key] = &recCopy
		total++
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("VerifyChain iterate: %w", err)
	}

	// Walk from the genesis row forward. Empty chain → trivially intact.
	current, ok := bucket[string(genesisPrevHash)]
	if !ok {
		if total == 0 {
			return uuid.Nil, time.Time{}, nil
		}
		// Rows exist but none claim to be the genesis row — the
		// genesis row was deleted (or never had prev_hash = 0x00).
		// Surface the first row we still hold so the operator has a
		// concrete id to investigate.
		for _, r := range bucket {
			return r.ev.ID, r.ev.OccurredAt, nil
		}
	}
	seen := 0
	for current != nil {
		// Verify this row's stored row_hash matches the recomputed
		// value from (prev_hash, canonical bytes).
		want, err := computeRowHash(current.prevHash, &current.ev)
		if err != nil {
			return uuid.Nil, time.Time{}, fmt.Errorf("VerifyChain recompute: %w", err)
		}
		if !bytesEqual(want, current.rowHash) {
			return current.ev.ID, current.ev.OccurredAt, nil
		}
		seen++
		// Hop to the row whose prev_hash equals THIS row's row_hash.
		// If none, we've reached the tip — exit the loop.
		current = bucket[string(current.rowHash)]
	}
	// All rows traversed → chain is intact. If `seen` is less than the
	// total we loaded, some rows are orphans (not reachable from the
	// genesis row). Surface the first orphan id so the operator can
	// investigate. This catches the case where a tamperer inserted a
	// row in the middle of the chain by hand.
	if seen != total {
		// Build the reachable set by walking the chain again. An
		// orphan is any bucket entry not in the reachable set.
		reachable := map[string]bool{}
		walk := bucket[string(genesisPrevHash)]
		for walk != nil {
			reachable[string(walk.rowHash)] = true
			walk = bucket[string(walk.rowHash)]
		}
		for _, r := range bucket {
			if !reachable[string(r.rowHash)] {
				return r.ev.ID, r.ev.OccurredAt, nil
			}
		}
	}
	return uuid.Nil, time.Time{}, nil
}

// bytesEqual is a fixed-length, constant-time-ish equality check used in
// VerifyChain. The hashes are short and not secret so we don't need
// crypto/subtle, but a length-first short-circuit avoids a panic on a
// truncated column.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ErrTenantIDRequired is returned when the inserter is called without a
// tenant_id. The chain is per-tenant so a missing tenant_id is a
// programmer error, not a fix-at-runtime situation.
var ErrTenantIDRequired = errors.New("audit Insert: tenant_id required for hash chain")

// canonicaliseJSON returns a deterministic byte representation of the
// input JSON: object keys sorted lexicographically at every depth, no
// whitespace anywhere. This matches the shape Postgres JSONB returns
// (key-sorted, whitespace-stripped) up to numeric formatting, which
// json.Marshal in Go uses the same RFC 8259 canonical form for as
// Postgres jsonb_out for the typical integer + bool + string + nested
// object payloads the audit consumer produces.
//
// We use encoding/json's intermediate any-tree rather than a streaming
// re-encoder because:
//   - the audit metadata blob is bounded in size (a few KB max),
//   - we need recursive key sorting which a stream encoder cannot do
//     without buffering the whole payload anyway, and
//   - the inserter and verifier MUST agree on the output, and
//     json.Marshal is deterministic for sorted-key maps.
//
// If a future audit consumer starts using exotic number types
// (json.Number, big floats) we'd need a custom encoder — for now the
// payload shapes are simple maps + strings + ints + booleans + nested
// raw JSON, all of which round-trip identically.
func canonicaliseJSON(raw []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve int/float distinction so 1 vs 1.0 stays stable
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonicaliseJSON decode: %w", err)
	}
	var buf bytes.Buffer
	if err := writeCanonicalValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeCanonicalValue serialises v with sorted keys and no whitespace.
// Recurses into maps and slices; primitives go through json.Marshal.
func writeCanonicalValue(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonicalValue(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}
