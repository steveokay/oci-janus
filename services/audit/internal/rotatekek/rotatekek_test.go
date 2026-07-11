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

// TestRotate_NotifyWebhook covers the `--notify-webhook` domain (FUT-019):
// notification_webhook_config.secret_enc is rotated with the webhook-channel key
// material, a config row that has never set a secret (NULL secret_enc) is skipped,
// and verify reports clean afterwards / flags a stale row. It runs against a
// dedicated KEK/spec, so no audit_export_configs table is present.
func TestRotate_NotifyWebhook(t *testing.T) {
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Minimal config table with just the cipher + version columns (tenant_id PK).
	if _, err := pool.Exec(ctx, `
		CREATE TABLE notification_webhook_config (
			tenant_id   UUID PRIMARY KEY,
			secret_enc  BYTEA,
			kek_version SMALLINT
		)`); err != nil {
		t.Fatalf("create notification_webhook_config: %v", err)
	}

	// One tenant with a configured secret (encrypted under the OLD webhook key)
	// and one tenant whose secret was never set (NULL) — the latter must be
	// skipped, not touched.
	oldKey, newKey := key32(0x55), key32(0x66)
	ct, _ := aes.Encrypt([]byte("webhook-signing-secret"), oldKey)
	if _, err := pool.Exec(ctx,
		`INSERT INTO notification_webhook_config (tenant_id, secret_enc)
		 VALUES (gen_random_uuid(), $1)`, ct); err != nil {
		t.Fatalf("seed configured: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO notification_webhook_config (tenant_id, secret_enc)
		 VALUES (gen_random_uuid(), NULL)`); err != nil {
		t.Fatalf("seed unset: %v", err)
	}

	t.Setenv("DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, []string{"--notify-webhook"}, &out); err != nil {
		t.Fatalf("Run --notify-webhook rotate: %v", err)
	}

	var enc []byte
	if err := pool.QueryRow(ctx,
		`SELECT secret_enc FROM notification_webhook_config WHERE secret_enc IS NOT NULL`).
		Scan(&enc); err != nil {
		t.Fatalf("read rotated secret: %v", err)
	}
	if _, err := aes.Decrypt(enc, newKey); err != nil {
		t.Fatalf("webhook secret must decrypt under new key: %v", err)
	}

	// The unset row must remain NULL.
	var nulls int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_webhook_config WHERE secret_enc IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count nulls: %v", err)
	}
	if nulls != 1 {
		t.Fatalf("expected the unset NULL row to be untouched, got %d NULL rows", nulls)
	}

	// verify (still --notify-webhook) must report zero rows on the old key.
	var vout bytes.Buffer
	if err := Run(ctx, []string{"--notify-webhook", "--verify"}, &vout); err != nil {
		t.Fatalf("verify --notify-webhook should succeed with 0 remaining: %v", err)
	}

	// Seed a stale row on the old key → verify must now flag it.
	if _, err := pool.Exec(ctx,
		`INSERT INTO notification_webhook_config (tenant_id, secret_enc)
		 VALUES (gen_random_uuid(), $1)`, ct); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	var vout2 bytes.Buffer
	if err := Run(ctx, []string{"--notify-webhook", "--verify"}, &vout2); !errors.Is(err, rekey.ErrRowsRemain) {
		t.Fatalf("verify --notify-webhook must return ErrRowsRemain when a row is on the old key, got %v", err)
	}
}

// TestRotate_NotifyEmail covers the `--notify-email` domain (FUT-019). It is the
// load-bearing test for email_transport_config's TWO mutually-exclusive cipher
// columns: a resend row (resend_api_key_enc set, smtp_password_enc NULL) and an
// smtp row (smtp_password_enc set, resend_api_key_enc NULL). The sweep must rotate
// whichever column is populated on each row while leaving the NULL sibling
// untouched — proving one TableSpec with two nullable columns is correct here.
func TestRotate_NotifyEmail(t *testing.T) {
	ctx := context.Background()
	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE email_transport_config (
			tenant_id          UUID PRIMARY KEY,
			resend_api_key_enc BYTEA,
			smtp_password_enc  BYTEA,
			kek_version        SMALLINT
		)`); err != nil {
		t.Fatalf("create email_transport_config: %v", err)
	}

	oldKey, newKey := key32(0x77), key32(0x88)
	resendCT, _ := aes.Encrypt([]byte("re_resend_api_key"), oldKey)
	smtpCT, _ := aes.Encrypt([]byte("smtp-app-password"), oldKey)

	// Resend-provider tenant: only resend_api_key_enc is set.
	resendTenant := "11111111-1111-1111-1111-111111111111"
	if _, err := pool.Exec(ctx,
		`INSERT INTO email_transport_config (tenant_id, resend_api_key_enc, smtp_password_enc)
		 VALUES ($1, $2, NULL)`, resendTenant, resendCT); err != nil {
		t.Fatalf("seed resend: %v", err)
	}
	// SMTP-provider tenant: only smtp_password_enc is set.
	smtpTenant := "22222222-2222-2222-2222-222222222222"
	if _, err := pool.Exec(ctx,
		`INSERT INTO email_transport_config (tenant_id, resend_api_key_enc, smtp_password_enc)
		 VALUES ($1, NULL, $2)`, smtpTenant, smtpCT); err != nil {
		t.Fatalf("seed smtp: %v", err)
	}

	t.Setenv("DB_DSN", dsn)
	t.Setenv("KEK_OLD_HEX", hex.EncodeToString(oldKey))
	t.Setenv("KEK_NEW_HEX", hex.EncodeToString(newKey))

	var out bytes.Buffer
	if err := Run(ctx, []string{"--notify-email"}, &out); err != nil {
		t.Fatalf("Run --notify-email rotate: %v", err)
	}

	// Resend row: resend key rotated under new key, smtp column still NULL.
	var resendEnc, resendSMTP []byte
	if err := pool.QueryRow(ctx,
		`SELECT resend_api_key_enc, smtp_password_enc FROM email_transport_config WHERE tenant_id=$1`,
		resendTenant).Scan(&resendEnc, &resendSMTP); err != nil {
		t.Fatalf("read resend row: %v", err)
	}
	if _, err := aes.Decrypt(resendEnc, newKey); err != nil {
		t.Fatalf("resend_api_key_enc must decrypt under new key: %v", err)
	}
	if resendSMTP != nil {
		t.Fatalf("resend row's smtp_password_enc must stay NULL, got %d bytes", len(resendSMTP))
	}

	// SMTP row: smtp password rotated under new key, resend column still NULL.
	var smtpResend, smtpEnc []byte
	if err := pool.QueryRow(ctx,
		`SELECT resend_api_key_enc, smtp_password_enc FROM email_transport_config WHERE tenant_id=$1`,
		smtpTenant).Scan(&smtpResend, &smtpEnc); err != nil {
		t.Fatalf("read smtp row: %v", err)
	}
	if _, err := aes.Decrypt(smtpEnc, newKey); err != nil {
		t.Fatalf("smtp_password_enc must decrypt under new key: %v", err)
	}
	if smtpResend != nil {
		t.Fatalf("smtp row's resend_api_key_enc must stay NULL, got %d bytes", len(smtpResend))
	}

	// verify (still --notify-email) must report zero rows on the old key.
	var vout bytes.Buffer
	if err := Run(ctx, []string{"--notify-email", "--verify"}, &vout); err != nil {
		t.Fatalf("verify --notify-email should succeed with 0 remaining: %v", err)
	}

	// Seed a stale row still on the old key → verify must now flag it. This
	// mirrors the webhook test's dirty-path assertion so both notification
	// domains exercise verifyTable's ErrRowsRemain branch (resendCT is still the
	// old-key ciphertext — the Go value was not mutated by the DB rotation).
	staleTenant := "33333333-3333-3333-3333-333333333333"
	if _, err := pool.Exec(ctx,
		`INSERT INTO email_transport_config (tenant_id, resend_api_key_enc, smtp_password_enc)
		 VALUES ($1, $2, NULL)`, staleTenant, resendCT); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	var vout2 bytes.Buffer
	if err := Run(ctx, []string{"--notify-email", "--verify"}, &vout2); !errors.Is(err, rekey.ErrRowsRemain) {
		t.Fatalf("verify --notify-email must return ErrRowsRemain when a row is on the old key, got %v", err)
	}
}
