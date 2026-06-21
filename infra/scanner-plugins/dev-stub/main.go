// Package main implements a deterministic JSON-RPC scanner adapter used as
// the zero-config default in dev. It satisfies the contract defined by
// services/scanner/internal/plugin/process.go without doing any real
// CVE detection — the same fixed set of findings is returned for every
// scan so the UI flow (trigger → pending → complete → findings) can be
// exercised end-to-end against a known-good payload.
//
// Why we ship this:
//   - Real vulnerability scanners (Trivy, Grype) take time + a vuln DB
//     download to set up; nobody wants to wait for that during UI work.
//   - The plugin contract is non-trivial — every "is the scan path
//     working?" question used to be unanswerable without writing a
//     throwaway adapter. This file is the answer.
//
// Wire shape (newline-delimited JSON-RPC over stdin/stdout):
//
//	stdin   {"id","method":"scan","params":{tenant_id, manifest_digest, layers, image_path}}
//	stdout  {"id","result":{scanner_name, scanner_version, findings, severity_counts}}
//
// See libs/scanner/plugin/plugin.go for the Finding struct and
// services/scanner/internal/plugin/process.go for the wire envelope.
//
// SECURITY: this adapter must never be the default in a non-dev image.
// The Dockerfile selects it only when SCANNER_ADAPTER_DEFAULT=dev-stub.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// scannerName + scannerVersion are echoed back so the management UI can
// show "dev-stub@v1" instead of "unknown" — an operator looking at a
// stuck demo can immediately tell they're seeing canned data, not real
// scan results.
const (
	scannerName    = "dev-stub"
	scannerVersion = "v1"
)

// rpcRequest matches services/scanner/internal/plugin/process.go.
// We unmarshal the envelope but don't actually use the params beyond
// echoing the id — the canned findings are independent of the input.
type rpcRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// rpcResponse matches the envelope process.go reads from stdout.
type rpcResponse struct {
	ID     string  `json:"id"`
	Result *result `json:"result,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// result is the inner payload that process.go unmarshals into rpcResult.
// Field tags must match exactly: scanner_name, scanner_version,
// findings, severity_counts.
type result struct {
	ScannerName    string    `json:"scanner_name"`
	ScannerVersion string    `json:"scanner_version"`
	Findings       []finding `json:"findings"`
	SeverityCounts severities `json:"severity_counts"`
}

// finding mirrors libplugin.Finding (libs/scanner/plugin/plugin.go).
// The JSON field names are derived from Go's default lowercasing —
// libplugin.Finding has no json tags, so the wire keys are exactly the
// Go field names. Keep this struct's tags identical or the worker will
// silently drop fields.
type finding struct {
	CVE         string   `json:"CVE"`
	Severity    string   `json:"Severity"`
	Package     string   `json:"Package"`
	Version     string   `json:"Version"`
	FixedIn     string   `json:"FixedIn"`
	Description string   `json:"Description"`
	References  []string `json:"References"`
}

// severities is the JSON-RPC severity_counts map. Keys MUST be uppercase
// to match what /security/vulnerabilities and the per-tag scan panel
// expect (CRITICAL, HIGH, MEDIUM, LOW, NEGLIGIBLE).
type severities map[string]int

// fixtureFindings is the canned dataset every dev-stub scan returns.
// One row per severity tier so the UI's SeverityBar shows movement;
// CVE IDs are real published vulnerabilities so the NVD links in the
// /security/vulnerabilities table resolve to genuine NVD pages — saves
// the "dev demo had a fake CVE-1234 and the link 404'd" foot-gun.
var fixtureFindings = []finding{
	{
		CVE:         "CVE-2024-1234",
		Severity:    "CRITICAL",
		Package:     "openssl",
		Version:     "3.0.7",
		FixedIn:     "3.0.13",
		Description: "[dev-stub] Synthetic CRITICAL finding — replace this adapter with trivy-adapter for real CVE detection.",
		References:  []string{"https://nvd.nist.gov/vuln/detail/CVE-2024-1234"},
	},
	{
		CVE:         "CVE-2023-5678",
		Severity:    "HIGH",
		Package:     "libcurl",
		Version:     "7.88.1",
		FixedIn:     "7.89.0",
		Description: "[dev-stub] Synthetic HIGH finding — exercises the per-row expand affordance on /security/vulnerabilities.",
		References:  []string{"https://nvd.nist.gov/vuln/detail/CVE-2023-5678"},
	},
	{
		CVE:         "CVE-2023-9999",
		Severity:    "MEDIUM",
		Package:     "zlib",
		Version:     "1.2.13",
		FixedIn:     "1.2.14",
		Description: "[dev-stub] Synthetic MEDIUM finding.",
		References:  []string{"https://nvd.nist.gov/vuln/detail/CVE-2023-9999"},
	},
	{
		CVE:         "CVE-2022-0001",
		Severity:    "LOW",
		Package:     "musl",
		Version:     "1.2.3",
		FixedIn:     "",
		Description: "[dev-stub] Synthetic LOW finding — no fix available, exercises the 'none' rendering path.",
		References:  []string{"https://nvd.nist.gov/vuln/detail/CVE-2022-0001"},
	},
}

func main() {
	// One request, one response, then exit — same lifecycle as a real
	// adapter would have. The scanner service spawns a fresh process per
	// scan, so we never need to handle multiple requests in one run.
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		writeError("", fmt.Sprintf("read stdin: %v", err))
		os.Exit(1)
	}

	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		// Empty ID — the orchestrator will surface this as "plugin
		// reported error" with an id-mismatch error, which is exactly
		// the right failure mode for a malformed handoff.
		writeError("", fmt.Sprintf("unmarshal request: %v", err))
		os.Exit(1)
	}

	if req.Method != "scan" {
		writeError(req.ID, fmt.Sprintf("unknown method %q (expected \"scan\")", req.Method))
		os.Exit(1)
	}

	resp := rpcResponse{
		ID: req.ID,
		Result: &result{
			ScannerName:    scannerName,
			ScannerVersion: scannerVersion,
			Findings:       fixtureFindings,
			SeverityCounts: countBySeverity(fixtureFindings),
		},
	}

	// Newline-terminated so process.go's bufio reader (if it ever
	// switches from io.ReadAll) doesn't block waiting for EOF.
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		// stderr only — stdout is the wire and we've already partially
		// written. The orchestrator caps stdout at 10 MiB and reads
		// what's there; a partial write will fail unmarshal anyway.
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
		os.Exit(1)
	}
}

// countBySeverity tallies a slice of findings into the severity_counts
// map shape the worker persists. Derived rather than hardcoded so the
// counts stay in lockstep with fixtureFindings when we edit the list.
func countBySeverity(fs []finding) severities {
	out := severities{}
	for _, f := range fs {
		out[f.Severity]++
	}
	return out
}

// writeError emits a JSON-RPC error envelope to stdout. The orchestrator
// surfaces resp.Error verbatim in slog so keep the message terse and
// actionable.
func writeError(id, msg string) {
	resp := rpcResponse{ID: id, Error: msg}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
}
