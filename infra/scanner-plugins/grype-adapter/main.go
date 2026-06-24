// Package main is the Grype scanner adapter for OCI-Janus.
//
// Mirrors the Trivy adapter's contract end-to-end so the orchestrator
// can swap between them at runtime via SetActiveAdapter — same
// JSON-RPC wire shape, same layer staging, same findings output. The
// only thing that differs is the engine: this binary shells out to
// `grype dir:<rootfs> -o json --quiet` instead of `trivy rootfs ...`.
//
// Flow (identical to trivy-adapter — kept in sync deliberately):
//
//	1. read newline-delimited JSON-RPC scan request from stdin
//	2. flatten the staged layer blobs (image_path/<digest_hex>) into a
//	   single rootfs directory, applying layers in manifest order
//	3. invoke `grype dir:<rootfs> -o json --quiet`
//	4. parse Grype's JSON, translate to the contract's findings shape
//	5. write the JSON-RPC response to stdout, exit 0
//
// Scope notes — same v1 trade-offs the Trivy adapter takes (Linux
// images, gzipped tar layers, no whiteout replay). Grype itself has
// broader language-ecosystem coverage than Trivy on JS/Python/Ruby
// dependencies but the same blind spots on OS-only images.
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

// rpcRequest / rpcResponse / result / finding / severities mirror the
// shapes dev-stub + trivy-adapter use. Keeping these byte-identical
// across all three adapters means the orchestrator never has to know
// which engine produced the response.
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

// grypeJSON is the subset of Grype's `-o json` output we consume.
// Full schema is larger (includes match details, signatures, ignored
// matches, language ecosystem metadata); we read only what the
// contract needs.
//
// Schema notes:
//   - Severity is title-case ("Critical" / "High" / etc.) — Trivy
//     emits uppercase. We normalise to uppercase in translateFindings
//     so downstream policy threshold matching (CRITICAL / HIGH /
//     MEDIUM / LOW) stays consistent across adapters.
//   - vulnerability.fix.versions is an array. Grype reports MULTIPLE
//     fix versions per match when an upstream advisory lists several.
//     We surface the first one to match the trivy contract's single
//     FixedVersion field.
//   - The same CVE can match multiple packages in one image (e.g.
//     openssl base + golang-vendored openssl). Grype does NOT dedupe;
//     we dedupe on (CVE, package, version) below for parity with
//     the trivy adapter.
type grypeJSON struct {
	Matches []struct {
		Vulnerability struct {
			ID          string   `json:"id"`
			Severity    string   `json:"severity"`
			Description string   `json:"description"`
			URLs        []string `json:"urls"`
			Fix         struct {
				Versions []string `json:"versions"`
			} `json:"fix"`
		} `json:"vulnerability"`
		Artifact struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"artifact"`
	} `json:"matches"`
	Descriptor struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"descriptor"`
}

// grypeBinary is the path to the grype CLI inside the scanner image.
// Overridable via GRYPE_BIN env var for local dev where someone might
// want to point at a custom build.
var grypeBinary = func() string {
	if p := os.Getenv("GRYPE_BIN"); p != "" {
		return p
	}
	return "/usr/local/bin/grype"
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

	rootfs, err := os.MkdirTemp("", "grype-rootfs-*")
	if err != nil {
		writeError(req.ID, fmt.Sprintf("mkdtemp rootfs: %v", err))
		os.Exit(1)
	}
	defer os.RemoveAll(rootfs)

	// Apply layers in manifest order — orchestrator already passes
	// them sorted; we trust the contract.
	for _, layer := range req.Params.Layers {
		blobPath := filepath.Join(req.Params.ImagePath, strings.TrimPrefix(layer.Digest, "sha256:"))
		if err := extractLayer(blobPath, layer.MediaType, rootfs); err != nil {
			writeError(req.ID, fmt.Sprintf("extract layer %s: %v", layer.Digest, err))
			os.Exit(1)
		}
	}

	report, scannerVersion, err := runGrype(rootfs)
	if err != nil {
		writeError(req.ID, fmt.Sprintf("run grype: %v", err))
		os.Exit(1)
	}

	findings := translateFindings(report)
	resp := rpcResponse{
		ID: req.ID,
		Result: &result{
			ScannerName:    "grype",
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
// into rootfs. Identical to the Trivy adapter's implementation — both
// scanners consume the same flattened rootfs, so the layer handling
// is one shared concern. Kept inline (not extracted into a shared
// package) because the two adapters are intentionally standalone
// single-file programs.
func extractLayer(blobPath, mediaType, rootfs string) error {
	data, err := os.ReadFile(blobPath)
	if err != nil {
		return fmt.Errorf("read blob: %w", err)
	}

	wantGzip := strings.Contains(strings.ToLower(mediaType), "gzip")
	if !wantGzip {
		// Gzip magic bytes: 1f 8b. Try that first so we handle the
		// "media type lies / is missing" case automatically.
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			wantGzip = true
		}
	}

	var reader io.Reader = bytes.NewReader(data)
	if wantGzip {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			// Gzip header lied — fall back to plain tar. Some layer
			// authors mark application/vnd.oci.image.layer.v1.tar+gzip
			// on uncompressed tarballs; refuse to die on that.
			reader = bytes.NewReader(data)
		} else {
			defer gz.Close()
			reader = gz
		}
	}

	tr := tar.NewReader(reader)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar header: %w", err)
		}
		// Skip whiteout files (.wh.*) — overlayfs deletion markers.
		// See package doc for the known-limitation note.
		base := filepath.Base(hdr.Name)
		if strings.HasPrefix(base, ".wh.") {
			continue
		}
		// Defence against tar path traversal: filepath.Clean + an
		// explicit prefix check against rootfs. Matches the same
		// guard the trivy adapter uses.
		target := filepath.Join(rootfs, hdr.Name)
		cleaned := filepath.Clean(target)
		if !strings.HasPrefix(cleaned, filepath.Clean(rootfs)+string(filepath.Separator)) &&
			cleaned != filepath.Clean(rootfs) {
			return fmt.Errorf("tar entry escapes rootfs: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(cleaned, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", cleaned, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(cleaned), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", cleaned, err)
			}
			f, err := os.OpenFile(cleaned, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec
			if err != nil {
				return fmt.Errorf("open %s: %w", cleaned, err)
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec
				_ = f.Close()
				return fmt.Errorf("write %s: %w", cleaned, err)
			}
			_ = f.Close()
		case tar.TypeSymlink, tar.TypeLink:
			// Skip symlinks for the same reason the trivy adapter
			// does: a malicious layer could symlink /etc/passwd into
			// rootfs and trick the next layer's regular file write
			// into following the link out of the sandbox.
			continue
		default:
			// Devices, FIFOs, etc. — irrelevant for a vuln scan.
			continue
		}
	}
}

// runGrype shells out to grype and returns the parsed report plus
// grype's self-reported version (read once from `grype version` so the
// response always reports the binary that actually ran).
func runGrype(rootfs string) (*grypeJSON, string, error) {
	version := grypeVersion()

	// `grype dir:<rootfs>` scans the directory tree directly.
	// -o json sends the structured report to stdout.
	// --quiet drops the spinner / progress so stdout is JSON-only.
	cmd := exec.Command(grypeBinary, "dir:"+rootfs, "-o", "json", "--quiet") //nolint:gosec
	cmd.Env = pluginEnv()

	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, version, fmt.Errorf("grype exited %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, version, err
	}

	var report grypeJSON
	if err := json.Unmarshal(stdout, &report); err != nil {
		return nil, version, fmt.Errorf("parse grype json: %w", err)
	}
	// Prefer the version embedded in the report when present —
	// authoritative for the binary that actually ran. Fall back to the
	// CLI probe.
	if report.Descriptor.Version != "" {
		version = report.Descriptor.Version
	}
	return &report, version, nil
}

// grypeVersion runs `grype version` and parses the "Application:" line.
// Grype's version output is multi-line; we grep for the relevant
// field. Errors are swallowed — a missing version reports as
// "unknown" because the findings are more valuable than the label.
func grypeVersion() string {
	out, err := exec.Command(grypeBinary, "version").Output() //nolint:gosec
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Application:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Application:"))
		}
		if strings.HasPrefix(line, "Version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		}
	}
	return "unknown"
}

// pluginEnv returns the env grype can see. Same allowlist shape as
// trivy-adapter — the orchestrator already strips host secrets before
// invoking us, this is defence-in-depth so a subverted grype can't
// inherit anything from this adapter. GRYPE_* env vars are explicitly
// passed through so operators can configure cache directories +
// vulnerability DB locations without code changes.
func pluginEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"USER": true, "USERNAME": true,
		"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
		"GRYPE_BIN": true,
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if allowed[k] || strings.HasPrefix(k, "GRYPE_") {
			env = append(env, e)
		}
	}
	return env
}

// translateFindings folds grype's matches[] into the flat findings
// list the contract expects. Dedupes on (CVE, package, version) for
// parity with the trivy adapter — same finding shouldn't surface
// twice from package match + language-ecosystem match. Severity is
// normalised to uppercase so the FE-API-050 block_on_severity
// comparison stays consistent across adapters (trivy emits uppercase
// already, grype emits title case).
func translateFindings(report *grypeJSON) []finding {
	if report == nil {
		return nil
	}
	type key struct{ cve, pkg, ver string }
	seen := map[key]bool{}
	out := make([]finding, 0)
	for _, m := range report.Matches {
		k := key{m.Vulnerability.ID, m.Artifact.Name, m.Artifact.Version}
		if seen[k] {
			continue
		}
		seen[k] = true
		fixedIn := ""
		if len(m.Vulnerability.Fix.Versions) > 0 {
			// Grype lists multiple fix versions when an upstream
			// advisory does (e.g. "fixed in 1.2.4 OR 1.3.1"). Surface
			// the first — that's the operator's "smallest upgrade"
			// answer in the common case.
			fixedIn = m.Vulnerability.Fix.Versions[0]
		}
		out = append(out, finding{
			CVE:         m.Vulnerability.ID,
			Severity:    strings.ToUpper(m.Vulnerability.Severity),
			Package:     m.Artifact.Name,
			Version:     m.Artifact.Version,
			FixedIn:     fixedIn,
			Description: m.Vulnerability.Description,
			References:  m.Vulnerability.URLs,
		})
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
// failure), making every adapter crash look identical.
func writeError(id, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	resp := rpcResponse{ID: id, Error: msg}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}
