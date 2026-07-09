package prregistry

// verify.go — the HMAC-SHA256 webhook signature gate (FUT-023 §7.1).
//
// GitHub signs each webhook delivery with HMAC-SHA256 over the raw request
// body, using the per-integration secret, and sends it as
//   X-Hub-Signature-256: sha256=<hex>
// We recompute the MAC with the tenant's KEK-unsealed secret and compare in
// constant time. Two fail modes are distinguished by sentinel so the handler
// can map them to different HTTP statuses:
//
//   - ErrFeatureDisabled — the integration is not usable (disabled flag, no
//     stored secret, or the KEK is unset). The handler maps this to a 404 so
//     an external probe can't tell "no integration configured" apart from a
//     "bad signature" — both look like "this endpoint isn't here".
//   - ErrSignatureMismatch — a stored secret exists but the presented
//     signature doesn't verify (wrong secret, tampered body, or a malformed
//     header). Handler maps this to 401.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// signaturePrefix is the algorithm marker GitHub prepends to the hex digest
// in the X-Hub-Signature-256 header.
const signaturePrefix = "sha256="

var (
	// ErrFeatureDisabled means the PR-registry integration cannot verify a
	// signature because it is switched off or misconfigured: the config's
	// enabled flag is false, no webhook secret is stored, or the KEK is unset.
	// Fail-closed by design — the handler renders this as HTTP 404.
	ErrFeatureDisabled = errors.New("prregistry: feature disabled")

	// ErrSignatureMismatch means a secret is configured but the presented
	// signature did not verify (wrong secret, tampered body, or malformed
	// header). The handler renders this as HTTP 401.
	ErrSignatureMismatch = errors.New("prregistry: signature mismatch")
)

// Verify authenticates a GitHub webhook delivery.
//
// It fails closed with ErrFeatureDisabled when the integration is not usable
// (disabled / no stored secret / KEK unset), unseals the stored secret with
// the KEK, recomputes HMAC-SHA256 over rawBody, and compares it in constant
// time against the signatureHeader (which carries the "sha256=" prefix). Any
// mismatch, a malformed header, or an unseal failure returns
// ErrSignatureMismatch.
//
// The rawBody must be the exact bytes GitHub signed — callers must pass the
// unparsed request body, since re-serialising JSON would change the digest.
func (s *Service) Verify(cfg repository.PRRegistryConfig, rawBody []byte, signatureHeader string) error {
	// Fail-closed gate: an off/misconfigured integration never authenticates.
	if !cfg.Enabled || cfg.WebhookSecretEnc == nil || len(s.kek) == 0 {
		return ErrFeatureDisabled
	}

	// Unseal the stored secret with the KEK. A decrypt failure (wrong KEK /
	// corrupted ciphertext) is treated as a mismatch, not a 500 — we can't
	// verify the caller, so we deny.
	secret, err := aes.Decrypt(cfg.WebhookSecretEnc, s.kek)
	if err != nil {
		return ErrSignatureMismatch
	}

	// The header must carry the "sha256=" prefix and a hex digest. Anything
	// else is a malformed signature ⇒ deny.
	if !strings.HasPrefix(signatureHeader, signaturePrefix) {
		return ErrSignatureMismatch
	}
	presented, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, signaturePrefix))
	if err != nil {
		return ErrSignatureMismatch
	}

	// Recompute the MAC over the raw body and compare in constant time.
	mac := hmac.New(sha256.New, secret)
	mac.Write(rawBody)
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, presented) {
		return ErrSignatureMismatch
	}
	return nil
}
