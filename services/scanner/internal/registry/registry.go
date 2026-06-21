// Package registry discovers, fingerprints, and tracks the scanner adapter
// binaries available to the scanner service at runtime.
//
// Why this exists (REM-011 Phase 2):
//
//   Phase 1 baked two adapter binaries (dev-stub, trivy-adapter) into the
//   scanner image and selected one via the SCANNER_PLUGIN_PATH env var.
//   Changing the active adapter required a container restart with a new
//   env value. Phase 2 lifts that to a runtime selection: this package
//   enumerates every executable scanner-* binary under a known directory,
//   tracks which one is currently active, and lets the gRPC layer swap
//   between them without a process restart.
//
// Concurrency:
//
//   The struct is safe for concurrent reads via List/Active/findByPath.
//   Writes (SetActive, RecordVersion) take a write lock — they happen
//   only from the SetActiveAdapter RPC handler and from the worker pool
//   after each successful scan, both low-frequency callers.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// adapterPrefix is the filename prefix every scanner adapter binary must
// carry. The registry glob looks for "<dir>/<adapterPrefix>*" entries.
// Chosen for Phase 1 compatibility: the existing in-tree dev-stub and
// trivy-adapter binaries are already named scanner-dev-stub and
// scanner-trivy-adapter respectively.
const adapterPrefix = "scanner-"

// DefaultAdapterDir is where the scanner Dockerfile installs adapter
// binaries. Production deployments inherit this; the test suite (and the
// odd local-dev workflow) overrides it via the SCANNER_ADAPTER_DIR env
// var when constructing the Registry.
const DefaultAdapterDir = "/usr/local/bin"

// Adapter is one discovered scanner binary.
//
// Fields are populated at discovery time except Version, which starts as
// "unknown" and is updated by RecordVersion once a successful scan has
// reported a self-version through the JSON-RPC contract. There is no
// separate "version" RPC on the plugin contract — we have to wait for a
// real scan response.
type Adapter struct {
	// Name is the segment of the filename after the "scanner-" prefix.
	// "scanner-trivy-adapter" → "trivy-adapter". Used for human-friendly
	// display + persisted nowhere (path is the canonical identifier).
	Name string
	// Path is the absolute on-disk location of the binary. This is the
	// stable key — multiple binaries with the same Name would collide on
	// the filesystem before they could collide here.
	Path string
	// SizeBytes is the file size at discovery time.
	SizeBytes int64
	// Checksum is the SHA-256 hex of the binary file. Recomputed at every
	// boot so a tampered binary trips immediately (Phase 1 already
	// enforces a checksum match in plugin.New; this duplicate read is
	// cheap given adapters are small static binaries).
	Checksum string
	// EnvKeys lists the env-var names that this adapter will actually see
	// at scan time — pluginEnv()'s allowlist intersected with the
	// container's environment at startup. Surfaced through the API so
	// admins can verify TRIVY_DB_REPOSITORY (etc.) is actually wired.
	EnvKeys []string
	// Version is the adapter's self-reported version, or "unknown" until
	// the first successful scan backfills it via RecordVersion.
	Version string
}

// Registry holds the discovered adapter set and the active-selection pointer.
type Registry struct {
	mu       sync.RWMutex
	adapters []Adapter
	// activePath is the absolute path of the currently active adapter.
	// Empty string when no adapter is active (only possible during boot
	// before SelectInitialActive runs).
	activePath string
}

// Options configures the discovery scan. Zero-value Options uses the
// DefaultAdapterDir and respects SCANNER_ADAPTER_DIR for overrides.
type Options struct {
	// Dir overrides DefaultAdapterDir when non-empty. Used by tests that
	// need to point at a fixture directory under t.TempDir().
	Dir string
	// EnvAllowlist is the static set of env-var names this adapter sees
	// regardless of value (PATH/HOME/etc.). The registry intersects this
	// against os.Environ to produce the per-adapter EnvKeys field.
	EnvAllowlist []string
	// EnvPrefixes are the dynamic env-var prefixes (TRIVY_, GRYPE_) that
	// the orchestrator forwards to the plugin process. Any env var whose
	// name starts with one of these prefixes is reported.
	EnvPrefixes []string
}

// New scans the configured directory and returns a populated Registry.
// Errors only on a hard I/O failure (missing dir, permission denied);
// finding zero adapters in a present directory is allowed and surfaces
// as a Registry with an empty adapter list — discovery is informational.
func New(opts Options) (*Registry, error) {
	dir := opts.Dir
	if dir == "" {
		// Env override takes precedence over the compiled-in default so
		// integration tests can swap the path without recompiling.
		if env := os.Getenv("SCANNER_ADAPTER_DIR"); env != "" {
			dir = env
		} else {
			dir = DefaultAdapterDir
		}
	}

	entries, err := filepath.Glob(filepath.Join(dir, adapterPrefix+"*"))
	if err != nil {
		// filepath.Glob only errors on a malformed pattern, which is a
		// programmer mistake — bubble it up so it crashes the boot.
		return nil, fmt.Errorf("glob %q: %w", dir, err)
	}
	sort.Strings(entries) // stable order regardless of FS iteration

	envKeys := computeEnvKeys(opts.EnvAllowlist, opts.EnvPrefixes)

	out := make([]Adapter, 0, len(entries))
	for _, path := range entries {
		info, statErr := os.Stat(path)
		if statErr != nil {
			// A racing rm between Glob and Stat is rare but harmless to
			// skip — better than crashing the whole service.
			continue
		}
		if info.IsDir() {
			continue
		}
		// On Unix we'd check the exec bit; Windows has no real exec
		// concept and the test fixtures use plain files. Stick with the
		// presence-of-file check + the bake-time guarantee that the
		// Dockerfile installs adapters as +x.
		sum, err := fileSHA256(path)
		if err != nil {
			// Surface checksum failures up — the operator needs to fix
			// the broken binary, not silently ignore it.
			return nil, fmt.Errorf("checksum %q: %w", path, err)
		}
		out = append(out, Adapter{
			Name:      deriveName(filepath.Base(path)),
			Path:      path,
			SizeBytes: info.Size(),
			Checksum:  sum,
			EnvKeys:   envKeys,
			Version:   "unknown",
		})
	}
	return &Registry{adapters: out}, nil
}

// deriveName strips the well-known prefix to produce a short human label.
// Empty results fall back to the original filename so the UI never has to
// render an empty Name field.
func deriveName(filename string) string {
	n := strings.TrimPrefix(filename, adapterPrefix)
	if n == "" {
		return filename
	}
	return n
}

// computeEnvKeys returns the sorted, deduplicated set of env-var names
// the adapter will see at scan time, based on the orchestrator's
// allowlist + prefix policy + the current process environment.
func computeEnvKeys(allowlist, prefixes []string) []string {
	seen := map[string]struct{}{}
	// Static allowlist is added unconditionally — these are the
	// "always forwarded if present" keys (PATH, HOME, etc.).
	for _, k := range allowlist {
		if _, ok := os.LookupEnv(k); ok {
			seen[k] = struct{}{}
		}
	}
	// Dynamic prefix sweep picks up TRIVY_DB_REPOSITORY etc.
	for _, e := range os.Environ() {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				seen[key] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// List returns a snapshot of the discovered adapters. The slice is a
// copy so callers can mutate it freely; the embedded EnvKeys slice is
// shared (immutable in practice).
func (r *Registry) List() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, len(r.adapters))
	copy(out, r.adapters)
	return out
}

// Active returns the currently active adapter or nil when none is set.
// Returns a pointer so the caller can distinguish "no active adapter"
// from "active adapter has all zero fields", which would otherwise be
// impossible to express in a value return.
func (r *Registry) Active() *Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.activePath == "" {
		return nil
	}
	for i := range r.adapters {
		if r.adapters[i].Path == r.activePath {
			a := r.adapters[i]
			return &a
		}
	}
	return nil
}

// ActivePath returns just the path of the active adapter; cheap helper
// that avoids the slice copy of List() when the caller only needs the
// identifier (e.g. for persistence into scanner_settings).
func (r *Registry) ActivePath() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activePath
}

// SetActive marks path as the active adapter. Returns an error when path
// is not in the registry; the swap is rejected so the gRPC layer can
// surface InvalidArgument without an orphan write.
func (r *Registry) SetActive(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.adapters {
		if r.adapters[i].Path == path {
			r.activePath = path
			return nil
		}
	}
	return fmt.Errorf("adapter %q not in registry", path)
}

// RecordVersion backfills the self-reported version for an adapter, keyed
// by Name (not Path) so different deployment paths of the same logical
// adapter share the cached version. Silently noops when the name isn't
// in the registry — the worker pool calls this on every scan and we
// don't want to fail a scan because a binary was removed mid-flight.
func (r *Registry) RecordVersion(name, version string) {
	if name == "" || version == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.adapters {
		if r.adapters[i].Name == name {
			r.adapters[i].Version = version
		}
	}
}

// FindByPath returns a copy of the adapter at path, or nil when absent.
// Used by the SetActiveAdapter handler to validate the request path
// before invoking SetActive.
func (r *Registry) FindByPath(path string) *Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.adapters {
		if r.adapters[i].Path == path {
			a := r.adapters[i]
			return &a
		}
	}
	return nil
}

// fileSHA256 returns the lowercase hex SHA256 of the file at path.
// Duplicated from plugin/process.go intentionally — the registry must
// not depend on the plugin package (the plugin package's checksum check
// is per-scan defensive; ours is for fingerprinting at boot).
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
