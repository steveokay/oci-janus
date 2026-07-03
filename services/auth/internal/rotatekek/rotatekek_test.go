//go:build integration

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

	var curEnc []byte
	if err := pool.QueryRow(ctx,
		`SELECT oauth_client_secret_enc FROM global_sso_config WHERE provider_id='google'`).
		Scan(&curEnc); err != nil {
		t.Fatalf("read current: %v", err)
	}
	if _, err := aes.Decrypt(curEnc, newKey); err != nil {
		t.Fatalf("current secret must decrypt under new key: %v", err)
	}
	var legEnc []byte
	if err := pool.QueryRow(ctx,
		`SELECT oauth_client_secret_enc FROM auth_providers`).Scan(&legEnc); err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if _, err := aes.Decrypt(legEnc, newKey); err != nil {
		t.Fatalf("legacy secret must decrypt under new key: %v", err)
	}

	var vout bytes.Buffer
	if err := Run(ctx, []string{"--verify"}, &vout); err != nil {
		t.Fatalf("verify should succeed with 0 remaining: %v", err)
	}

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
