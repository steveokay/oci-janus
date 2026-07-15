// compare_tags_diff_test.go — unit tests for the PURE diff helpers behind the
// image-diff feature (Tier 2 #3). These functions take already-fetched bytes /
// parsed structures and compute the added/removed/changed deltas; the handler
// (compare_tags.go) orchestrates the fetching around them. Testing the pure
// functions directly keeps the bulk of the correctness surface free of gRPC
// fakes.
package handler

import (
	"testing"
)

// --- Layer diff -------------------------------------------------------------

func TestDiffLayers_AddedRemovedCommonAndSizeDelta(t *testing.T) {
	from := []manifestLayer{
		{Digest: "sha256:a", Size: 100, MediaType: "layer"},
		{Digest: "sha256:b", Size: 200, MediaType: "layer"},
	}
	to := []manifestLayer{
		{Digest: "sha256:b", Size: 200, MediaType: "layer"}, // common
		{Digest: "sha256:c", Size: 350, MediaType: "layer"}, // added
	}
	// from total 300, to total 550 → delta +250.
	d := diffLayers(from, to, 300, 550)

	if len(d.Added) != 1 || d.Added[0].Digest != "sha256:c" {
		t.Errorf("added: want [sha256:c], got %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Digest != "sha256:a" {
		t.Errorf("removed: want [sha256:a], got %+v", d.Removed)
	}
	if d.CommonCount != 1 {
		t.Errorf("common: want 1, got %d", d.CommonCount)
	}
	if d.SizeDeltaBytes != 250 {
		t.Errorf("size delta: want +250, got %d", d.SizeDeltaBytes)
	}
}

func TestDiffLayers_Identical(t *testing.T) {
	layers := []manifestLayer{{Digest: "sha256:a", Size: 100}}
	d := diffLayers(layers, layers, 100, 100)
	if len(d.Added) != 0 || len(d.Removed) != 0 {
		t.Errorf("identical layers should have no added/removed, got +%d -%d", len(d.Added), len(d.Removed))
	}
	if d.CommonCount != 1 || d.SizeDeltaBytes != 0 {
		t.Errorf("identical: want common=1 delta=0, got common=%d delta=%d", d.CommonCount, d.SizeDeltaBytes)
	}
}

// --- Config diff ------------------------------------------------------------

func TestDiffConfig_EnvCmdEntrypointPortsChanged(t *testing.T) {
	from := []byte(`{"config":{
		"Env":["PATH=/usr/bin","NODE_ENV=prod"],
		"Cmd":["nginx"],
		"Entrypoint":["/entrypoint.sh"],
		"ExposedPorts":{"80/tcp":{}},
		"WorkingDir":"/app",
		"User":"root"
	}}`)
	to := []byte(`{"config":{
		"Env":["PATH=/usr/bin","NODE_ENV=stage","EXTRA=1"],
		"Cmd":["nginx","-g","daemon off;"],
		"Entrypoint":["/entrypoint.sh"],
		"ExposedPorts":{"80/tcp":{},"8443/tcp":{}},
		"WorkingDir":"/srv",
		"User":"root"
	}}`)
	d, err := diffConfig(from, to)
	if err != nil {
		t.Fatalf("diffConfig error: %v", err)
	}
	if !d.Available {
		t.Fatal("config diff should be available")
	}
	// Env: EXTRA added, NODE_ENV changed prod→stage, PATH unchanged, none removed.
	if len(d.Env.Added) != 1 || d.Env.Added[0] != "EXTRA=1" {
		t.Errorf("env added: want [EXTRA=1], got %+v", d.Env.Added)
	}
	if len(d.Env.Removed) != 0 {
		t.Errorf("env removed: want none, got %+v", d.Env.Removed)
	}
	if len(d.Env.Changed) != 1 || d.Env.Changed[0].Key != "NODE_ENV" ||
		d.Env.Changed[0].From != "prod" || d.Env.Changed[0].To != "stage" {
		t.Errorf("env changed: want NODE_ENV prod→stage, got %+v", d.Env.Changed)
	}
	if !d.CmdChanged {
		t.Error("cmd should be flagged changed")
	}
	if d.EntrypointChanged {
		t.Error("entrypoint unchanged should not be flagged")
	}
	if len(d.ExposedPortsAdded) != 1 || d.ExposedPortsAdded[0] != "8443/tcp" {
		t.Errorf("ports added: want [8443/tcp], got %+v", d.ExposedPortsAdded)
	}
	if d.WorkingDirFrom != "/app" || d.WorkingDirTo != "/srv" {
		t.Errorf("workingdir: want /app→/srv, got %q→%q", d.WorkingDirFrom, d.WorkingDirTo)
	}
}

func TestDiffConfig_Unparseable(t *testing.T) {
	_, err := diffConfig([]byte(`not json`), []byte(`{"config":{}}`))
	if err == nil {
		t.Error("expected error on unparseable config blob")
	}
}

// --- Package (SBOM) diff ----------------------------------------------------

func TestDiffPackages_AddedRemovedChanged(t *testing.T) {
	from := []byte(`{"packages":[
		{"name":"openssl","versionInfo":"3.0.1"},
		{"name":"wget","versionInfo":"1.21"}
	]}`)
	to := []byte(`{"packages":[
		{"name":"openssl","versionInfo":"3.0.2"},
		{"name":"curl","versionInfo":"8.5"}
	]}`)
	d, err := diffPackages(from, to)
	if err != nil {
		t.Fatalf("diffPackages error: %v", err)
	}
	if !d.Available {
		t.Fatal("package diff should be available when both SBOMs parse")
	}
	if len(d.Added) != 1 || d.Added[0].Name != "curl" || d.Added[0].Version != "8.5" {
		t.Errorf("added: want curl@8.5, got %+v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Name != "wget" {
		t.Errorf("removed: want wget, got %+v", d.Removed)
	}
	if len(d.Changed) != 1 || d.Changed[0].Name != "openssl" ||
		d.Changed[0].FromVersion != "3.0.1" || d.Changed[0].ToVersion != "3.0.2" {
		t.Errorf("changed: want openssl 3.0.1→3.0.2, got %+v", d.Changed)
	}
}

// --- Vulnerability diff -----------------------------------------------------

func TestDiffVulns_AddedRemoved(t *testing.T) {
	// findings_json uses capitalized keys (plugin.Finding has no json tags).
	from := []byte(`[
		{"CVE":"CVE-2024-A","Severity":"HIGH","Package":"openssl","Version":"3.0.1","FixedIn":"3.0.2"},
		{"CVE":"CVE-2024-B","Severity":"LOW","Package":"zlib","Version":"1.2.11"}
	]`)
	to := []byte(`[
		{"CVE":"CVE-2024-B","Severity":"LOW","Package":"zlib","Version":"1.2.11"},
		{"CVE":"CVE-2024-C","Severity":"MEDIUM","Package":"curl","Version":"8.5"}
	]`)
	d := diffVulns(from, to)
	if !d.Available {
		t.Fatal("vuln diff should be available when both scans parse")
	}
	// CVE-A fixed (removed), CVE-C introduced (added), CVE-B unchanged.
	if len(d.Removed) != 1 || d.Removed[0].CVE != "CVE-2024-A" {
		t.Errorf("removed: want CVE-2024-A, got %+v", d.Removed)
	}
	if len(d.Added) != 1 || d.Added[0].CVE != "CVE-2024-C" || d.Added[0].Severity != "MEDIUM" {
		t.Errorf("added: want CVE-2024-C (MEDIUM), got %+v", d.Added)
	}
}

func TestDiffVulns_EmptyFindings(t *testing.T) {
	d := diffVulns([]byte(`[]`), []byte(`[]`))
	if !d.Available || len(d.Added) != 0 || len(d.Removed) != 0 {
		t.Errorf("empty findings: want available, no deltas, got %+v", d)
	}
}
