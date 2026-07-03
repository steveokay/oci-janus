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
		CREATE TABLE audit_export_configs (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id    UUID NOT NULL UNIQUE,
			hmac_secret  BYTEA,
			bearer_token BYTEA,
			kek_version  SMALLINT
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
