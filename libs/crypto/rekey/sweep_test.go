//go:build integration

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

	rep, err := rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeVerify, NewKey: newKey,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.RowsOnOldKey != 1 {
		t.Fatalf("want 1 row on old key, got %d", rep.RowsOnOldKey)
	}

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

	v, err := rekey.NextVersion(ctx, pool, fixtureSpecs())
	if err != nil {
		t.Fatalf("NextVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("want next version 1 on fresh table, got %d", v)
	}

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

// TestSweep_RotateIdempotent verifies that re-running rotate with the same keys
// is a safe no-op rather than an error. A previous run left every row on the
// new key; the second run must recognise that (via trial-decryption), skip
// those rows, rotate nothing, and not fail — the property that makes rotation
// re-runnable and a partially-completed multi-table rotation resumable.
func TestSweep_RotateIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, containers.Postgres(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	oldKey, newKey := key32(0x11), key32(0x22)
	setupTable(t, ctx, pool, oldKey)

	// First rotation: the one seeded row moves to the new key.
	rep, err := rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeRotate, OldKey: oldKey, NewKey: newKey, ToVersion: 1,
	})
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	if rep.RowsRotated != 1 {
		t.Fatalf("first rotate: want 1 row, got %d", rep.RowsRotated)
	}

	// Second rotation with the SAME keys must be a clean no-op — the rows now
	// decrypt under NEW and no longer decrypt under OLD, so a naive re-encrypt
	// would fail. The engine must skip already-rotated cells instead.
	rep, err = rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeRotate, OldKey: oldKey, NewKey: newKey, ToVersion: 2,
	})
	if err != nil {
		t.Fatalf("re-run rotate must not error, got: %v", err)
	}
	if rep.RowsRotated != 0 {
		t.Fatalf("re-run should rotate 0 rows, got %d", rep.RowsRotated)
	}

	// Dry-run over an already-rotated table also reports 0 candidates.
	rep, err = rekey.Sweep(ctx, pool, fixtureSpecs(), rekey.SweepOpts{
		Mode: rekey.ModeDryRun, OldKey: oldKey, NewKey: newKey, ToVersion: 2,
	})
	if err != nil {
		t.Fatalf("re-run dry-run must not error, got: %v", err)
	}
	if rep.RowsRotated != 0 {
		t.Fatalf("re-run dry-run should report 0 candidates, got %d", rep.RowsRotated)
	}

	// Data is still valid under NEW after the no-op re-runs.
	var blob []byte
	var hexStr string
	if err := pool.QueryRow(ctx,
		`SELECT blob_enc, hex_enc FROM sweep_fixture WHERE pk='row-1'`).Scan(&blob, &hexStr); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, err := aes.Decrypt(blob, newKey); err != nil {
		t.Fatalf("blob must still decrypt under new key after re-runs: %v", err)
	}
	hb, _ := hex.DecodeString(hexStr)
	if _, err := aes.Decrypt(hb, newKey); err != nil {
		t.Fatalf("hex must still decrypt under new key after re-runs: %v", err)
	}
}
