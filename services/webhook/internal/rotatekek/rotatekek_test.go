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
