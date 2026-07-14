package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestClientPathsExistInOpenAPISpec is the load-bearing guard for FUT-082.
//
// Every path the MCP client builds MUST correspond to a real route in the
// management BFF's committed OpenAPI spec (docs/openapi.json). The bug FUT-082
// fixed was exactly this drift: the client pointed at .../manifests/{tag},
// .../scans/{digest}, and .../signatures/{digest} routes that never existed,
// so three tools always 404'd. This test fails the build if any client path
// is not backed by a spec route — catching the drift at CI time instead of at
// an LLM tool call.
//
// The templates below use the OpenAPI placeholder names ({org}, {repo}, {tag},
// {digest}) that convertPath emits in openapi-gen, NOT the client's %s verbs.
// When you add or rename a client method, add its template here.
func TestClientPathsExistInOpenAPISpec(t *testing.T) {
	// Every OpenAPI path template the MCP client depends on. Kept as an
	// explicit contract list rather than reflected from the client so a
	// silent path change in registry.go trips this test.
	want := []string{
		"/api/v1/repositories",
		"/api/v1/repositories/{org}/{repo}/tags",
		"/api/v1/repositories/{org}/{repo}/tags/{tag}/manifest",
		"/api/v1/service-accounts",
		"/api/v1/access/review/stale",
		"/api/v1/audit",
		"/api/v1/scan-by-digest/{digest}",
		"/api/v1/signatures-by-digest/{digest}",
		"/api/v1/repositories/{org}/{repo}/promotions",
		"/api/v1/promotions",
	}

	specPaths := loadOpenAPIPaths(t)
	for _, p := range want {
		if _, ok := specPaths[p]; !ok {
			t.Errorf("MCP client path %q has no matching route in docs/openapi.json — "+
				"the BFF route was renamed/removed or the client drifted", p)
		}
	}
}

// loadOpenAPIPaths walks up from the test's working directory to the repo root
// (identified by docs/openapi.json) and returns the set of path templates in
// the spec's "paths" object.
func loadOpenAPIPaths(t *testing.T) map[string]struct{} {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var specFile string
	for {
		candidate := filepath.Join(dir, "docs", "openapi.json")
		if _, statErr := os.Stat(candidate); statErr == nil {
			specFile = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate docs/openapi.json walking up from test dir")
		}
		dir = parent
	}

	raw, err := os.ReadFile(specFile) //nolint:gosec // specFile is repo-internal, resolved by walking to the repo root
	if err != nil {
		t.Fatalf("read %s: %v", specFile, err)
	}
	var spec struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("unmarshal openapi.json: %v", err)
	}
	out := make(map[string]struct{}, len(spec.Paths))
	for p := range spec.Paths {
		out[p] = struct{}{}
	}
	return out
}
