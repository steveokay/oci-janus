// Package repository provides PostgreSQL persistence for signature records.
// It is the only package in registry-signer that issues SQL — all other packages
// go through this interface.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// Repository persists signature records to PostgreSQL.
// All queries are parameterised — no fmt.Sprintf in SQL.
type Repository struct {
	pool *pgxpool.Pool
}

// New creates a Repository backed by the given connection pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Store upserts a signature record.
// On conflict for (manifest_digest, signer_id) the row is updated so that a
// re-sign with a new key produces a fresh record rather than a duplicate-key error.
// Note: SigB64 (raw base64 DER bytes) is intentionally NOT stored — SEC-015.
func (r *Repository) Store(ctx context.Context, rec *sigstore.Record) error {
	const q = `
INSERT INTO signatures (manifest_digest, repository_name, signer_id, key_id, signature_digest, signed_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (manifest_digest, signer_id) DO UPDATE
    SET key_id           = EXCLUDED.key_id,
        signature_digest = EXCLUDED.signature_digest,
        signed_at        = EXCLUDED.signed_at`

	_, err := r.pool.Exec(ctx, q,
		rec.ManifestDigest,
		rec.RepositoryName,
		rec.SignerID,
		rec.KeyID,
		rec.SignatureDigest,
		rec.SignedAt,
	)
	if err != nil {
		return fmt.Errorf("store signature: %w", err)
	}
	return nil
}

// List returns all signature records for the given manifest digest, ordered by
// signed_at ascending. Returns an empty slice (not nil) when no rows are found.
func (r *Repository) List(ctx context.Context, manifestDigest string) ([]*sigstore.Record, error) {
	const q = `
SELECT manifest_digest, repository_name, signer_id, key_id, signature_digest, signed_at
FROM   signatures
WHERE  manifest_digest = $1
ORDER  BY signed_at ASC`

	rows, err := r.pool.Query(ctx, q, manifestDigest)
	if err != nil {
		return nil, fmt.Errorf("list signatures: %w", err)
	}
	defer rows.Close()

	var out []*sigstore.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan signature row: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate signatures: %w", err)
	}
	if out == nil {
		out = []*sigstore.Record{}
	}
	return out, nil
}

// FindRec returns the single record matching (manifestDigest, signerID), or
// (nil, nil) when no row exists. Returns an error only on unexpected DB failures.
func (r *Repository) FindRec(ctx context.Context, manifestDigest, signerID string) (*sigstore.Record, error) {
	const q = `
SELECT manifest_digest, repository_name, signer_id, key_id, signature_digest, signed_at
FROM   signatures
WHERE  manifest_digest = $1
AND    signer_id       = $2`

	rows, err := r.pool.Query(ctx, q, manifestDigest, signerID)
	if err != nil {
		return nil, fmt.Errorf("find signature: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("find signature: %w", err)
		}
		// No row found — not an error per the contract.
		return nil, nil
	}
	rec, err := scanRecord(rows)
	if err != nil {
		return nil, fmt.Errorf("scan signature row: %w", err)
	}
	return rec, nil
}

// scanRecord reads the common column set into a *sigstore.Record.
// Column order must match the SELECT lists in List and FindRec.
// SigB64 is left as the zero-value (empty string) because it is never stored.
func scanRecord(rows pgx.Rows) (*sigstore.Record, error) {
	var (
		manifestDigest  string
		repositoryName  string
		signerID        string
		keyID           string
		signatureDigest string
		signedAt        time.Time
	)
	if err := rows.Scan(
		&manifestDigest,
		&repositoryName,
		&signerID,
		&keyID,
		&signatureDigest,
		&signedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &sigstore.Record{
		ManifestDigest:  manifestDigest,
		RepositoryName:  repositoryName,
		SignerID:        signerID,
		KeyID:           keyID,
		SignatureDigest: signatureDigest,
		// SigB64 intentionally omitted — SEC-015 prohibits persisting raw sig bytes.
		SignedAt: signedAt,
	}, nil
}
