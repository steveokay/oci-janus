// Package signing — Vault Transit backend.
//
// The vault backend delegates all signing operations to HashiCorp Vault's
// Transit secrets engine. Private key material never leaves Vault — the
// service holds only a long-lived auth token plus a cached copy of the
// public key (used for KeyID derivation and local verification).
//
// Expected Vault state (see infra/docker-compose/vault/init.sh):
//   - Transit engine enabled at `transit/`
//   - ECDSA P-256 key created at `transit/keys/<name>`
//   - The auth token holds capabilities: `update` on transit/sign/<name>,
//     `update` on transit/verify/<name>, and `read` on transit/keys/<name>.
//
// VAULT_COSIGN_PATH must be the transit sign path, e.g.
// `transit/sign/registry-signer` — the backend derives the public-key path
// (`transit/keys/registry-signer`) from it by replacing the `sign/` segment.
package signing

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// vaultSigner signs through Vault Transit and verifies locally with the
// cached public key.
type vaultSigner struct {
	addr       string
	token      string
	signPath   string // e.g. "transit/sign/registry-signer"
	keysPath   string // e.g. "transit/keys/registry-signer"
	httpClient *http.Client
	publicKey  *ecdsa.PublicKey
	keyID      string
}

// NewVault constructs a Vault-backed Signer. It fetches the active public
// key at startup so KeyID() and local verification both work without an
// extra round-trip on every operation. Returns an error if Vault is
// unreachable or the configured transit key cannot be read.
func NewVault(addr, token, signPath string) (Signer, error) {
	if addr == "" {
		return nil, fmt.Errorf("VAULT_ADDR is required")
	}
	if token == "" {
		return nil, fmt.Errorf("VAULT_TOKEN is required")
	}
	if signPath == "" {
		return nil, fmt.Errorf("VAULT_COSIGN_PATH is required (e.g. transit/sign/registry-signer)")
	}

	// transit/sign/<name> → transit/keys/<name>. Keeping a single env var
	// avoids operator confusion about which path to point where.
	keysPath := strings.Replace(signPath, "/sign/", "/keys/", 1)
	if keysPath == signPath {
		return nil, fmt.Errorf("VAULT_COSIGN_PATH must contain '/sign/'; got %q", signPath)
	}

	s := &vaultSigner{
		addr:     strings.TrimRight(addr, "/"),
		token:    token,
		signPath: signPath,
		keysPath: keysPath,
		// Bounded timeout — Vault is on the same network in production and
		// signing must not hang behind a missing TCP RST.
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	pub, err := s.fetchPublicKey(context.Background())
	if err != nil {
		return nil, fmt.Errorf("fetch vault public key: %w", err)
	}
	kid, err := publicKeyFingerprint(pub)
	if err != nil {
		return nil, fmt.Errorf("fingerprint vault public key: %w", err)
	}
	s.publicKey = pub
	s.keyID = kid
	return s, nil
}

// KeyID returns the short identifier for the active signing key, derived from
// the SHA256 fingerprint of the public key bytes.
func (s *vaultSigner) KeyID() string { return s.keyID }

// SignPayload builds the Cosign payload, hashes it locally, and asks Vault to
// sign the prehashed digest. Only the digest leaves the service — never the
// payload bytes — so Vault audit logs do not include image metadata.
func (s *vaultSigner) SignPayload(tenantID, repositoryName, manifestDigest string) (string, error) {
	payload, err := buildSigningPayload(repositoryName, manifestDigest)
	if err != nil {
		return "", fmt.Errorf("build payload: %w", err)
	}
	digest := sha256.Sum256(payload)

	body, _ := json.Marshal(map[string]any{
		"input":          base64.StdEncoding.EncodeToString(digest[:]),
		"prehashed":      true,
		"hash_algorithm": "sha2-256",
		"signature_algorithm": "pkcs1v15", // ignored for ECDSA keys; harmless
		"marshaling_algorithm": "asn1",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.addr+"/v1/"+s.signPath, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build vault request: %w", err)
	}
	req.Header.Set("X-Vault-Token", s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("vault sign: %w", err)
	}
	defer resp.Body.Close()

	// PENTEST-007: cap the body. A real sign response is a few hundred bytes.
	respBody := io.LimitReader(resp.Body, 64*1024)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(respBody)
		return "", fmt.Errorf("vault sign returned %d: %s", resp.StatusCode, truncate(string(b), 256))
	}

	var out struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	if err := json.NewDecoder(respBody).Decode(&out); err != nil {
		return "", fmt.Errorf("decode vault response: %w", err)
	}

	// Vault returns "vault:v<key-version>:<base64-asn1-sig>"; strip the prefix
	// so callers receive a plain base64 ASN.1 signature identical to the env
	// backend's output.
	asn1B64, err := stripVaultPrefix(out.Data.Signature)
	if err != nil {
		return "", err
	}
	return asn1B64, nil
}

// VerifyPayload checks the signature locally with the cached public key.
// Round-tripping verification through Vault works but doubles the latency
// of a pull-with-verify flow; the public key is not secret so local
// verification is the canonical pattern.
func (s *vaultSigner) VerifyPayload(repositoryName, manifestDigest, sigB64 string) (bool, error) {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false, fmt.Errorf("decode signature: %w", err)
	}
	payload, err := buildSigningPayload(repositoryName, manifestDigest)
	if err != nil {
		return false, fmt.Errorf("build payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	return ecdsa.VerifyASN1(s.publicKey, digest[:], sig), nil
}

// fetchPublicKey reads the active version's PEM-encoded public key from
// Vault and returns it parsed. Called once at startup.
func (s *vaultSigner) fetchPublicKey(ctx context.Context) (*ecdsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.addr+"/v1/"+s.keysPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault get key: %w", err)
	}
	defer resp.Body.Close()

	// PENTEST-007: cap the body we read so a misbehaving / impersonating Vault
	// endpoint cannot stream unbounded bytes into our process at startup.
	// A real Vault transit/keys response is well under 16 KB.
	body := io.LimitReader(resp.Body, 64*1024)

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("vault get key returned %d: %s", resp.StatusCode, truncate(string(b), 256))
	}

	// Vault Transit key read returns:
	// {"data":{"latest_version":1,"keys":{"1":{"public_key":"-----BEGIN PUBLIC KEY-----..."}}}}
	var out struct {
		Data struct {
			LatestVersion int                       `json:"latest_version"`
			Keys          map[string]map[string]any `json:"keys"`
			Type          string                    `json:"type"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode vault key response: %w", err)
	}
	if out.Data.Type != "" && !strings.HasPrefix(out.Data.Type, "ecdsa") {
		return nil, fmt.Errorf("vault transit key is type %q; only ecdsa-* is supported", out.Data.Type)
	}

	versionKey := fmt.Sprintf("%d", out.Data.LatestVersion)
	entry, ok := out.Data.Keys[versionKey]
	if !ok {
		return nil, fmt.Errorf("vault response missing key version %s", versionKey)
	}
	pemStr, _ := entry["public_key"].(string)
	if pemStr == "" {
		return nil, fmt.Errorf("vault response missing public_key for version %s", versionKey)
	}
	return parseECPublicKey([]byte(pemStr))
}

// stripVaultPrefix removes the "vault:v<n>:" header from a Vault signature.
// The prefix carries the key version for re-validation; callers that want
// the raw ASN.1 signature must strip it before passing to crypto/ecdsa.
func stripVaultPrefix(s string) (string, error) {
	if !strings.HasPrefix(s, "vault:") {
		return "", fmt.Errorf("vault signature missing 'vault:' prefix: %q", truncate(s, 64))
	}
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("vault signature malformed: %q", truncate(s, 64))
	}
	return parts[2], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
