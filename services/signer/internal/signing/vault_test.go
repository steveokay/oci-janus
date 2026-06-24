package signing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewVault_fetchesPublicKeyOnStartup verifies that NewVault reads the
// active key version's public PEM and derives a non-empty KeyID. This is the
// happy-path bootstrap covered by every production deployment.
func TestNewVault_fetchesPublicKeyOnStartup(t *testing.T) {
	_, pubPEM := genECPair(t)
	srv := newFakeVault(t, pubPEM, nil)
	defer srv.Close()

	s, err := NewVault(srv.URL, "tok", "transit/sign/registry-signer")
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	if s.KeyID() == "" {
		t.Fatal("KeyID is empty")
	}
}

// TestVault_SignAndVerify_roundtrip exercises the full Sign → Vault → strip
// prefix → local Verify cycle.
func TestVault_SignAndVerify_roundtrip(t *testing.T) {
	priv, pubPEM := genECPair(t)
	srv := newFakeVault(t, pubPEM, priv)
	defer srv.Close()

	s, err := NewVault(srv.URL, "tok", "transit/sign/registry-signer")
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	sig, err := s.SignPayload("tenant-1", "acme/api", "sha256:deadbeef")
	if err != nil {
		t.Fatalf("SignPayload: %v", err)
	}
	if sig == "" {
		t.Fatal("SignPayload returned empty signature")
	}
	if strings.HasPrefix(sig, "vault:") {
		t.Fatalf("SignPayload returned vault prefix; should have been stripped: %q", sig)
	}

	ok, err := s.VerifyPayload("tenant-1", "acme/api", "sha256:deadbeef", sig)
	if err != nil {
		t.Fatalf("VerifyPayload: %v", err)
	}
	if !ok {
		t.Fatal("VerifyPayload returned false for our own signature")
	}
}

// TestNewVault_missingArgs ensures the constructor rejects empty configuration
// up-front rather than failing on the first network call.
func TestNewVault_missingArgs(t *testing.T) {
	cases := []struct {
		addr, tok, path string
	}{
		{"", "t", "transit/sign/k"},
		{"http://x", "", "transit/sign/k"},
		{"http://x", "t", ""},
		{"http://x", "t", "no-sign-segment"},
	}
	for _, c := range cases {
		if _, err := NewVault(c.addr, c.tok, c.path); err == nil {
			t.Errorf("expected error for (%q,%q,%q), got nil", c.addr, c.tok, c.path)
		}
	}
}

// TestStripVaultPrefix_rejectsMalformed catches signatures that don't follow
// the documented "vault:v<n>:<b64>" format.
func TestStripVaultPrefix_rejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "notvault:x", "vault:v1"} {
		if _, err := stripVaultPrefix(in); err == nil {
			t.Errorf("expected error for %q, got nil", in)
		}
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// genECPair generates a P-256 keypair and returns the private key plus a
// PEM-encoded public key matching Vault's transit/keys response format.
func genECPair(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return priv, string(pemBytes)
}

// newFakeVault returns an httptest.Server that mimics Vault Transit's two
// endpoints: GET transit/keys/* (returns the supplied PEM) and POST
// transit/sign/* (signs with the supplied private key if non-nil).
func newFakeVault(t *testing.T, pubPEM string, priv *ecdsa.PrivateKey) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/transit/keys/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Vault-Token"); got == "" {
			http.Error(w, "missing token", http.StatusForbidden)
			return
		}
		body := map[string]any{
			"data": map[string]any{
				"latest_version": 1,
				"type":           "ecdsa-p256",
				"keys": map[string]any{
					"1": map[string]any{"public_key": pubPEM},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	mux.HandleFunc("/v1/transit/sign/", func(w http.ResponseWriter, r *http.Request) {
		if priv == nil {
			http.Error(w, "sign disabled", http.StatusInternalServerError)
			return
		}
		var req struct {
			Input     string `json:"input"`
			Prehashed bool   `json:"prehashed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// Decode the (prehashed) digest, then sign it. The handler always
		// sends prehashed=true and the digest length must be 32 bytes for sha256.
		digest, err := base64.StdEncoding.DecodeString(req.Input)
		if err != nil || len(digest) != 32 {
			http.Error(w, "bad input", http.StatusBadRequest)
			return
		}
		r1, s1, err := ecdsa.Sign(rand.Reader, priv, digest)
		if err != nil {
			http.Error(w, "sign failed", http.StatusInternalServerError)
			return
		}
		// Marshal ASN.1 R,S — VerifyASN1 expects this format.
		sig, err := asn1.Marshal(struct{ R, S *big.Int }{r1, s1})
		if err != nil {
			http.Error(w, "marshal sig", http.StatusInternalServerError)
			return
		}
		out := map[string]any{
			"data": map[string]any{
				"signature": fmt.Sprintf("vault:v1:%s", base64.StdEncoding.EncodeToString(sig)),
			},
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	return httptest.NewServer(mux)
}
