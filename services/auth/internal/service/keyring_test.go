// Package service — keyring_test.go covers the Phase 6.5 multi-key JWT
// support: signing with one kid + validating against a ring that contains
// multiple kids, the fallback path for tokens whose kid is unknown, and the
// JWKS enumeration that lets external validators rotate too.
//
// The tests construct RSA keys at runtime (small 1024-bit for speed; never
// for production) and assemble rings directly via newKeyRing — they do not
// touch disk except for one test that exercises loadKeyRingFromDir's parse
// + sort behaviour.
package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// genTestKey produces a fresh RSA key plus its PEM-encoded private form.
// Returns (priv, pemBytes). 1024 bits is intentionally weak so the test
// suite stays fast — never use under production.
func genTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return priv, pemBytes
}

// genTestKeyB64 produces a fresh RSA key as the (privB64, pubB64, kid)
// triple expected by NewWithFakes. Reuses the parsing primitives so the
// generated PEM is round-trip-safe with parsePrivateKey / parsePublicKey.
func genTestKeyB64(t *testing.T, kid string) (privB64, pubB64, returnedKid string) {
	t.Helper()
	priv, privPEM := genTestKey(t)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal PKIX pub: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return base64.StdEncoding.EncodeToString(privPEM),
		base64.StdEncoding.EncodeToString(pubPEM),
		kid
}

// newTestRedis spins up a miniredis instance for the test and registers
// a cleanup that closes it. Used by tests that need a real *redis.Client
// because ValidateToken hits Redis for revocation checks.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// TestNewKeyRing_rejectsEmpty asserts that an empty ring cannot be built —
// the invariant that protects IssueToken from ever signing with no key.
func TestNewKeyRing_rejectsEmpty(t *testing.T) {
	if _, err := newKeyRing(nil, "anything"); err == nil {
		t.Fatal("expected error for empty ring, got nil")
	}
}

// TestNewKeyRing_rejectsMissingSigningKID asserts that nominating a kid not
// present in the ring fails at construction.
func TestNewKeyRing_rejectsMissingSigningKID(t *testing.T) {
	priv, _ := genTestKey(t)
	keys := []signingKey{{kid: "a", privateKey: priv, publicKey: &priv.PublicKey}}
	if _, err := newKeyRing(keys, "b"); err == nil {
		t.Fatal("expected error when signing kid is absent, got nil")
	}
}

// TestNewKeyRing_rejectsDuplicateKID asserts that two entries with the same
// kid are rejected — otherwise the find() lookup would be non-deterministic.
func TestNewKeyRing_rejectsDuplicateKID(t *testing.T) {
	priv, _ := genTestKey(t)
	keys := []signingKey{
		{kid: "a", privateKey: priv, publicKey: &priv.PublicKey},
		{kid: "a", privateKey: priv, publicKey: &priv.PublicKey},
	}
	if _, err := newKeyRing(keys, "a"); err == nil {
		t.Fatal("expected error for duplicate kid, got nil")
	}
}

// TestKeyRing_findReturnsMatchingPublicKey verifies the fast-path lookup
// used by ValidateToken when the JWT carries a known kid.
func TestKeyRing_findReturnsMatchingPublicKey(t *testing.T) {
	privA, _ := genTestKey(t)
	privB, _ := genTestKey(t)
	ring, err := newKeyRing([]signingKey{
		{kid: "a", privateKey: privA, publicKey: &privA.PublicKey},
		{kid: "b", privateKey: privB, publicKey: &privB.PublicKey},
	}, "b")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	if got := ring.find("a"); got != &privA.PublicKey {
		t.Errorf("find(a) returned wrong key")
	}
	if got := ring.find("nope"); got != nil {
		t.Errorf("find(nope) = %v, want nil", got)
	}
}

// TestValidateToken_SignAValidateAB — the headline rotation case. A token
// signed with kid A validates successfully against a ring containing both
// A and B (the operator added B in preparation for promoting it).
func TestValidateToken_SignAValidateAB(t *testing.T) {
	ctx := context.Background()
	// Two key pairs: A is the current signer, B is the incoming key.
	privA, _ := genTestKey(t)
	privB, _ := genTestKey(t)
	rdb := newTestRedis(t)

	ringAOnly, err := newKeyRing([]signingKey{
		{kid: "kid-a", privateKey: privA, publicKey: &privA.PublicKey},
	}, "kid-a")
	if err != nil {
		t.Fatalf("ring A: %v", err)
	}
	// Service #1 mints a token while only A is in the ring.
	svcSigner, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ringAOnly)
	if err != nil {
		t.Fatalf("svc signer: %v", err)
	}
	token, err := svcSigner.IssueToken(ctx, uuid.New().String(), uuid.New().String(), nil, nil, false, "human", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	// Operator rotates: a second service instance now sees both A and B.
	ringAB, err := newKeyRing([]signingKey{
		{kid: "kid-a", privateKey: privA, publicKey: &privA.PublicKey},
		{kid: "kid-b", privateKey: privB, publicKey: &privB.PublicKey},
	}, "kid-b") // signing has flipped to B
	if err != nil {
		t.Fatalf("ring AB: %v", err)
	}
	svcValidator, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ringAB)
	if err != nil {
		t.Fatalf("svc validator: %v", err)
	}
	claims, err := svcValidator.ValidateToken(ctx, token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Subject == "" {
		t.Errorf("expected non-empty subject")
	}
}

// TestValidateToken_SignAValidateBOnly_fails — the inverse. A token signed
// with kid A fails to validate when the ring only contains B (i.e. the
// operator retired A prematurely while the token was still live).
func TestValidateToken_SignAValidateBOnly_fails(t *testing.T) {
	ctx := context.Background()
	privA, _ := genTestKey(t)
	privB, _ := genTestKey(t)
	rdb := newTestRedis(t)

	ringA, err := newKeyRing([]signingKey{
		{kid: "kid-a", privateKey: privA, publicKey: &privA.PublicKey},
	}, "kid-a")
	if err != nil {
		t.Fatalf("ring A: %v", err)
	}
	svcA, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ringA)
	if err != nil {
		t.Fatalf("svc A: %v", err)
	}
	token, err := svcA.IssueToken(ctx, uuid.New().String(), uuid.New().String(), nil, nil, false, "human", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	ringB, err := newKeyRing([]signingKey{
		{kid: "kid-b", privateKey: privB, publicKey: &privB.PublicKey},
	}, "kid-b")
	if err != nil {
		t.Fatalf("ring B: %v", err)
	}
	svcB, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ringB)
	if err != nil {
		t.Fatalf("svc B: %v", err)
	}
	if _, err := svcB.ValidateToken(ctx, token); err == nil {
		t.Fatal("expected error when validating token signed by kid not in ring, got nil")
	}
}

// TestValidateToken_FallbackWhenKidMissing — covers the legacy-token path.
// We mint a JWT WITHOUT a kid header (simulating a pre-Phase-6.5 token),
// then validate it against a ring that contains the matching key. The
// service should succeed via the try-every-key fallback.
func TestValidateToken_FallbackWhenKidMissing(t *testing.T) {
	ctx := context.Background()
	priv, _ := genTestKey(t)
	rdb := newTestRedis(t)

	// Mint a "legacy" token by hand: no kid header, signed with priv.
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "registry-auth",
			Subject:   uuid.New().String(),
			Audience:  jwt.ClaimStrings{"registry-core"},
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
		TenantID: uuid.New().String(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	// Deliberately do NOT set the kid header — that's the legacy case.
	legacyToken, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}

	ring, err := newKeyRing([]signingKey{
		{kid: "kid-legacy", privateKey: priv, publicKey: &priv.PublicKey},
	}, "kid-legacy")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	svc, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ring)
	if err != nil {
		t.Fatalf("svc: %v", err)
	}

	got, err := svc.ValidateToken(ctx, legacyToken)
	if err != nil {
		t.Fatalf("ValidateToken (fallback) failed: %v", err)
	}
	if got.Subject != claims.Subject {
		t.Errorf("subject mismatch via fallback: got %q, want %q", got.Subject, claims.Subject)
	}
}

// TestJWKS_listsEveryKid asserts that the /.well-known/jwks.json output
// enumerates every public key in the ring so external validators can verify
// tokens minted by any kid in rotation.
func TestJWKS_listsEveryKid(t *testing.T) {
	privA, _ := genTestKey(t)
	privB, _ := genTestKey(t)
	rdb := newTestRedis(t)

	ring, err := newKeyRing([]signingKey{
		{kid: "kid-a", privateKey: privA, publicKey: &privA.PublicKey},
		{kid: "kid-b", privateKey: privB, publicKey: &privB.PublicKey},
	}, "kid-b")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	svc, err := NewWithFakesAndRing(nil, nil, nil, nil, rdb, ring)
	if err != nil {
		t.Fatalf("svc: %v", err)
	}

	jwks := svc.JWKS()
	if len(jwks.Keys) != 2 {
		t.Fatalf("expected 2 keys in JWKS, got %d", len(jwks.Keys))
	}
	got := map[string]bool{}
	for _, k := range jwks.Keys {
		got[k.Kid] = true
		if k.Alg != "RS256" {
			t.Errorf("kid %q: alg = %q, want RS256", k.Kid, k.Alg)
		}
	}
	if !got["kid-a"] || !got["kid-b"] {
		t.Errorf("JWKS missing expected kid; got %v", got)
	}
}

// TestLoadKeyRingFromDir_loadsAndSorts exercises the disk loader: it writes
// two PEM files to a temp directory, calls LoadKeyRing, and asserts that
// (a) both kids are present, (b) the MOST-RECENTLY-MODIFIED kid is the
// default signer when JWT_SIGNING_KID is empty (SEC-049 changed this from
// lex-greatest because operators using semantic names like `prod-a.pem`,
// `prod-b.pem`, `prod-c.pem` would have the OLDEST file selected by lex
// order). The test forces the mtime ordering explicitly so the result is
// independent of write-order timing on coarse-resolution filesystems.
func TestLoadKeyRingFromDir_loadsAndSorts(t *testing.T) {
	dir := t.TempDir()
	_, pemA := genTestKey(t)
	_, pemB := genTestKey(t)
	pathA := filepath.Join(dir, "kid-a.pem")
	pathB := filepath.Join(dir, "kid-b.pem")
	if err := os.WriteFile(pathA, pemA, 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(pathB, pemB, 0o600); err != nil {
		t.Fatalf("write b: %v", err)
	}
	// Force kid-a to be the most-recently-modified file — even though kid-b
	// is the lex-greatest, the SEC-049 mtime selector should pick kid-a.
	// 2-second skew defeats coarse mtime resolution on Windows NTFS / older
	// HFS+.
	pastT := time.Now().Add(-1 * time.Hour)
	futureT := time.Now()
	if err := os.Chtimes(pathB, pastT, pastT); err != nil {
		t.Fatalf("chtimes b: %v", err)
	}
	if err := os.Chtimes(pathA, futureT, futureT); err != nil {
		t.Fatalf("chtimes a: %v", err)
	}
	// A stray non-PEM file must be tolerated (skipped, not failed).
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	// Empty signing kid → SEC-049 default = most-recently-modified file =
	// kid-a (we set its mtime to "now", kid-b to "1 hour ago").
	ring, err := LoadKeyRing(dir, "")
	if err != nil {
		t.Fatalf("LoadKeyRing: %v", err)
	}
	if ring.Size() != 2 {
		t.Errorf("ring size = %d, want 2", ring.Size())
	}
	if got := ring.SigningKID(); got != "kid-a" {
		t.Errorf("SEC-049 default signing kid = %q, want kid-a (most recently modified)", got)
	}

	// Explicit signing kid wins over the default.
	ring2, err := LoadKeyRing(dir, "kid-a")
	if err != nil {
		t.Fatalf("LoadKeyRing explicit: %v", err)
	}
	if got := ring2.SigningKID(); got != "kid-a" {
		t.Errorf("explicit signing kid = %q, want kid-a", got)
	}
}

// TestLoadKeyRingFromDir_emptyDirIsAFailure asserts that pointing at an
// empty directory is a startup-time error — per CLAUDE.md §7 we never
// silently fall back to single-key mode.
func TestLoadKeyRingFromDir_emptyDirIsAFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadKeyRing(dir, ""); err == nil {
		t.Fatal("expected error for empty dir, got nil")
	}
}

// TestLoadKeyRingFromDir_missingDirIsAFailure asserts that a non-existent
// directory is a startup-time error.
func TestLoadKeyRingFromDir_missingDirIsAFailure(t *testing.T) {
	if _, err := LoadKeyRing(filepath.Join(t.TempDir(), "does-not-exist"), ""); err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

// TestLoadKeyRingFromDir_SEC048_RejectsOversizedRing is the regression for
// SEC-048: validation falls back to "try every key" when kid is missing or
// unknown, so an unbounded ring is a CPU-amplification vector. Operators
// who leave every retired key in the directory should hear about it at
// startup, not silently amplify per-RPC RSA verify cost on every fallback
// hit.
func TestLoadKeyRingFromDir_SEC048_RejectsOversizedRing(t *testing.T) {
	dir := t.TempDir()
	// Write maxKeyRingSize + 1 PEM files so the loader trips the cap.
	for i := 0; i <= maxKeyRingSize; i++ {
		_, pem := genTestKey(t)
		name := filepath.Join(dir, fmt.Sprintf("kid-%02d.pem", i))
		if err := os.WriteFile(name, pem, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	_, err := LoadKeyRing(dir, "")
	if err == nil {
		t.Fatal("SEC-048: expected ring-size-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "too many keys") {
		t.Errorf("SEC-048: error must call out the cap clearly; got: %v", err)
	}
}

// TestLoadKeyRingFromDir_explicitSigningKIDMustExist asserts that nominating
// a kid that's not in the directory is a startup-time error.
func TestLoadKeyRingFromDir_explicitSigningKIDMustExist(t *testing.T) {
	dir := t.TempDir()
	_, pemA := genTestKey(t)
	if err := os.WriteFile(filepath.Join(dir, "kid-a.pem"), pemA, 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if _, err := LoadKeyRing(dir, "kid-does-not-exist"); err == nil {
		t.Fatal("expected error when signing kid is missing from dir, got nil")
	}
}

// TestSingleKeyRingFromB64_roundTrip asserts that the legacy single-key
// path still works: a base64-encoded PEM pair builds a 1-element ring, and
// IssueToken / ValidateToken cycle through cleanly.
func TestSingleKeyRingFromB64_roundTrip(t *testing.T) {
	ctx := context.Background()
	privB64, pubB64, kid := genTestKeyB64(t, "single-kid")
	rdb := newTestRedis(t)

	svc, err := NewWithFakes(nil, nil, nil, nil, rdb, privB64, pubB64, kid)
	if err != nil {
		t.Fatalf("NewWithFakes: %v", err)
	}
	token, err := svc.IssueToken(ctx, uuid.New().String(), uuid.New().String(), nil, nil, false, "human", nil)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if _, err := svc.ValidateToken(ctx, token); err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if got := svc.JWKS(); len(got.Keys) != 1 || got.Keys[0].Kid != kid {
		t.Errorf("JWKS = %+v, want single key with kid %q", got.Keys, kid)
	}
}
