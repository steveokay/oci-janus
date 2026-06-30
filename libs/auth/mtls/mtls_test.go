package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// genLeafPEM generates a self-signed ECDSA P-256 leaf cert with the given
// SerialNumber + SAN and returns the PEM-encoded cert + key bytes. ECDSA
// chosen over RSA so the per-cert generation cost in tests stays under a
// millisecond.
func genLeafPEM(t *testing.T, serial int64, dnsName string) (certPEM, keyPEM []byte) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ec key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: dnsName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{dnsName},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ec key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// writeCertPair writes cert + key + (cert again, reused as CA) into the given
// directory. Returns the file paths. We use the leaf cert itself as the CA
// pool because the reload tests only exercise the cert-side hot-swap; the CA
// pool is intentionally static (see package doc).
func writeCertPair(t *testing.T, dir, name string, serial int64) (caPath, certPath, keyPath string) {
	t.Helper()

	certPEM, keyPEM := genLeafPEM(t, serial, name)
	certPath = filepath.Join(dir, name+".crt")
	keyPath = filepath.Join(dir, name+".key")
	caPath = filepath.Join(dir, name+".ca")

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return caPath, certPath, keyPath
}

// overwriteCertPair rewrites the cert + key files in place with a new cert
// pair (different SerialNumber). To make the test resilient to coarse
// filesystem mtime granularity (Windows NTFS = 100ns, but virtualised CI runs
// can effectively coalesce within the same second), we explicitly bump the
// mtime forward by 2 seconds via os.Chtimes.
func overwriteCertPair(t *testing.T, certPath, keyPath string, serial int64, dnsName string) {
	t.Helper()

	certPEM, keyPEM := genLeafPEM(t, serial, dnsName)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("overwrite cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("overwrite key: %v", err)
	}
	// Bump mtime forward deterministically — see comment above.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(certPath, future, future); err != nil {
		t.Fatalf("chtimes cert: %v", err)
	}
	if err := os.Chtimes(keyPath, future, future); err != nil {
		t.Fatalf("chtimes key: %v", err)
	}
}

// serialOf extracts the SerialNumber from a tls.Certificate by parsing its
// first DER-encoded leaf. We use the serial as the cheap discriminator
// between cert generations in the reload tests.
func serialOf(t *testing.T, cert *tls.Certificate) int64 {
	t.Helper()
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatalf("nil/empty tls.Certificate")
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return parsed.SerialNumber.Int64()
}

// TestReloadingServerTLSConfig_ReloadsOnMtimeChange is the core hot-reload
// regression test: build a config, observe cert A, swap the files to cert B
// (with a mtime bump), observe cert B. If GetCertificate had baked the
// initial cert in statically (the pre-Phase-6.9 behaviour), we'd still see
// cert A after rotation.
func TestReloadingServerTLSConfig_ReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, keyPath := writeCertPair(t, dir, "rotate-server", 1001)

	cfg, err := ReloadingServerTLSConfig(caPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("ReloadingServerTLSConfig: %v", err)
	}
	if cfg.GetCertificate == nil {
		t.Fatalf("GetCertificate not set — config would not hot-reload")
	}

	// First handshake: expect cert A (serial 1001).
	first, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("first GetCertificate: %v", err)
	}
	if got := serialOf(t, first); got != 1001 {
		t.Fatalf("first cert serial = %d, want 1001", got)
	}

	// Cached handshake (no file change): expect same cert returned, no
	// re-read. We don't directly assert the cache hit but the second call
	// should return the same *tls.Certificate pointer.
	second, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("second GetCertificate: %v", err)
	}
	if first != second {
		t.Fatalf("expected cached cert to be returned by pointer identity")
	}

	// Rotate cert + key on disk with a fresh serial + bumped mtime.
	overwriteCertPair(t, certPath, keyPath, 2002, "rotate-server")

	// Post-rotation handshake: expect cert B (serial 2002).
	third, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("third GetCertificate: %v", err)
	}
	if got := serialOf(t, third); got != 2002 {
		t.Fatalf("post-rotation cert serial = %d, want 2002", got)
	}
	if first == third {
		t.Fatalf("expected new cert pointer after rotation")
	}
}

// TestReloadingClientTLSConfig_ReloadsOnMtimeChange mirrors the server test
// for the client-side GetClientCertificate path. The two paths share certCache
// but the closure wiring differs, so we exercise both.
func TestReloadingClientTLSConfig_ReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, keyPath := writeCertPair(t, dir, "rotate-client", 3003)

	cfg, err := ReloadingClientTLSConfig(caPath, certPath, keyPath, "rotate-client")
	if err != nil {
		t.Fatalf("ReloadingClientTLSConfig: %v", err)
	}
	if cfg.GetClientCertificate == nil {
		t.Fatalf("GetClientCertificate not set — config would not hot-reload")
	}
	if cfg.ServerName != "rotate-client" {
		t.Fatalf("ServerName = %q, want %q", cfg.ServerName, "rotate-client")
	}

	first, err := cfg.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("first GetClientCertificate: %v", err)
	}
	if got := serialOf(t, first); got != 3003 {
		t.Fatalf("first cert serial = %d, want 3003", got)
	}

	overwriteCertPair(t, certPath, keyPath, 4004, "rotate-client")

	second, err := cfg.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("post-rotation GetClientCertificate: %v", err)
	}
	if got := serialOf(t, second); got != 4004 {
		t.Fatalf("post-rotation cert serial = %d, want 4004", got)
	}
}

// TestServerTLSConfig_DelegatesToReloading verifies the public ServerTLSConfig
// API now picks up hot reload — important because every service in the
// platform calls ServerTLSConfig directly, not the new Reloading variant.
func TestServerTLSConfig_DelegatesToReloading(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, keyPath := writeCertPair(t, dir, "delegate-server", 5005)

	cfg, err := ServerTLSConfig(caPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.GetCertificate == nil {
		t.Fatalf("ServerTLSConfig should now wire GetCertificate for hot reload")
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("expected empty Certificates slice when GetCertificate is set, got %d entries", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3 (PENTEST-012)", cfg.MinVersion)
	}
}

// TestClientTLSConfig_DelegatesToReloading mirrors the server delegation
// check on the client side.
func TestClientTLSConfig_DelegatesToReloading(t *testing.T) {
	dir := t.TempDir()
	caPath, certPath, keyPath := writeCertPair(t, dir, "delegate-client", 6006)

	cfg, err := ClientTLSConfig(caPath, certPath, keyPath, "delegate-client")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	if cfg.GetClientCertificate == nil {
		t.Fatalf("ClientTLSConfig should now wire GetClientCertificate for hot reload")
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("expected empty Certificates slice when GetClientCertificate is set, got %d entries", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3 (PENTEST-012)", cfg.MinVersion)
	}
}

// TestReloadingServerTLSConfig_FailLoudOnBadInitialConfig verifies the
// constructor still performs an eager load so misconfiguration (missing cert
// path) surfaces at startup, not at first handshake.
func TestReloadingServerTLSConfig_FailLoudOnBadInitialConfig(t *testing.T) {
	dir := t.TempDir()
	caPath, _, _ := writeCertPair(t, dir, "exists", 7007)

	missingCert := filepath.Join(dir, "does-not-exist.crt")
	missingKey := filepath.Join(dir, "does-not-exist.key")

	if _, err := ReloadingServerTLSConfig(caPath, missingCert, missingKey); err == nil {
		t.Fatalf("expected error for missing cert/key, got nil")
	}
}

// TestReloadingServerTLSConfig_BadCAPath verifies the CA pool error surfaces
// at construction time too (the CA is not reloaded, but it must load
// successfully once).
func TestReloadingServerTLSConfig_BadCAPath(t *testing.T) {
	dir := t.TempDir()
	_, certPath, keyPath := writeCertPair(t, dir, "ok", 8008)

	missingCA := filepath.Join(dir, "does-not-exist.ca")
	if _, err := ReloadingServerTLSConfig(missingCA, certPath, keyPath); err == nil {
		t.Fatalf("expected error for missing CA, got nil")
	}
}

// TestCertCache_StatFailureFallsBackToCached verifies the resilience clause
// in certCache.current(): if the cert file disappears mid-flight (e.g. during
// a cert-manager rename window), we serve the cached cert rather than
// breaking handshakes.
func TestCertCache_StatFailureFallsBackToCached(t *testing.T) {
	dir := t.TempDir()
	_, certPath, keyPath := writeCertPair(t, dir, "transient", 9009)

	cache, err := newCertCache(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertCache: %v", err)
	}
	first, err := cache.current()
	if err != nil {
		t.Fatalf("first current: %v", err)
	}

	// Remove the cert file — stat will fail on the next call.
	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove cert: %v", err)
	}

	second, err := cache.current()
	if err != nil {
		t.Fatalf("current after cert removal: %v (expected cached fallback)", err)
	}
	if first != second {
		t.Fatalf("expected cached cert pointer on stat failure")
	}
}
