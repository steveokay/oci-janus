// Package main is the Trivy scanner adapter for OCI-Janus.
//
// It implements the JSON-RPC contract defined by
// services/scanner/internal/plugin/process.go on top of Aqua Security's
// trivy binary. The orchestrator never sees trivy directly — every
// scanner_id swap is just pointing SCANNER_PLUGIN_PATH at a different
// adapter, of which this is one.
//
// Flow:
//
//  1. read newline-delimited JSON-RPC scan request from stdin
//  2. flatten the staged layer blobs (image_path/<digest_hex>) into a
//     single rootfs directory, applying layers in manifest order
//  3. invoke `trivy rootfs --format json --quiet --no-progress <rootfs>`
//  4. parse Trivy's JSON, translate to the contract's findings shape
//  5. write the JSON-RPC response to stdout, exit 0
//
// Scope note: this is a v1 adapter aimed at the common case — Linux base
// images (alpine, debian, distroless, ubuntu) with gzipped tar layers.
// Known limitations:
//
//   - Whiteout files (.wh.*) from layered FS deletes are NOT replayed; if
//     a vulnerable package was installed in layer N and removed in layer
//     N+1, this adapter will still report it. Stricter-than-real but
//     never under-reports. A correct overlayfs replay is a follow-up.
//   - zstd / uncompressed layer media types fall back to "try gzip,
//     then try plain tar". Anything else returns an error.
//   - No multi-platform manifest list handling — orchestrator already
//     resolved to a single manifest before invoking the adapter.
//
// These trade-offs are deliberate: covers ~95% of real images, the
// remaining edge cases produce explicit errors rather than silent
// wrong-output.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// rpcRequest / rpcResponse / result / finding / severities mirror what
// dev-stub uses; kept identical so a single change to the wire shape
// touches both adapters in obvious places. See dev-stub/main.go for the
// shape comments.
type rpcRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params struct {
		TenantID       string `json:"tenant_id"`
		ManifestDigest string `json:"manifest_digest"`
		Layers         []struct {
			Digest    string `json:"Digest"`
			MediaType string `json:"MediaType"`
			Size      int64  `json:"Size"`
		} `json:"layers"`
		ImagePath string `json:"image_path"`
	} `json:"params"`
}

type rpcResponse struct {
	ID     string  `json:"id"`
	Result *result `json:"result,omitempty"`
	Error  string  `json:"error,omitempty"`
}

type result struct {
	ScannerName    string     `json:"scanner_name"`
	ScannerVersion string     `json:"scanner_version"`
	Findings       []finding  `json:"findings"`
	SeverityCounts severities `json:"severity_counts"`
}

type finding struct {
	CVE         string   `json:"CVE"`
	Severity    string   `json:"Severity"`
	Package     string   `json:"Package"`
	Version     string   `json:"Version"`
	FixedIn     string   `json:"FixedIn"`
	Description string   `json:"Description"`
	References  []string `json:"References"`
}

type severities map[string]int

// trivyJSON is the subset of trivy's --format json output we consume.
// Full schema is much larger; we only read what we need. Keep this list
// short — every field added here is one more Trivy contract drift risk.
type trivyJSON struct {
	Results []struct {
		Target          string `json:"Target"`
		Type            string `json:"Type"`
		Vulnerabilities []struct {
			VulnerabilityID  string   `json:"VulnerabilityID"`
			PkgName          string   `json:"PkgName"`
			InstalledVersion string   `json:"InstalledVersion"`
			FixedVersion     string   `json:"FixedVersion"`
			Severity         string   `json:"Severity"`
			Title            string   `json:"Title"`
			Description      string   `json:"Description"`
			References       []string `json:"References"`
		} `json:"Vulnerabilities"`
	} `json:"Results"`
}

// trivyBinary is the path to the trivy CLI inside the scanner image.
// Overridable via TRIVY_BIN env var for local dev where someone might
// want to point at a custom build.
var trivyBinary = func() string {
	if p := os.Getenv("TRIVY_BIN"); p != "" {
		return p
	}
	return "/usr/local/bin/trivy"
}()

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeError("", fmt.Sprintf("read stdin: %v", err))
		os.Exit(1)
	}

	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError("", fmt.Sprintf("unmarshal request: %v", err))
		os.Exit(1)
	}
	if req.Method != "scan" {
		writeError(req.ID, fmt.Sprintf("unknown method %q (expected \"scan\")", req.Method))
		os.Exit(1)
	}
	if req.Params.ImagePath == "" {
		writeError(req.ID, "missing image_path in request params")
		os.Exit(1)
	}

	rootfs, err := os.MkdirTemp("", "trivy-rootfs-*")
	if err != nil {
		writeError(req.ID, fmt.Sprintf("mkdtemp rootfs: %v", err))
		os.Exit(1)
	}
	defer os.RemoveAll(rootfs)

	// Apply layers in manifest order. The orchestrator already passes
	// them sorted; we just trust that contract here.
	for _, layer := range req.Params.Layers {
		blobPath := filepath.Join(req.Params.ImagePath, strings.TrimPrefix(layer.Digest, "sha256:"))
		if err := extractLayer(blobPath, layer.MediaType, rootfs); err != nil {
			writeError(req.ID, fmt.Sprintf("extract layer %s: %v", layer.Digest, err))
			os.Exit(1)
		}
	}

	report, scannerVersion, err := runTrivy(rootfs)
	if err != nil {
		writeError(req.ID, fmt.Sprintf("run trivy: %v", err))
		os.Exit(1)
	}

	findings := translateFindings(report)
	resp := rpcResponse{
		ID: req.ID,
		Result: &result{
			ScannerName:    "trivy",
			ScannerVersion: scannerVersion,
			Findings:       findings,
			SeverityCounts: countBySeverity(findings),
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
		os.Exit(1)
	}
}

// extractLayer reads a single staged layer blob and writes its contents
// into rootfs. Decompression strategy is driven by MediaType:
//   - *.tar.gzip / *tar+gzip → gzip
//   - everything else → try gzip first (most layers are gzipped even
//     when the media type isn't explicit), fall back to plain tar
func extractLayer(blobPath, mediaType, rootfs string) error {
	data, err := os.ReadFile(blobPath)
	if err != nil {
		return fmt.Errorf("read blob: %w", err)
	}

	wantGzip := strings.Contains(strings.ToLower(mediaType), "gzip")
	if !wantGzip {
		// Heuristic: gzip magic bytes are 1f 8b. Try that first so we
		// handle the "media type lies / is missing" case automatically.
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			wantGzip = true
		}
	}

	var reader io.Reader = bytes.NewReader(data)
	if wantGzip {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	return untarTo(reader, rootfs)
}

// untarTo writes a tar stream into rootfs. Whiteout files (.wh.*) are
// silently skipped — see "scope note" in the package doc.
func untarTo(r io.Reader, rootfs string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar header: %w", err)
		}

		// Skip whiteouts entirely. Documented overcount; correct
		// overlayfs replay is a follow-up.
		name := filepath.Base(hdr.Name)
		if strings.HasPrefix(name, ".wh.") {
			continue
		}

		// Sanitise the path so a malicious layer can't write outside
		// rootfs via ../../etc/passwd. filepath.Clean + a prefix check
		// is the conventional belt-and-suspenders for this.
		target := filepath.Join(rootfs, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, rootfs+string(os.PathSeparator)) && target != rootfs {
			return fmt.Errorf("layer escape attempt: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			// Cap each file at 200 MiB to prevent a malicious layer
			// from exhausting disk. 200 MiB is generous enough for
			// even huge package metadata files.
			const maxFileBytes = 200 << 20
			if _, err := io.Copy(f, io.LimitReader(tr, maxFileBytes)); err != nil {
				f.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			f.Close()
		case tar.TypeSymlink, tar.TypeLink:
			// Symlinks point inside the rootfs in valid OCI images,
			// but Trivy works fine on a rootfs without them — just
			// recreate so package metadata symlinks resolve.
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			_ = os.Symlink(hdr.Linkname, target)
		}
	}
}

// trivyDBDir returns the directory trivy stores its vulnerability DB in.
// Trivy keeps the DB under $TRIVY_CACHE_DIR/db (the scanner image sets
// TRIVY_CACHE_DIR=/trivy-cache), falling back to trivy's default
// ~/.cache/trivy/db when the env var is unset.
func trivyDBDir() string {
	if c := os.Getenv("TRIVY_CACHE_DIR"); c != "" {
		return filepath.Join(c, "db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "trivy", "db")
}

// trivyDBPresent reports whether the trivy vulnerability DB is already on
// disk. When it is, the scan runs with --skip-db-update so the hot path does
// ZERO network I/O — the core of the REM-019 Phase 2 fix (a live per-scan DB
// download made scans slow and intermittently fail).
func trivyDBPresent() bool {
	_, err := os.Stat(filepath.Join(trivyDBDir(), "trivy.db"))
	return err == nil
}

// trivyScanArgs builds the `trivy rootfs` argument vector.
//
//   - --quiet drops the progress bar; --no-progress drops the DB spinner that
//     would otherwise pollute stdout; --format json directs the report to
//     stdout (trivy keeps human progress on stderr).
//   - --skip-db-update (when skipDBUpdate) makes the scan fully offline: trivy
//     uses the already-downloaded DB and never reaches out to the network. This
//     is what makes scans deterministic instead of coupling every scan to a
//     ~100MB download from mirror.gcr.io.
func trivyScanArgs(rootfs string, skipDBUpdate bool) []string {
	args := []string{"rootfs", "--quiet", "--no-progress", "--format", "json"}
	if skipDBUpdate {
		args = append(args, "--skip-db-update")
	}
	return append(args, rootfs)
}

// runTrivyOnce runs a single `trivy rootfs` invocation and parses its report.
func runTrivyOnce(rootfs string, skipDBUpdate bool) (*trivyJSON, error) {
	cmd := exec.Command(trivyBinary, trivyScanArgs(rootfs, skipDBUpdate)...) //nolint:gosec
	cmd.Env = pluginEnv()

	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("trivy exited %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, err
	}

	var report trivyJSON
	if err := json.Unmarshal(stdout, &report); err != nil {
		return nil, fmt.Errorf("parse trivy json: %w", err)
	}
	return &report, nil
}

// runTrivy shells out to trivy rootfs and returns the parsed report plus
// trivy's self-reported version (read once from `trivy --version` so the
// response always reports the binary that actually ran).
//
// REM-019 Phase 2 root-cause fix: prefer an OFFLINE scan (--skip-db-update)
// whenever the vulnerability DB is already present (pre-warmed at container
// boot, or downloaded by an earlier scan). This removes the live per-scan DB
// download from the hot path — the download made scans take ~30s and fail
// intermittently on transient registry/network errors. Only when the cache is
// genuinely cold (fresh volume, or the boot pre-warm failed) do we fall back to
// a one-time online run so the adapter self-heals rather than hard-failing
// every scan. The online run also covers the rare case where the DB file exists
// but trivy rejects it (corrupt / schema-mismatched) — a first offline attempt
// that errors is retried online once.
func runTrivy(rootfs string) (*trivyJSON, string, error) {
	version, _ := trivyVersion()

	skipDBUpdate := trivyDBPresent()
	report, err := runTrivyOnce(rootfs, skipDBUpdate)
	if err != nil && skipDBUpdate {
		// DB looked present but the offline scan failed — retry once allowing a
		// fresh download before giving up.
		report, err = runTrivyOnce(rootfs, false)
	}
	if err != nil {
		return nil, version, err
	}
	return report, version, nil
}

// trivyVersion runs `trivy --version` and parses the first line.
// Output format: "Version: 0.52.0\n..." — we slice the second field.
// Errors are swallowed because the report is more valuable than the
// version label; a missing version is reported as "unknown".
func trivyVersion() (string, error) {
	out, err := exec.Command(trivyBinary, "--version").Output() //nolint:gosec
	if err != nil {
		return "unknown", err
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if !strings.HasPrefix(line, "Version:") {
		return "unknown", nil
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "Version:")), nil
}

// pluginEnv returns the env trivy can see. The orchestrator already
// strips host secrets before invoking us, but we further trim our own
// env so a subverted trivy can't inherit anything from this adapter.
func pluginEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"USER": true, "USERNAME": true,
		"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
		"TRIVY_BIN": true, "TRIVY_CACHE_DIR": true,
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if allowed[k] || strings.HasPrefix(k, "TRIVY_") {
			env = append(env, e)
		}
	}
	return env
}

// translateFindings folds trivy's nested Results[].Vulnerabilities[]
// into the flat findings list the contract expects. The same CVE can
// appear under multiple Results targets (e.g. once for the OS package
// and once for a language-level dependency); we dedupe on (CVE,
// package, version) so the operator doesn't see the same finding twice.
func translateFindings(report *trivyJSON) []finding {
	if report == nil {
		return nil
	}
	type key struct{ cve, pkg, ver string }
	seen := map[key]bool{}
	out := make([]finding, 0)
	for _, r := range report.Results {
		for _, v := range r.Vulnerabilities {
			k := key{v.VulnerabilityID, v.PkgName, v.InstalledVersion}
			if seen[k] {
				continue
			}
			seen[k] = true
			desc := v.Description
			if v.Title != "" {
				desc = v.Title + " — " + desc
			}
			out = append(out, finding{
				CVE:         v.VulnerabilityID,
				Severity:    strings.ToUpper(v.Severity),
				Package:     v.PkgName,
				Version:     v.InstalledVersion,
				FixedIn:     v.FixedVersion,
				Description: desc,
				References:  v.References,
			})
		}
	}
	return out
}

func countBySeverity(fs []finding) severities {
	out := severities{}
	for _, f := range fs {
		out[f.Severity]++
	}
	return out
}

// writeError emits the JSON-RPC error envelope on stdout AND mirrors the human
// message to stderr. REM-019: without the stderr mirror the orchestrator only
// sees `exit status 1` with no payload (it parses stdout but logs stderr on
// failure), making every adapter crash look identical. Stderr is the layer
// the platform log pipeline reads; stdout is the wire protocol.
func writeError(id, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	resp := rpcResponse{ID: id, Error: msg}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}
