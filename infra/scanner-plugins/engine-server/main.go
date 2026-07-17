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

// healthArg is the CLI flag that triggers the self-probe path in main()
// instead of starting the server. Used as the container healthcheck in
// distroless images that lack a shell/wget (e.g. grype-engine).
const healthArg = "-health"

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
	engineCmd   string // absolute path to the engine binary
	engineName  string // "trivy" | "grype" (for the response)
	scanArgs    func(rootfs string) []string
	versionArgs []string // argv (excluding the binary) that prints a "Version: X" line
	warmArgs    []string // argv (excluding the binary) that pre-downloads the vuln DB
	workDir     string   // shared mount; rootfs must live under it
}

func main() {
	// The same binary doubles as its own healthcheck probe so distroless
	// engine images (no shell/wget, e.g. grype-engine) can still be probed
	// by `docker healthcheck` / K8s exec probes.
	if len(os.Args) > 1 && os.Args[1] == healthArg {
		os.Exit(healthProbe())
	}

	s, addr, err := serverFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine-server: config error: %v\n", err)
		os.Exit(1)
	}
	s.warm()

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
// shared mount); ENGINE_HTTP_ADDR defaults to :8085. ENGINE_CMD defaults per
// engine when unset — only ENGINE_NAME is required.
func serverFromEnv() (*server, string, error) {
	name := envOr("ENGINE_NAME", "")
	cmd := envOr("ENGINE_CMD", "")
	if name == "" {
		return nil, "", fmt.Errorf("ENGINE_NAME is required")
	}
	work := envOr("ENGINE_WORK_DIR", "/scan-work")
	addr := envOr("ENGINE_HTTP_ADDR", ":8085")
	s := &server{engineName: name, workDir: work}
	switch name {
	case "trivy":
		// Mirrors infra/scanner-plugins/trivy-adapter's trivyScanArgs: quiet,
		// no-progress, JSON, offline (the DB is pre-warmed in this image).
		if cmd == "" {
			cmd = "/usr/local/bin/trivy"
		}
		s.scanArgs = func(rootfs string) []string {
			return []string{"rootfs", "--quiet", "--no-progress", "--format", "json", "--skip-db-update", rootfs}
		}
		s.versionArgs = []string{"--version"}
		s.warmArgs = []string{"image", "--download-db-only"}
	case "grype":
		// grype scans a dir: prefix and emits JSON via -o json. Offline is
		// implicit — grype uses its pre-warmed cache and only updates on a
		// scheduled check, which we disable via GRYPE_DB_AUTO_UPDATE=false
		// in the image env.
		if cmd == "" {
			cmd = "/grype"
		}
		s.scanArgs = func(rootfs string) []string {
			return []string{"dir:" + rootfs, "-o", "json", "--quiet"}
		}
		s.versionArgs = []string{"version"}
		s.warmArgs = []string{"db", "update"}
	default:
		return nil, "", fmt.Errorf("unsupported ENGINE_NAME %q (want trivy|grype)", name)
	}
	s.engineCmd = cmd
	return s, addr, nil
}

// warm runs the engine's one-time DB download so the first scan pays no
// network cost. Best-effort: a failure logs but never blocks serving — the
// first real scan self-heals (trivy falls back online for a cold cache; grype
// auto-updates). Skipped when SCANNER_SKIP_ENGINE_WARM is set.
func (s *server) warm() {
	if os.Getenv("SCANNER_SKIP_ENGINE_WARM") != "" {
		return
	}
	fmt.Fprintf(os.Stderr, "engine-server: pre-warming %s DB...\n", s.engineName)
	cmd := exec.Command(s.engineCmd, s.warmArgs...) //nolint:gosec
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "engine-server: %s DB warm failed (%v) — first scan will fetch online\n", s.engineName, err)
		return
	}
	fmt.Fprintf(os.Stderr, "engine-server: %s DB ready\n", s.engineName)
}

// healthProbe GETs the local /healthz and returns a process exit code. Used as
// the container healthcheck in distroless images that lack a shell/wget.
func healthProbe() int {
	addr := envOr("ENGINE_HTTP_ADDR", ":8085")
	// Normalize ":8085" -> "127.0.0.1:8085".
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "engine-server -health: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
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

// engineVersion runs the engine's versionArgs and scans all output lines for
// one with a "Version:" prefix. trivy's `--version` output is a single
// "Version: X" line; grype's `version` output is multi-line but also
// contains a "Version: X" line — scanning every line covers both.
func (s *server) engineVersion() (string, error) {
	out, err := exec.Command(s.engineCmd, s.versionArgs...).Output() //nolint:gosec
	if err != nil {
		return "unknown", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Version:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
			if v != "" {
				return v, nil
			}
		}
	}
	return "unknown", nil
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
