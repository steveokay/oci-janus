// Package signing implements Cosign-compatible ECDSA P-256 image signing.
// Multiple backends are supported via the Signer interface: env (local PEM
// keys), vault (HashiCorp Vault Transit), and cloud KMS (AWS / GCP / Azure).
package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
)

// Signer is the abstraction over all signing backends. Implementations may
// hold key material locally (envSigner) or delegate to an external service
// such as Vault Transit or a cloud KMS.
type Signer interface {
	// KeyID returns a short, stable identifier for the active signing key.
	KeyID() string
	// SignPayload signs a Cosign-compatible JSON payload for the given manifest
	// digest and repository reference. Returns the base64-encoded DER signature.
	SignPayload(tenantID, repositoryName, manifestDigest string) (string, error)
	// VerifyPayload verifies a base64-encoded DER signature against the payload.
	VerifyPayload(repositoryName, manifestDigest, sigB64 string) (bool, error)
}

// envSigner holds a loaded ECDSA key pair in memory and performs sign/verify
// operations locally. Suitable for development; production deployments should
// use the vault or KMS backends so the private key never leaves the KMS.
type envSigner struct {
	privateKey *ecdsa.PrivateKey
	publicKey  *ecdsa.PublicKey
	keyID      string // short ID derived from public key fingerprint
}

// NewEnv loads a Signer from base64-encoded PEM strings.
// privateKeyB64 and publicKeyB64 must be base64 encodings of PEM-encoded ECDSA keys.
func NewEnv(privateKeyB64, publicKeyB64 string) (Signer, error) {
	privPEM, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode private key base64: %w", err)
	}
	pubPEM, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode public key base64: %w", err)
	}

	privKey, err := parseECPrivateKey(privPEM)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	pubKey, err := parseECPublicKey(pubPEM)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	keyID, err := publicKeyFingerprint(pubKey)
	if err != nil {
		return nil, fmt.Errorf("fingerprint public key: %w", err)
	}

	return &envSigner{privateKey: privKey, publicKey: pubKey, keyID: keyID}, nil
}

// KeyID returns the short identifier for the active signing key.
func (s *envSigner) KeyID() string { return s.keyID }

// SignPayload signs a Cosign-compatible JSON payload for the given manifest digest and
// repository reference. Returns the base64-encoded DER signature.
func (s *envSigner) SignPayload(tenantID, repositoryName, manifestDigest string) (string, error) {
	payload, err := buildSigningPayload(repositoryName, manifestDigest)
	if err != nil {
		return "", fmt.Errorf("build payload: %w", err)
	}

	digest := sha256.Sum256(payload)
	sig, err := s.privateKey.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("ecdsa sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyPayload verifies a base64-encoded DER signature against the signing payload.
func (s *envSigner) VerifyPayload(repositoryName, manifestDigest, sigB64 string) (bool, error) {
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

// SignatureDigest returns the sha256 hex digest of the raw signature bytes (for content-addressing).
func SignatureDigest(sigB64 string) (string, error) {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return "", fmt.Errorf("decode signature: %w", err)
	}
	h := sha256.Sum256(sig)
	return fmt.Sprintf("sha256:%x", h), nil
}

// cosignPayload mirrors the Cosign simple-signing JSON structure.
type cosignPayload struct {
	Critical cosignCritical `json:"critical"`
	Optional any           `json:"optional"`
}

type cosignCritical struct {
	Identity cosignIdentity `json:"identity"`
	Image    cosignImage    `json:"image"`
	Type     string         `json:"type"`
}

type cosignIdentity struct {
	DockerReference string `json:"docker-reference"`
}

type cosignImage struct {
	DockerManifestDigest string `json:"docker-manifest-digest"`
}

func buildSigningPayload(repositoryName, manifestDigest string) ([]byte, error) {
	p := cosignPayload{
		Critical: cosignCritical{
			Identity: cosignIdentity{DockerReference: repositoryName},
			Image:    cosignImage{DockerManifestDigest: manifestDigest},
			Type:     "cosign container image signature",
		},
	}
	return json.Marshal(p)
}

func parseECPrivateKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	// Try PKCS#8 first (-----BEGIN PRIVATE KEY-----).
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not ECDSA")
		}
		return ec, nil
	}
	// Fall back to SEC 1 (-----BEGIN EC PRIVATE KEY-----).
	return x509.ParseECPrivateKey(block.Bytes)
}

func parseECPublicKey(pemBytes []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not ECDSA")
	}
	return ec, nil
}

// publicKeyFingerprint returns the first 16 hex chars of sha256(DER(pubkey)).
func publicKeyFingerprint(pub *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(der)
	return fmt.Sprintf("%x", h[:8]), nil
}
