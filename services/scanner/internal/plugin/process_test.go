// Package plugin_test tests the ProcessPlugin helpers and checksum validation.
// The external subprocess Scan path is exercised via an inline shell script
// written to a temp file at test time — no real scanner binary is required.
package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	libplugin "github.com/steveokay/oci-janus/libs/scanner/plugin"
)

// writeTempFile creates a temporary file with the given content and returns its path.
// The caller is responsible for removing the file.
func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", "plugin-test-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

// sha256OfBytes returns the lowercase hex SHA256 of b.
func sha256OfBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestNew_relativePath verifies that New() rejects a relative plugin path.
func TestNew_relativePath(t *testing.T) {
	_, err := New("relative/path/plugin", "deadbeef")
	if err == nil {
		t.Fatal("expected error for relative plugin path")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("error message should mention 'absolute path', got: %v", err)
	}
}

// TestNew_nonExistentFile verifies that New() returns an error when the plugin
// binary does not exist (fileSHA256 will fail to open the file).
func TestNew_nonExistentFile(t *testing.T) {
	_, err := New("/nonexistent/path/to/plugin", "anychecksum")
	if err == nil {
		t.Fatal("expected error for non-existent plugin file")
	}
}

// TestNew_checksumMismatch verifies that New() returns an error when the actual
// checksum of the plugin binary does not match the expected value.
func TestNew_checksumMismatch(t *testing.T) {
	content := []byte("#!/bin/sh\necho hello\n")
	path := writeTempFile(t, content)
	defer os.Remove(path)

	_, err := New(path, "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should mention 'checksum mismatch', got: %v", err)
	}
}

// TestNew_checksumMatch verifies that New() succeeds when the checksum matches.
func TestNew_checksumMatch(t *testing.T) {
	content := []byte("#!/bin/sh\necho hello\n")
	path := writeTempFile(t, content)
	defer os.Remove(path)

	checksum := sha256OfBytes(content)
	p, err := New(path, checksum)
	if err != nil {
		t.Fatalf("expected success with matching checksum, got: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil ProcessPlugin")
	}
}

// TestNew_checksumCaseInsensitive verifies that checksum comparison is case-insensitive
// (hex is case-insensitive by convention).
func TestNew_checksumCaseInsensitive(t *testing.T) {
	content := []byte("binary content here")
	path := writeTempFile(t, content)
	defer os.Remove(path)

	checksum := strings.ToUpper(sha256OfBytes(content))
	_, err := New(path, checksum)
	if err != nil {
		t.Fatalf("checksum comparison should be case-insensitive, got: %v", err)
	}
}

// TestProcessPlugin_nameAndVersion verifies that Name() returns the plugin path
// and Version() returns the static "unknown" sentinel.
func TestProcessPlugin_nameAndVersion(t *testing.T) {
	content := []byte("placeholder binary")
	path := writeTempFile(t, content)
	defer os.Remove(path)

	checksum := sha256OfBytes(content)
	p, err := New(path, checksum)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// filepath.Clean(path) is applied in New(), so Name() returns the cleaned path.
	if p.Name() != filepath.Clean(path) {
		t.Errorf("Name(): got %q, want %q", p.Name(), filepath.Clean(path))
	}
	if p.Version() != "unknown" {
		t.Errorf("Version(): got %q, want %q", p.Version(), "unknown")
	}
}

// TestFileSHA256_knownContent verifies that fileSHA256 returns the correct
// SHA256 for a file with known content.
func TestFileSHA256_knownContent(t *testing.T) {
	content := []byte("hello, registry")
	path := writeTempFile(t, content)
	defer os.Remove(path)

	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	want := sha256OfBytes(content)
	if got != want {
		t.Errorf("fileSHA256: got %q, want %q", got, want)
	}
}

// TestFileSHA256_emptyFile verifies that an empty file returns the SHA256 of
// an empty byte slice (well-defined value: e3b0c44298...).
func TestFileSHA256_emptyFile(t *testing.T) {
	path := writeTempFile(t, []byte{})
	defer os.Remove(path)

	got, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	want := sha256OfBytes([]byte{})
	if got != want {
		t.Errorf("fileSHA256(empty): got %q, want %q", got, want)
	}
}

// TestFileSHA256_nonExistentFile verifies that fileSHA256 returns an error for
// a path that does not exist.
func TestFileSHA256_nonExistentFile(t *testing.T) {
	_, err := fileSHA256("/tmp/this-file-does-not-exist-99999")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

// TestPluginEnv_doesNotContainSecrets verifies that pluginEnv() strips
// sensitive-looking variables from the environment.
func TestPluginEnv_doesNotContainSecrets(t *testing.T) {
	// Set some secret-looking variables in the test process environment.
	os.Setenv("DB_DSN", "postgres://secret@localhost/db")
	os.Setenv("JWT_PRIVATE_KEY", "super-secret-key")
	os.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	defer func() {
		os.Unsetenv("DB_DSN")
		os.Unsetenv("JWT_PRIVATE_KEY")
		os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	}()

	env := pluginEnv()

	forbidden := []string{"DB_DSN", "JWT_PRIVATE_KEY", "AWS_SECRET_ACCESS_KEY"}
	for _, kv := range env {
		for _, bad := range forbidden {
			if strings.HasPrefix(kv, bad+"=") {
				t.Errorf("pluginEnv() leaked secret variable: %s", bad)
			}
		}
	}
}

// TestPluginEnv_allowsPathAndHome verifies that PATH and HOME are forwarded
// to the plugin (necessary for it to locate binaries and read config).
func TestPluginEnv_allowsPathAndHome(t *testing.T) {
	// Ensure PATH is set.
	original := os.Getenv("PATH")
	if original == "" {
		os.Setenv("PATH", "/usr/bin:/bin")
		defer os.Unsetenv("PATH")
	}

	env := pluginEnv()

	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("pluginEnv() should forward PATH to the plugin process")
	}
}

// TestPluginEnv_allowsTrivyPrefix verifies that TRIVY_* scanner config variables
// are forwarded to the plugin.
func TestPluginEnv_allowsTrivyPrefix(t *testing.T) {
	os.Setenv("TRIVY_CACHE_DIR", "/tmp/trivy-cache")
	defer os.Unsetenv("TRIVY_CACHE_DIR")

	env := pluginEnv()

	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRIVY_CACHE_DIR=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("pluginEnv() should forward TRIVY_* variables to the plugin")
	}
}

// TestScan_processFailsOnBadBinary verifies that Scan() returns an error when
// the plugin binary exits non-zero (simulated via a failing script on Unix).
// This test is skipped on Windows where shell scripts are not executable.
func TestScan_processFailsOnBadBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script plugin not supported on Windows")
	}

	// Script that exits non-zero.
	script := []byte("#!/bin/sh\nexit 1\n")
	path := writeTempFile(t, script)
	defer os.Remove(path)

	// Make it executable.
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	checksum := sha256OfBytes(script)
	p, err := New(path, checksum)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Scan should fail because the process exits non-zero.
	_, scanErr := p.Scan(context.Background(), pluginScanRequestWithNoLayers())
	if scanErr == nil {
		t.Fatal("expected error when plugin process exits non-zero")
	}
}

// TestScan_invalidJSONResponse verifies that Scan() returns an error when the
// plugin emits invalid JSON on stdout.
func TestScan_invalidJSONResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script plugin not supported on Windows")
	}

	// Script that writes non-JSON to stdout and exits 0.
	script := []byte("#!/bin/sh\nprintf 'NOT_JSON\\n'\n")
	path := writeTempFile(t, script)
	defer os.Remove(path)

	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	checksum := sha256OfBytes(script)
	p, err := New(path, checksum)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, scanErr := p.Scan(context.Background(), pluginScanRequestWithNoLayers())
	if scanErr == nil {
		t.Fatal("expected error for invalid JSON from plugin")
	}
}

// pluginScanRequestWithNoLayers returns a minimal ScanRequest with no layers
// (safe for tests where we only care whether the process exits correctly).
func pluginScanRequestWithNoLayers() libplugin.ScanRequest {
	return libplugin.ScanRequest{
		TenantID:       "test-tenant",
		RepositoryName: "org/repo",
		ManifestDigest: "sha256:abc",
		Layers:         nil,
		StorageFetcher: nil, // no layers to fetch
	}
}

// TestIsPrivateIP_notExported verifies indirectly via net parsing that private
// IPs used in tests are valid. This exercises the CIDR table via the exported
// delivery.ValidateURL in the webhook service; here we just make sure our
// local loopback is parseable.
func TestNetParseLoopback(t *testing.T) {
	ip := net.ParseIP("127.0.0.1")
	if ip == nil {
		t.Fatal("127.0.0.1 should parse to a valid IP")
	}
}
