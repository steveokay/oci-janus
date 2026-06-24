// Package plugin implements the external process scanner plugin interface.
// Plugins are invoked as subprocesses; all communication uses newline-delimited
// JSON-RPC on stdin/stdout. No user-supplied data is passed as CLI arguments.
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	libplugin "github.com/steveokay/oci-janus/libs/scanner/plugin"
)

// rpcRequest is the JSON-RPC envelope sent to the plugin process via stdin.
type rpcRequest struct {
	ID     string    `json:"id"`
	Method string    `json:"method"`
	Params rpcParams `json:"params"`
}

// rpcParams carries the scan job details sent to the plugin process.
// ImagePath is a host-local temp directory pre-populated with layer blobs;
// the plugin reads files from it so it never needs direct storage credentials.
type rpcParams struct {
	TenantID       string               `json:"tenant_id"`
	ManifestDigest string               `json:"manifest_digest"`
	Layers         []libplugin.LayerRef `json:"layers"`
	ImagePath      string               `json:"image_path"`
}

// rpcResponse is the JSON-RPC envelope received from the plugin process via stdout.
type rpcResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// rpcResult is the inner result object returned by the plugin inside rpcResponse.Result.
type rpcResult struct {
	ScannerName    string              `json:"scanner_name"`
	ScannerVersion string              `json:"scanner_version"`
	Findings       []libplugin.Finding `json:"findings"`
	SeverityCounts map[string]int      `json:"severity_counts"`
}

// ProcessPlugin invokes a scanner binary as an external process.
type ProcessPlugin struct {
	path string
}

// New validates the plugin binary path and checksum then returns a ProcessPlugin.
// Returns an error and must not be used if either check fails.
// Path sanitisation and checksum verification both happen here so the service
// fails fast at startup rather than executing an unexpected binary at scan time.
func New(pluginPath, expectedChecksum string) (*ProcessPlugin, error) {
	// Sanitise plugin path per CLAUDE.md §17: filepath.Clean removes ".." segments
	// and redundant slashes. Require an absolute path to prevent relative-path
	// confusion attacks where the working directory affects which binary is executed.
	pluginPath = filepath.Clean(pluginPath)
	if !filepath.IsAbs(pluginPath) {
		return nil, fmt.Errorf("SCANNER_PLUGIN_PATH must be an absolute path, got %q", pluginPath)
	}

	actual, err := fileSHA256(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read plugin binary %q: %w", pluginPath, err)
	}
	if !strings.EqualFold(actual, expectedChecksum) {
		slog.Error("plugin checksum mismatch — refusing to start",
			"path", pluginPath,
			"expected", expectedChecksum,
			"actual", actual,
		)
		return nil, fmt.Errorf("plugin checksum mismatch: expected %s got %s", expectedChecksum, actual)
	}
	slog.Info("plugin checksum verified", "path", pluginPath, "sha256", actual)
	return &ProcessPlugin{path: pluginPath}, nil
}

// Name returns the plugin binary path as the identifier (the process itself reports name in the result).
func (p *ProcessPlugin) Name() string { return p.path }

// Version is resolved at scan time from the plugin response.
func (p *ProcessPlugin) Version() string { return "unknown" }

// Scan stages layer blobs into a temporary directory, invokes the plugin process with a
// JSON-RPC request over stdin, and parses the result from stdout.
// The layer blobs are fetched via req.StorageFetcher before spawning the process so the
// plugin only needs to read local files — it never has storage credentials.
func (p *ProcessPlugin) Scan(ctx context.Context, req libplugin.ScanRequest) (*libplugin.ScanResult, error) {
	tmpDir, err := os.MkdirTemp("", "registry-scan-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Stage layer blobs to disk so the plugin can read them without storage credentials.
	for _, layer := range req.Layers {
		if err := stageBlobToDir(ctx, req.StorageFetcher, tmpDir, layer.Digest); err != nil {
			return nil, fmt.Errorf("stage layer %s: %w", layer.Digest, err)
		}
	}

	reqID := fmt.Sprintf("%d", time.Now().UnixNano())
	rpcReq := rpcRequest{
		ID:     reqID,
		Method: "scan",
		Params: rpcParams{
			TenantID:       req.TenantID,
			ManifestDigest: req.ManifestDigest,
			Layers:         req.Layers,
			ImagePath:      tmpDir,
		},
	}
	reqJSON, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}
	reqJSON = append(reqJSON, '\n')

	// exec.CommandContext cancels the process when ctx expires.
	// No user-supplied arguments — all data flows through stdin.
	cmd := exec.CommandContext(ctx, p.path) //nolint:gosec
	cmd.Stdin = strings.NewReader(string(reqJSON))
	// Restrict environment to an explicit allowlist so the plugin cannot
	// inherit host secrets (DB_DSN, JWT keys, cloud credentials, etc.).
	cmd.Env = pluginEnv()
	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin process: %w", err)
	}

	// Cap stdout at 10 MiB to prevent a malicious or runaway plugin from
	// exhausting memory on the orchestrator host.
	const maxStdout = 10 << 20
	stdout, err := io.ReadAll(io.LimitReader(stdoutPipe, maxStdout))
	waitErr := cmd.Wait()
	if waitErr != nil {
		// REM-019: adapters that follow the JSON-RPC convention write their
		// error to stdout then exit non-zero. Without this branch the
		// payload is discarded and every adapter crash logs as a generic
		// `exit status 1` with empty stderr. Try to parse the stdout
		// envelope first so we surface the actual error string; fall back
		// to the raw exit error if stdout isn't parseable.
		pluginErrMsg := ""
		if len(stdout) > 0 {
			var resp rpcResponse
			if jerr := json.Unmarshal(stdout, &resp); jerr == nil && resp.Error != "" {
				pluginErrMsg = resp.Error
			}
		}
		slog.Error("plugin process failed",
			"path", p.path,
			"stderr", stderr.String(),
			"stdout_error", pluginErrMsg,
			"error", waitErr,
		)
		if pluginErrMsg != "" {
			return nil, fmt.Errorf("plugin process exited with error: %s: %w", pluginErrMsg, waitErr)
		}
		return nil, fmt.Errorf("plugin process exited with error: %w", waitErr)
	}
	if err != nil {
		return nil, fmt.Errorf("read plugin stdout: %w", err)
	}

	var resp rpcResponse
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal plugin response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("plugin reported error: %s", resp.Error)
	}
	if resp.ID != reqID {
		return nil, fmt.Errorf("plugin response id mismatch: expected %s got %s", reqID, resp.ID)
	}

	var result rpcResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal plugin result: %w", err)
	}

	return &libplugin.ScanResult{
		ScannerName:    result.ScannerName,
		ScannerVersion: result.ScannerVersion,
		Findings:       result.Findings,
		SeverityCounts: result.SeverityCounts,
		ScannedAt:      time.Now(),
	}, nil
}

// stageBlobToDir fetches a blob via BlobFetcher and writes it to <dir>/<digest_hex>.
func stageBlobToDir(ctx context.Context, fetcher libplugin.BlobFetcher, dir, digest string) error {
	rc, err := fetcher.FetchBlob(ctx, digest)
	if err != nil {
		return err
	}
	defer rc.Close()

	hex := strings.TrimPrefix(digest, "sha256:")
	f, err := os.Create(fmt.Sprintf("%s/%s", dir, hex))
	if err != nil {
		return fmt.Errorf("create layer file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, rc); err != nil {
		return fmt.Errorf("write layer data: %w", err)
	}
	return nil
}

// pluginEnv returns a minimal environment for the scanner subprocess.
// Only well-known, non-sensitive variables are forwarded; all service secrets
// (DB_DSN, JWT keys, cloud credentials) are intentionally excluded.
func pluginEnv() []string {
	allowed := []string{
		"PATH", "HOME", "TMPDIR", "TMP", "TEMP",
		"USER", "USERNAME",
		"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
	}
	// Prefixes for scanner-specific config that plugins may legitimately need.
	// REM-014: CLAIR_ joins the list — the clair-adapter reads CLAIR_URL +
	// CLAIR_FETCH_HOST + CLAIR_FETCH_PORT + CLAIR_POLL_INTERVAL +
	// CLAIR_TIMEOUT. The same allowlist gate the other adapters live
	// behind — services/scanner can't inadvertently leak DB_DSN / JWT
	// keys / cloud credentials into a subprocess by adding a prefix.
	allowedPrefixes := []string{"TRIVY_", "GRYPE_", "CLAIR_"}

	var env []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if slices.Contains(allowed, k) {
			env = append(env, e)
			continue
		}
		for _, p := range allowedPrefixes {
			if strings.HasPrefix(k, p) {
				env = append(env, e)
				break
			}
		}
	}
	return env
}

// fileSHA256 returns the lowercase hex SHA256 digest of the file at path.
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
