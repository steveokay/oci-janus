// Package main is the Trivy scanner adapter for OCI-Janus.
//
// It implements the JSON-RPC contract defined by
// services/scanner/internal/plugin/process.go on top of the trivy-engine
// sidecar (see infra/scanner-plugins/trivy-engine). The orchestrator never
// talks to trivy directly — every scanner_id swap is just pointing
// SCANNER_PLUGIN_PATH at a different adapter, of which this is one.
//
// Flow:
//
//  1. read newline-delimited JSON-RPC scan request from stdin
//  2. flatten the staged layer blobs (image_path/<digest_hex>) into a
//     single rootfs directory under the shared scan-work volume, applying
//     layers in manifest order
//  3. POST the flattened rootfs path to the trivy-engine sidecar
//     ($TRIVY_ENGINE_URL/scan) and read back the engine's JSON
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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	rootfs, err := os.MkdirTemp(scanWorkDir(), "trivy-rootfs-*")
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

	report, scannerVersion, err := scanViaEngine(engineURL(), rootfs)
	if err != nil {
		writeError(req.ID, err.Error())
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
