// Package handler — bufconn tests for the FE-API-033 per-tag SBOM download
// route. Mirrors the in-process httptest harness used by handler_test.go so
// the route is exercised end-to-end through RequireAuth and the metadata
// gRPC fake without standing up a real Postgres or RabbitMQ.
package handler_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// resetSBOMHooks clears the package-level overrides between tests so a
// failing or skipped test never bleeds state into the next one. Called via
// t.Cleanup in every SBOM test below.
func resetSBOMHooks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		scanSBOMOverride = nil
		scanSBOMErr = nil
		getTagErr = nil
	})
}

// TestGetTagSBOM_happyPath_returnsBytesAndHeaders verifies the route streams
// the SBOM payload with the right Content-Type, the right
// Content-Disposition filename (digest-derived), and the exact bytes the
// metadata fake returned.
func TestGetTagSBOM_happyPath_returnsBytesAndHeaders(t *testing.T) {
	resetSBOMHooks(t)
	env := newTestEnv(t)

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/spdx+json" {
		t.Errorf("Content-Type: got %q, want application/spdx+json", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	// Filename should be the digest (sans "sha256:" prefix) + .spdx.json.
	if !strings.Contains(cd, `filename="abc123.spdx.json"`) {
		t.Errorf("Content-Disposition: got %q, want attachment with digest filename", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	want := `{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT"}`
	if string(body) != want {
		t.Errorf("body: got %q, want %q", string(body), want)
	}
}

// TestGetTagSBOM_defaultsToSpdxJson_whenFormatOmitted ensures the handler
// applies the spdx-json default when ?format= is absent. The fake returns
// the same payload either way, so the test asserts on the headers.
func TestGetTagSBOM_defaultsToSpdxJson_whenFormatOmitted(t *testing.T) {
	resetSBOMHooks(t)
	env := newTestEnv(t)

	// No ?format= — implicit default.
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/spdx+json" {
		t.Errorf("Content-Type: got %q, want application/spdx+json", ct)
	}
}

// TestGetTagSBOM_noSBOMRecorded_returns404WithCode covers the case where the
// metadata service has no SBOM for the tag's manifest. The body must carry
// the stable `code: "no-sbom"` identifier so the dashboard can branch on it.
func TestGetTagSBOM_noSBOMRecorded_returns404WithCode(t *testing.T) {
	resetSBOMHooks(t)
	scanSBOMErr = status.Error(codes.NotFound, "no sbom")

	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	var body struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code != "no-sbom" {
		t.Errorf("body.code: got %q, want %q", body.Code, "no-sbom")
	}
	if body.Error == "" {
		t.Errorf("body.error should be non-empty")
	}
}

// TestGetTagSBOM_unsupportedFormat_returns400 exercises the CycloneDX
// deferral — the format is recognised but explicitly not implemented yet,
// and the handler must surface a clear 400 rather than falling through to
// SPDX silently.
func TestGetTagSBOM_unsupportedFormat_returns400(t *testing.T) {
	resetSBOMHooks(t)
	env := newTestEnv(t)

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom?format=cyclonedx-json", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// TestGetTagSBOM_unknownFormat_returns400 ensures a typo or future format
// value is rejected outright rather than silently downgraded to spdx-json.
func TestGetTagSBOM_unknownFormat_returns400(t *testing.T) {
	resetSBOMHooks(t)
	env := newTestEnv(t)

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom?format=swid-xml", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// TestGetTagSBOM_unknownTag_returns404 ensures a missing tag yields 404
// (not 500) — the route returns the same status as FE-API-002 manifest
// lookup for consistency.
func TestGetTagSBOM_unknownTag_returns404(t *testing.T) {
	resetSBOMHooks(t)
	// Force the metadata fake's GetTag to return a transport error so the
	// handler hits the "tag not found" branch.
	getTagErr = status.Error(codes.NotFound, "tag not found")

	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestGetTagSBOM_noAuth_returns401 verifies the route is behind RequireAuth.
// Missing Authorization header must surface as 401 from the middleware
// before any metadata call is attempted.
func TestGetTagSBOM_noAuth_returns401(t *testing.T) {
	resetSBOMHooks(t)
	env := newTestEnv(t)

	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sbom", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestGetTagSBOM_invalidOrgName_returns400 sanity-checks the input
// validation guard. Any failure of validateOrgName must short-circuit
// before we touch metadata.
func TestGetTagSBOM_invalidOrgName_returns400(t *testing.T) {
	resetSBOMHooks(t)
	env := newTestEnv(t)

	// "BAD..ORG" violates the org allowlist (uppercase + consecutive dots).
	resp := env.get(t, "/api/v1/repositories/BAD..ORG/myrepo/tags/v1.0/sbom", adminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}
