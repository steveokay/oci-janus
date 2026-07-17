// Package main is the Grype scanner adapter for OCI-Janus.
//
// Mirrors the Trivy adapter's contract end-to-end so the orchestrator
// can swap between them at runtime via SetActiveAdapter — same
// JSON-RPC wire shape, same layer staging, same findings output. The
// only thing that differs is the engine: this binary talks to the
// grype-engine sidecar over HTTP instead of the trivy-engine sidecar.
//
// Flow (identical to trivy-adapter — kept in sync deliberately):
//
//  1. read newline-delimited JSON-RPC scan request from stdin
//  2. flatten the staged layer blobs (image_path/<digest_hex>) into a
//     single rootfs directory under the shared scan-work volume,
//     applying layers in manifest order
//  3. POST the flattened rootfs path to the grype-engine sidecar
//     ($GRYPE_ENGINE_URL/scan) and read back the engine's JSON
//  4. parse Grype's JSON, translate to the contract's findings shape
//  5. write the JSON-RPC response to stdout, exit 0
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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// errEngineUnreachable is the sentinel substring the scanner orchestrator
// keys on to distinguish "the engine sidecar is down/unreachable" (a deploy
// problem) from "the engine ran and errored" (a real scan failure). Mirrors
// the trivy-adapter's constant of the same name.
const errEngineUnreachable = "engine_unreachable"

// engineURL returns the grype-engine sidecar base URL. Required — an unset
// value is a deployment misconfiguration and fails the scan cleanly.
func engineURL() string { return os.Getenv("GRYPE_ENGINE_URL") }

// scanWorkDir is the shared volume both this adapter (rw) and the engine
// sidecar (ro) mount. The adapter flattens the rootfs here so the sidecar
// can read it at the identical path. Overridable for tests / local dev.
func scanWorkDir() string {
	if d := os.Getenv("SCANNER_SCAN_WORK_DIR"); d != "" {
		return d
	}
	return "/scan-work"
}

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

	// Flatten onto the SHARED work volume so the engine sidecar can read the
	// rootfs at the same absolute path. os.MkdirTemp under scanWorkDir() keeps
	// per-scan isolation; defer RemoveAll cleans it after the engine responds.
	if err := os.MkdirAll(scanWorkDir(), 0o755); err != nil {
		writeError(req.ID, fmt.Sprintf("mkdir work dir: %v", err))
		os.Exit(1)
	}
	rootfs, err := os.MkdirTemp(scanWorkDir(), "grype-rootfs-*")
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

	report, scannerVersion, err := scanViaEngine(engineURL(), rootfs)
	if err != nil {
		writeError(req.ID, err.Error())
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

// scanViaEngine POSTs the flattened rootfs path to the grype-engine sidecar and
// returns the parsed grype report + the engine's self-reported version. A
// connection/timeout failure is wrapped with errEngineUnreachable so the
// orchestrator classifies it as a deploy problem, not a scan failure.
func scanViaEngine(baseURL, rootfs string) (*grypeJSON, string, error) {
	if baseURL == "" {
		return nil, "unknown", fmt.Errorf("GRYPE_ENGINE_URL not set; cannot reach grype-engine sidecar")
	}
	body, _ := json.Marshal(map[string]string{"rootfs": rootfs})
	// Generous deadline: a cold engine may still be warming its DB; matches
	// the trivy-adapter's minute-scale timeout.
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
		return nil, "unknown", fmt.Errorf("grype-engine returned %d: %s", resp.StatusCode, strings.TrimSpace(er.Error))
	}
	var env struct {
		Version string          `json:"version"`
		Raw     json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "unknown", fmt.Errorf("decode engine response: %w", err)
	}
	var report grypeJSON
	if err := json.Unmarshal(env.Raw, &report); err != nil {
		return nil, env.Version, fmt.Errorf("parse grype json: %w", err)
	}
	// Prefer the version embedded in the report when present — authoritative
	// for the binary that actually ran inside the engine. Fall back to the
	// engine's self-reported version.
	if report.Descriptor.Version != "" {
		return &report, report.Descriptor.Version, nil
	}
	return &report, env.Version, nil
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
