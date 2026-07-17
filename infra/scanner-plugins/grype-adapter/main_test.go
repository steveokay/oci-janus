package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Scanner-engine decoupling (Phase 2 Task 2): the grype-adapter no longer
// execs a locally baked `grype` binary or manages its own vulnerability DB
// cache — that logic moved into the grype-engine sidecar, mirroring the
// trivy-adapter's Phase 1 change. The adapter's job now is: flatten the
// rootfs onto the shared work volume, then POST it to the sidecar over
// HTTP. These tests cover that HTTP handoff.

func TestScanViaEngine_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["rootfs"] == "" {
			t.Errorf("rootfs missing from POST")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"engine":"grype","version":"0.93.0","raw":{"matches":[{"vulnerability":{"id":"CVE-1","severity":"High"},"artifact":{"name":"p","version":"1"}}],"descriptor":{"name":"grype","version":"0.93.0"}}}`))
	}))
	defer srv.Close()
	report, version, err := scanViaEngine(srv.URL, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if version != "0.93.0" {
		t.Fatalf("want version 0.93.0, got %q", version)
	}
	fs := translateFindings(report)
	if len(fs) != 1 || fs[0].CVE != "CVE-1" || fs[0].Severity != "HIGH" {
		t.Fatalf("bad findings: %+v", fs)
	}
}

func TestScanViaEngine_Unreachable(t *testing.T) {
	_, _, err := scanViaEngine("http://127.0.0.1:1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), errEngineUnreachable) {
		t.Fatalf("want %q error, got %v", errEngineUnreachable, err)
	}
}
