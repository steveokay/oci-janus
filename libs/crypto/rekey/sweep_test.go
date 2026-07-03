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
