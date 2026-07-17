# Scanner Engine Decoupling (Phase 1: trivy) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the Trivy engine CLI + its vuln DB out of the `registry-scanner` image into an independently-versioned `trivy-engine` sidecar, so bumping Trivy no longer requires rebuilding the scanner.

**Architecture:** A new stdlib HTTP wrapper (`scanner-engine-server`) is baked into a thin `trivy-engine` image (`FROM aquasec/trivy:<pin>` + wrapper). The scanner's `trivy-adapter` shim keeps flatten+translate but replaces `exec trivy` with an HTTP POST to the sidecar; the flattened rootfs is handed over on a read-only shared `scan_work` volume. Trust root for the engine shifts from binary-checksum to image-digest pinning. A new `engine_unreachable` error and an additive health field make the new failure mode observable on `/settings/scanning`.

**Tech Stack:** Go 1.25.11 (stdlib-only adapters/wrapper), Docker multi-stage, docker-compose, Helm, buf/protobuf, React/TypeScript (Vitest).

**Spec:** `docs/superpowers/specs/2026-07-17-scanner-engine-decoupling-design.md`

**Scope note:** This plan is **Phase 1 (trivy only)** — a complete, shippable deliverable (Trivy scanning end-to-end through the sidecar). **Phase 2 (grype)** is mechanical reuse of the same wrapper and will be authored as a separate follow-on plan once Phase 1's final shape is proven. Until Phase 2 lands, the grype-adapter keeps `exec`ing the baked grype binary — so the scanner Dockerfile removes only the Trivy stage in Phase 1, not the Grype stage.

**Conventions for the executor:**
- All adapter/wrapper binaries are **stdlib-only**, their own `go.mod`, built with `GOWORK=off` (they are outside the `go.work` workspace — see `services/scanner/Dockerfile`).
- Run scanner Go tests with `GOWORK=off` from the module dir: `cd services/scanner && GOWORK=off go test ./...`.
- Adapter tests run from the adapter dir: `cd infra/scanner-plugins/<name> && GOWORK=off go test ./...`.
- Commit after every green step. Never commit to `main` (already on branch `feat/scanner-engine-decoupling`).

---

## File Structure

**Create:**
- `infra/scanner-plugins/engine-server/go.mod` — module for the shared wrapper.
- `infra/scanner-plugins/engine-server/main.go` — the HTTP wrapper (`POST /scan`, `GET /healthz`, exec engine).
- `infra/scanner-plugins/engine-server/main_test.go` — wrapper unit tests (hermetic, shell-stub engine).
- `infra/scanner-plugins/engine-server/Dockerfile` — thin sidecar image, `ENGINE` build-arg selects trivy/grype base.
- `infra/scanner-plugins/engine-server/engine-entrypoint.sh` — DB pre-warm, moved from the scanner entrypoint.
- `.github/workflows/ci-scanner-engines.yml` — build + test the wrapper and engine image.
- `infra/helm/registry/charts/scanner/templates/engine-deployment.yaml` — trivy-engine Deployment + Service.

**Modify:**
- `services/scanner/internal/plugin/process.go:244` — add `SCANNER_` to the env allowlist prefixes.
- `infra/scanner-plugins/trivy-adapter/main.go` — replace `exec trivy` with HTTP POST to the engine sidecar; flatten onto the shared work dir; add `engine_unreachable` error.
- `infra/scanner-plugins/trivy-adapter/main_test.go` — tests for the HTTP-call branch + unreachable.
- `services/scanner/Dockerfile` — remove the Trivy stage, Trivy binary COPY, Trivy cache env/volume; add `/scan-work`.
- `services/scanner/scripts/entrypoint.sh` — remove the Trivy pre-warm block (moves to the sidecar).
- `infra/docker-compose/docker-compose.yml` — add `trivy-engine` service + `scan_work` volume + scanner env/mount.
- `proto/scanner/v1/scanner.proto:540` — additive `ScannerHealthResponse` engine-reachability fields.
- `services/scanner/internal/handler/grpc.go:1161` — probe the active adapter's engine in `GetScannerHealth`.
- `services/scanner/internal/handler/grpc_test.go` — test the probe branch.
- `frontend/src/components/admin/scanner/scanner-health-card.tsx` — render the "engine unreachable" degraded badge.
- `frontend/src/lib/api/admin-scanners.ts` — extend the health type.
- `docs/SERVICES.md` (scanner section) + trackers (`status.md`, `status-tracker.md`, `futures.md`).

---

## Task 1: The wrapper program — `scanner-engine-server`

The wrapper is one shared stdlib HTTP server. It execs an engine CLI named by `ENGINE_CMD`, on a `rootfs` path validated to sit under the read-only shared mount, and returns the engine's raw JSON.

**Files:**
- Create: `infra/scanner-plugins/engine-server/go.mod`
- Create: `infra/scanner-plugins/engine-server/main.go`
- Test: `infra/scanner-plugins/engine-server/main_test.go`

- [ ] **Step 1: Create the module file**

Create `infra/scanner-plugins/engine-server/go.mod`:

```
module github.com/steveokay/oci-janus/infra/scanner-plugins/engine-server

// Single-file HTTP wrapper using only the standard library. Pinned to the
// same toolchain as the rest of the monorepo so cross-builds in the engine
// sidecar image stay reproducible. Deliberately OUTSIDE go.work (GOWORK=off
// builds) like the adapter shims.
go 1.25.11
```

- [ ] **Step 2: Write the failing test**

Create `infra/scanner-plugins/engine-server/main_test.go`. These tests use a shell/script stub as the engine so they never need a real trivy binary.

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeStubEngine writes an executable stub that mimics the tiny slice of
// engine CLI behaviour the wrapper depends on: it prints a canned JSON blob
// for a `rootfs` scan and a version string for `--version`. Skips on Windows
// where the exec-bit + shebang model doesn't apply (CI runs Linux).
func writeStubEngine(t *testing.T, body string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub engine relies on a POSIX shebang; CI runs Linux")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "stub-engine")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'Version: 9.9.9'; exit 0; fi\n" +
		"cat <<'EOF'\n" + body + "\nEOF\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return p
}

func itoa(n int) string { return strings.TrimSpace(string(rune('0'+n))) } // single-digit exit codes only

func newTestServer(t *testing.T, enginePath, workDir string) *server {
	t.Helper()
	return &server{
		engineCmd:  enginePath,
		engineName: "stub",
		scanArgs:   func(rootfs string) []string { return []string{"rootfs", rootfs} },
		workDir:    workDir,
	}
}

func TestScan_HappyPath(t *testing.T) {
	work := t.TempDir()
	rootfs := filepath.Join(work, "abc", "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	engine := writeStubEngine(t, `{"Results":[]}`, 0)
	s := newTestServer(t, engine, work)

	rr := httptest.NewRecorder()
	req := postScan(t, map[string]string{"scan_id": "abc", "rootfs": rootfs})
	s.handleScan(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp scanResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Engine != "stub" || resp.Version != "9.9.9" {
		t.Fatalf("bad meta: %+v", resp)
	}
	if !strings.Contains(string(resp.Raw), `"Results"`) {
		t.Fatalf("raw not passed through: %s", resp.Raw)
	}
}

func TestScan_EngineNonZero_Returns5xx(t *testing.T) {
	work := t.TempDir()
	rootfs := filepath.Join(work, "abc", "rootfs")
	_ = os.MkdirAll(rootfs, 0o755)
	engine := writeStubEngine(t, `boom on stderr`, 2)
	s := newTestServer(t, engine, work)

	rr := httptest.NewRecorder()
	s.handleScan(rr, postScan(t, map[string]string{"scan_id": "abc", "rootfs": rootfs}))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "error") {
		t.Fatalf("want error body, got %s", rr.Body.String())
	}
}

func TestScan_RootfsOutsideWorkDir_Rejected(t *testing.T) {
	work := t.TempDir()
	engine := writeStubEngine(t, `{}`, 0)
	s := newTestServer(t, engine, work)

	rr := httptest.NewRecorder()
	// Path traversal: escape the work dir.
	s.handleScan(rr, postScan(t, map[string]string{"scan_id": "x", "rootfs": "/etc"}))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for out-of-tree rootfs, got %d", rr.Code)
	}
}

func TestHealthz_OK(t *testing.T) {
	engine := writeStubEngine(t, `{}`, 0)
	s := newTestServer(t, engine, t.TempDir())
	rr := httptest.NewRecorder()
	s.handleHealthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func postScan(t *testing.T, body map[string]string) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	return httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader(string(b)))
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd infra/scanner-plugins/engine-server && GOWORK=off go test ./...`
Expected: FAIL — `undefined: server`, `undefined: scanResponse`, etc.

- [ ] **Step 4: Write the wrapper implementation**

Create `infra/scanner-plugins/engine-server/main.go`:

```go
// Package main is the shared scanner-engine wrapper for OCI-Janus.
//
// It runs as a sidecar next to registry-scanner, one instance per engine
// (trivy-engine, grype-engine). The scanner's adapter shim POSTs a scan
// request naming a rootfs path on a shared read-only volume; this wrapper
// execs the engine CLI on that path and returns the engine's raw JSON.
//
// This is the network half of the "engine decoupling" design: bumping the
// engine version means rebuilding THIS small image, never registry-scanner.
// The wrapper is deliberately stdlib-only + single-file, mirroring the
// adapter shims, so an engine bump can never break on a libs/ change.
//
// Endpoints:
//
//	POST /scan     {scan_id, rootfs}     -> {engine, version, raw} | {error}
//	GET  /healthz                        -> 200 when `<engine> --version` works
//
// Security: no auth (binds only on the private `registry` network, no host
// port). The rootfs path is validated to sit under $ENGINE_WORK_DIR, which
// is mounted read-only here — mirrors the clair-adapter's traversal guards.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// scanRequest is the wrapper's POST /scan body. rootfs is an absolute path
// on the shared work volume; scan_id is echoed only for log correlation.
type scanRequest struct {
	ScanID string `json:"scan_id"`
	Rootfs string `json:"rootfs"`
}

// scanResponse is the success envelope. Raw is the engine's native JSON,
// passed through untouched so the shim's existing translate step is the
// single source of truth for the finding shape.
type scanResponse struct {
	Engine  string          `json:"engine"`
	Version string          `json:"version"`
	Raw     json.RawMessage `json:"raw"`
}

// errorResponse is the failure envelope for any non-200.
type errorResponse struct {
	Error string `json:"error"`
}

// server holds the resolved engine configuration. scanArgs builds the argv
// for a rootfs scan so trivy and grype differ only in this closure.
type server struct {
	engineCmd  string                    // absolute path to the engine binary
	engineName string                    // "trivy" | "grype" (for the response)
	scanArgs   func(rootfs string) []string
	workDir    string                    // shared mount; rootfs must live under it
}

func main() {
	s, addr, err := serverFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine-server: config error: %v\n", err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/scan", s.handleScan)
	mux.HandleFunc("/healthz", s.handleHealthz)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "engine-server: %s engine on %s (workdir=%s)\n", s.engineName, addr, s.workDir)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "engine-server: %v\n", err)
		os.Exit(1)
	}
}

// serverFromEnv resolves the engine from ENGINE_CMD/ENGINE_NAME and picks a
// per-engine scan-arg template. ENGINE_WORK_DIR defaults to /scan-work (the
// shared mount); ENGINE_HTTP_ADDR defaults to :8085.
func serverFromEnv() (*server, string, error) {
	name := envOr("ENGINE_NAME", "")
	cmd := envOr("ENGINE_CMD", "")
	if name == "" || cmd == "" {
		return nil, "", fmt.Errorf("ENGINE_NAME and ENGINE_CMD are required")
	}
	work := envOr("ENGINE_WORK_DIR", "/scan-work")
	addr := envOr("ENGINE_HTTP_ADDR", ":8085")
	s := &server{engineCmd: cmd, engineName: name, workDir: work}
	switch name {
	case "trivy":
		// Mirrors infra/scanner-plugins/trivy-adapter's trivyScanArgs: quiet,
		// no-progress, JSON, offline (the DB is pre-warmed in this image).
		s.scanArgs = func(rootfs string) []string {
			return []string{"rootfs", "--quiet", "--no-progress", "--format", "json", "--skip-db-update", rootfs}
		}
	case "grype":
		// grype scans a dir: prefix and emits JSON via -o json. Offline is
		// implicit — grype uses its pre-warmed cache and only updates on a
		// scheduled check, which we disable via GRYPE_DB_AUTO_UPDATE=false
		// in the image env.
		s.scanArgs = func(rootfs string) []string {
			return []string{"dir:" + rootfs, "-o", "json"}
		}
	default:
		return nil, "", fmt.Errorf("unsupported ENGINE_NAME %q (want trivy|grype)", name)
	}
	return s, addr, nil
}

// handleScan runs the engine on the requested rootfs and returns its raw JSON.
func (s *server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "decode request: "+err.Error())
		return
	}
	if req.Rootfs == "" {
		writeErr(w, http.StatusBadRequest, "rootfs is required")
		return
	}
	// Path guard: the rootfs must resolve inside the shared work dir. Mirrors
	// the clair-adapter layer-server traversal check — defence in depth even
	// though the mount is read-only.
	clean := filepath.Clean(req.Rootfs)
	base := filepath.Clean(s.workDir)
	if clean != base && !strings.HasPrefix(clean, base+string(os.PathSeparator)) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("rootfs %q escapes work dir %q", clean, base))
		return
	}
	if fi, err := os.Stat(clean); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "rootfs is not a readable directory")
		return
	}

	cmd := exec.Command(s.engineCmd, s.scanArgs(clean)...) //nolint:gosec // args are engine flags + a validated in-tree path
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	stdout, err := cmd.Output()
	if err != nil {
		// Engine ran and failed (bad image, engine bug) — 500 with stderr so
		// the shim propagates the real reason, same as today's exec path.
		writeErr(w, http.StatusInternalServerError,
			fmt.Sprintf("%s failed: %v: %s", s.engineName, err, strings.TrimSpace(stderr.String())))
		return
	}

	version, _ := s.engineVersion()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(scanResponse{
		Engine:  s.engineName,
		Version: version,
		Raw:     json.RawMessage(stdout),
	})
}

// handleHealthz reports 200 when the engine binary is executable and answers
// `--version`. Used by compose/K8s liveness AND by the scanner's health probe
// so an operator can tell "engine sidecar down" from "scan errored".
func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if _, err := s.engineVersion(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "engine unavailable: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// engineVersion runs `<engine> --version` and returns the parsed version.
// Both trivy ("Version: X") and grype ("grype X") put the version token
// second, so we split on whitespace and take field [1].
func (s *server) engineVersion() (string, error) {
	out, err := exec.Command(s.engineCmd, "--version").Output() //nolint:gosec
	if err != nil {
		return "unknown", err
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	fields := strings.Fields(strings.TrimPrefix(line, "Version:"))
	if len(fields) == 0 {
		return "unknown", nil
	}
	return fields[0], nil
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd infra/scanner-plugins/engine-server && GOWORK=off go test ./... && GOWORK=off go vet ./...`
Expected: PASS (all 4 tests), vet clean.

- [ ] **Step 6: Commit**

```bash
git add infra/scanner-plugins/engine-server/go.mod infra/scanner-plugins/engine-server/main.go infra/scanner-plugins/engine-server/main_test.go
git commit -m "feat(scanner): shared engine-server HTTP wrapper (exec engine CLI over HTTP)"
```

---

## Task 2: The `trivy-engine` sidecar image + DB pre-warm entrypoint

Thin image: the upstream trivy binary + the wrapper. Owns the vuln-DB cache and the REM-019 P2 pre-warm (moved out of the scanner entrypoint).

**Files:**
- Create: `infra/scanner-plugins/engine-server/engine-entrypoint.sh`
- Create: `infra/scanner-plugins/engine-server/Dockerfile`

- [ ] **Step 1: Write the engine entrypoint (DB pre-warm)**

Create `infra/scanner-plugins/engine-server/engine-entrypoint.sh`:

```sh
#!/bin/sh
# engine-entrypoint.sh — pre-warm the engine vuln DB once per cache volume,
# then exec the wrapper. Relocated from services/scanner/scripts/entrypoint.sh
# (REM-019 Phase 2): the DB now lives with the engine, so the warm belongs here.
#
# Best-effort — a failed warm logs but never blocks startup; the first scan
# self-heals. Skip with SCANNER_SKIP_ENGINE_WARM=1 for fast CI runs.
set -eu

if [ -z "${SCANNER_SKIP_ENGINE_WARM:-}" ]; then
    case "${ENGINE_NAME:-}" in
    trivy)
        echo "engine-entrypoint: pre-warming Trivy DB..." >&2
        if trivy image --download-db-only >/tmp/trivy-warm.log 2>&1; then
            echo "engine-entrypoint: Trivy DB ready." >&2
        else
            echo "engine-entrypoint: Trivy DB warm failed (see /tmp/trivy-warm.log) — first scan will fetch online." >&2
        fi
        ;;
    grype)
        echo "engine-entrypoint: pre-warming Grype DB..." >&2
        if grype db update >/tmp/grype-warm.log 2>&1; then
            echo "engine-entrypoint: Grype DB ready." >&2
        else
            echo "engine-entrypoint: Grype DB warm failed (see /tmp/grype-warm.log) — first scan will retry." >&2
        fi
        ;;
    esac
fi

exec "$@"
```

- [ ] **Step 2: Write the sidecar Dockerfile**

Create `infra/scanner-plugins/engine-server/Dockerfile`. One parameterized Dockerfile; the `ENGINE` build-arg selects the upstream base so trivy-engine and (later) grype-engine share it.

```dockerfile
# syntax=docker/dockerfile:1.7
#
# Thin engine sidecar for OCI-Janus scanner. Bumping the engine version = bump
# the pinned digest below + rebuild THIS image; registry-scanner is untouched.
#
# Build:
#   docker build -f infra/scanner-plugins/engine-server/Dockerfile \
#     --build-arg ENGINE=trivy -t oci-janus/trivy-engine:0.71.2 .
# (build context is the repo root so the wrapper module is in scope.)

# ── Stage 1: build the wrapper (stdlib-only, GOWORK=off) ──────────────
FROM golang:1.25-bookworm AS builder
WORKDIR /build
COPY infra/scanner-plugins/engine-server/ ./engine-server/
WORKDIR /build/engine-server
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/engine-server .

# ── Stage 2: engine bases (pin by DIGEST — the trust root, §7 of the spec) ──
# Bump the tag AND the @sha256 digest together. Digest pinning is stronger
# than a mutable tag and is what replaces the in-image checksum guarantee once
# the engine runs in its own container.
FROM aquasec/trivy:0.71.2 AS engine-trivy
FROM anchore/grype:v0.93.0 AS engine-grype

# ── Stage 3: final, selected by ENGINE build-arg ──────────────────────
ARG ENGINE=trivy
FROM engine-${ENGINE} AS final
ARG ENGINE=trivy

USER root
# Both upstream images are alpine/distroless-ish; ensure /bin/sh + ca-certs
# for the entrypoint. Trivy's image is alpine-based (has sh); grype's is
# distroless (no sh) — grype support is finalised in Phase 2, which will
# adjust this. For trivy this is a no-op safety net.
COPY infra/scanner-plugins/engine-server/engine-entrypoint.sh /engine-entrypoint.sh
COPY --from=builder /bin/engine-server /usr/local/bin/engine-server

ENV ENGINE_NAME=${ENGINE}
# ENGINE_CMD is the engine binary path inside the base image.
# trivy: /usr/local/bin/trivy ; grype: /grype (finalised in Phase 2).
ENV ENGINE_CMD=/usr/local/bin/trivy
ENV ENGINE_WORK_DIR=/scan-work
ENV ENGINE_HTTP_ADDR=:8085
# Trivy DB cache lives here now (moved off the scanner container).
ENV TRIVY_CACHE_DIR=/engine-cache
RUN mkdir -p /engine-cache /scan-work
VOLUME ["/engine-cache"]

EXPOSE 8085
ENTRYPOINT ["/engine-entrypoint.sh"]
CMD ["/usr/local/bin/engine-server"]
```

- [ ] **Step 3: Build the trivy-engine image to verify it compiles**

Run (from repo root):
```bash
docker build -f infra/scanner-plugins/engine-server/Dockerfile --build-arg ENGINE=trivy -t oci-janus/trivy-engine:dev .
```
Expected: image builds; final stage has `/usr/local/bin/engine-server` + `/usr/local/bin/trivy`.

- [ ] **Step 4: Smoke-test the container answers /healthz**

Run:
```bash
docker run -d --name trivy-engine-smoke -p 18085:8085 oci-janus/trivy-engine:dev
sleep 3 && curl -fsS http://localhost:18085/healthz
docker rm -f trivy-engine-smoke
```
Expected: `{"status":"ok"}` (after the entrypoint warms the DB; healthz only needs `trivy --version`).

- [ ] **Step 5: Commit**

```bash
git add infra/scanner-plugins/engine-server/engine-entrypoint.sh infra/scanner-plugins/engine-server/Dockerfile
git commit -m "feat(scanner): trivy-engine sidecar image + DB pre-warm entrypoint"
```

---

## Task 3: Shared work-dir env plumbing

The adapter must flatten the rootfs onto the shared volume and know the engine URL. `TRIVY_ENGINE_URL` is already forwarded (the `TRIVY_` prefix). Add `SCANNER_` to the allowlist so a single `SCANNER_SCAN_WORK_DIR` var can configure both adapters uniformly.

**Files:**
- Modify: `services/scanner/internal/plugin/process.go:244`
- Test: `services/scanner/internal/plugin/process_test.go`

- [ ] **Step 1: Write the failing test**

Add to `services/scanner/internal/plugin/process_test.go` (package `plugin`):

```go
func TestPluginEnv_ForwardsScannerPrefix(t *testing.T) {
	t.Setenv("SCANNER_SCAN_WORK_DIR", "/scan-work")
	t.Setenv("DB_DSN", "postgres://secret") // must NOT be forwarded
	env := pluginEnv()
	var sawWork, sawSecret bool
	for _, e := range env {
		if strings.HasPrefix(e, "SCANNER_SCAN_WORK_DIR=") {
			sawWork = true
		}
		if strings.HasPrefix(e, "DB_DSN=") {
			sawSecret = true
		}
	}
	if !sawWork {
		t.Fatal("expected SCANNER_SCAN_WORK_DIR to be forwarded")
	}
	if sawSecret {
		t.Fatal("DB_DSN must never be forwarded to a plugin")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/scanner && GOWORK=off go test ./internal/plugin/ -run TestPluginEnv_ForwardsScannerPrefix -v`
Expected: FAIL — `SCANNER_SCAN_WORK_DIR` not forwarded.

- [ ] **Step 3: Add the `SCANNER_` prefix**

In `services/scanner/internal/plugin/process.go`, change the `allowedPrefixes` line (~244):

```go
	// SCANNER_ joins the list so shared adapter config (SCANNER_SCAN_WORK_DIR —
	// where an adapter flattens the rootfs for an engine sidecar to read) is
	// forwarded to every adapter uniformly. SCANNER_ vars are non-secret
	// config; the allowlist still blocks DB_DSN / JWT keys / cloud creds.
	allowedPrefixes := []string{"TRIVY_", "GRYPE_", "CLAIR_", "SCANNER_"}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/scanner && GOWORK=off go test ./internal/plugin/ -v`
Expected: PASS (new test + all existing plugin tests).

- [ ] **Step 5: Commit**

```bash
git add services/scanner/internal/plugin/process.go services/scanner/internal/plugin/process_test.go
git commit -m "feat(scanner): forward SCANNER_ env prefix to adapters (shared work dir)"
```

---

## Task 4: Repoint the trivy-adapter shim to the engine sidecar

Replace `exec trivy` with an HTTP POST. Flatten onto the shared work dir. Return a distinct `engine_unreachable` error when the sidecar can't be reached.

**Files:**
- Modify: `infra/scanner-plugins/trivy-adapter/main.go`
- Test: `infra/scanner-plugins/trivy-adapter/main_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `infra/scanner-plugins/trivy-adapter/main_test.go`:

```go
func TestScanViaEngine_HappyPath(t *testing.T) {
	// Fake engine sidecar returns a trivy report wrapped in the engine envelope.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["rootfs"] == "" {
			t.Errorf("rootfs missing from POST")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"engine":"trivy","version":"0.71.2","raw":{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"p","InstalledVersion":"1","Severity":"HIGH"}]}]}}`))
	}))
	defer srv.Close()

	report, version, err := scanViaEngine(srv.URL, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if version != "0.71.2" {
		t.Fatalf("want version 0.71.2, got %q", version)
	}
	fs := translateFindings(report)
	if len(fs) != 1 || fs[0].CVE != "CVE-1" {
		t.Fatalf("bad findings: %+v", fs)
	}
}

func TestScanViaEngine_Unreachable(t *testing.T) {
	// Point at a closed port — connection refused.
	_, _, err := scanViaEngine("http://127.0.0.1:1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), errEngineUnreachable) {
		t.Fatalf("want %q error, got %v", errEngineUnreachable, err)
	}
}
```

Ensure the test file imports `encoding/json`, `net/http`, `net/http/httptest`, `strings`.

- [ ] **Step 2: Run to verify it fails**

Run: `cd infra/scanner-plugins/trivy-adapter && GOWORK=off go test ./... -run TestScanViaEngine -v`
Expected: FAIL — `undefined: scanViaEngine`, `undefined: errEngineUnreachable`.

- [ ] **Step 3: Replace the exec path with the HTTP call**

In `infra/scanner-plugins/trivy-adapter/main.go`:

(a) Add imports `net/http`, `net`, `time`, `bytes` (keep existing). Remove `os/exec` only after all exec callers are gone (Step 3d).

(b) Add near the top (after the type decls):

```go
// errEngineUnreachable is the sentinel substring the scanner orchestrator
// keys on to distinguish "the engine sidecar is down/unreachable" (a deploy
// problem) from "the engine ran and errored" (a real scan failure). Threaded
// up through services/scanner so /settings/scanning can show the adapter as
// degraded rather than making an operator guess.
const errEngineUnreachable = "engine_unreachable"

// engineURL returns the trivy-engine sidecar base URL. Required — an unset
// value is a deployment misconfiguration and fails the scan cleanly.
func engineURL() string { return os.Getenv("TRIVY_ENGINE_URL") }

// scanWorkDir is the shared volume both this adapter (rw) and the engine
// sidecar (ro) mount. The adapter flattens the rootfs here so the sidecar
// can read it at the identical path. Overridable for tests / local dev.
func scanWorkDir() string {
	if d := os.Getenv("SCANNER_SCAN_WORK_DIR"); d != "" {
		return d
	}
	return "/scan-work"
}
```

(c) Replace `runTrivy`/`runTrivyOnce`/`trivyScanArgs`/`trivyDBPresent`/`trivyDBDir`/`trivyBinary` (the whole exec-and-DB block) with the HTTP client. Delete those funcs/vars and add:

```go
// scanViaEngine POSTs the flattened rootfs path to the trivy-engine sidecar
// and returns the parsed trivy report plus the engine's self-reported version.
// A connection/timeout failure is wrapped with errEngineUnreachable so the
// orchestrator can classify it as a deploy problem, not a scan failure.
func scanViaEngine(baseURL, rootfs string) (*trivyJSON, string, error) {
	if baseURL == "" {
		return nil, "unknown", fmt.Errorf("TRIVY_ENGINE_URL not set; cannot reach trivy-engine sidecar")
	}
	body, _ := json.Marshal(map[string]string{"rootfs": rootfs})
	// Generous deadline: a cold engine may still be warming its DB; matches
	// the clair-adapter's minute-scale timeouts.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(strings.TrimRight(baseURL, "/")+"/scan", "application/json", bytes.NewReader(body))
	if err != nil {
		// net.Error (conn refused, timeout, DNS) => the sidecar is unreachable.
		return nil, "unknown", fmt.Errorf("%s: POST /scan: %w", errEngineUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &er)
		return nil, "unknown", fmt.Errorf("trivy-engine returned %d: %s", resp.StatusCode, strings.TrimSpace(er.Error))
	}
	var env struct {
		Version string          `json:"version"`
		Raw     json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "unknown", fmt.Errorf("decode engine response: %w", err)
	}
	var report trivyJSON
	if err := json.Unmarshal(env.Raw, &report); err != nil {
		return nil, env.Version, fmt.Errorf("parse trivy json: %w", err)
	}
	return &report, env.Version, nil
}
```

(d) In `main()`, change the rootfs temp dir to sit under the shared work dir, and swap the `runTrivy` call:

```go
	// Flatten onto the SHARED work volume so the engine sidecar can read the
	// rootfs at the same absolute path. os.MkdirTemp under scanWorkDir() keeps
	// per-scan isolation; defer RemoveAll cleans it after the engine responds.
	if err := os.MkdirAll(scanWorkDir(), 0o755); err != nil {
		writeError(req.ID, fmt.Sprintf("mkdir work dir: %v", err))
		os.Exit(1)
	}
	rootfs, err := os.MkdirTemp(scanWorkDir(), "trivy-rootfs-*")
	if err != nil {
		writeError(req.ID, fmt.Sprintf("mkdtemp rootfs: %v", err))
		os.Exit(1)
	}
	defer os.RemoveAll(rootfs)
```

...and replace the `report, scannerVersion, err := runTrivy(rootfs)` line with:

```go
	report, scannerVersion, err := scanViaEngine(engineURL(), rootfs)
	if err != nil {
		writeError(req.ID, err.Error())
		os.Exit(1)
	}
```

(e) Update the package doc comment (top of file) step 3: change
"invoke `trivy rootfs …`" to "POST the flattened rootfs path to the trivy-engine sidecar (`$TRIVY_ENGINE_URL/scan`) and read back the engine's JSON."

- [ ] **Step 4: Run tests + vet to verify green**

Run: `cd infra/scanner-plugins/trivy-adapter && GOWORK=off go test ./... -v && GOWORK=off go vet ./...`
Expected: PASS — new `TestScanViaEngine_*` plus the untouched `translateFindings`/`extractLayer` tests; vet clean (no leftover `os/exec` import).

- [ ] **Step 5: Commit**

```bash
git add infra/scanner-plugins/trivy-adapter/main.go infra/scanner-plugins/trivy-adapter/main_test.go
git commit -m "feat(scanner): trivy-adapter posts to trivy-engine sidecar instead of exec"
```

---

## Task 5: Scanner Dockerfile surgery + entrypoint trim (Trivy only)

Remove the Trivy stage, binary, cache env/volume, and pre-warm from the scanner image; add the `/scan-work` dir. **Leave Grype in place** (Phase 2 removes it).

**Files:**
- Modify: `services/scanner/Dockerfile`
- Modify: `services/scanner/scripts/entrypoint.sh`

- [ ] **Step 1: Edit the Dockerfile**

In `services/scanner/Dockerfile`:
- Delete the `FROM aquasec/trivy:0.71.2 AS trivy` stage (lines ~47–54).
- Delete `COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy` (~91).
- Delete `ENV TRIVY_CACHE_DIR=/trivy-cache` (~130).
- Remove `/trivy-cache` from the `mkdir -p` (~112), the `chown` (~115), and the `VOLUME` line (~158) — keep `/grype-cache` until Phase 2.
- Add `/scan-work` to the `mkdir -p` + `chown` so the shared-volume mount point exists and is owned by the nonroot UID:

```dockerfile
RUN mkdir -p /grype-cache /scan-work /tmp/reports \
 && groupadd -g 65532 nonroot \
 && useradd -u 65532 -g nonroot -s /sbin/nologin -M nonroot \
 && chown nonroot:nonroot /grype-cache /scan-work /tmp/reports
```

```dockerfile
VOLUME ["/grype-cache", "/scan-work"]
```

- [ ] **Step 2: Trim the Trivy pre-warm from the entrypoint**

In `services/scanner/scripts/entrypoint.sh`, delete the entire "REM-019 Phase 2: Pre-warm the Trivy vulnerability DB" block (lines ~66–87). Leave the Grype warm block (Phase 2 removes it). Add a one-line comment where it was:

```sh
# Trivy DB pre-warm moved to the trivy-engine sidecar (engine decoupling,
# 2026-07-17). Grype warm stays until grype-engine ships in Phase 2.
```

- [ ] **Step 3: Build the scanner image to verify it still compiles**

Run (from repo root):
```bash
docker build -f services/scanner/Dockerfile -t oci-janus/scanner:dev .
```
Expected: builds; final image has the 4 `scanner-*` adapters + `grype` but **no** `trivy` binary and **no** `/trivy-cache`.

- [ ] **Step 4: Commit**

```bash
git add services/scanner/Dockerfile services/scanner/scripts/entrypoint.sh
git commit -m "refactor(scanner): drop baked Trivy engine + cache from scanner image"
```

---

## Task 6: Compose wiring

Add the `trivy-engine` service + shared `scan_work` volume; point the scanner at the sidecar.

**Files:**
- Modify: `infra/docker-compose/docker-compose.yml`

- [ ] **Step 1: Add the trivy-engine service**

In `infra/docker-compose/docker-compose.yml`, add a service alongside `scanner` (match the existing indentation + `registry` network + `profiles` used by `scanner`):

```yaml
  trivy-engine:
    build:
      context: ../..
      dockerfile: infra/scanner-plugins/engine-server/Dockerfile
      args:
        ENGINE: trivy
    image: oci-janus/trivy-engine:dev
    profiles: ["scanner"]      # match whatever profile guards `scanner`
    networks: [registry]
    environment:
      ENGINE_NAME: trivy
      ENGINE_HTTP_ADDR: ":8085"
    volumes:
      - scan_work:/scan-work:ro          # read-only: engine only reads the rootfs
      - trivy_engine_cache:/engine-cache  # DB persists across restarts
    healthcheck:
      # Trivy's upstream image is alpine-based (has /bin/sh + wget), so a shell
      # healthcheck works for Phase 1. Phase 2's grype base is distroless (no
      # shell) — that phase adds a `-health` self-probe flag to the wrapper.
      test: ["CMD-SHELL", "wget -qO- http://localhost:8085/healthz || exit 1"]
      interval: 10s
      timeout: 5s
      retries: 5
```

- [ ] **Step 2: Wire the scanner service**

In the `scanner` service block, add the shared volume (rw) + the engine URL env, and remove the `/trivy-cache` volume mount if one exists:

```yaml
    environment:
      # ... existing ...
      TRIVY_ENGINE_URL: "http://trivy-engine:8085"
      SCANNER_SCAN_WORK_DIR: "/scan-work"
    volumes:
      - scan_work:/scan-work        # rw: scanner stages + flattens here
      # (remove any trivy-cache mount; keep grype-cache until Phase 2)
    depends_on:
      trivy-engine:
        condition: service_healthy
```

- [ ] **Step 3: Declare the volumes**

In the top-level `volumes:` block:

```yaml
  scan_work:
  trivy_engine_cache:
```

- [ ] **Step 4: Validate compose parses**

Run: `cd infra/docker-compose && docker compose --profile scanner config >/dev/null && echo OK`
Expected: `OK` (no YAML/interpolation errors).

- [ ] **Step 5: Commit**

```bash
git add infra/docker-compose/docker-compose.yml
git commit -m "feat(scanner): compose wiring for trivy-engine sidecar + scan_work volume"
```

---

## Task 7: CI workflow for the engine images

The scanner had no engine-build CI before (engines were baked). Add a path-filtered workflow so the wrapper + engine image are built/tested in CI.

**Files:**
- Create: `.github/workflows/ci-scanner-engines.yml`

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/ci-scanner-engines.yml`, modeled on `ci-mcp.yml`/`ci-gc.yml`:

```yaml
name: ci-scanner-engines
on:
  push:
    paths:
      - "infra/scanner-plugins/engine-server/**"
      - ".github/workflows/ci-scanner-engines.yml"
  pull_request:
    paths:
      - "infra/scanner-plugins/engine-server/**"
      - ".github/workflows/ci-scanner-engines.yml"
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.25.11"
      - name: vet + test (wrapper)
        working-directory: infra/scanner-plugins/engine-server
        run: |
          GOWORK=off go vet ./...
          GOWORK=off go test ./...
      - name: golangci-lint
        uses: golangci/golangci-lint-action@v6
        with:
          working-directory: infra/scanner-plugins/engine-server
          args: --timeout=5m
  build-image:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: build trivy-engine image
        run: |
          docker build -f infra/scanner-plugins/engine-server/Dockerfile \
            --build-arg ENGINE=trivy -t oci-janus/trivy-engine:ci .
```

- [ ] **Step 2: Validate the workflow YAML**

Run: `cd infra/scanner-plugins/engine-server && GOWORK=off go vet ./...` (proves the job's core command works locally). Optionally lint the YAML with `yamllint` if available.
Expected: vet clean.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci-scanner-engines.yml
git commit -m "ci(scanner): build + test the engine-server wrapper and trivy-engine image"
```

---

## Task 8: Helm — trivy-engine Deployment + Service

Add a co-located engine Deployment to the scanner subchart with a shared `emptyDir`.

**Files:**
- Create: `infra/helm/registry/charts/scanner/templates/engine-deployment.yaml`
- Modify: `infra/helm/registry/charts/scanner/values.yaml`
- Modify: `infra/helm/registry/charts/scanner/templates/deployment.yaml`

- [ ] **Step 1: Add engine values**

Append to `infra/helm/registry/charts/scanner/values.yaml` (match the existing key style):

```yaml
trivyEngine:
  enabled: true
  image:
    repository: oci-janus/trivy-engine
    # Pin by digest in prod (values.prod.yaml); tag here is dev convenience.
    tag: "0.71.2"
  httpPort: 8085
  resources: {}
```

- [ ] **Step 2: Add the engine Deployment + Service**

Create `infra/helm/registry/charts/scanner/templates/engine-deployment.yaml`, mirroring the existing `scanner/templates/deployment.yaml` label/selector helpers:

```yaml
{{- if .Values.trivyEngine.enabled }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "scanner.fullname" . }}-trivy-engine
  labels:
    {{- include "scanner.labels" . | nindent 4 }}
    app.kubernetes.io/component: trivy-engine
spec:
  replicas: 1
  selector:
    matchLabels:
      {{- include "scanner.selectorLabels" . | nindent 6 }}
      app.kubernetes.io/component: trivy-engine
  template:
    metadata:
      labels:
        {{- include "scanner.selectorLabels" . | nindent 8 }}
        app.kubernetes.io/component: trivy-engine
    spec:
      containers:
        - name: trivy-engine
          image: "{{ .Values.trivyEngine.image.repository }}:{{ .Values.trivyEngine.image.tag }}"
          env:
            - name: ENGINE_NAME
              value: trivy
            - name: ENGINE_HTTP_ADDR
              value: ":{{ .Values.trivyEngine.httpPort }}"
          ports:
            - containerPort: {{ .Values.trivyEngine.httpPort }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: {{ .Values.trivyEngine.httpPort }}
            initialDelaySeconds: 20
            periodSeconds: 15
          volumeMounts:
            - name: scan-work
              mountPath: /scan-work
              readOnly: true
            - name: engine-cache
              mountPath: /engine-cache
          resources:
            {{- toYaml .Values.trivyEngine.resources | nindent 12 }}
      volumes:
        - name: scan-work
          emptyDir: {}
        - name: engine-cache
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "scanner.fullname" . }}-trivy-engine
  labels:
    {{- include "scanner.labels" . | nindent 4 }}
spec:
  selector:
    {{- include "scanner.selectorLabels" . | nindent 4 }}
    app.kubernetes.io/component: trivy-engine
  ports:
    - port: {{ .Values.trivyEngine.httpPort }}
      targetPort: {{ .Values.trivyEngine.httpPort }}
{{- end }}
```

> **Co-location note:** the spec says co-located sidecars sharing the rootfs via a pod-local `emptyDir`. A separate Deployment cannot share an `emptyDir` with the scanner pod. For K8s, the faithful realization is a **sidecar container in the scanner pod** OR a shared `ReadWriteMany` PVC. To keep Phase 1 tractable and match compose's shared named volume, use a shared PVC (`scan-work`) mounted rw by scanner and ro by the engine — replace both `emptyDir: {}` entries above and the scanner's mount with a `persistentVolumeClaim`. If your cluster lacks RWX storage, fall back to running trivy-engine as a **sidecar container inside `scanner/templates/deployment.yaml`** sharing a pod `emptyDir`. Pick one and note it in the PR; the compose path (Tasks 2/6) is the source of truth for local verification.

- [ ] **Step 3: Set the scanner env + shared mount in the scanner Deployment**

In `infra/helm/registry/charts/scanner/templates/deployment.yaml`, add to the scanner container `env`:

```yaml
            - name: TRIVY_ENGINE_URL
              value: "http://{{ include "scanner.fullname" . }}-trivy-engine:{{ .Values.trivyEngine.httpPort }}"
            - name: SCANNER_SCAN_WORK_DIR
              value: "/scan-work"
```

...and mount the shared `scan-work` volume rw (matching the storage choice from Step 2's note).

- [ ] **Step 4: Lint the chart**

Run: `helm lint infra/helm/registry` (or `helm template infra/helm/registry >/dev/null`).
Expected: no errors; the engine Deployment renders when `trivyEngine.enabled=true`.

- [ ] **Step 5: Commit**

```bash
git add infra/helm/registry/charts/scanner/
git commit -m "feat(scanner): helm trivy-engine deployment + scanner engine wiring"
```

---

## Task 9: Observability — surface `engine_unreachable` as degraded health

Additive proto fields on `ScannerHealthResponse`; the handler probes the active adapter's engine; the FE health card shows a degraded badge.

**Files:**
- Modify: `proto/scanner/v1/scanner.proto:540`
- Modify: `services/scanner/internal/handler/grpc.go:1161`
- Test: `services/scanner/internal/handler/grpc_test.go`
- Modify: `frontend/src/lib/api/admin-scanners.ts`
- Modify: `frontend/src/components/admin/scanner/scanner-health-card.tsx`

- [ ] **Step 1: Add additive proto fields + regenerate stubs**

In `proto/scanner/v1/scanner.proto`, extend `ScannerHealthResponse` (next free field numbers after 6):

```proto
  // active_adapter_engine_reachable is false when the active adapter runs
  // against an external engine sidecar (e.g. trivy-engine) that the scanner
  // could not reach on the last health probe. True when reachable OR when the
  // active adapter has no external engine (dev-stub). Lets /settings/scanning
  // show "degraded" for a deploy problem vs a genuine scan error.
  bool active_adapter_engine_reachable = 7;
  // active_adapter_engine_detail carries a short human string (e.g. the
  // engine URL + probe error) when active_adapter_engine_reachable is false.
  string active_adapter_engine_detail = 8;
```

Regenerate: `cd proto && buf generate --template buf.gen.yaml`
Verify additive-only: `buf breaking proto --against '.git#branch=main,subdir=proto'` → exit 0.

- [ ] **Step 2: Write the failing handler test**

Add to `services/scanner/internal/handler/grpc_test.go`:

```go
func TestGetScannerHealth_EngineUnreachable(t *testing.T) {
	// Active adapter is "trivy-adapter"; point its engine URL at a dead port.
	t.Setenv("TRIVY_ENGINE_URL", "http://127.0.0.1:1")
	h := newHealthHandlerWithActiveAdapter(t, "trivy-adapter") // helper: pool + registry w/ active trivy-adapter
	resp, err := h.GetScannerHealth(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetActiveAdapterEngineReachable() {
		t.Fatal("expected engine unreachable=false for a dead trivy-engine")
	}
	if resp.GetActiveAdapterEngineDetail() == "" {
		t.Fatal("expected a detail string on unreachable")
	}
}

func TestGetScannerHealth_NoEngineAdapter_Reachable(t *testing.T) {
	// dev-stub has no external engine → reachable must be true (nothing to probe).
	h := newHealthHandlerWithActiveAdapter(t, "dev-stub")
	resp, _ := h.GetScannerHealth(context.Background(), &emptypb.Empty{})
	if !resp.GetActiveAdapterEngineReachable() {
		t.Fatal("dev-stub has no engine; reachable must be true")
	}
}
```

Add the `newHealthHandlerWithActiveAdapter` helper next to the existing adapter-registry test helpers (reuse whatever fixture `grpc_test.go` already uses to build a `Registry` with a discovered adapter + set active).

- [ ] **Step 3: Run to verify it fails**

Run: `cd services/scanner && GOWORK=off go test ./internal/handler/ -run TestGetScannerHealth_Engine -v`
Expected: FAIL — the new proto getters return zero-value `false`/`""` unconditionally (no probe yet).

- [ ] **Step 4: Implement the engine probe in `GetScannerHealth`**

In `services/scanner/internal/handler/grpc.go`, extend `GetScannerHealth` (after the active-adapter name/version block). Add a package-level map + helper:

```go
// adapterEngineURLEnv maps an active adapter Name to the env var holding its
// external engine sidecar URL. Adapters absent from this map (dev-stub) have
// no external engine, so their engine is "reachable" by definition. Grype
// joins this map in Phase 2 (GRYPE_ENGINE_URL).
var adapterEngineURLEnv = map[string]string{
	"trivy-adapter": "TRIVY_ENGINE_URL",
}

// probeActiveEngine reports whether the active adapter's external engine
// sidecar answers /healthz. Returns (true, "") when the adapter has no
// external engine. Cheap (2s timeout) and read-only — safe for the polled
// health endpoint.
func probeActiveEngine(activeName string) (bool, string) {
	envKey, hasEngine := adapterEngineURLEnv[activeName]
	if !hasEngine {
		return true, ""
	}
	url := os.Getenv(envKey)
	if url == "" {
		return false, fmt.Sprintf("%s not set for active adapter %q", envKey, activeName)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(url, "/") + "/healthz")
	if err != nil {
		return false, fmt.Sprintf("engine %s unreachable: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("engine %s returned %d", url, resp.StatusCode)
	}
	return true, ""
}
```

Add imports `fmt`, `net/http`, `time` (already-present ones stay). Then in `GetScannerHealth`, inside the `if h.adapterReg != nil` block after setting name/version:

```go
	// Default: no adapter / no external engine → reachable.
	resp.ActiveAdapterEngineReachable = true
	if h.adapterReg != nil {
		if a := h.adapterReg.Active(); a != nil {
			reachable, detail := probeActiveEngine(a.Name)
			resp.ActiveAdapterEngineReachable = reachable
			resp.ActiveAdapterEngineDetail = detail
		}
	}
```

(Fold this into the existing `if h.adapterReg != nil` block rather than duplicating the nil check.)

- [ ] **Step 5: Run to verify it passes**

Run: `cd services/scanner && GOWORK=off go test ./internal/handler/ -v`
Expected: PASS (new tests + existing handler tests).

- [ ] **Step 6: Surface it in the FE health card**

In `frontend/src/lib/api/admin-scanners.ts`, extend the health type with:

```ts
  active_adapter_engine_reachable: boolean;
  active_adapter_engine_detail: string;
```

In `frontend/src/components/admin/scanner/scanner-health-card.tsx`, add a degraded badge when `!health.active_adapter_engine_reachable`:

```tsx
{!health.active_adapter_engine_reachable && (
  <span
    className="inline-flex items-center gap-1 rounded bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-800"
    title={health.active_adapter_engine_detail}
  >
    Engine sidecar unreachable
  </span>
)}
```

(Match the card's existing badge markup/utility classes; the snippet is illustrative — use the same badge component the card already uses for other states if one exists.)

- [ ] **Step 7: Run the FE gates**

Run: `cd frontend && npm run lint && npm run typecheck && npm run test`
Expected: all green (per CLAUDE.md §15.1; add a small vitest case for the degraded badge if the health card already has a test file).

- [ ] **Step 8: Commit**

```bash
git add proto/ services/scanner/internal/handler/grpc.go services/scanner/internal/handler/grpc_test.go frontend/src/lib/api/admin-scanners.ts frontend/src/components/admin/scanner/scanner-health-card.tsx
git commit -m "feat(scanner): surface engine_unreachable as degraded health on /settings/scanning"
```

---

## Task 10: Live verification + tracker hygiene

Prove the vertical works end-to-end and the new failure mode is observable, then update trackers per CLAUDE.md §15.3.

- [ ] **Step 1: Rebuild + bring up the stack** (per [[feedback-rebuild-full-vertical]])

Run (from repo root):
```bash
docker compose -f infra/docker-compose/docker-compose.yml --profile scanner build scanner trivy-engine frontend management
docker compose -f infra/docker-compose/docker-compose.yml --profile scanner up -d
docker compose -f infra/docker-compose/docker-compose.yml ps
```
Expected: `scanner` + `trivy-engine` both healthy.

- [ ] **Step 2: Baseline a real scan through the sidecar**

Ensure the trivy adapter is active (via `/settings/scanning` or `SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-trivy-adapter`), push a real image, trigger a scan, and confirm findings match a pre-change baseline (same CVE count/severity as trivy produced when baked in).

```bash
docker push localhost:8081/dev/enginetest:v1     # a real image (e.g. alpine)
# trigger scan via UI or TriggerScan RPC; then read scan results
```
Expected: scan `complete`, `scanner_name=trivy`, findings identical to baseline. Confirm the scan ran in the sidecar: `docker compose logs trivy-engine` shows a `/scan` request.

- [ ] **Step 3: Prove `engine_unreachable` is observable**

```bash
docker compose -f infra/docker-compose/docker-compose.yml stop trivy-engine
# trigger another scan
```
Expected: scan `failed`; the error contains `engine_unreachable`; `/settings/scanning` (GetScannerHealth) shows `active_adapter_engine_reachable=false` + the degraded badge. Then `start trivy-engine` and confirm scans recover.

- [ ] **Step 4: Update docs + trackers**

- `docs/SERVICES.md` (registry-scanner §): note engines now run as `trivy-engine` sidecars; DB lives with the engine; bump = rebuild the engine image, not the scanner.
- `status.md`: prepend a resolution row for "scanner engine decoupling — Phase 1 (trivy)".
- `status-tracker.md`: add/close the tracking item.
- `futures.md`: add the named out-of-scope follow-ons (cosign-verify engine images, A3 registry-pull, operator-installable engines, **Phase 2 grype-engine**).

- [ ] **Step 5: Commit + open PR**

```bash
git add docs/SERVICES.md status.md status-tracker.md futures.md
git commit -m "docs(scanner): tracker hygiene for engine decoupling Phase 1 (trivy)"
git push -u origin feat/scanner-engine-decoupling
gh pr create --title "feat(scanner): decouple trivy engine into a sidecar (Phase 1)" --body "..."
```

---

## Out of scope → Phase 2 and beyond (named, not dropped)

- **Phase 2 — grype-engine:** reuse the wrapper (`ENGINE=grype`), finalize the grype Dockerfile (distroless base has no shell — the `-health` self-probe flag matters there), add the compose service, repoint the grype-adapter, remove the Grype stage + warm from the scanner image, add `GRYPE_ENGINE_URL` to `adapterEngineURLEnv`. Separate follow-on plan.
- **Cosign verification of engine images** (Option B dogfooding endgame).
- **A3 registry-pull model** (engine pulls from registry-core by digest — no staging).
- **Operator-installable new engines at runtime** (Option B install registry + persisted `scanner_adapters` table).
- **Clair** — already a sidecar; unaffected.
