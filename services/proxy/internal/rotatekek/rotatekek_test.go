//go:build integration

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
}
