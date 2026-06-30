// Package mtls provides helpers for constructing mTLS tls.Config values used
// by all internal gRPC clients and servers. Every service-to-service call in the
// registry requires mutual certificate verification — no unauthenticated gRPC.
//
// REDESIGN-001 Phase 6.9 (2026-06-30) — hot reload.
//
// Every TLS config returned from this package supports cert-manager-style
// in-place certificate rotation without a service restart. The cert files are
// re-read on the next TLS handshake after their on-disk mtime changes; an
// in-memory cache keyed on (mtime, size) keeps the steady-state handshake cost
// at one stat() per connection rather than a full file read + PEM parse.
//
// We intentionally do NOT pull in fsnotify here:
//   - One extra dep + one extra goroutine per process for a benefit measured
//     in single-digit seconds (handshake-after-renewal latency).
//   - cert-manager renews well before expiry (default: 2/3 of cert lifetime),
//     so a long-idle gRPC connection picking up the new cert at the *next*
//     handshake — rather than the *moment* the file changed — is fine.
//   - The mtime check happens inside GetCertificate / GetClientCertificate, so
//     a flurry of new connections immediately after rotation reloads the cert
//     exactly once (mutex-guarded) and all subsequent handshakes hit the cache.
//
// Tradeoff documented: a connection that's been idle since before rotation
// will not see the new cert until it next handshakes (i.e. either reconnects
// or completes its keepalive cycle). That's the trade we accept for a no-dep,
// no-goroutine, no-shutdown-coordination implementation.
//
// SEC-046 — revocation caveat. On reload failure (malformed PEM, stat error,
// EOF mid-write) the cache deliberately falls back to the LAST GOOD cert
// rather than breaking the handshake. This is the right choice for the common
// case (cert-manager renewal hits an FS glitch) but it is the WRONG channel
// for emergency cert revocation — if you rotate specifically because a leaf
// cert is suspected compromised, the cached cert will continue to be served
// until either the process restarts OR the new file successfully parses on a
// subsequent handshake. Real revocation must flow through the CA pool (CRL /
// OCSP), not through deleting a leaf cert file from disk. The fallback path
// emits slog.Warn so a stuck rotation is at least visible in the log; if you
// need it visible in metrics, file a follow-up to add an
// `mtls_reload_failure_total` counter.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// certCache holds the most recently parsed cert pair together with the file
// fingerprint (mtime + size) used to decide whether to re-parse on the next
// handshake. One certCache instance is created per ReloadingServerTLSConfig /
// ReloadingClientTLSConfig invocation and captured in the GetCertificate
// closure.
//
// mtime granularity on Linux/macOS ext4/apfs is nanosecond; on Windows NTFS
// it is 100ns. Either is finer than any realistic cert rotation cadence, so we
// rely on mtime directly rather than hashing file contents. We additionally
// include file size in the fingerprint so that the rare case of "two writes
// within the FS timestamp granularity" still gets caught (cert.pem size
// changes with the new SerialNumber and signature length).
type certCache struct {
	mu sync.Mutex

	// certPath / keyPath are captured from the constructor; the closure
	// re-reads them on every fingerprint mismatch.
	certPath string
	keyPath  string

	// fp is the file fingerprint of the *cert* file at the time `cert` was
	// loaded. We only stat the cert file (not the key) on the fast path —
	// the key is re-read alongside the cert if (and only if) the fingerprint
	// changes. cert-manager rewrites both atomically (rename(2)) so a stale
	// key paired with a fresh cert is not a realistic failure mode.
	fp fileFingerprint

	// cert is the most recently parsed tls.Certificate. Returned by value
	// (via pointer copy) to all handshake callers between rotations.
	cert *tls.Certificate
}

// fileFingerprint captures the cert file's mtime + size. Two writes that
// produce the same fingerprint are treated as no-op (the cached cert is
// reused). See certCache for the size-as-tiebreaker rationale.
type fileFingerprint struct {
	modTime time.Time
	size    int64
}

// load reads the cert pair from disk and parses it into a tls.Certificate.
// Called once at construction time (to fail-loud on bad config) and again on
// every fingerprint mismatch detected during a handshake.
func (c *certCache) load() (*tls.Certificate, fileFingerprint, error) {
	// Stat the cert file BEFORE reading so the fingerprint we cache is
	// consistent with the bytes we just parsed. If a writer races us between
	// stat and read, the next handshake will see a fingerprint mismatch and
	// reload; correctness is preserved.
	info, err := os.Stat(c.certPath)
	if err != nil {
		return nil, fileFingerprint{}, fmt.Errorf("stat cert: %w", err)
	}
	fp := fileFingerprint{modTime: info.ModTime(), size: info.Size()}

	cert, err := tls.LoadX509KeyPair(c.certPath, c.keyPath)
	if err != nil {
		return nil, fileFingerprint{}, fmt.Errorf("load cert/key: %w", err)
	}
	return &cert, fp, nil
}

// current returns the cached cert if the on-disk fingerprint is unchanged, or
// re-reads + re-parses the cert pair if it has. The mutex serialises
// concurrent reloaders so that N parallel handshakes after a rotation produce
// exactly one disk read.
func (c *certCache) current() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Fast path: stat the cert file, compare against the cached fingerprint.
	// A failed stat is treated as "use cached cert" so that a transient FS
	// hiccup (e.g. cert-manager mid-rename) does not break in-flight
	// handshakes. If the cert file is permanently gone, the cached cert
	// remains valid until its NotAfter; observability surfaces it via the
	// usual TLS expiry alerting.
	if info, err := os.Stat(c.certPath); err == nil {
		newFp := fileFingerprint{modTime: info.ModTime(), size: info.Size()}
		if newFp == c.fp && c.cert != nil {
			return c.cert, nil
		}
	} else if c.cert != nil {
		// Stat failed but we have a cached cert; serve it.
		// Log at WARN so operators can correlate "I deleted the cert file"
		// or "cert-manager hit a permission error mid-rotation" with
		// continued serving of an old cert. Code-review-agent follow-up
		// on the 6.9 batch.
		slog.Warn("mtls: cert stat failed; serving cached certificate",
			"cert_path", c.certPath,
			"err", err,
		)
		return c.cert, nil
	} else {
		// Stat failed AND no cached cert — the caller will get the raw
		// error back from load() below, but log here so the operator can
		// see "cert file went away before the first successful read".
		slog.Warn("mtls: cert stat failed and no cached certificate available",
			"cert_path", c.certPath,
			"err", err,
		)
	}

	// Slow path: fingerprint changed (or no cache yet) — reload.
	cert, fp, err := c.load()
	if err != nil {
		// Reload failed: prefer to keep serving the cached cert (if any)
		// over breaking the handshake. cert-manager's atomic rename should
		// make this impossible, but defence in depth.
		if c.cert != nil {
			// Code-review-agent follow-up on the 6.9 batch — surface the
			// "silently served stale cert" event at WARN so operators can
			// debug a stuck rotation. Without this log, a malformed
			// renewal looks identical to a successful one until the cert
			// hits NotAfter.
			slog.Warn("mtls: cert reload failed; serving cached certificate",
				"cert_path", c.certPath,
				"err", err,
			)
			return c.cert, nil
		}
		return nil, err
	}
	c.cert = cert
	c.fp = fp
	return cert, nil
}

// newCertCache constructs a certCache, performing the initial cert load so
// that obvious misconfiguration (missing files, bad PEM, mismatched key) fails
// at startup rather than on the first inbound connection.
func newCertCache(certPath, keyPath string) (*certCache, error) {
	c := &certCache{certPath: certPath, keyPath: keyPath}
	cert, fp, err := c.load()
	if err != nil {
		return nil, err
	}
	c.cert = cert
	c.fp = fp
	return c, nil
}

// loadCAPool reads a PEM-encoded CA bundle from disk and returns it as an
// x509.CertPool. The CA pool is intentionally NOT hot-reloaded — operators
// rotate the internal CA on a much longer cadence than leaf certs (typically
// years vs days), and a per-handshake CA reload would either need its own
// cache layer or pay full parse cost on every connection. When the CA needs
// to rotate, services restart.
func loadCAPool(caCertPath string) (*x509.CertPool, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert")
	}
	return pool, nil
}

// ReloadingServerTLSConfig returns a tls.Config for gRPC servers that require
// and verify client certs, with the server-leaf cert hot-reloaded on every
// handshake via GetCertificate. See package doc for the mtime-cache rationale.
//
// Returned tls.Config sets Certificates to nil and GetCertificate to a
// reloading closure; this is the standard pattern for ServerHelloDone-time
// cert resolution.
func ReloadingServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error) {
	pool, err := loadCAPool(caCertPath)
	if err != nil {
		return nil, err
	}
	cache, err := newCertCache(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	return &tls.Config{
		// Certificates is left nil; GetCertificate handles every handshake.
		// crypto/tls falls back to Certificates only when GetCertificate is
		// nil, so leaving it empty is correct.
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return cache.current()
		},
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		// PENTEST-012: TLS 1.3 minimum for all internal mTLS. TLS 1.3 mandates
		// forward secrecy + AEAD-only cipher suites and removes legacy
		// renegotiation. There are no external clients on these gRPC ports
		// (all calls are service-to-service inside the cluster), so backwards
		// compatibility with TLS 1.2-only clients is a non-issue.
		MinVersion: tls.VersionTLS13,
	}, nil
}

// ServerTLSConfig returns a tls.Config for gRPC servers that require and verify
// client certs.
//
// REDESIGN-001 Phase 6.9: this is now a thin wrapper around
// ReloadingServerTLSConfig — every server in the platform gets hot reload by
// default. The signature is preserved so existing callers compile unchanged.
// We made universal reload opt-out rather than opt-in because the cost is
// near-zero (one extra stat() per handshake) and forgetting to opt in is a
// silent reliability bug at cert renewal time.
func ServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error) {
	return ReloadingServerTLSConfig(caCertPath, certPath, keyPath)
}

// ReloadingClientTLSConfig returns a tls.Config for gRPC clients with the
// client-leaf cert hot-reloaded on every handshake via GetClientCertificate.
// See package doc for the mtime-cache rationale.
//
// serverName must match the expected SAN/CN on the remote server's cert
// (e.g. "registry-tenant").
func ReloadingClientTLSConfig(caCertPath, certPath, keyPath, serverName string) (*tls.Config, error) {
	pool, err := loadCAPool(caCertPath)
	if err != nil {
		return nil, err
	}
	cache, err := newCertCache(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	return &tls.Config{
		// Certificates is left nil; GetClientCertificate handles every
		// handshake. crypto/tls invokes GetClientCertificate first when set,
		// so leaving Certificates empty is correct.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return cache.current()
		},
		RootCAs:    pool,
		ServerName: serverName,
		// PENTEST-012: TLS 1.3 minimum for all internal mTLS. See
		// ReloadingServerTLSConfig for full rationale.
		MinVersion: tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig returns a tls.Config for gRPC clients presenting a cert.
//
// REDESIGN-001 Phase 6.9: this is now a thin wrapper around
// ReloadingClientTLSConfig — every client dial in the platform gets hot
// reload by default. See ServerTLSConfig for the opt-out-vs-opt-in rationale.
func ClientTLSConfig(caCertPath, certPath, keyPath, serverName string) (*tls.Config, error) {
	return ReloadingClientTLSConfig(caCertPath, certPath, keyPath, serverName)
}

// ClientCreds returns gRPC TransportCredentials for outbound dials.
//
// REDESIGN-001 Phase 3.4 rule-of-three extraction. Previously duplicated as
// `buildClientCreds` in services/auth and services/metadata; lifted here so
// the remaining services don't copy-paste.
//
// When all three cert paths are configured, builds the standard mTLS
// credentials via ClientTLSConfig + credentials.NewTLS. When any path is
// empty (typical dev compose stack without certs), returns insecure
// credentials — production-mode startup must reject this via
// libs/config/loader.ValidateMTLSConfig, which is the layer that
// distinguishes "dev fallback" from "missing config in prod."
//
// serverName must match the expected SAN/CN on the remote server's cert
// (e.g. "registry-tenant"). In dev (insecure) mode it's ignored.
func ClientCreds(caCertPath, certPath, keyPath, serverName string) (credentials.TransportCredentials, error) {
	if caCertPath == "" || certPath == "" || keyPath == "" {
		return insecure.NewCredentials(), nil
	}
	tlsCfg, err := ClientTLSConfig(caCertPath, certPath, keyPath, serverName)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(tlsCfg), nil
}
