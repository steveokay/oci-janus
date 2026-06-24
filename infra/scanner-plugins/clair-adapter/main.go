// Package main is the Clair v4 scanner adapter for OCI-Janus.
//
// REM-014 (2026-06-22) — Clair multi-scanner integration.
//
// Unlike the Trivy + Grype adapters (which receive pre-staged layer
// files and shell out to a CLI), Clair operates as a long-running HTTP
// service that pulls layers itself given URIs. This adapter bridges
// the gap by:
//
//  1. Reading the standard JSON-RPC scan request from stdin (same wire
//     shape Trivy + Grype use — `tenant_id`, `manifest_digest`,
//     `layers[]`, `image_path`).
//  2. Starting an embedded HTTP server on $CLAIR_FETCH_PORT (default
//     9099) that serves the staged layer files under
//     `/<digest_hex>`. The server binds on the scanner container's
//     `registry` network so the Clair container can reach it at
//     `http://$CLAIR_FETCH_HOST:$CLAIR_FETCH_PORT/<digest>`.
//  3. POSTing a manifest to Clair's indexer
//     (`POST /indexer/api/v1/index_report`) with the layer URIs.
//  4. Polling `/indexer/api/v1/index_report/<manifest_hash>` until
//     state ∈ {indexed, error}.
//  5. Fetching the vulnerability report from the matcher
//     (`GET /matcher/api/v1/vulnerability_report/<manifest_hash>`).
//  6. Translating Clair's report into the platform's flat findings
//     shape (same struct Trivy + Grype emit) so the rest of the
//     pipeline doesn't have to special-case Clair output.
//
// Environment:
//
//	CLAIR_URL           Required. Base URL of the Clair indexer + matcher
//	                    (combo mode on the same port). Example:
//	                    http://clair:6060.
//	CLAIR_FETCH_HOST    Hostname the embedded HTTP server is reachable as
//	                    from Clair's perspective. Defaults to the value
//	                    of `HOSTNAME` (the scanner container's name).
//	CLAIR_FETCH_PORT    Port the embedded server binds. Defaults to 9099.
//	CLAIR_POLL_INTERVAL Indexer poll cadence. Defaults to 2s.
//	CLAIR_TIMEOUT       Hard timeout for the full index + match flow.
//	                    Defaults to 5 minutes. Generous because a cold
//	                    Clair start has to fetch vulnerability feeds.
//
// SECURITY: the embedded HTTP server has no authentication. It binds
// only on the docker compose `registry` network (no host port mapping),
// and the registry network already isolates the scanner subnet from
// the public internet. In a production-grade Kubernetes deployment
// this server would be replaced by Clair fetching from registry-core
// directly with a service-account JWT — tracked as a REM-014 follow-up.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// scannerName + scannerVersion identify the adapter on the wire.
// Version mirrors the upstream Clair container pin in docker-compose;
// bumping that should bump this string too so the /security UI shows
// the actual engine version, not the adapter's own.
const (
	scannerName    = "clair"
	scannerVersion = "v4.7"
)

// ─── Wire shape — same envelope as dev-stub / trivy / grype ───────────

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

// ─── Clair wire shapes — minimal slice of the upstream API ────────────
//
// The full schema lives in https://quay.github.io/clair/openapi.html;
// we only deserialise the fields we need so a Clair version bump that
// adds optional fields doesn't break us.

// clairManifest is the POST /indexer/api/v1/index_report body.
type clairManifest struct {
	Hash   string       `json:"hash"`
	Layers []clairLayer `json:"layers"`
}

type clairLayer struct {
	Hash    string            `json:"hash"`
	URI     string            `json:"uri"`
	Headers map[string][]string `json:"headers,omitempty"`
}

// clairIndexReport is what /index_report/<hash> returns. We only care
// about state for the poll loop + err for failure-path logging.
type clairIndexReport struct {
	ManifestHash string `json:"manifest_hash"`
	State        string `json:"state"`
	Err          string `json:"err,omitempty"`
}

// clairVulnReport is the matcher's vulnerability report. Schema:
// `packages[id]`, `vulnerabilities[id]`, `package_vulnerabilities`
// keyed by package id → []vuln_id. The flatten loop walks the map of
// maps so we don't need the full nested schema in Go.
type clairVulnReport struct {
	ManifestHash    string                       `json:"manifest_hash"`
	Packages        map[string]clairPackage      `json:"packages"`
	Vulnerabilities map[string]clairVulnerability `json:"vulnerabilities"`
	// PackageVulnerabilities maps a package id to a list of vuln ids
	// that affect it. The "fix" version (if any) lives on the vuln,
	// not the package, so we need a join across this map to populate
	// our finding.FixedIn field.
	PackageVulnerabilities map[string][]string `json:"package_vulnerabilities"`
}

type clairPackage struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type clairVulnerability struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	NormalizedSeverity string `json:"normalized_severity"`
	FixedInVersion     string `json:"fixed_in_version"`
	Links              string `json:"links"`
}

// ─── main ────────────────────────────────────────────────────────────

func main() {
	// Newline-delimited JSON-RPC over stdin/stdout. One request per
	// process — the parent (services/scanner) execs us per scan.
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)

	var req rpcRequest
	if err := dec.Decode(&req); err != nil {
		writeError(enc, "", fmt.Errorf("decode rpc request: %w", err))
		os.Exit(1)
	}
	if req.Method != "scan" {
		writeError(enc, req.ID, fmt.Errorf("unsupported method %q (expected \"scan\")", req.Method))
		os.Exit(1)
	}

	res, err := scan(req)
	if err != nil {
		writeError(enc, req.ID, err)
		os.Exit(1)
	}

	if encErr := enc.Encode(rpcResponse{ID: req.ID, Result: res}); encErr != nil {
		// Encoding failure on stdout is unrecoverable — log to stderr
		// (scanner reads it for the error column on /admin/scanner)
		// and exit non-zero.
		fmt.Fprintf(os.Stderr, "clair-adapter: encode response: %v\n", encErr)
		os.Exit(1)
	}
}

func scan(req rpcRequest) (*result, error) {
	clairURL := os.Getenv("CLAIR_URL")
	if clairURL == "" {
		return nil, errors.New("CLAIR_URL not set; cannot reach Clair indexer/matcher")
	}
	clairURL = strings.TrimRight(clairURL, "/")

	fetchHost := os.Getenv("CLAIR_FETCH_HOST")
	if fetchHost == "" {
		// Fall back to the container's hostname. Docker compose sets
		// HOSTNAME to the service name by default, which is the
		// hostname Clair sees on the registry network.
		fetchHost, _ = os.Hostname()
	}
	if fetchHost == "" {
		return nil, errors.New("CLAIR_FETCH_HOST not set and os.Hostname() returned empty")
	}

	fetchPort := envOrDefault("CLAIR_FETCH_PORT", "9099")
	pollInterval, err := time.ParseDuration(envOrDefault("CLAIR_POLL_INTERVAL", "2s"))
	if err != nil {
		return nil, fmt.Errorf("invalid CLAIR_POLL_INTERVAL: %w", err)
	}
	timeout, err := time.ParseDuration(envOrDefault("CLAIR_TIMEOUT", "5m"))
	if err != nil {
		return nil, fmt.Errorf("invalid CLAIR_TIMEOUT: %w", err)
	}

	// Spin up the embedded HTTP layer server. The parent process owns
	// req.ImagePath — its lifetime exceeds ours, so the file handles
	// stay open as long as Clair needs to fetch them.
	stop, fetchBase, err := startLayerServer(fetchHost, fetchPort, req.Params.ImagePath)
	if err != nil {
		return nil, fmt.Errorf("start layer server: %w", err)
	}
	defer stop()

	// Build the Clair manifest body. Layer hashes are digest strings
	// with the sha256: prefix — Clair expects them that way too.
	manifest := clairManifest{
		Hash:   req.Params.ManifestDigest,
		Layers: make([]clairLayer, 0, len(req.Params.Layers)),
	}
	for _, l := range req.Params.Layers {
		manifest.Layers = append(manifest.Layers, clairLayer{
			Hash: l.Digest,
			URI:  fmt.Sprintf("%s/%s", fetchBase, hexFromDigest(l.Digest)),
		})
	}

	// Submit the manifest to the indexer. The first call kicks off
	// async indexing; subsequent polls observe the state machine.
	if err := submitManifest(clairURL, manifest); err != nil {
		return nil, fmt.Errorf("submit manifest to Clair indexer: %w", err)
	}

	// Poll until indexer state is `indexed` (success) or `error` (terminal failure).
	if err := waitForIndex(clairURL, manifest.Hash, pollInterval, timeout); err != nil {
		return nil, fmt.Errorf("wait for index: %w", err)
	}

	// Fetch the vulnerability report from the matcher.
	report, err := fetchVulnReport(clairURL, manifest.Hash, timeout)
	if err != nil {
		return nil, fmt.Errorf("fetch vulnerability report: %w", err)
	}

	return translate(report), nil
}

// ─── HTTP layer server ────────────────────────────────────────────────

// startLayerServer binds an HTTP server on 0.0.0.0:<port> that serves
// each layer file under `/<digest_hex>`. Returns:
//
//	stop      - call to shut the server down on the success / error path
//	fetchBase - URL prefix Clair should use, e.g. http://host:port
//	err       - non-nil if the listener could not be bound
//
// The server is intentionally minimal: no path traversal (we resolve
// every request to a flat read-only file inside ImagePath), no auth.
func startLayerServer(host, port, imagePath string) (stop func(), fetchBase string, err error) {
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return nil, "", fmt.Errorf("listen on :%s: %w", port, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Strip the leading slash and reject anything containing path
		// separators — we serve a flat namespace keyed on digest hex.
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || strings.ContainsAny(name, "/\\") {
			http.NotFound(w, r)
			return
		}
		// Hex digests are 64 lowercase hex chars; anything else is
		// suspicious. Cheap allowlist check rather than a regex.
		if len(name) != 64 || !isHex(name) {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(imagePath, name)
		// Re-clean + re-prefix-check to defeat traversal even though
		// the allowlist above already covers it. Defence in depth.
		clean := filepath.Clean(path)
		if !strings.HasPrefix(clean, filepath.Clean(imagePath)+string(os.PathSeparator)) &&
			clean != filepath.Clean(imagePath) {
			http.NotFound(w, r)
			return
		}
		f, openErr := os.Open(clean)
		if openErr != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, f)
	})

	srv := &http.Server{
		Handler: mux,
		// Generous timeouts because Clair's layer fetcher can stream
		// hundreds of MB on a single connection; the registry network
		// is loopback-fast but the read may stall on local IO.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      10 * time.Minute,
	}

	go func() {
		_ = srv.Serve(listener)
	}()

	stop = func() {
		_ = srv.Close()
	}
	fetchBase = fmt.Sprintf("http://%s:%s", host, port)
	return stop, fetchBase, nil
}

// ─── Clair API calls ─────────────────────────────────────────────────

func submitManifest(clairURL string, m clairManifest) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, clairURL+"/indexer/api/v1/index_report", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST /index_report: %w", err)
	}
	defer resp.Body.Close()
	// Clair returns 201 on first submission, 200 on subsequent re-
	// submissions of the same hash (idempotent). Anything else is an
	// error.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("Clair indexer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	return nil
}

// waitForIndex polls /index_report/<hash> until state ∈ {indexed,
// error}. Returns a wrapped error if the state machine lands on
// `error`; otherwise nil on `indexed`. Timeout is enforced via a
// deadline so a stuck indexer can't pin this adapter forever.
func waitForIndex(clairURL, hash string, interval, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 30 * time.Second}
	url := clairURL + "/indexer/api/v1/index_report/" + hash
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Clair indexer (%s)", timeout)
		}
		resp, err := client.Get(url)
		if err != nil {
			return fmt.Errorf("GET /index_report/%s: %w", hash, err)
		}
		if resp.StatusCode == http.StatusNotFound {
			// 404 right after submission can happen — the indexer
			// row hasn't materialised yet. Sleep + retry.
			resp.Body.Close()
			time.Sleep(interval)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return fmt.Errorf("indexer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var report clairIndexReport
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
			resp.Body.Close()
			return fmt.Errorf("decode index report: %w", err)
		}
		resp.Body.Close()
		switch report.State {
		case "IndexFinished", "indexed":
			return nil
		case "IndexError", "error":
			return fmt.Errorf("Clair indexer reported error: %s", report.Err)
		default:
			// In-flight states: ManifestQueued, ScanningPackages, etc.
			// Sleep + retry.
			time.Sleep(interval)
		}
	}
}

func fetchVulnReport(clairURL, hash string, timeout time.Duration) (*clairVulnReport, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(clairURL + "/matcher/api/v1/vulnerability_report/" + hash)
	if err != nil {
		return nil, fmt.Errorf("GET /vulnerability_report/%s: %w", hash, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("matcher returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var report clairVulnReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return nil, fmt.Errorf("decode vulnerability report: %w", err)
	}
	return &report, nil
}

// ─── Translation ─────────────────────────────────────────────────────

// translate flattens Clair's nested vulnerability report into the
// platform's flat findings shape. We dedupe on (CVE, package, version)
// so the same vuln applied to two different layers doesn't surface as
// two findings — that matches the behaviour of trivy-adapter +
// grype-adapter so the FE's severity counters stay comparable across
// adapter swaps.
func translate(report *clairVulnReport) *result {
	counts := severities{}
	seen := map[string]struct{}{}
	findings := make([]finding, 0, 32)

	for pkgID, vulnIDs := range report.PackageVulnerabilities {
		pkg, ok := report.Packages[pkgID]
		if !ok {
			// Package metadata missing — skip rather than emit a
			// finding without a package name (would render as
			// "vulnerable: " in the UI).
			continue
		}
		for _, vulnID := range vulnIDs {
			v, ok := report.Vulnerabilities[vulnID]
			if !ok {
				continue
			}
			cve := v.Name
			if cve == "" {
				// Some advisories don't have a CVE ID — fall back to
				// the Clair-internal vuln id so the finding has
				// something stable to dedupe on.
				cve = v.ID
			}
			key := cve + "|" + pkg.Name + "|" + pkg.Version
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}

			sev := normaliseSeverity(v.NormalizedSeverity)
			counts[sev]++

			refs := []string{}
			if v.Links != "" {
				// Clair packs multiple URLs into a single space-
				// separated string on .Links; split so the FE can
				// render them as individual link chips.
				for _, ref := range strings.Fields(v.Links) {
					refs = append(refs, ref)
				}
			}

			findings = append(findings, finding{
				CVE:         cve,
				Severity:    sev,
				Package:     pkg.Name,
				Version:     pkg.Version,
				FixedIn:     v.FixedInVersion,
				Description: v.Description,
				References:  refs,
			})
		}
	}

	return &result{
		ScannerName:    scannerName,
		ScannerVersion: scannerVersion,
		Findings:       findings,
		SeverityCounts: counts,
	}
}

// normaliseSeverity maps Clair's normalized_severity values to the
// platform's uppercase canonical set. Clair's possible values per the
// upstream docs: "Unknown", "Negligible", "Low", "Medium", "High",
// "Critical". Anything else falls back to UNKNOWN.
func normaliseSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "CRITICAL"
	case "high":
		return "HIGH"
	case "medium":
		return "MEDIUM"
	case "low":
		return "LOW"
	case "negligible":
		return "NEGLIGIBLE"
	default:
		return "UNKNOWN"
	}
}

// ─── helpers ────────────────────────────────────────────────────────

// writeError emits the JSON-RPC error envelope on stdout AND mirrors the human
// message to stderr. REM-019: without the stderr mirror the orchestrator only
// sees `exit status 1` with no payload (it parses stdout but logs stderr on
// failure), making every adapter crash look identical.
func writeError(enc *json.Encoder, id string, err error) {
	fmt.Fprintln(os.Stderr, err.Error())
	_ = enc.Encode(rpcResponse{ID: id, Error: err.Error()})
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hexFromDigest(digest string) string {
	// Strip the `sha256:` prefix so the HTTP path is the bare hex
	// string — both the embedded server and the orchestrator stage
	// files using that convention.
	if i := strings.IndexByte(digest, ':'); i >= 0 {
		return digest[i+1:]
	}
	return digest
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// unused helper kept to mirror the trivy/grype adapters' shape — they
// stitch their own strconv.Atoi for the port. Leaving it here documents
// that the port env var IS validated even though Go's net package will
// already reject "abc" with a clear error.
var _ = strconv.Atoi
