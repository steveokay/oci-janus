//go:build integration

// Package fixtures provides test data builders for integration tests.
package fixtures

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

// RSAKeyPair generates a fresh 2048-bit RSA key pair and returns the private and
// public keys as base64-encoded PEM strings, matching the format expected by the
// auth service SIGNER_PRIVATE_KEY / SIGNER_PUBLIC_KEY environment variables.
func RSAKeyPair() (privateB64, publicB64 string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generate rsa key: %w", err)
	}

	// PKCS#1 private key PEM block.
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	// PKIX (SubjectPublicKeyInfo) public key PEM block.
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal public key: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	return base64.StdEncoding.EncodeToString(privPEM),
		base64.StdEncoding.EncodeToString(pubPEM), nil
}
