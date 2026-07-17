package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Scanner-engine decoupling (Task 4): the trivy-adapter no longer execs a
// locally baked `trivy` binary or manages its own vulnerability DB cache —
// that logic (and the REM-019 Phase 2 offline-scan optimisation) moved into
// the trivy-engine sidecar. The adapter's job now is: flatten the rootfs
// onto the shared work volume, then POST it to the sidecar over HTTP. These
// tests cover that HTTP handoff.

func TestScanViaEngine_HappyPath(t *testing.T) {
	// Fake engine sidecar returns a trivy report wrapped in the engine envelope.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["rootfs"] == "" {
			t.Errorf("rootfs missing from POST")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"engine":"trivy","version":"0.71.2","raw":{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"p","InstalledVersion":"1","Severity":"HIGH"}]}]}}`))
	}))
	defer srv.Close()

	report, version, err := scanViaEngine(srv.URL, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if version != "0.71.2" {
		t.Fatalf("want version 0.71.2, got %q", version)
	}
	fs := translateFindings(report)
	if len(fs) != 1 || fs[0].CVE != "CVE-1" {
		t.Fatalf("bad findings: %+v", fs)
	}
}

func TestScanViaEngine_Unreachable(t *testing.T) {
	// Point at a closed port — connection refused.
	_, _, err := scanViaEngine("http://127.0.0.1:1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), errEngineUnreachable) {
		t.Fatalf("want %q error, got %v", errEngineUnreachable, err)
	}
}
