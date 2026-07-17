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

func itoa(n int) string { return strings.TrimSpace(string(rune('0' + n))) } // single-digit exit codes only

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
