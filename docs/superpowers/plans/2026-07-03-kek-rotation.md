# KEK Rotation Tool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give operators a safe, per-service `rotate-kek` CLI subcommand that re-encrypts every KEK-encrypted column across the four affected databases (auth, proxy, webhook, audit) from an OLD key to a NEW key, with dry-run and verify modes.

**Architecture:** A shared, table-agnostic `libs/crypto/rekey` package holds the crypto core (`Rekey`, `OnNewKey`, key helpers) plus a declarative DB-sweep engine and a CLI runner. Each affected service declares its own `TableSpec`s (which table, PK, cipher columns, BYTEA-vs-hex encoding) and dispatches a `rotate-kek` subcommand in `cmd/server/main.go` exactly as the existing `bootstrap` subcommand does. Each service gains one goose migration adding a nullable `kek_version SMALLINT` tracking column. Rotation runs in a brief maintenance window; each table rotates in a single all-or-nothing transaction.

**Tech Stack:** Go 1.25, `pgx/v5` + `pgxpool`, `pressly/goose` SQL migrations, `libs/crypto/aes` (AES-256-GCM), testcontainers via `libs/testutil/containers`.

**Source spec:** `docs/superpowers/specs/2026-07-03-kek-rotation-design.md` (approved).

---

## Settled design decisions (resolving spec §11 open questions)

1. **`--to-version` defaulting:** default is `1 + max(COALESCE(kek_version,0))` computed across all of a service's non-optional tables *before* the sweep, so one rotation run stamps a single generation across the service. `--to-version N` overrides. Fresh DB (all NULL) → version `1`.
2. **Audit's two columns:** rotate in **one transaction per table** (both `hmac_secret` and `bearer_token` live on `audit_export_configs`). The `TableSpec` carries a slice of cipher columns; the sweep selects, rekeys, and `UPDATE`s them together per row.
3. **Verify exit codes:** `rotate-kek --verify` exits `0` when every row is on the NEW key, and `3` when rows remain on the old key (for scripting). Operator-input errors exit `2`; infrastructure errors exit `1`.
4. **PK type-agnosticism:** PK columns differ (`upstream_id`, `id`, `provider_id` which is **TEXT**). The sweep selects `<pk>::text` and updates `WHERE <pk>::text = $n`, so it handles UUID and TEXT PKs uniformly without per-table type knowledge. Tables are tiny, so the index-cast cost is irrelevant.

## Affected surfaces (authoritative — verified against migrations)

| Service | Table | PK column | Cipher column(s) | Encoding | DSN env |
|---|---|---|---|---|---|
| proxy | `upstream_registries` | `upstream_id` | `password_enc` | BYTEA | `DB_DSN` |
| webhook | `webhook_endpoints` | `id` | `secret_enc` | **hex TEXT** | `DB_DSN` |
| audit | `audit_export_configs` | `id` | `hmac_secret`, `bearer_token` | BYTEA | `DB_DSN` |
| auth | `global_sso_config` | `provider_id` (**TEXT**) | `oauth_client_secret_enc` | BYTEA | `AUTH_DB_DSN` |
| auth (legacy, optional) | `auth_providers` | `id` | `oauth_client_secret_enc` | BYTEA | `AUTH_DB_DSN` |

## File Structure

**New shared package `libs/crypto/rekey/`:**
- `rekey.go` — crypto core: `Rekey`, `OnNewKey`, `GenerateKeyHex`, `ParseKeyHex`.
- `rekey_test.go` — pure unit tests for the core (no DB).
- `sweep.go` — declarative engine: `Encoding`, `CipherColumn`, `TableSpec`, `Mode`, `Report`, `Sweep`.
- `cli.go` — `RunCLI`: flag parsing (`--dry-run`/`--verify`/`--generate`/`--to-version`), key-from-env, DSN connect, dispatch to `Sweep`; typed `ValidationError` + `ErrRowsRemain`.
- `sweep_test.go` — integration tests for the engine + CLI against a throwaway table (testcontainers).

**Per service (`proxy`, `webhook`, `audit`, `auth`):**
- `internal/rotatekek/rotatekek.go` — declares the service's `TableSpec`s + DSN env var; thin `Run(ctx, args, stdout) error` wrapper over `rekey.RunCLI`.
- `internal/rotatekek/rotatekek_test.go` — integration test: seed rows under OLD, run rotate, assert NEW-decryptable + `kek_version` stamped, assert verify/dry-run behaviour.
- `cmd/server/main.go` — add `rotate-kek` dispatch block (mirrors `bootstrap`).
- `migrations/20260703HHMMSS_add_kek_version_<table>.sql` — nullable `kek_version SMALLINT` + down migration.

**Docs / trackers:**
- `infra/runbooks/kek-rotation.md` — operator runbook.
- `futures.md`, `status.md` — tracker updates.
- Each service `.env.example` — document `KEK_OLD_HEX` / `KEK_NEW_HEX` (subcommand-only).

---

## Task 1: `libs/crypto/rekey` crypto core

**Files:**
- Create: `libs/crypto/rekey/rekey.go`
- Test: `libs/crypto/rekey/rekey_test.go`

- [ ] **Step 1: Write the failing tests**

Create `libs/crypto/rekey/rekey_test.go`:

```go
// Package rekey unit tests — pure crypto core, no database.
package rekey

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
)

// key32 returns a deterministic 32-byte key whose bytes are all b.
func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func TestRekey_RoundTrip(t *testing.T) {
	oldKey, newKey := key32(0x11), key32(0x22)
	plaintext := []byte("super-secret-oauth-client-secret")

	oldCT, err := aes.Encrypt(plaintext, oldKey)
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}

	newCT, err := Rekey(oldKey, newKey, oldCT)
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	// Decrypts under NEW.
	got, err := aes.Decrypt(newCT, newKey)
	if err != nil {
		t.Fatalf("decrypt under new key: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}

	// Does NOT decrypt under OLD any more.
	if _, err := aes.Decrypt(newCT, oldKey); err == nil {
		t.Fatal("re-encrypted ciphertext must not decrypt under the old key")
	}
}

func TestRekey_WrongOldKeyFails(t *testing.T) {
	realOld, wrongOld, newKey := key32(0x11), key32(0x99), key32(0x22)
	ct, _ := aes.Encrypt([]byte("x"), realOld)

	if _, err := Rekey(wrongOld, newKey, ct); err == nil {
		t.Fatal("Rekey must fail when the ciphertext does not decrypt under oldKey")
	}
}

func TestOnNewKey(t *testing.T) {
	oldKey, newKey := key32(0x11), key32(0x22)
	oldCT, _ := aes.Encrypt([]byte("x"), oldKey)
	newCT, _ := aes.Encrypt([]byte("x"), newKey)

	if OnNewKey(newKey, oldCT) {
		t.Fatal("OnNewKey must be false for a ciphertext encrypted under the old key")
	}
	if !OnNewKey(newKey, newCT) {
		t.Fatal("OnNewKey must be true for a ciphertext encrypted under the new key")
	}
}

func TestParseKeyHex(t *testing.T) {
	valid := hex.EncodeToString(key32(0x33))
	k, err := ParseKeyHex("  " + valid + "\n")
	if err != nil {
		t.Fatalf("ParseKeyHex(valid): %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(k))
	}

	if _, err := ParseKeyHex("not-hex"); err == nil {
		t.Fatal("ParseKeyHex must reject non-hex input")
	}
	if _, err := ParseKeyHex(hex.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("ParseKeyHex must reject a 16-byte key")
	}
}

func TestGenerateKeyHex(t *testing.T) {
	h, err := GenerateKeyHex()
	if err != nil {
		t.Fatalf("GenerateKeyHex: %v", err)
	}
	k, err := ParseKeyHex(h)
	if err != nil {
		t.Fatalf("generated key not parseable: %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("generated key wrong length: %d", len(k))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd libs && go test ./crypto/rekey/ -run 'Rekey|OnNewKey|ParseKeyHex|GenerateKeyHex' -v`
Expected: FAIL to compile — `undefined: Rekey` (and the other symbols).

- [ ] **Step 3: Write the implementation**

Create `libs/crypto/rekey/rekey.go`:

```go
// Package rekey provides KEK-rotation primitives: re-encrypting an
// AES-256-GCM ciphertext from an old key-encryption key (KEK) to a new one,
// and the declarative sweep engine + CLI runner used by each service's
// `rotate-kek` subcommand (RED-FU-015).
//
// The crypto core composes the two existing single-key calls in
// libs/crypto/aes — it deliberately does not touch the AES codec itself, so
// the ciphertext layout is unchanged and re-encrypted rows stay v1.
package rekey

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
)

// keyLen is the required KEK length in bytes (AES-256).
const keyLen = 32

// Rekey re-encrypts one ciphertext from oldKey to newKey. It returns the new
// ciphertext, or an error if the cell does not decrypt under oldKey (wrong
// key, corrupt, or tampered — a GCM authentication failure). The plaintext is
// never returned or logged.
func Rekey(oldKey, newKey, ciphertext []byte) ([]byte, error) {
	plain, err := aes.Decrypt(ciphertext, oldKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt under old key: %w", err)
	}
	out, err := aes.Encrypt(plain, newKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt under new key: %w", err)
	}
	return out, nil
}

// OnNewKey reports whether a ciphertext already decrypts under newKey. It is
// the authoritative "is this row done?" check used by --verify: a row that
// returns true needs no rotation.
func OnNewKey(newKey, ciphertext []byte) bool {
	_, err := aes.Decrypt(ciphertext, newKey)
	return err == nil
}

// ParseKeyHex decodes a hex-encoded KEK and validates it is exactly 32 bytes.
// Surrounding whitespace (e.g. a trailing newline from a piped env var) is
// trimmed. The key material is never logged by callers.
func ParseKeyHex(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("key is not valid hex: %w", err)
	}
	if len(b) != keyLen {
		return nil, fmt.Errorf("key must be %d bytes (%d hex chars), got %d", keyLen, keyLen*2, len(b))
	}
	return b, nil
}

// GenerateKeyHex mints a fresh 32-byte KEK from crypto/rand and returns it
// hex-encoded, ready to paste into a secrets manager. Used by --generate.
func GenerateKeyHex() (string, error) {
	b := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd libs && go test ./crypto/rekey/ -v`
Expected: PASS (all Task 1 tests).

- [ ] **Step 5: Commit**

```bash
git add libs/crypto/rekey/rekey.go libs/crypto/rekey/rekey_test.go
git commit -m "feat(rekey): KEK re-encryption crypto core (RED-FU-015)"
```

---

## Task 2: `libs/crypto/rekey` sweep engine + CLI runner

**Files:**
- Create: `libs/crypto/rekey/sweep.go`
- Create: `libs/crypto/rekey/cli.go`
- Test: `libs/crypto/rekey/sweep_test.go`

- [ ] **Step 1: Write the failing integration test**

Create `libs/crypto/rekey/sweep_test.go`. It builds a throwaway table exercising both a BYTEA column and a hex-TEXT column plus a TEXT primary key, then drives `Sweep` in all three modes.

```go
package rekey_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/crypto/rekey"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// setupTable creates a table with a TEXT PK, one BYTEA cipher column, one
// hex-TEXT cipher column, and a nullable kek_version, then seeds one row whose
// secrets are encrypted under oldKey.
func setupTable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, oldKey []byte) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		CREATE TABLE sweep_fixture (
			pk           TEXT PRIMARY KEY,
			blob_enc     BYTEA,
			hex_enc      TEXT,
			kek_version  SMALLINT
		)`)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	blobCT, _ := aes.Encrypt([]byte("blob-secret"), oldKey)
	hexCT, _ := aes.Encrypt([]byte("hex-secret"), oldKey)
	_, err = pool.Exec(ctx,
		`INSERT INTO sweep_fixture (pk, blob_enc, hex_enc) VALUES ('row-1', $1, $2)`,
		blobCT, hex.EncodeToString(hexCT))
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}
}

func fixtureSpecs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "sweep_fixture",
		PKColumn:      "pk",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "blob_enc", Encoding: rekey.EncodingBytea},
			{Name: "hex_enc", Encoding: rekey.EncodingHexText},
		},
	}}
}

func TestSweep_Rotate(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, containers.Postgres(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oldKey, newKey := key32(0x11), key32(0x22)
	setupTable(t, ctx, pool, oldKey)

	rep, err := rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeRotate, OldKey: oldKey, NewKey: newKey, ToVersion: 1,
	})
	if err != nil {
		t.Fatalf("Sweep rotate: %v", err)
	}
	if rep.RowsRotated != 1 {
		t.Fatalf("want 1 row rotated, got %d", rep.RowsRotated)
	}

	// Both columns now decrypt under NEW, and kek_version is stamped.
	var blob []byte
	var hexStr string
	var ver int16
	if err := pool.QueryRow(ctx,
		`SELECT blob_enc, hex_enc, kek_version FROM sweep_fixture WHERE pk='row-1'`).
		Scan(&blob, &hexStr, &ver); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, err := aes.Decrypt(blob, newKey); err != nil {
		t.Fatalf("blob_enc must decrypt under new key: %v", err)
	}
	hexBytes, _ := hex.DecodeString(hexStr)
	if _, err := aes.Decrypt(hexBytes, newKey); err != nil {
		t.Fatalf("hex_enc must decrypt under new key: %v", err)
	}
	if ver != 1 {
		t.Fatalf("want kek_version=1, got %d", ver)
	}
}

func TestSweep_DryRunDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, containers.Postgres(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oldKey, newKey := key32(0x11), key32(0x22)
	setupTable(t, ctx, pool, oldKey)

	rep, err := rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeDryRun, OldKey: oldKey, NewKey: newKey, ToVersion: 1,
	})
	if err != nil {
		t.Fatalf("Sweep dry-run: %v", err)
	}
	if rep.RowsRotated != 1 {
		t.Fatalf("dry-run should report 1 candidate row, got %d", rep.RowsRotated)
	}
	// Row is untouched: still on OLD, kek_version still NULL.
	var blob []byte
	var ver *int16
	if err := pool.QueryRow(ctx,
		`SELECT blob_enc, kek_version FROM sweep_fixture WHERE pk='row-1'`).
		Scan(&blob, &ver); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, err := aes.Decrypt(blob, oldKey); err != nil {
		t.Fatalf("dry-run must leave the row on the old key: %v", err)
	}
	if ver != nil {
		t.Fatalf("dry-run must not stamp kek_version, got %v", *ver)
	}
}

func TestSweep_Verify(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, containers.Postgres(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oldKey, newKey := key32(0x11), key32(0x22)
	setupTable(t, ctx, pool, oldKey)

	// Before rotation: verify reports 1 row still on old.
	rep, err := rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeVerify, NewKey: newKey,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.RowsOnOldKey != 1 {
		t.Fatalf("want 1 row on old key, got %d", rep.RowsOnOldKey)
	}

	// Rotate, then verify reports 0.
	if _, err := rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeRotate, OldKey: oldKey, NewKey: newKey, ToVersion: 1,
	}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	rep, err = rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeVerify, NewKey: newKey,
	})
	if err != nil {
		t.Fatalf("verify post-rotate: %v", err)
	}
	if rep.RowsOnOldKey != 0 {
		t.Fatalf("want 0 rows on old key after rotation, got %d", rep.RowsOnOldKey)
	}
}

func TestSweep_CorruptCellRollsBack(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, containers.Postgres(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oldKey, newKey := key32(0x11), key32(0x22)
	setupTable(t, ctx, pool, oldKey)
	// Add a second row that does NOT decrypt under oldKey (garbage BYTEA).
	if _, err := pool.Exec(ctx,
		`INSERT INTO sweep_fixture (pk, blob_enc) VALUES ('bad', $1)`,
		[]byte("not-a-valid-ciphertext")); err != nil {
		t.Fatalf("seed bad row: %v", err)
	}

	_, err = rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeRotate, OldKey: oldKey, NewKey: newKey, ToVersion: 1,
	})
	if err == nil {
		t.Fatal("Sweep must fail when a cell does not decrypt under the old key")
	}
	// The good row must be untouched (transaction rolled back): still on OLD.
	var blob []byte
	if err := pool.QueryRow(ctx,
		`SELECT blob_enc FROM sweep_fixture WHERE pk='row-1'`).Scan(&blob); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, derr := aes.Decrypt(blob, oldKey); derr != nil {
		t.Fatal("rollback failed: good row was mutated despite the corrupt sibling row")
	}
}

func TestNextVersion(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, containers.Postgres(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oldKey := key32(0x11)
	setupTable(t, ctx, pool, oldKey)

	// All kek_version NULL → next version is 1.
	v, err := rekey.NextVersion(ctx, pool, fixtureSpecs())
	if err != nil {
		t.Fatalf("NextVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("want next version 1 on fresh table, got %d", v)
	}

	// Stamp version 4, next should be 5.
	if _, err := pool.Exec(ctx, `UPDATE sweep_fixture SET kek_version = 4`); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	v, err = rekey.NextVersion(ctx, pool, fixtureSpecs())
	if err != nil {
		t.Fatalf("NextVersion: %v", err)
	}
	if v != 5 {
		t.Fatalf("want next version 5, got %d", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd libs && go test ./crypto/rekey/ -run TestSweep -v`
Expected: FAIL to compile — `undefined: rekey.TableSpec`, `rekey.Sweep`, etc.

- [ ] **Step 3: Write the sweep engine**

Create `libs/crypto/rekey/sweep.go`:

```go
package rekey

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Encoding describes how a cipher column stores its bytes.
type Encoding int

const (
	// EncodingBytea is a raw BYTEA column (the common case).
	EncodingBytea Encoding = iota
	// EncodingHexText is a TEXT column holding hex-encoded ciphertext
	// (webhook.secret_enc is the only such column in the platform).
	EncodingHexText
)

// CipherColumn identifies one encrypted column and how it is stored.
type CipherColumn struct {
	Name     string
	Encoding Encoding
}

// TableSpec declares everything the sweep needs to rotate one table: its name,
// primary-key column, the tracking column, and its cipher columns. A table may
// carry more than one cipher column (audit_export_configs has two); they all
// rotate together in the table's single transaction.
type TableSpec struct {
	Table         string
	PKColumn      string
	VersionColumn string
	Columns       []CipherColumn
	// Optional marks a table that may not exist in every deployment (the
	// legacy auth_providers table). When true and the table is absent, the
	// sweep logs a skip and moves on instead of erroring.
	Optional bool
}

// Mode selects the sweep behaviour.
type Mode int

const (
	// ModeRotate re-encrypts every candidate row and commits.
	ModeRotate Mode = iota
	// ModeDryRun performs every decrypt/encrypt but rolls back — it reports
	// how many rows would rotate without mutating anything.
	ModeDryRun
	// ModeVerify never mutates; it reports how many rows still fail to
	// decrypt under NewKey (i.e. remain on the old key). Only NewKey is used.
	ModeVerify
)

// SweepOpts carries the keys, mode, and target version for a sweep.
type SweepOpts struct {
	Mode      Mode
	OldKey    []byte // required for ModeRotate/ModeDryRun
	NewKey    []byte // required for all modes
	ToVersion int16  // stamped on rotated rows (ModeRotate)
}

// Report summarises a sweep across all tables.
type Report struct {
	RowsRotated  int            // rows re-encrypted (ModeRotate) or candidate rows (ModeDryRun)
	RowsOnOldKey int            // rows still on the old key (ModeVerify)
	PerTable     map[string]int // table name → row count touched/inspected
}

// Sweep runs the requested mode over every spec. Each table is processed in its
// own transaction (all-or-nothing): if any cell in a table fails to decrypt
// under OldKey, that table's transaction rolls back and Sweep returns the error
// with the offending primary key. Secrets are never logged — only counts and
// primary keys.
func Sweep(ctx context.Context, pool *pgxpool.Pool, specs []TableSpec, opts SweepOpts) (Report, error) {
	rep := Report{PerTable: map[string]int{}}
	for _, spec := range specs {
		if spec.Optional {
			exists, err := tableExists(ctx, pool, spec.Table)
			if err != nil {
				return rep, err
			}
			if !exists {
				slog.Info("rotate-kek: optional table absent, skipping", "table", spec.Table)
				continue
			}
		}
		if opts.Mode == ModeVerify {
			n, err := verifyTable(ctx, pool, spec, opts.NewKey)
			if err != nil {
				return rep, err
			}
			rep.RowsOnOldKey += n
			rep.PerTable[spec.Table] = n
			continue
		}
		n, err := rotateTable(ctx, pool, spec, opts)
		if err != nil {
			return rep, err
		}
		rep.RowsRotated += n
		rep.PerTable[spec.Table] = n
	}
	return rep, nil
}

// tableExists reports whether a table is present in the current database.
func tableExists(ctx context.Context, pool *pgxpool.Pool, table string) (bool, error) {
	var reg *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&reg); err != nil {
		return false, fmt.Errorf("check table %s exists: %w", table, err)
	}
	return reg != nil, nil
}

// selectSQL builds the FOR UPDATE candidate query for a table. It selects the
// PK as text (uniform across UUID and TEXT PKs) plus every cipher column, and
// filters to rows where at least one cipher column is non-null.
func selectSQL(spec TableSpec) string {
	cols := make([]string, len(spec.Columns))
	notNull := make([]string, len(spec.Columns))
	for i, c := range spec.Columns {
		cols[i] = c.Name
		notNull[i] = c.Name + " IS NOT NULL"
	}
	return fmt.Sprintf(
		"SELECT %s::text, %s FROM %s WHERE %s FOR UPDATE",
		spec.PKColumn, strings.Join(cols, ", "), spec.Table, strings.Join(notNull, " OR "),
	)
}

// rotateTable re-encrypts every candidate row in one transaction. In ModeDryRun
// it performs the full decrypt/encrypt but rolls back at the end. Returns the
// number of rows touched.
func rotateTable(ctx context.Context, pool *pgxpool.Pool, spec TableSpec, opts SweepOpts) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx on %s: %w", spec.Table, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after an explicit Commit

	rows, err := tx.Query(ctx, selectSQL(spec))
	if err != nil {
		return 0, fmt.Errorf("select %s: %w", spec.Table, err)
	}

	type update struct {
		pk      string
		newVals [][]byte // len == len(spec.Columns); nil for a NULL cell
	}
	var updates []update

	for rows.Next() {
		// Scan PK (text) + each cipher column as []byte (BYTEA) or string (TEXT).
		dest := make([]any, 1+len(spec.Columns))
		var pk string
		dest[0] = &pk
		rawByte := make([][]byte, len(spec.Columns))
		rawText := make([]string, len(spec.Columns))
		for i, c := range spec.Columns {
			if c.Encoding == EncodingHexText {
				dest[1+i] = &rawText[i]
			} else {
				dest[1+i] = &rawByte[i]
			}
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan %s row: %w", spec.Table, err)
		}

		u := update{pk: pk, newVals: make([][]byte, len(spec.Columns))}
		for i, c := range spec.Columns {
			cell := rawByte[i]
			if c.Encoding == EncodingHexText {
				if rawText[i] == "" {
					continue // NULL/empty cell — leave as-is
				}
				decoded, derr := hex.DecodeString(rawText[i])
				if derr != nil {
					rows.Close()
					return 0, fmt.Errorf("%s.%s pk=%s: not valid hex: %w", spec.Table, c.Name, pk, derr)
				}
				cell = decoded
			}
			if len(cell) == 0 {
				continue // NULL cell
			}
			newCT, rerr := Rekey(opts.OldKey, opts.NewKey, cell)
			if rerr != nil {
				rows.Close()
				return 0, fmt.Errorf("%s.%s pk=%s: %w", spec.Table, c.Name, pk, rerr)
			}
			u.newVals[i] = newCT
		}
		updates = append(updates, u)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate %s: %w", spec.Table, err)
	}
	rows.Close()

	if opts.Mode == ModeDryRun {
		// Everything decrypted/encrypted cleanly; report the count and roll back.
		return len(updates), nil
	}

	for _, u := range updates {
		if err := applyUpdate(ctx, tx, spec, u.pk, u.newVals, opts.ToVersion); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit %s: %w", spec.Table, err)
	}
	slog.Info("rotate-kek: table rotated", "table", spec.Table, "rows", len(updates), "to_version", opts.ToVersion)
	return len(updates), nil
}

// applyUpdate writes the re-encrypted cells + version stamp for one row.
func applyUpdate(ctx context.Context, tx pgx.Tx, spec TableSpec, pk string, newVals [][]byte, toVersion int16) error {
	set := make([]string, 0, len(spec.Columns)+1)
	args := make([]any, 0, len(spec.Columns)+2)
	argN := 1
	for i, c := range spec.Columns {
		if newVals[i] == nil {
			continue // NULL cell was skipped
		}
		if c.Encoding == EncodingHexText {
			set = append(set, fmt.Sprintf("%s = $%d", c.Name, argN))
			args = append(args, hex.EncodeToString(newVals[i]))
		} else {
			set = append(set, fmt.Sprintf("%s = $%d", c.Name, argN))
			args = append(args, newVals[i])
		}
		argN++
	}
	set = append(set, fmt.Sprintf("%s = $%d", spec.VersionColumn, argN))
	args = append(args, toVersion)
	argN++
	args = append(args, pk)
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE %s::text = $%d",
		spec.Table, strings.Join(set, ", "), spec.PKColumn, argN)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("update %s pk=%s: %w", spec.Table, pk, err)
	}
	return nil
}

// verifyTable counts rows with at least one cipher cell that does not decrypt
// under newKey (i.e. still on the old key). It never mutates.
func verifyTable(ctx context.Context, pool *pgxpool.Pool, spec TableSpec, newKey []byte) (int, error) {
	rows, err := pool.Query(ctx, selectSQL(spec)+"") // reuse candidate select (FOR UPDATE is harmless here)
	if err != nil {
		return 0, fmt.Errorf("verify select %s: %w", spec.Table, err)
	}
	defer rows.Close()

	remaining := 0
	for rows.Next() {
		dest := make([]any, 1+len(spec.Columns))
		var pk string
		dest[0] = &pk
		rawByte := make([][]byte, len(spec.Columns))
		rawText := make([]string, len(spec.Columns))
		for i, c := range spec.Columns {
			if c.Encoding == EncodingHexText {
				dest[1+i] = &rawText[i]
			} else {
				dest[1+i] = &rawByte[i]
			}
		}
		if err := rows.Scan(dest...); err != nil {
			return 0, fmt.Errorf("verify scan %s: %w", spec.Table, err)
		}
		onOld := false
		for i, c := range spec.Columns {
			cell := rawByte[i]
			if c.Encoding == EncodingHexText {
				if rawText[i] == "" {
					continue
				}
				decoded, derr := hex.DecodeString(rawText[i])
				if derr != nil {
					onOld = true // undecodable ⇒ definitely not on the new key
					break
				}
				cell = decoded
			}
			if len(cell) == 0 {
				continue
			}
			if !OnNewKey(newKey, cell) {
				onOld = true
				break
			}
		}
		if onOld {
			remaining++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("verify iterate %s: %w", spec.Table, err)
	}
	return remaining, nil
}

// NextVersion returns 1 + the maximum kek_version across all non-optional
// specs' tables (treating NULL as 0), so one rotation run stamps a single
// generation. Fresh tables (all NULL) yield 1.
func NextVersion(ctx context.Context, pool *pgxpool.Pool, specs []TableSpec) (int16, error) {
	var maxVer int16
	for _, spec := range specs {
		if spec.Optional {
			exists, err := tableExists(ctx, pool, spec.Table)
			if err != nil {
				return 0, err
			}
			if !exists {
				continue
			}
		}
		var v int16
		q := fmt.Sprintf("SELECT COALESCE(MAX(%s), 0) FROM %s", spec.VersionColumn, spec.Table)
		if err := pool.QueryRow(ctx, q).Scan(&v); err != nil {
			return 0, fmt.Errorf("max version %s: %w", spec.Table, err)
		}
		if v > maxVer {
			maxVer = v
		}
	}
	return maxVer + 1, nil
}
```

- [ ] **Step 4: Write the CLI runner**

Create `libs/crypto/rekey/cli.go`:

```go
package rekey

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ValidationError signals operator-input problems (bad flags, missing/invalid
// keys). The service main.go dispatch maps it to exit code 2.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func validationError(format string, a ...any) *ValidationError {
	return &ValidationError{msg: fmt.Sprintf(format, a...)}
}

// ErrRowsRemain is returned by RunCLI in --verify mode when at least one row is
// still on the old key. The dispatch maps it to exit code 3 for scripting.
var ErrRowsRemain = errors.New("rows still on the old key")

// RunCLI implements the shared `rotate-kek` subcommand body. Each service calls
// it with its own DSN environment-variable name and TableSpecs.
//
//	args      — os.Args[2:] (everything after "rotate-kek")
//	dsnEnv    — name of the env var holding the service DSN (e.g. "DB_DSN")
//	specs     — the service's table specs
//	stdout    — where human-readable output is written
//
// Modes (mutually exclusive flags; default is rotate):
//
//	--generate       mint + print a fresh 32-byte hex KEK, then exit (no DB)
//	--dry-run        report candidate counts without mutating
//	--verify         report rows still on the old key (exit 3 if any remain)
//	--to-version N   override the stamped generation (default: max+1)
//
// Keys come from the environment, never flags (avoids shell-history leakage):
//
//	KEK_OLD_HEX  required for rotate + dry-run
//	KEK_NEW_HEX  required for rotate + dry-run + verify
func RunCLI(ctx context.Context, args []string, dsnEnv string, specs []TableSpec, stdout io.Writer) error {
	fs := flag.NewFlagSet("rotate-kek", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "report counts without mutating")
	verify := fs.Bool("verify", false, "report rows still on the old key")
	generate := fs.Bool("generate", false, "mint and print a fresh 32-byte hex KEK, then exit")
	toVersion := fs.Int("to-version", 0, "override the stamped kek_version (default: max+1)")
	if err := fs.Parse(args); err != nil {
		return validationError("parse flags: %v", err)
	}

	if *generate {
		h, err := GenerateKeyHex()
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		fmt.Fprintln(stdout, h)
		return nil
	}
	if *dryRun && *verify {
		return validationError("--dry-run and --verify are mutually exclusive")
	}

	// New key is always needed (rotate, dry-run, verify all reference it).
	newKey, err := ParseKeyHex(os.Getenv("KEK_NEW_HEX"))
	if err != nil {
		return validationError("KEK_NEW_HEX: %v", err)
	}
	var oldKey []byte
	if !*verify {
		oldKey, err = ParseKeyHex(os.Getenv("KEK_OLD_HEX"))
		if err != nil {
			return validationError("KEK_OLD_HEX: %v", err)
		}
	}

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		return validationError("%s environment variable is required", dsnEnv)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect DB: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping DB: %w", err)
	}

	switch {
	case *verify:
		rep, err := Sweep(ctx, pool, specs, SweepOpts{Mode: ModeVerify, NewKey: newKey})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "verify: %d row(s) still on the old key\n", rep.RowsOnOldKey)
		for tbl, n := range rep.PerTable {
			fmt.Fprintf(stdout, "  %s: %d on old key\n", tbl, n)
		}
		if rep.RowsOnOldKey > 0 {
			return ErrRowsRemain
		}
		return nil

	default: // rotate or dry-run
		ver := int16(*toVersion)
		if ver == 0 {
			ver, err = NextVersion(ctx, pool, specs)
			if err != nil {
				return err
			}
		}
		mode := ModeRotate
		label := "rotated"
		if *dryRun {
			mode = ModeDryRun
			label = "would rotate"
		}
		rep, err := Sweep(ctx, pool, specs, SweepOpts{
			Mode: mode, OldKey: oldKey, NewKey: newKey, ToVersion: ver,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s %d row(s) to kek_version %d\n", label, rep.RowsRotated, ver)
		for tbl, n := range rep.PerTable {
			fmt.Fprintf(stdout, "  %s: %d\n", tbl, n)
		}
		return nil
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd libs && go test ./crypto/rekey/ -v`
Expected: PASS (Task 1 + Task 2 tests). Note: the sweep tests need Docker running for testcontainers.

- [ ] **Step 6: Vet + commit**

```bash
cd libs && go vet ./crypto/rekey/ && cd ..
git add libs/crypto/rekey/sweep.go libs/crypto/rekey/cli.go libs/crypto/rekey/sweep_test.go
git commit -m "feat(rekey): declarative sweep engine + rotate-kek CLI runner (RED-FU-015)"
```

---

## Task 3: proxy `rotate-kek` subcommand

**Files:**
- Create: `services/proxy/migrations/20260703120000_add_kek_version_upstream_registries.sql`
- Create: `services/proxy/internal/rotatekek/rotatekek.go`
- Create: `services/proxy/internal/rotatekek/rotatekek_test.go`
- Modify: `services/proxy/cmd/server/main.go`

- [ ] **Step 1: Write the migration**

Create `services/proxy/migrations/20260703120000_add_kek_version_upstream_registries.sql`:

```sql
-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted a row.
-- Nullable — NULL means "never rotated" (original bootstrap key). The rotate-kek
-- subcommand stamps this on every re-encrypted row; trial-decryption remains the
-- authoritative verify.
ALTER TABLE upstream_registries ADD COLUMN kek_version SMALLINT;

-- +goose Down
ALTER TABLE upstream_registries DROP COLUMN kek_version;
```

- [ ] **Step 2: Write the failing integration test**

Create `services/proxy/internal/rotatekek/rotatekek_test.go`:

```go
package rotatekek

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// newTestDB spins up Postgres and creates the proxy table with the kek_version
// column already present (mirrors post-migration schema).
func newTestDB(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE upstream_registries (
			upstream_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id    UUID NOT NULL,
			name         TEXT NOT NULL,
			password_enc BYTEA,
			kek_version  SMALLINT
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return ctx, pool, dsn
}

func TestRotate_ProxyPassword(t *testing.T) {
	ctx, pool, dsn := newTestDB(t)
	oldKey, newKey := key32(0x11), key32(0x22)

	ct, _ := aes.Encrypt([]byte("upstream-pass"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO upstream_registries (tenant_id, name, password_enc)
		 VALUES (gen_random_uuid(), 'docker.io', $1)`, ct); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv("DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, nil, &out); err != nil {
		t.Fatalf("Run rotate: %v", err)
	}

	var enc []byte
	var ver int16
	if err := pool.QueryRow(ctx,
		`SELECT password_enc, kek_version FROM upstream_registries`).Scan(&enc, &ver); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, err := aes.Decrypt(enc, newKey); err != nil {
		t.Fatalf("password_enc must decrypt under new key: %v", err)
	}
	if ver != 1 {
		t.Fatalf("want kek_version 1, got %d", ver)
	}
	_ = os.Environ // keep import used if trimmed
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd services/proxy && go test ./internal/rotatekek/ -v`
Expected: FAIL to compile — `undefined: Run`.

- [ ] **Step 4: Write the subcommand**

Create `services/proxy/internal/rotatekek/rotatekek.go`:

```go
// Package rotatekek implements registry-proxy's `rotate-kek` subcommand.
// It re-encrypts upstream_registries.password_enc from KEK_OLD_HEX to
// KEK_NEW_HEX (RED-FU-015). All table/column knowledge lives here; the sweep
// engine and CLI plumbing live in libs/crypto/rekey.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs declares the proxy schema's KEK-encrypted columns.
func specs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "upstream_registries",
		PKColumn:      "upstream_id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "password_enc", Encoding: rekey.EncodingBytea},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. The proxy DSN comes
// from DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "DB_DSN", specs(), stdout)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd services/proxy && go test ./internal/rotatekek/ -v`
Expected: PASS (Docker required).

- [ ] **Step 6: Wire the dispatch in main.go**

In `services/proxy/cmd/server/main.go`, add the import and a dispatch block immediately after the existing imports/at the top of `main()`, before config load. Mirror the auth `bootstrap` dispatch pattern.

Add to imports:

```go
	"github.com/steveokay/oci-janus/services/proxy/internal/rotatekek"
```

Add as the first statement inside `main()` (before OTEL/config):

```go
	// rotate-kek subcommand (RED-FU-015). Dispatched before config load so the
	// KEK rotation CLI does not require the full server environment.
	if len(os.Args) > 1 && os.Args[1] == "rotate-kek" {
		if err := rotatekek.Run(context.Background(), os.Args[2:], os.Stdout); err != nil {
			var verr *rekey.ValidationError
			if errors.As(err, &verr) {
				slog.Error("rotate-kek validation error", "err", err)
				os.Exit(2)
			}
			if errors.Is(err, rekey.ErrRowsRemain) {
				slog.Error("rotate-kek verify: rows remain on the old key", "err", err)
				os.Exit(3)
			}
			slog.Error("rotate-kek failed", "err", err)
			os.Exit(1)
		}
		return
	}
```

Ensure these imports exist in the file (add any missing): `context`, `errors`, `log/slog`, `os`, and `"github.com/steveokay/oci-janus/libs/crypto/rekey"`.

- [ ] **Step 7: Build + vet**

Run: `cd services/proxy && go build ./... && go vet ./...`
Expected: builds cleanly.

- [ ] **Step 8: Commit**

```bash
git add services/proxy/migrations/20260703120000_add_kek_version_upstream_registries.sql \
        services/proxy/internal/rotatekek/ services/proxy/cmd/server/main.go
git commit -m "feat(proxy): rotate-kek subcommand + kek_version column (RED-FU-015)"
```

---

## Task 4: webhook `rotate-kek` subcommand (hex-TEXT column)

**Files:**
- Create: `services/webhook/migrations/20260703120100_add_kek_version_webhook_endpoints.sql`
- Create: `services/webhook/internal/rotatekek/rotatekek.go`
- Create: `services/webhook/internal/rotatekek/rotatekek_test.go`
- Modify: `services/webhook/cmd/server/main.go`

- [ ] **Step 1: Write the migration**

Create `services/webhook/migrations/20260703120100_add_kek_version_webhook_endpoints.sql`:

```sql
-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted
-- webhook_endpoints.secret_enc (stored as hex TEXT, not BYTEA).
ALTER TABLE webhook_endpoints ADD COLUMN kek_version SMALLINT;

-- +goose Down
ALTER TABLE webhook_endpoints DROP COLUMN kek_version;
```

- [ ] **Step 2: Write the failing integration test**

Create `services/webhook/internal/rotatekek/rotatekek_test.go`. The key difference from proxy: `secret_enc` is hex-encoded TEXT, so the test seeds `hex.EncodeToString(ct)` and reads it back hex-decoded before decrypting.

```go
package rotatekek

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func newTestDB(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE webhook_endpoints (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id   UUID NOT NULL,
			url         TEXT NOT NULL,
			secret_enc  TEXT NOT NULL,
			kek_version SMALLINT
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return ctx, pool, dsn
}

func TestRotate_WebhookHexSecret(t *testing.T) {
	ctx, pool, dsn := newTestDB(t)
	oldKey, newKey := key32(0x11), key32(0x22)

	ct, _ := aes.Encrypt([]byte("hmac-key"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO webhook_endpoints (tenant_id, url, secret_enc)
		 VALUES (gen_random_uuid(), 'https://x', $1)`, hex.EncodeToString(ct)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv("DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, nil, &out); err != nil {
		t.Fatalf("Run rotate: %v", err)
	}

	var secretHex string
	var ver int16
	if err := pool.QueryRow(ctx,
		`SELECT secret_enc, kek_version FROM webhook_endpoints`).Scan(&secretHex, &ver); err != nil {
		t.Fatalf("read back: %v", err)
	}
	raw, err := hex.DecodeString(secretHex)
	if err != nil {
		t.Fatalf("secret_enc must stay valid hex: %v", err)
	}
	if _, err := aes.Decrypt(raw, newKey); err != nil {
		t.Fatalf("secret must decrypt under new key: %v", err)
	}
	if ver != 1 {
		t.Fatalf("want kek_version 1, got %d", ver)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd services/webhook && go test ./internal/rotatekek/ -v`
Expected: FAIL to compile — `undefined: Run`.

- [ ] **Step 4: Write the subcommand**

Create `services/webhook/internal/rotatekek/rotatekek.go`:

```go
// Package rotatekek implements registry-webhook's `rotate-kek` subcommand.
// It re-encrypts webhook_endpoints.secret_enc from KEK_OLD_HEX to KEK_NEW_HEX
// (RED-FU-015). secret_enc is stored as hex-encoded TEXT (not BYTEA), so this
// column is declared with EncodingHexText — the sweep hex-decodes before
// decrypt and hex-encodes after encrypt.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

func specs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "webhook_endpoints",
		PKColumn:      "id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "secret_enc", Encoding: rekey.EncodingHexText},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "DB_DSN", specs(), stdout)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd services/webhook && go test ./internal/rotatekek/ -v`
Expected: PASS.

- [ ] **Step 6: Wire the dispatch in main.go**

Add the import `"github.com/steveokay/oci-janus/services/webhook/internal/rotatekek"` and `"github.com/steveokay/oci-janus/libs/crypto/rekey"` (plus `context`, `errors`, `log/slog`, `os` if missing), and insert the same dispatch block as Task 3 Step 6 at the top of `main()`, swapping `rotatekek.Run` for the webhook package's `Run`:

```go
	if len(os.Args) > 1 && os.Args[1] == "rotate-kek" {
		if err := rotatekek.Run(context.Background(), os.Args[2:], os.Stdout); err != nil {
			var verr *rekey.ValidationError
			if errors.As(err, &verr) {
				slog.Error("rotate-kek validation error", "err", err)
				os.Exit(2)
			}
			if errors.Is(err, rekey.ErrRowsRemain) {
				slog.Error("rotate-kek verify: rows remain on the old key", "err", err)
				os.Exit(3)
			}
			slog.Error("rotate-kek failed", "err", err)
			os.Exit(1)
		}
		return
	}
```

- [ ] **Step 7: Build + vet**

Run: `cd services/webhook && go build ./... && go vet ./...`
Expected: builds cleanly.

- [ ] **Step 8: Commit**

```bash
git add services/webhook/migrations/20260703120100_add_kek_version_webhook_endpoints.sql \
        services/webhook/internal/rotatekek/ services/webhook/cmd/server/main.go
git commit -m "feat(webhook): rotate-kek subcommand + kek_version column, hex-TEXT aware (RED-FU-015)"
```

---

## Task 5: audit `rotate-kek` subcommand (two columns, one table)

**Files:**
- Create: `services/audit/migrations/20260703120200_add_kek_version_audit_export_configs.sql`
- Create: `services/audit/internal/rotatekek/rotatekek.go`
- Create: `services/audit/internal/rotatekek/rotatekek_test.go`
- Modify: `services/audit/cmd/server/main.go`

- [ ] **Step 1: Write the migration**

Create `services/audit/migrations/20260703120200_add_kek_version_audit_export_configs.sql`:

```sql
-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted the
-- two secret columns (hmac_secret, bearer_token) on audit_export_configs. Both
-- columns rotate together in one transaction, so a single tracking column suffices.
ALTER TABLE audit_export_configs ADD COLUMN kek_version SMALLINT;

-- +goose Down
ALTER TABLE audit_export_configs DROP COLUMN kek_version;
```

- [ ] **Step 2: Write the failing integration test**

Create `services/audit/internal/rotatekek/rotatekek_test.go`. It seeds both cipher columns under OLD and asserts both decrypt under NEW after rotation.

```go
package rotatekek

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func newTestDB(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE audit_export_configs (
			id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id   UUID NOT NULL UNIQUE,
			hmac_secret BYTEA,
			bearer_token BYTEA,
			kek_version SMALLINT
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return ctx, pool, dsn
}

func TestRotate_AuditTwoColumns(t *testing.T) {
	ctx, pool, dsn := newTestDB(t)
	oldKey, newKey := key32(0x11), key32(0x22)

	hmacCT, _ := aes.Encrypt([]byte("hmac-secret"), oldKey)
	bearerCT, _ := aes.Encrypt([]byte("bearer-token"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_export_configs (tenant_id, hmac_secret, bearer_token)
		 VALUES (gen_random_uuid(), $1, $2)`, hmacCT, bearerCT); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv("DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, nil, &out); err != nil {
		t.Fatalf("Run rotate: %v", err)
	}

	var hmacEnc, bearerEnc []byte
	var ver int16
	if err := pool.QueryRow(ctx,
		`SELECT hmac_secret, bearer_token, kek_version FROM audit_export_configs`).
		Scan(&hmacEnc, &bearerEnc, &ver); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, err := aes.Decrypt(hmacEnc, newKey); err != nil {
		t.Fatalf("hmac_secret must decrypt under new key: %v", err)
	}
	if _, err := aes.Decrypt(bearerEnc, newKey); err != nil {
		t.Fatalf("bearer_token must decrypt under new key: %v", err)
	}
	if ver != 1 {
		t.Fatalf("want kek_version 1, got %d", ver)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd services/audit && go test ./internal/rotatekek/ -v`
Expected: FAIL to compile — `undefined: Run`.

- [ ] **Step 4: Write the subcommand**

Create `services/audit/internal/rotatekek/rotatekek.go`:

```go
// Package rotatekek implements registry-audit's `rotate-kek` subcommand.
// audit_export_configs carries two KEK-encrypted BYTEA columns (hmac_secret,
// bearer_token); both are declared on one TableSpec so they rotate together in
// the table's single transaction (RED-FU-015).
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

func specs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "audit_export_configs",
		PKColumn:      "id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "hmac_secret", Encoding: rekey.EncodingBytea},
			{Name: "bearer_token", Encoding: rekey.EncodingBytea},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "DB_DSN", specs(), stdout)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd services/audit && go test ./internal/rotatekek/ -v`
Expected: PASS.

- [ ] **Step 6: Wire the dispatch in main.go**

Add imports `"github.com/steveokay/oci-janus/services/audit/internal/rotatekek"`, `"github.com/steveokay/oci-janus/libs/crypto/rekey"` (plus `context`, `errors`, `log/slog`, `os` if missing) and insert the dispatch block from Task 3 Step 6 at the top of `main()`.

```go
	if len(os.Args) > 1 && os.Args[1] == "rotate-kek" {
		if err := rotatekek.Run(context.Background(), os.Args[2:], os.Stdout); err != nil {
			var verr *rekey.ValidationError
			if errors.As(err, &verr) {
				slog.Error("rotate-kek validation error", "err", err)
				os.Exit(2)
			}
			if errors.Is(err, rekey.ErrRowsRemain) {
				slog.Error("rotate-kek verify: rows remain on the old key", "err", err)
				os.Exit(3)
			}
			slog.Error("rotate-kek failed", "err", err)
			os.Exit(1)
		}
		return
	}
```

- [ ] **Step 7: Build + vet**

Run: `cd services/audit && go build ./... && go vet ./...`
Expected: builds cleanly.

- [ ] **Step 8: Commit**

```bash
git add services/audit/migrations/20260703120200_add_kek_version_audit_export_configs.sql \
        services/audit/internal/rotatekek/ services/audit/cmd/server/main.go
git commit -m "feat(audit): rotate-kek subcommand + kek_version column, dual-column (RED-FU-015)"
```

---

## Task 6: auth `rotate-kek` subcommand (current + legacy tables)

**Files:**
- Create: `services/auth/migrations/20260703120300_add_kek_version_sso_config.sql`
- Create: `services/auth/internal/rotatekek/rotatekek.go`
- Create: `services/auth/internal/rotatekek/rotatekek_test.go`
- Modify: `services/auth/cmd/server/main.go`

- [ ] **Step 1: Write the migration**

Create `services/auth/migrations/20260703120300_add_kek_version_sso_config.sql`. It adds `kek_version` to `global_sso_config` unconditionally and to the legacy `auth_providers` only if that table is present (some deployments never had it).

```sql
-- +goose Up
-- RED-FU-015: kek_version tracks which KEK generation last re-encrypted
-- oauth_client_secret_enc. global_sso_config is current; auth_providers is the
-- legacy table (added conditionally — not every deployment has it).
ALTER TABLE global_sso_config ADD COLUMN kek_version SMALLINT;

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.auth_providers') IS NOT NULL THEN
        ALTER TABLE auth_providers ADD COLUMN IF NOT EXISTS kek_version SMALLINT;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE global_sso_config DROP COLUMN kek_version;

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.auth_providers') IS NOT NULL THEN
        ALTER TABLE auth_providers DROP COLUMN IF EXISTS kek_version;
    END IF;
END
$$;
-- +goose StatementEnd
```

- [ ] **Step 2: Write the failing integration test**

Create `services/auth/internal/rotatekek/rotatekek_test.go`. It covers the current table (TEXT PK `provider_id`), the legacy table present with a row, and verify mode. Auth's DSN env var is `AUTH_DB_DSN`.

```go
package rotatekek

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/crypto/rekey"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// newTestDB creates both the current and legacy SSO tables (TEXT PK on the
// current one) with kek_version columns present.
func newTestDB(t *testing.T) (context.Context, *pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE global_sso_config (
			provider_id             TEXT PRIMARY KEY,
			oauth_client_secret_enc BYTEA,
			kek_version             SMALLINT
		);
		CREATE TABLE auth_providers (
			id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id               UUID NOT NULL,
			oauth_client_secret_enc BYTEA,
			kek_version             SMALLINT
		)`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return ctx, pool, dsn
}

func TestRotate_AuthCurrentAndLegacy(t *testing.T) {
	ctx, pool, dsn := newTestDB(t)
	oldKey, newKey := key32(0x11), key32(0x22)

	curCT, _ := aes.Encrypt([]byte("google-secret"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO global_sso_config (provider_id, oauth_client_secret_enc)
		 VALUES ('google', $1)`, curCT); err != nil {
		t.Fatalf("seed current: %v", err)
	}
	legCT, _ := aes.Encrypt([]byte("legacy-secret"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth_providers (tenant_id, oauth_client_secret_enc)
		 VALUES (gen_random_uuid(), $1)`, legCT); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	t.Setenv("AUTH_DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, nil, &out); err != nil {
		t.Fatalf("Run rotate: %v", err)
	}

	// Current table decrypts under NEW.
	var curEnc []byte
	if err := pool.QueryRow(ctx,
		`SELECT oauth_client_secret_enc FROM global_sso_config WHERE provider_id='google'`).
		Scan(&curEnc); err != nil {
		t.Fatalf("read current: %v", err)
	}
	if _, err := aes.Decrypt(curEnc, newKey); err != nil {
		t.Fatalf("current secret must decrypt under new key: %v", err)
	}
	// Legacy table decrypts under NEW.
	var legEnc []byte
	if err := pool.QueryRow(ctx,
		`SELECT oauth_client_secret_enc FROM auth_providers`).Scan(&legEnc); err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if _, err := aes.Decrypt(legEnc, newKey); err != nil {
		t.Fatalf("legacy secret must decrypt under new key: %v", err)
	}

	// Verify mode reports 0 remaining and returns nil (no ErrRowsRemain).
	var vout bytes.Buffer
	if err := Run(ctx, []string{"--verify"}, &vout); err != nil {
		t.Fatalf("verify should succeed with 0 remaining: %v", err)
	}

	// Sanity: verify on a fresh row still on OLD returns ErrRowsRemain.
	if _, err := pool.Exec(ctx,
		`INSERT INTO global_sso_config (provider_id, oauth_client_secret_enc)
		 VALUES ('github', $1)`, curCT); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	var vout2 bytes.Buffer
	if err := Run(ctx, []string{"--verify"}, &vout2); !errors.Is(err, rekey.ErrRowsRemain) {
		t.Fatalf("verify must return ErrRowsRemain when a row is on the old key, got %v", err)
	}
}

func TestRotate_LegacyAbsentSkips(t *testing.T) {
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// Only the current table exists — auth_providers absent.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE global_sso_config (
			provider_id             TEXT PRIMARY KEY,
			oauth_client_secret_enc BYTEA,
			kek_version             SMALLINT
		)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	oldKey, newKey := key32(0x11), key32(0x22)
	ct, _ := aes.Encrypt([]byte("s"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO global_sso_config (provider_id, oauth_client_secret_enc) VALUES ('google', $1)`, ct); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv("AUTH_DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, nil, &out); err != nil {
		t.Fatalf("Run must skip the absent legacy table cleanly: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd services/auth && go test ./internal/rotatekek/ -v`
Expected: FAIL to compile — `undefined: Run`.

- [ ] **Step 4: Write the subcommand**

Create `services/auth/internal/rotatekek/rotatekek.go`:

```go
// Package rotatekek implements registry-auth's `rotate-kek` subcommand.
// It re-encrypts oauth_client_secret_enc on the current global_sso_config table
// and, when present, the legacy auth_providers table (RED-FU-015). auth's DSN
// comes from AUTH_DB_DSN. global_sso_config has a TEXT primary key
// (provider_id) — handled uniformly by the sweep's ::text PK casting.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

func specs() []rekey.TableSpec {
	return []rekey.TableSpec{
		{
			Table:         "global_sso_config",
			PKColumn:      "provider_id",
			VersionColumn: "kek_version",
			Columns: []rekey.CipherColumn{
				{Name: "oauth_client_secret_enc", Encoding: rekey.EncodingBytea},
			},
		},
		{
			Table:         "auth_providers",
			PKColumn:      "id",
			VersionColumn: "kek_version",
			Columns: []rekey.CipherColumn{
				{Name: "oauth_client_secret_enc", Encoding: rekey.EncodingBytea},
			},
			Optional: true, // legacy — skip cleanly if the table is absent
		},
	}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from
// AUTH_DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "AUTH_DB_DSN", specs(), stdout)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd services/auth && go test ./internal/rotatekek/ -v`
Expected: PASS.

- [ ] **Step 6: Wire the dispatch in main.go**

`services/auth/cmd/server/main.go` already dispatches `bootstrap`. Add a sibling `rotate-kek` block right after the existing bootstrap block, and add imports `"github.com/steveokay/oci-janus/services/auth/internal/rotatekek"` and `"github.com/steveokay/oci-janus/libs/crypto/rekey"` (`context`, `errors`, `log/slog`, `os` already present):

```go
	if len(os.Args) > 1 && os.Args[1] == "rotate-kek" {
		if err := rotatekek.Run(context.Background(), os.Args[2:], os.Stdout); err != nil {
			var verr *rekey.ValidationError
			if errors.As(err, &verr) {
				slog.Error("rotate-kek validation error", "err", err)
				os.Exit(2)
			}
			if errors.Is(err, rekey.ErrRowsRemain) {
				slog.Error("rotate-kek verify: rows remain on the old key", "err", err)
				os.Exit(3)
			}
			slog.Error("rotate-kek failed", "err", err)
			os.Exit(1)
		}
		return
	}
```

- [ ] **Step 7: Build + vet**

Run: `cd services/auth && go build ./... && go vet ./...`
Expected: builds cleanly.

- [ ] **Step 8: Commit**

```bash
git add services/auth/migrations/20260703120300_add_kek_version_sso_config.sql \
        services/auth/internal/rotatekek/ services/auth/cmd/server/main.go
git commit -m "feat(auth): rotate-kek subcommand + kek_version column, current + legacy SSO (RED-FU-015)"
```

---

## Task 7: Operator runbook

**Files:**
- Create: `infra/runbooks/kek-rotation.md`

- [ ] **Step 1: Write the runbook**

Create `infra/runbooks/kek-rotation.md`:

```markdown
# Runbook — KEK Rotation (RED-FU-015)

Rotate a per-service key-encryption key (KEK) that protects secrets at rest.
Four services own an independent KEK; rotate each one separately. There is **no
single master KEK**.

| Service | Secrets protected | DSN env | KEK env (runtime) |
|---|---|---|---|
| registry-auth | SSO OAuth client secrets | `AUTH_DB_DSN` | `SSO_CREDENTIAL_KEY_HEX` |
| registry-proxy | upstream registry passwords | `DB_DSN` | `CREDENTIAL_KEY_HEX` |
| registry-webhook | webhook HMAC keys | `DB_DSN` | `CREDENTIAL_KEY_HEX` |
| registry-audit | export HMAC secret + bearer token | `DB_DSN` | `AUDIT_EXPORT_SECRETS_KEY_HEX` |

> `CREDENTIAL_KEY_HEX` is the *same variable name* in proxy and webhook but a
> **different value per deployment**. Rotating one does not affect the other.

The `rotate-kek` subcommand reads the OLD and NEW keys from `KEK_OLD_HEX` /
`KEK_NEW_HEX` (never flags — avoids shell-history leakage). Keys are 32-byte
AES-256 keys, hex-encoded (64 hex chars).

## 1. Mint a new key

```bash
<service> rotate-kek --generate
# prints one 64-char hex string — store it in your secrets manager
```

## 2. Pre-flight (no mutation)

```bash
KEK_OLD_HEX=<old> KEK_NEW_HEX=<new> <service> rotate-kek --dry-run
# "would rotate N row(s) to kek_version V"
```

## 3. Stop the service

Rotation runs in a brief maintenance window (tables hold tens of rows — seconds).
Stopping the service ensures no writer encrypts a fresh row under the OLD key
after the sweep has passed it.

## 4. Rotate

```bash
KEK_OLD_HEX=<old> KEK_NEW_HEX=<new> <service> rotate-kek
# "rotated N row(s) to kek_version V"
```

Rotation is **per-table all-or-nothing**: if any cell fails to decrypt under the
OLD key, the transaction rolls back and the tool exits non-zero with the
offending primary key. Nothing is left half-rotated.

## 5. Verify

```bash
KEK_NEW_HEX=<new> <service> rotate-kek --verify
# "verify: 0 row(s) still on the old key"   → exit 0
# "verify: N row(s) still on the old key"   → exit 3
```

Verify trial-decrypts every row under the NEW key — the authoritative "done"
check. Exit 3 means rows remain; investigate before restarting.

## 6. Restart with the NEW key

Set the service's runtime KEK env var (see table) to the NEW key and start the
service. Confirm secrets decrypt (e.g. an SSO login for auth, an upstream pull
for proxy, a webhook delivery for webhook, an audit export for audit).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success (or verify: all rows on the new key) |
| 1 | infrastructure/internal error (DB unreachable, etc.) |
| 2 | operator input error (bad flags, missing/invalid `KEK_*_HEX`) |
| 3 | verify only: rows still on the old key |

## Ordering & rollback

- Rotate services in any order — each is independent.
- **Rollback:** if step 6 shows secrets failing to decrypt, restart with the OLD
  key still set and re-run `rotate-kek` with OLD and NEW swapped to reverse the
  sweep (the tool is symmetric).
- Zero-downtime dual-key rotation is a documented future upgrade path (spec §9);
  the `kek_version` column and `rekey` helper are forward-compatible with it.
```

- [ ] **Step 2: Commit**

```bash
git add infra/runbooks/kek-rotation.md
git commit -m "docs(runbook): KEK rotation operator procedure (RED-FU-015)"
```

---

## Task 8: Tracker + .env.example updates

**Files:**
- Modify: `futures.md` (RED-FU-015 → done, corrected-scope note)
- Modify: `status.md` (prepend a row)
- Modify: `services/{auth,proxy,webhook,audit}/.env.example` (document subcommand env vars)

- [ ] **Step 1: Mark RED-FU-015 done in futures.md**

Find the RED-FU-015 entry in `futures.md` and update its status to done with a one-line corrected-scope note: "Shipped as a per-service `rotate-kek` subcommand across auth/proxy/webhook/audit (4 KEKs, not one master key); signer keys out of scope (Vault/KMS). See `docs/superpowers/plans/2026-07-03-kek-rotation.md`."

Run first to locate the exact line:

Run: `grep -n "RED-FU-015" futures.md`

Then edit that entry's status/notes columns to reflect completion.

- [ ] **Step 2: Prepend a status.md row**

Add a row at the top of `status.md`'s log (match the existing row format in that file — inspect the first data row first):

Run: `sed -n '1,20p' status.md`

Add: RED-FU-015 KEK rotation tool — shipped `rotate-kek` subcommand + `libs/crypto/rekey` + `kek_version` columns across auth/proxy/webhook/audit, 2026-07-03.

- [ ] **Step 3: Document env vars in each .env.example**

Append to each of `services/auth/.env.example`, `services/proxy/.env.example`, `services/webhook/.env.example`, `services/audit/.env.example`:

```
# --- rotate-kek subcommand only (RED-FU-015); not read by the server ---
# 32-byte AES-256 keys, hex-encoded (64 hex chars). Supplied at rotation time.
# KEK_OLD_HEX=
# KEK_NEW_HEX=
```

- [ ] **Step 4: Commit**

```bash
git add futures.md status.md services/*/.env.example
git commit -m "chore(trackers): RED-FU-015 KEK rotation shipped; document rotate-kek env vars"
```

---

## Task 9: Full-suite verification before push

- [ ] **Step 1: Run affected module tests**

Run each affected module's tests (Docker must be running for the testcontainers integration tests):

```bash
cd libs && go test ./crypto/rekey/... && cd ..
cd services/proxy && go test ./... && cd ../..
cd services/webhook && go test ./... && cd ../..
cd services/audit && go test ./... && cd ../..
cd services/auth && go test ./... && cd ../..
```
Expected: all PASS.

- [ ] **Step 2: Lint + build the changed services (CLAUDE.md §15.2)**

Run each changed service's Makefile target (or `make build && make test && make lint` at the root):

```bash
make -C services/proxy build test lint
make -C services/webhook build test lint
make -C services/audit build test lint
make -C services/auth build test lint
```
Expected: green. Fix any lint error in touched files inline (the gate is "CI is green," not "my diff is clean").

- [ ] **Step 3: Confirm migrations apply cleanly**

Bring up a dev stack (or a throwaway Postgres) and run each service's goose migrations up then down then up, confirming the `kek_version` column is added and dropped without error. If the repo has a `make migrate` per service, use it; otherwise apply the four migration files against a scratch DB.

- [ ] **Step 4: Open the PR**

```bash
git push -u origin feat/red-fu-015-kek-rotation
gh pr create --title "feat: KEK rotation tool (RED-FU-015)" --body "$(cat <<'EOF'
Implements RED-FU-015 per docs/superpowers/plans/2026-07-03-kek-rotation.md.

- `libs/crypto/rekey`: Rekey/OnNewKey crypto core + declarative sweep engine + rotate-kek CLI runner
- `rotate-kek` subcommand in auth/proxy/webhook/audit (mirrors `bootstrap`)
- `kek_version SMALLINT` tracking column per affected table (one goose migration each)
- `--dry-run`, `--verify` (exit 3 if rows remain), `--generate`, `--to-version`
- Operator runbook: infra/runbooks/kek-rotation.md

Corrected three backlog errors during scoping: version byte is a layout marker not a KEK id; four independent KEKs not one master; signer keys are Vault/KMS (out of scope). See spec §2.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review

**1. Spec coverage:**
- §3 decisions (Approach A window, per-service subcommand, legacy rotate-if-present, kek_version column, env-var keys, dry-run/generate) → Tasks 2–8. ✅
- §4 affected surfaces (all 6 columns across 4 services incl. webhook hex-TEXT + both audit columns + legacy) → Tasks 3–6, verified against migrations. ✅
- §5.1 `libs/crypto/rekey` Rekey/OnNewKey → Task 1; §5.2 per-service subcommand → Tasks 3–6; §5.3 kek_version column → migrations in Tasks 3–6. ✅
- §6 operator procedure + `--generate` → Task 7 runbook; §7 atomicity/fail-closed/no-secret-logging → sweep engine (Task 2), tested in Task 2 corrupt-cell rollback. ✅
- §8 testing strategy (rekey unit, per-service integration incl. hex + dual-column, legacy absent/present, failure path, key validation) → Tasks 1–6 tests. ✅
- §10 deliverables 1–5 → Tasks 1–8. ✅
- §11 open questions → resolved in "Settled design decisions". ✅

**2. Placeholder scan:** No TBD/TODO; every code step shows full code; every test shows assertions; migrations shown in full. ✅

**3. Type consistency:** `TableSpec{Table, PKColumn, VersionColumn, Columns []CipherColumn, Optional}`, `CipherColumn{Name, Encoding}`, `Encoding` (`EncodingBytea`/`EncodingHexText`), `Mode` (`ModeRotate`/`ModeDryRun`/`ModeVerify`), `SweepOpts{Mode, OldKey, NewKey, ToVersion}`, `Report{RowsRotated, RowsOnOldKey, PerTable}`, `Sweep`, `NextVersion`, `RunCLI`, `ValidationError`, `ErrRowsRemain` — used consistently across the sweep engine, CLI runner, and all four service subcommands + tests. Each service's `Run(ctx, args, stdout) error` signature is uniform. ✅
```
