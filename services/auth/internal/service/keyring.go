// Package service — keyring.go is the multi-key support for RS256 JWT signing
// + validation. It is the foundation for online JWKS key rotation
// (REDESIGN-001 Phase 6.5): the operator introduces a new key alongside the
// current one, lets the old one drain over the 300 s JWT TTL, then deletes the
// old key file. No restart-with-outage is required.
//
// The ring is intentionally append-only at runtime — once `New` /
// `NewWithFakes` has built the ring, the slice is never mutated. Rotation
// happens by **restarting** the service with a different `JWT_KEY_RING_PATH`
// directory contents. fsnotify-driven hot reload is the natural follow-up and
// lives behind Task 6.9 (mTLS hot reload) — both touch the same lifecycle
// surface and should ship together.
//
// Security rules (CLAUDE.md §7):
//   - We NEVER log private/public PEM bytes at any level (including DEBUG).
//   - Only the `kid` is safe to log — it is, by construction, a public
//     identifier already exposed via the JWKS endpoint.
//   - When `JWT_KEY_RING_PATH` is set but unreadable / empty / contains no
//     valid PEMs, the service MUST fail to start. Falling back to single-key
//     mode would silently degrade the rotation surface and is the exact bug
//     this work item is intended to prevent.
package service

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// signingKey is one entry in the in-memory key ring. Each entry pairs an RSA
// key pair with the `kid` (key ID) used in the JWT header / JWKS document.
//
// Both privateKey and publicKey are required: validation uses publicKey, and
// signing uses privateKey (only for the entry that matches the active signing
// kid). Callers obtain the active signing key via keyRing.signer(); validation
// callers iterate ring.find(kid).
type signingKey struct {
	// kid is the JWT `kid` header value AND the JWKS `kid` field. It must be
	// non-empty and unique within the ring.
	kid string
	// privateKey is the RS256 signing key. Used only for the entry returned by
	// keyRing.signer(); other entries hold it for parity / future rotation
	// flexibility (e.g. promoting an old verify-only key back to signing) but
	// never sign with it during normal operation.
	privateKey *rsa.PrivateKey
	// publicKey is the verification half of the key pair. Embedded in the JWKS
	// document so external validators can verify tokens issued by this kid.
	publicKey *rsa.PublicKey
}

// keyRing is the read-only multi-key container that backs JWT signing and
// validation. It is constructed once at service startup and never mutated
// afterwards — rotation requires a restart (Phase 6.5 scope).
type keyRing struct {
	// keys is the ordered list of all RS256 keys this service knows about.
	// Order matches the order returned by loadKeyRingFromDir (sorted by kid
	// for determinism); callers do not rely on the order except for the
	// fallback validation path, which iterates in slice order.
	keys []signingKey
	// signingKID is the kid of the key used for IssueToken. Always present
	// in the ring — newKeyRing guarantees this.
	signingKID string
}

// newKeyRing builds a ring from a non-empty slice of signingKey entries plus
// the kid that should be used for signing. It validates that:
//   - the ring is non-empty,
//   - the signing kid exists in the ring,
//   - all kids are unique.
//
// Returns an error rather than panicking so the caller (server startup) can
// fail loud with a typed error rather than crashing mid-init.
func newKeyRing(keys []signingKey, signingKID string) (*keyRing, error) {
	if len(keys) == 0 {
		// An empty ring means there is no key to sign with — issuance would
		// always fail. Reject at construction so callers do not have to
		// branch on this case at every IssueToken.
		return nil, errors.New("keyring: at least one key is required")
	}
	if signingKID == "" {
		// Caller must explicitly nominate a signing kid; we never silently
		// pick "the first one" because that's an easy way to ship the wrong
		// key into prod after a directory listing reordering.
		return nil, errors.New("keyring: signing kid must be specified")
	}
	seen := make(map[string]struct{}, len(keys))
	found := false
	for _, k := range keys {
		if k.kid == "" {
			return nil, errors.New("keyring: every entry must have a non-empty kid")
		}
		if k.privateKey == nil || k.publicKey == nil {
			return nil, fmt.Errorf("keyring: kid %q is missing private or public half", k.kid)
		}
		if _, dup := seen[k.kid]; dup {
			return nil, fmt.Errorf("keyring: duplicate kid %q", k.kid)
		}
		seen[k.kid] = struct{}{}
		if k.kid == signingKID {
			found = true
		}
	}
	if !found {
		// The operator nominated a signing kid that does not exist in the
		// ring. This is almost always a typo in `JWT_SIGNING_KID`; fail loud
		// rather than fall through to "use whatever's first".
		return nil, fmt.Errorf("keyring: signing kid %q not present in ring", signingKID)
	}
	return &keyRing{keys: keys, signingKID: signingKID}, nil
}

// signer returns the (kid, *rsa.PrivateKey) that IssueToken should use to
// sign new JWTs. The kid is returned alongside the key so the caller can
// stamp the JWT header with the same value.
//
// Guaranteed to succeed for a ring built via newKeyRing — the constructor
// rejects rings whose signing kid is absent.
func (r *keyRing) signer() (string, *rsa.PrivateKey) {
	for i := range r.keys {
		if r.keys[i].kid == r.signingKID {
			return r.keys[i].kid, r.keys[i].privateKey
		}
	}
	// Unreachable: newKeyRing rejects rings without the signing kid. The
	// panic here is a defensive belt-and-braces — if it ever fires, the ring
	// has been mutated outside the constructor (which the design forbids).
	panic("keyring: signer kid not found — keyring was mutated post-construction")
}

// find returns the public key for the given kid, or nil if no entry matches.
// Validation callers use this to look up the key matching the JWT's `kid`
// header without paying the cost of trying every entry.
//
// Returning nil rather than an error is intentional — the caller decides
// whether absence is a hard failure (e.g. when the JWT carried a kid) or a
// soft signal that triggers the legacy fallback path.
func (r *keyRing) find(kid string) *rsa.PublicKey {
	if kid == "" {
		return nil
	}
	for i := range r.keys {
		if r.keys[i].kid == kid {
			return r.keys[i].publicKey
		}
	}
	return nil
}

// all returns the full key list so the JWKS handler can enumerate every
// public key. The returned slice is a copy of the internal slice header so
// callers cannot mutate the ring through it. The signingKey entries
// themselves still reference the same key material — callers must not mutate
// them.
func (r *keyRing) all() []signingKey {
	out := make([]signingKey, len(r.keys))
	copy(out, r.keys)
	return out
}

// loadKeyRingFromDir scans the given directory for PEM files and returns a
// ring entry per file. The kid is the file's base name with the extension
// stripped. Files with a non-PEM body or duplicate kids cause an error so
// the operator hears about it at startup rather than at the next token-sign
// call.
//
// Supported extensions: ".pem", ".key", ".rsa", or no extension. Other files
// are skipped (so an accidental README.md in the directory does not break
// startup). Hidden files (leading dot) are also skipped to tolerate editor
// swap files.
//
// The directory MUST contain a private key for each kid. A future task may
// allow public-key-only entries (for validators that don't sign), but Phase
// 6.5's scope is the signer service itself, where every kid must be capable
// of signing.
//
// The function does not log key bytes; only kids and file names appear in
// the returned error (when files are invalid).
func loadKeyRingFromDir(dir string) ([]signingKey, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Wrap so the caller's startup log shows the directory path.
		return nil, fmt.Errorf("keyring: read dir %q: %w", dir, err)
	}
	type loaded struct {
		key     signingKey
		modTime time.Time
	}
	var loadedKeys []loaded
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			// Skip editor swap / dotfiles.
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case "", ".pem", ".key", ".rsa":
			// Accepted — fall through to load.
		default:
			// Skip files with unrelated extensions so the operator can
			// keep e.g. README.md or kid-rotation notes in the dir.
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("keyring: read %s: %w", name, err)
		}
		// derive kid from base name minus extension. We never echo the
		// file contents in any error / log.
		kid := strings.TrimSuffix(name, ext)
		if kid == "" {
			return nil, fmt.Errorf("keyring: file %s has empty kid (base name)", name)
		}
		priv, err := parsePrivateKeyPEM(raw)
		if err != nil {
			return nil, fmt.Errorf("keyring: parse private key for kid %q (file %s): %w", kid, name, err)
		}
		info, err := e.Info()
		if err != nil {
			// Skipping mod-time on this entry is harmless — we only use
			// it to break ties when picking a default signing kid. Treat
			// as zero time so the kid sorts to the bottom in that case.
			info = nil
		}
		var mtime time.Time
		if info != nil {
			mtime = info.ModTime()
		}
		loadedKeys = append(loadedKeys, loaded{
			key: signingKey{
				kid:        kid,
				privateKey: priv,
				publicKey:  &priv.PublicKey,
			},
			modTime: mtime,
		})
	}
	if len(loadedKeys) == 0 {
		// An empty directory is almost certainly a misconfiguration — the
		// operator pointed `JWT_KEY_RING_PATH` at the wrong path. Fail
		// loud rather than fall through to "ring is empty, no signing
		// possible".
		return nil, fmt.Errorf("keyring: no PEM files found in %q", dir)
	}
	// Sort by kid for deterministic ring order — useful for both stable
	// test fixtures and for the fallback-validation loop's iteration order
	// (deterministic order means deterministic warn logs).
	sort.Slice(loadedKeys, func(i, j int) bool {
		return loadedKeys[i].key.kid < loadedKeys[j].key.kid
	})
	out := make([]signingKey, len(loadedKeys))
	for i := range loadedKeys {
		out[i] = loadedKeys[i].key
	}
	return out, nil
}

// ── Phase 6.5 — exported surface for the server package ──────────────────────
//
// The server package builds the ring at startup and hands it to the Service
// constructor. We expose just enough of the keyRing type to make that
// possible without leaking the full mutation-sensitive internals.

// KeyRing is the exported alias of keyRing so server.Run can hold a
// *service.KeyRing and pass it to NewWithKeyRing. The internal type stays
// unexported so callers cannot construct one with bare struct literals;
// LoadKeyRing is the only public constructor.
type KeyRing = keyRing

// LoadKeyRing is the public entry point for building a multi-key ring from a
// directory of PEM files. signingKID is the kid that should be used to sign
// new tokens; if empty, defaults to the lexicographically-greatest kid in
// the directory.
//
// Fails fast on every error path — per CLAUDE.md §7, an unreadable or empty
// directory MUST NOT silently fall back to a single-key default.
func LoadKeyRing(dir, signingKID string) (*KeyRing, error) {
	keys, err := loadKeyRingFromDir(dir)
	if err != nil {
		return nil, err
	}
	if signingKID == "" {
		// Operator did not nominate a signing kid; pick the
		// lexicographically-greatest one. Naming conventions like
		// timestamps or ULIDs give automatic promotion of the freshest
		// key on the next restart.
		signingKID = pickDefaultSigningKID(keys)
	}
	return newKeyRing(keys, signingKID)
}

// SigningKID returns the kid the ring uses to sign new tokens. Exported so
// the server can log the active signer at startup for operator visibility.
func (r *keyRing) SigningKID() string {
	if r == nil {
		return ""
	}
	return r.signingKID
}

// Size returns the number of keys in the ring. Exported so the server can
// log the ring size at startup, which is the headline operator-visible
// signal during a rotation window.
func (r *keyRing) Size() int {
	if r == nil {
		return 0
	}
	return len(r.keys)
}

// pickDefaultSigningKID returns the kid that should be used for signing when
// the operator did not nominate one via JWT_SIGNING_KID. We pick the
// lexicographically LAST kid (i.e. the "highest" sorted name) so operators
// can adopt a timestamp / monotonic-id convention and have new keys auto-
// promote to signer on restart. Same input → same output, by design.
//
// Callers should still surface a slog.Info / Warn at startup so the chosen
// kid is visible in the boot log.
func pickDefaultSigningKID(keys []signingKey) string {
	if len(keys) == 0 {
		return ""
	}
	// Caller will have called loadKeyRingFromDir, which sorts by kid
	// ascending; the last element is the lexicographically greatest.
	return keys[len(keys)-1].kid
}
