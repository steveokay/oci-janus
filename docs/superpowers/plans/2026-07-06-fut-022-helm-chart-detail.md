# FUT-022 Helm Chart Detail Page — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class **Chart** tab to the tag-detail page that renders a Helm chart's `Chart.yaml` metadata and its `values.yaml` inline, without the operator pulling the chart.

**Architecture:** A generic size-capped `CoreService.GetBlob` gRPC is added to `registry-core`. The `registry-management` BFF resolves the tag → manifest via `registry-metadata`, parses the manifest to find the Helm config + content-layer digests, fetches both blobs through `GetBlob`, parses `Chart.yaml` (JSON) and gunzip/untars `values.yaml`, then serves one combined JSON endpoint. The React tag-detail page renders a Chart tab gated on the tag's `artifact_type === "helm"`.

**Tech Stack:** Go 1.25 (pgx, grpc, buf), `archive/tar` + `compress/gzip` (stdlib), React + TanStack Query/Router + Vitest.

**Spec:** `docs/superpowers/specs/2026-07-06-fut-022-helm-chart-detail-design.md`

**Branch:** `feat/fut-022-helm-chart-detail` (already checked out; spec committed).

---

## Conventions for every task

- **Per-module Go commands** (match Docker/CI — the workspace is not used for a single module):
  - core: `cd services/core && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
  - management: `cd services/management && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
- **Proto regen:** `cd proto && buf generate` (scoped to `proto/` — never from repo root, which would rescan stale `.claude/worktrees/*` copies).
- **Frontend gates (all four before any FE push):** `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`.
- **Comments:** every new function/type gets a godoc/JSDoc comment (project rule). Match the density of the neighbouring `referrers.go` / `referrers-panel.tsx`.
- **Commit trailer:** end each commit body with
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

## Helm constants + caps (referenced across tasks — defined once in Task 3)

```go
// Helm-on-OCI media types.
const (
	helmConfigMediaType  = "application/vnd.cncf.helm.config.v1+json"
	helmContentMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// Size caps (see spec §5).
const (
	configBlobCap  = 1 << 20   // 1 MiB — Chart.yaml config blob
	contentBlobCap = 10 << 20  // 10 MiB — chart .tgz content layer
	valuesCap      = 256 << 10 // 256 KiB — extracted values.yaml
	maxTarEntries  = 2000      // tar entries scanned before giving up
)

// gRPC message ceiling shared by the core server (send) + BFF client (recv).
const grpcBlobMsgCap = 16 << 20 // 16 MiB
```

---

## Task 1: proto — add `CoreService.GetBlob`

**Files:**
- Modify: `proto/core/v1/core.proto`
- Regenerate: `proto/gen/go/core/v1/core.pb.go`, `proto/gen/go/core/v1/core_grpc.pb.go`

- [ ] **Step 1: Add the RPC + messages to `core.proto`**

In `service CoreService { ... }`, add the RPC after `ListReferrers`:

```proto
  // GetBlob returns the raw bytes of a blob by digest, capped at max_bytes.
  // Generic (not Helm-specific): the management BFF uses it to fetch a Helm
  // chart's config + content-layer blobs, but any internal reader can use it.
  // The server refuses (FailedPrecondition) once a blob exceeds max_bytes
  // rather than truncating silently.
  rpc GetBlob(GetBlobRequest) returns (GetBlobResponse);
```

At the end of the file add:

```proto
// GetBlobRequest identifies a blob by tenant + digest. max_bytes is a hard
// ceiling on the returned payload so a caller can't pull an arbitrarily large
// blob into a unary gRPC message. 0 means "use the server default cap".
message GetBlobRequest {
  string tenant_id = 1;
  string digest    = 2; // "sha256:<hex64>"
  int64  max_bytes = 3;
}

// GetBlobResponse carries the blob bytes. size == len(data). media_type is
// reserved for future use (storage does not persist a per-blob media type
// today) and is empty.
message GetBlobResponse {
  bytes  data       = 1;
  int64  size       = 2;
  string media_type = 3;
}
```

- [ ] **Step 2: Regenerate the committed stubs**

Run: `cd proto && buf generate`
Expected: no errors; `git status` shows `proto/gen/go/core/v1/core.pb.go` + `core_grpc.pb.go` modified with a new `GetBlob` client/server method and the two message types.

- [ ] **Step 3: Verify the stubs compile in both modules**

Run: `cd services/core && GOWORK=off go build ./...` then `cd services/management && GOWORK=off go build ./...`
Expected: PASS (the new proto symbols resolve; nothing calls them yet).

- [ ] **Step 4: Commit**

```bash
git add proto/core/v1/core.proto proto/gen/go/core/v1/
git commit -m "feat(proto): add generic CoreService.GetBlob RPC (FUT-022)"
```

---

## Task 2: core — `GetBlob` gRPC handler + size cap

**Files:**
- Modify: `services/core/internal/handler/grpc_core.go`
- Modify: `services/core/internal/handler/grpc_core_test.go`
- Modify: `services/core/internal/server/server.go` (raise `MaxSendMsgSize`)

Context: `(*service.Registry).GetBlob(ctx, tenantID, digest string, w io.Writer) (int64, error)` streams a blob into `w` and returns `service.ErrNotFound` when absent (`services/core/internal/service/registry.go:164`). `digestRE` (`^sha256:[a-f0-9]{64}$`) is package-level in `services/core/internal/handler/http.go:38`.

- [ ] **Step 1: Write the failing handler tests**

Add to `services/core/internal/handler/grpc_core_test.go`. First extend the existing fake to implement blob fetch, then the tests:

```go
// fakeReferrerLister already exists in this file. Add a blob field + method so
// the same fake backs the coreReader seam GetBlob depends on.
func (f *fakeReferrerLister) GetBlob(_ context.Context, _, digest string, w io.Writer) (int64, error) {
	if f.blobErr != nil {
		return 0, f.blobErr
	}
	n, err := w.Write(f.blob)
	return int64(n), err
}

func TestGetBlob_returnsBytes(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{blob: []byte("hello-chart")})
	resp, err := h.GetBlob(context.Background(), &corev1.GetBlobRequest{
		TenantId: "t1",
		Digest:   "sha256:" + strings.Repeat("a", 64),
		MaxBytes: 1024,
	})
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	if string(resp.GetData()) != "hello-chart" || resp.GetSize() != 11 {
		t.Fatalf("got %q size=%d", resp.GetData(), resp.GetSize())
	}
}

func TestGetBlob_missingTenant_invalidArgument(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{})
	_, err := h.GetBlob(context.Background(), &corev1.GetBlobRequest{
		Digest: "sha256:" + strings.Repeat("a", 64),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestGetBlob_badDigest_invalidArgument(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{})
	_, err := h.GetBlob(context.Background(), &corev1.GetBlobRequest{
		TenantId: "t1", Digest: "not-a-digest",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestGetBlob_notFound(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{blobErr: service.ErrNotFound})
	_, err := h.GetBlob(context.Background(), &corev1.GetBlobRequest{
		TenantId: "t1", Digest: "sha256:" + strings.Repeat("a", 64),
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

func TestGetBlob_exceedsCap_failedPrecondition(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{blob: bytes.Repeat([]byte("x"), 2048)})
	_, err := h.GetBlob(context.Background(), &corev1.GetBlobRequest{
		TenantId: "t1", Digest: "sha256:" + strings.Repeat("a", 64), MaxBytes: 1024,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %v", err)
	}
}
```

Add `blob []byte` and `blobErr error` fields to the existing `fakeReferrerLister` struct, and add `"bytes"`, `"io"`, `"strings"` to the test imports if missing.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd services/core && GOWORK=off go test ./internal/handler/ -run TestGetBlob -v`
Expected: FAIL — `GetBlob` undefined on `*CoreHandler`, `coreReader` seam has no `GetBlob`.

- [ ] **Step 3: Implement the handler + cap writer**

In `services/core/internal/handler/grpc_core.go`:

Rename the `referrerLister` interface to `coreReader` (or add a second interface — either is fine; the plan assumes rename) and add the blob method:

```go
// coreReader is the subset of *service.Registry the CoreHandler depends on.
// Declaring it as an interface keeps the handler unit-testable with a fake.
type coreReader interface {
	GetReferrers(ctx context.Context, tenantID, repoName, subjectDigest, artifactType string) ([]service.ReferrerDescriptor, bool, error)
	// GetBlob streams the blob with the given digest into w and returns the
	// number of bytes written. Mirrors (*service.Registry).GetBlob exactly.
	GetBlob(ctx context.Context, tenantID, digest string, w io.Writer) (int64, error)
}
```

Update the struct field type (`registry coreReader`) and `NewCoreHandler(registry coreReader)`.

Add the cap-enforcing writer + defaults + handler:

```go
// defaultBlobCap bounds a GetBlob response when the caller passes max_bytes<=0.
// hardBlobCap is the absolute ceiling regardless of the requested max_bytes so
// a client can't ask the server to buffer an unbounded blob into one message.
const (
	defaultBlobCap = 10 << 20 // 10 MiB
	hardBlobCap    = 16 << 20 // 16 MiB — matches the server MaxSendMsgSize
)

// errBlobTooLarge is the sentinel returned by cappedBuffer once a write would
// exceed the cap; GetBlob maps it to codes.FailedPrecondition.
var errBlobTooLarge = errors.New("blob exceeds max_bytes")

// cappedBuffer accumulates bytes up to limit, then fails the next write with
// errBlobTooLarge so Registry.GetBlob stops streaming instead of buffering the
// whole (potentially huge) blob into memory. (Field is `limit`, not `cap`, to
// avoid shadowing the builtin.)
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int64
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if int64(c.buf.Len()+len(p)) > c.limit {
		return 0, errBlobTooLarge
	}
	return c.buf.Write(p)
}

// GetBlob returns the raw bytes of a blob by digest, capped at max_bytes.
func (h *CoreHandler) GetBlob(ctx context.Context, req *corev1.GetBlobRequest) (*corev1.GetBlobResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if !digestRE.MatchString(req.GetDigest()) {
		return nil, status.Error(codes.InvalidArgument, "digest must match sha256:<hex64>")
	}

	maxBytes := req.GetMaxBytes()
	if maxBytes <= 0 {
		maxBytes = defaultBlobCap
	}
	if maxBytes > hardBlobCap {
		maxBytes = hardBlobCap
	}

	cb := &cappedBuffer{limit: maxBytes}
	if _, err := h.registry.GetBlob(ctx, req.GetTenantId(), req.GetDigest(), cb); err != nil {
		if errors.Is(err, errBlobTooLarge) {
			return nil, status.Error(codes.FailedPrecondition, "blob exceeds max_bytes")
		}
		if errors.Is(err, service.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "blob not found")
		}
		slog.ErrorContext(ctx, "GetBlob: read failed",
			"tenant_id", req.GetTenantId(), "digest", req.GetDigest(), "error", err)
		return nil, status.Error(codes.Internal, "failed to read blob")
	}

	data := cb.buf.Bytes()
	return &corev1.GetBlobResponse{Data: data, Size: int64(len(data))}, nil
}
```

Add `"bytes"`, `"errors"`, `"io"` to the imports.

- [ ] **Step 4: Raise the core gRPC server send cap**

In `services/core/internal/server/server.go` `buildGRPCOptions`, add to the `opts` slice (alongside the interceptors):

```go
		grpc.MaxSendMsgSize(16 << 20), // FUT-022: GetBlob may return up to hardBlobCap
```

- [ ] **Step 5: Run tests + build**

Run: `cd services/core && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
Expected: PASS (all `TestGetBlob*` green, existing referrer tests still green).

- [ ] **Step 6: Commit**

```bash
git add services/core/internal/handler/grpc_core.go services/core/internal/handler/grpc_core_test.go services/core/internal/server/server.go
git commit -m "feat(core): GetBlob gRPC handler with size cap (FUT-022)"
```

---

## Task 3: BFF — pure Helm parse/extract functions

**Files:**
- Create: `services/management/internal/handler/chartparse.go`
- Create: `services/management/internal/handler/chartparse_test.go`

These are pure functions (no gRPC, no HTTP) so they test without any fake. The Helm config blob is Chart.yaml serialised as JSON with camelCase keys (`appVersion`, `apiVersion`, `kubeVersion`).

- [ ] **Step 1: Write the failing tests**

Create `services/management/internal/handler/chartparse_test.go`:

```go
package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// makeChartTGZ builds a gzip+tar chart archive with the given files
// (path -> content) for the extractValuesYAML tests.
func makeChartTGZ(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return gzBuf.Bytes()
}

func TestParseManifestConfigAndLayer_helm(t *testing.T) {
	raw := []byte(`{
		"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":100},
		"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip","digest":"sha256:` + strings.Repeat("b", 64) + `","size":2048}]
	}`)
	cfgDigest, cfgMT, contentDigest, err := parseManifestConfigAndLayer(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfgMT != helmConfigMediaType {
		t.Fatalf("cfgMT=%q", cfgMT)
	}
	if cfgDigest != "sha256:"+strings.Repeat("a", 64) || contentDigest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("digests: %q %q", cfgDigest, contentDigest)
	}
}

func TestParseManifestConfigAndLayer_notHelm(t *testing.T) {
	raw := []byte(`{"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `"},"layers":[]}`)
	_, cfgMT, _, err := parseManifestConfigAndLayer(raw)
	if err != nil {
		t.Fatalf("parse should not error on non-helm: %v", err)
	}
	if cfgMT == helmConfigMediaType {
		t.Fatal("expected non-helm mediaType")
	}
}

func TestParseChartMetadata_full(t *testing.T) {
	cfg := []byte(`{"name":"myapp","version":"1.2.3","appVersion":"2.0.0","description":"d","apiVersion":"v2","type":"application","kubeVersion":">=1.24.0","home":"https://h","keywords":["web"],"sources":["https://s"],"maintainers":[{"name":"Ada","email":"a@x.io"}],"dependencies":[{"name":"pg","version":"12.x","repository":"oci://r"}],"annotations":{"category":"DB"}}`)
	m, err := parseChartMetadata(cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "myapp" || m.Version != "1.2.3" || m.AppVersion != "2.0.0" {
		t.Fatalf("meta: %+v", m)
	}
	if len(m.Maintainers) != 1 || m.Maintainers[0].Name != "Ada" {
		t.Fatalf("maintainers: %+v", m.Maintainers)
	}
	if len(m.Dependencies) != 1 || m.Dependencies[0].Repository != "oci://r" {
		t.Fatalf("deps: %+v", m.Dependencies)
	}
}

func TestParseChartMetadata_garbage(t *testing.T) {
	if _, err := parseChartMetadata([]byte("not json")); err == nil {
		t.Fatal("expected error on garbage config")
	}
}

func TestExtractValuesYAML_root(t *testing.T) {
	tgz := makeChartTGZ(t, map[string]string{
		"myapp/Chart.yaml":  "name: myapp",
		"myapp/values.yaml": "replicaCount: 1\n",
		"myapp/charts/sub/values.yaml": "subchart: true\n",
	})
	got, truncated, err := extractValuesYAML(tgz, valuesCap)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if truncated || got != "replicaCount: 1\n" {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}

func TestExtractValuesYAML_subchartOnly_notFound(t *testing.T) {
	tgz := makeChartTGZ(t, map[string]string{
		"myapp/charts/sub/values.yaml": "subchart: true\n",
	})
	_, _, err := extractValuesYAML(tgz, valuesCap)
	if err == nil {
		t.Fatal("expected not-found when only a subchart values.yaml exists")
	}
}

func TestExtractValuesYAML_truncated(t *testing.T) {
	big := strings.Repeat("a: 1\n", 100000) // > valuesCap
	tgz := makeChartTGZ(t, map[string]string{"myapp/values.yaml": big})
	got, truncated, err := extractValuesYAML(tgz, valuesCap)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !truncated || len(got) != valuesCap {
		t.Fatalf("truncated=%v len=%d", truncated, len(got))
	}
}

func TestExtractValuesYAML_badGzip(t *testing.T) {
	if _, _, err := extractValuesYAML([]byte("not gzip"), valuesCap); err == nil {
		t.Fatal("expected error on non-gzip input")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run 'Chart|Values|Manifest' -v`
Expected: FAIL — undefined `parseManifestConfigAndLayer`, `parseChartMetadata`, `extractValuesYAML`, and the constants.

- [ ] **Step 3: Implement `chartparse.go`**

Create `services/management/internal/handler/chartparse.go`:

```go
// Package handler — chartparse.go
//
// Pure parsing helpers for the Helm chart detail endpoint (FUT-022): they turn
// raw manifest / config / content bytes into structured chart data with no I/O,
// so they unit-test without any gRPC or HTTP fake. handleGetChart (chart.go)
// fetches the bytes and calls these.
package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
)

// Helm-on-OCI media types.
const (
	helmConfigMediaType  = "application/vnd.cncf.helm.config.v1+json"
	helmContentMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// Size caps (spec §5).
const (
	configBlobCap  = 1 << 20   // 1 MiB — Chart.yaml config blob
	contentBlobCap = 10 << 20  // 10 MiB — chart .tgz content layer
	valuesCap      = 256 << 10 // 256 KiB — extracted values.yaml
	maxTarEntries  = 2000      // tar entries scanned before giving up
)

// ChartMetadata is the Chart.yaml view returned to the frontend. JSON tags are
// snake_case to match the BFF wire contract; omitempty on the optional fields.
type ChartMetadata struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	AppVersion   string            `json:"app_version,omitempty"`
	Description  string            `json:"description,omitempty"`
	APIVersion   string            `json:"api_version,omitempty"`
	Type         string            `json:"type,omitempty"`
	KubeVersion  string            `json:"kube_version,omitempty"`
	Home         string            `json:"home,omitempty"`
	Icon         string            `json:"icon,omitempty"`
	Deprecated   bool              `json:"deprecated,omitempty"`
	Keywords     []string          `json:"keywords,omitempty"`
	Sources      []string          `json:"sources,omitempty"`
	Maintainers  []ChartMaintainer `json:"maintainers,omitempty"`
	Dependencies []ChartDependency `json:"dependencies,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// ChartMaintainer is one Chart.yaml maintainer entry.
type ChartMaintainer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

// ChartDependency is one Chart.yaml dependency entry.
type ChartDependency struct {
	Name       string `json:"name,omitempty"`
	Version    string `json:"version,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// ociDescriptor is the subset of an OCI content descriptor we read.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ociManifest is the subset of an OCI image manifest we read.
type ociManifest struct {
	Config ociDescriptor   `json:"config"`
	Layers []ociDescriptor `json:"layers"`
}

// parseManifestConfigAndLayer reads the manifest JSON and returns the config
// digest + config mediaType + the Helm content-layer digest. It does NOT error
// on a non-Helm manifest — the caller inspects the returned mediaType and
// decides. contentDigest is empty when no Helm content layer is present.
func parseManifestConfigAndLayer(raw []byte) (configDigest, configMediaType, contentDigest string, err error) {
	var m ociManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return "", "", "", err
	}
	for _, l := range m.Layers {
		if l.MediaType == helmContentMediaType {
			contentDigest = l.Digest
			break
		}
	}
	return m.Config.Digest, m.Config.MediaType, contentDigest, nil
}

// helmConfig mirrors the camelCase JSON of a Helm config blob (Chart.yaml).
type helmConfig struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	AppVersion  string            `json:"appVersion"`
	Description string            `json:"description"`
	APIVersion  string            `json:"apiVersion"`
	Type        string            `json:"type"`
	KubeVersion string            `json:"kubeVersion"`
	Home        string            `json:"home"`
	Icon        string            `json:"icon"`
	Deprecated  bool              `json:"deprecated"`
	Keywords    []string          `json:"keywords"`
	Sources     []string          `json:"sources"`
	Maintainers []struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		URL   string `json:"url"`
	} `json:"maintainers"`
	Dependencies []struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Repository string `json:"repository"`
	} `json:"dependencies"`
	Annotations map[string]string `json:"annotations"`
}

// parseChartMetadata unmarshals a Helm config blob into the snake_case
// ChartMetadata returned to the FE.
func parseChartMetadata(configJSON []byte) (ChartMetadata, error) {
	var c helmConfig
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return ChartMetadata{}, err
	}
	m := ChartMetadata{
		Name: c.Name, Version: c.Version, AppVersion: c.AppVersion,
		Description: c.Description, APIVersion: c.APIVersion, Type: c.Type,
		KubeVersion: c.KubeVersion, Home: c.Home, Icon: c.Icon,
		Deprecated: c.Deprecated, Keywords: c.Keywords, Sources: c.Sources,
		Annotations: c.Annotations,
	}
	for _, mt := range c.Maintainers {
		m.Maintainers = append(m.Maintainers, ChartMaintainer{Name: mt.Name, Email: mt.Email, URL: mt.URL})
	}
	for _, d := range c.Dependencies {
		m.Dependencies = append(m.Dependencies, ChartDependency{Name: d.Name, Version: d.Version, Repository: d.Repository})
	}
	return m, nil
}

// errValuesNotFound is returned when no chart-root values.yaml is in the archive.
var errValuesNotFound = errors.New("values.yaml not found in chart archive")

// extractValuesYAML gunzips + untars a chart .tgz and returns the chart-root
// values.yaml (a path shaped "<single-segment>/values.yaml"). It ignores
// subchart values, directory-traversal paths, and non-regular entries; caps
// the returned string at `cap` bytes (truncated=true when it hit the cap); and
// wraps the tar reader in an io.LimitReader so a lying header can't blow memory.
// (`limit`, not `cap`, to avoid shadowing the builtin.)
func extractValuesYAML(tgz []byte, limit int) (values string, truncated bool, err error) {
	gr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return "", false, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for i := 0; i < maxTarEntries; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := path.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, "/") {
			continue // reject traversal / absolute paths
		}
		// Chart-root values.yaml only: exactly "<name>/values.yaml".
		parts := strings.Split(clean, "/")
		if len(parts) != 2 || parts[1] != "values.yaml" {
			continue
		}
		lr := io.LimitReader(tr, int64(limit)+1)
		buf, err := io.ReadAll(lr)
		if err != nil {
			return "", false, err
		}
		if len(buf) > limit {
			return string(buf[:limit]), true, nil
		}
		return string(buf), false, nil
	}
	return "", false, errValuesNotFound
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run 'Chart|Values|Manifest' -v`
Expected: PASS (all 8 parse/extract tests green).

- [ ] **Step 5: Commit**

```bash
git add services/management/internal/handler/chartparse.go services/management/internal/handler/chartparse_test.go
git commit -m "feat(management): Helm chart parse + values extraction helpers (FUT-022)"
```

---

## Task 4: BFF — `handleGetChart` endpoint + route + client cap

**Files:**
- Create: `services/management/internal/handler/chart.go`
- Create: `services/management/internal/handler/chart_test.go`
- Modify: `services/management/internal/handler/handler.go` (register route)
- Modify: `services/management/internal/server/server.go` (raise core-client recv cap)

Context (verified): `Handler` has `core corev1.CoreServiceClient` (nil when unset) and `meta metadatav1.MetadataServiceClient`. `findRepo(r, tenantID, org, repoName) (*metadatav1.Repository, error)`. `meta.GetManifest(ctx, &metadatav1.GetManifestRequest{RepoId, TenantId, Reference})` resolves a **tag** reference and returns `*metadatav1.Manifest` (with `GetRawJson()`). `writeJSON` / `writeError` and `validateOrgName/validateRepoName/validateTagName` are package-level. The referrers route is registered at `handler.go:362`.

- [ ] **Step 1: Write the failing handler tests**

Create `services/management/internal/handler/chart_test.go`. **Reuse the existing harness** in `services/management/internal/handler/referrers_test.go`: it already has `fakeCoreServer` (an in-process `CoreService` registered on a real gRPC listener via `corev1.RegisterCoreServiceServer` + `h.WithCoreClient`), the metadata fake, and the `env` setup. Extend `fakeCoreServer` with a `GetBlob(ctx, *corev1.GetBlobRequest) (*corev1.GetBlobResponse, error)` method that returns canned bytes keyed by `req.GetDigest()` (a `map[string][]byte`), and configure the metadata fake's `GetManifest` to return a manifest whose `raw_json` is a Helm (or non-Helm) manifest per test. Build the `.tgz` fixture with `makeChartTGZ` from `chartparse_test.go`. Follow `referrers_test.go`'s request construction (path values + tenant context) exactly — do not invent a parallel harness. The tests to add:

```go
func TestHandleGetChart_nilCore_404(t *testing.T) {
	h := &Handler{} // core == nil
	rec := doChartRequest(t, h, "acme", "web", "1.0.0")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestHandleGetChart_notHelm_400(t *testing.T) {
	// meta returns an OCI image manifest (config mediaType != helm).
	h := newChartHandler(t, /* image manifest raw_json */)
	rec := doChartRequest(t, h, "acme", "web", "1.0.0")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body)
	}
}

func TestHandleGetChart_helm_happyPath(t *testing.T) {
	// meta returns a helm manifest; fake core returns config JSON for the
	// config digest and a real .tgz (built with makeChartTGZ) for the layer.
	h := newChartHandler(t /* helm manifest + config JSON + tgz keyed by digest */)
	rec := doChartRequest(t, h, "acme", "web", "1.0.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body)
	}
	var resp ChartResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Metadata == nil || resp.Metadata.Name != "web" {
		t.Fatalf("metadata: %+v", resp.Metadata)
	}
	if !strings.Contains(resp.Values, "replicaCount") {
		t.Fatalf("values: %q", resp.Values)
	}
}

func TestHandleGetChart_malformedConfig_metadataError(t *testing.T) {
	// config blob is garbage; values layer is fine → 200 with metadata_error
	// set and values still populated.
	h := newChartHandler(t /* helm manifest + garbage config + good tgz */)
	rec := doChartRequest(t, h, "acme", "web", "1.0.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp ChartResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Metadata != nil || resp.MetadataError == "" {
		t.Fatalf("expected metadata_error, got %+v", resp)
	}
	if !strings.Contains(resp.Values, "replicaCount") {
		t.Fatalf("values should survive a bad config: %q", resp.Values)
	}
}
```

Implement the small test helpers `doChartRequest` (builds an `http.Request` with `org`/`repo`/`tag` path values + a tenant context via `middleware.WithTenantID` — copy the exact context/path-value setup the existing referrers/manifest handler tests in this package use) and `newChartHandler` (wires a `*Handler` with the fakes, `core` non-nil). Follow the established test scaffolding in the package; do not invent a parallel one.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run TestHandleGetChart -v`
Expected: FAIL — `handleGetChart`, `ChartResponse` undefined.

- [ ] **Step 3: Implement `chart.go`**

Create `services/management/internal/handler/chart.go`:

```go
// Package handler — chart.go
//
// Chart tab — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart
//
// Renders a Helm chart's Chart.yaml metadata + values.yaml for the dashboard.
// The BFF resolves the tag -> manifest via registry-metadata, reads the config
// + content-layer digests out of the manifest JSON, and fetches both blobs from
// registry-core over gRPC (CoreService.GetBlob). Helm-specific parsing lives in
// chartparse.go so this file stays a thin fetch/aggregate/serve layer, mirroring
// referrers.go. Authorization matches the sibling tag-detail routes (pull access
// via RequireAuth + findRepo). Returns 404 "route disabled" when the core client
// is nil so the FE can hide the Chart tab.
package handler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// chartTimeout bounds each outgoing registry-core GetBlob call (CLAUDE.md §6).
const chartTimeout = 5 * time.Second

// ChartResponse is the JSON body for GET …/tags/{tag}/chart. Metadata is null
// when the config blob is missing/unparseable (MetadataError explains why);
// Values is "" with a non-empty ValuesError when the content layer can't be
// read. The two halves fail independently.
type ChartResponse struct {
	Metadata        *ChartMetadata `json:"metadata"`
	MetadataError   string         `json:"metadata_error,omitempty"`
	Values          string         `json:"values"`
	ValuesTruncated bool           `json:"values_truncated"`
	ValuesError     string         `json:"values_error,omitempty"`
}

// handleGetChart resolves the tag's manifest, then fetches + parses the Helm
// config + content-layer blobs.
func (h *Handler) handleGetChart(w http.ResponseWriter, r *http.Request) {
	if h.core == nil {
		writeError(w, http.StatusNotFound, "route disabled")
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	org, repoName, tagName := r.PathValue("org"), r.PathValue("repo"), r.PathValue("tag")

	if err := validateOrgName(org); err != nil {
		writeError(w, http.StatusBadRequest, "invalid org name")
		return
	}
	if err := validateRepoName(repoName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid repository name")
		return
	}
	if err := validateTagName(tagName); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag name")
		return
	}

	repo, err := h.findRepo(r, tenantID, org, repoName)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return
	}

	// GetManifest resolves a tag reference at the metadata repo layer and
	// returns the raw manifest JSON we need for the config + layer digests.
	mf, err := h.meta.GetManifest(r.Context(), &metadatav1.GetManifestRequest{
		RepoId:    repo.GetRepoId(),
		TenantId:  tenantID,
		Reference: tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	cfgDigest, cfgMediaType, contentDigest, err := parseManifestConfigAndLayer(mf.GetRawJson())
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable manifest")
		return
	}
	if cfgMediaType != helmConfigMediaType {
		writeError(w, http.StatusBadRequest, "not a Helm chart")
		return
	}

	resp := ChartResponse{}

	// --- metadata half (config blob) ---
	cfgBytes, cerr := h.fetchBlob(r.Context(), tenantID, cfgDigest, configBlobCap)
	if cerr != nil {
		resp.MetadataError = "could not read chart metadata"
		slog.Warn("chart: config blob", "err", cerr, "digest", cfgDigest)
	} else if meta, perr := parseChartMetadata(cfgBytes); perr != nil {
		resp.MetadataError = "could not parse Chart.yaml"
	} else {
		resp.Metadata = &meta
	}

	// --- values half (content layer) ---
	if contentDigest == "" {
		resp.ValuesError = "chart has no content layer"
	} else if tgz, verr := h.fetchBlob(r.Context(), tenantID, contentDigest, contentBlobCap); verr != nil {
		if status.Code(verr) == codes.FailedPrecondition {
			resp.ValuesTruncated = true
			resp.ValuesError = "chart archive too large to inspect"
		} else {
			resp.ValuesError = "could not read chart archive"
		}
	} else if vals, truncated, xerr := extractValuesYAML(tgz, valuesCap); xerr != nil {
		if errors.Is(xerr, errValuesNotFound) {
			resp.ValuesError = "no values.yaml in chart"
		} else {
			resp.ValuesError = "could not extract values.yaml"
		}
	} else {
		resp.Values = vals
		resp.ValuesTruncated = truncated
	}

	// Only hard-fail when BOTH halves failed to read (likely core is down).
	if resp.Metadata == nil && resp.Values == "" && resp.MetadataError != "" && resp.ValuesError != "" {
		writeError(w, http.StatusInternalServerError, "failed to read chart")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// fetchBlob calls registry-core GetBlob with an independent deadline and cap,
// returning the raw bytes. Errors are returned verbatim so the caller can
// distinguish FailedPrecondition (too large) from the rest.
func (h *Handler) fetchBlob(ctx context.Context, tenantID, digest string, maxBytes int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, chartTimeout)
	defer cancel()
	resp, err := h.core.GetBlob(ctx, &corev1.GetBlobRequest{
		TenantId: tenantID, Digest: digest, MaxBytes: maxBytes,
	})
	if err != nil {
		return nil, err
	}
	return bytes.Clone(resp.GetData()), nil
}
```

- [ ] **Step 4: Register the route**

In `services/management/internal/handler/handler.go`, immediately after the referrers route (line ~362) add:

```go
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart", authMW(http.HandlerFunc(h.handleGetChart)))
```

- [ ] **Step 5: Raise the core-client receive cap**

In `services/management/internal/server/server.go`, the core client dial (line ~195) — add a default-call option:

```go
		coreConn, err := grpc.NewClient(cfg.CoreGRPCAddr,
			grpc.WithTransportCredentials(coreCreds),
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)), // FUT-022: GetBlob payloads
		)
```

- [ ] **Step 6: Run tests + build**

Run: `cd services/management && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
Expected: PASS (all `TestHandleGetChart*` green, existing routes unaffected).

- [ ] **Step 7: Commit**

```bash
git add services/management/internal/handler/chart.go services/management/internal/handler/chart_test.go services/management/internal/handler/handler.go services/management/internal/server/server.go
git commit -m "feat(management): GET tags/{tag}/chart Helm detail endpoint (FUT-022)"
```

---

## Task 5: FE — `chart.ts` API hook

**Files:**
- Create: `frontend/src/lib/api/chart.ts`

Context: mirror `frontend/src/lib/api/referrers.ts` — same `apiClient`, react-query, and 404-means-not-enabled handling.

- [ ] **Step 1: Implement `chart.ts`**

```ts
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";
import { isAxiosError } from "axios";

// ChartMaintainer / ChartDependency / ChartMetadata mirror the BFF
// ChartResponse (services/management/internal/handler/chart.go) — snake_case
// on the wire.
export interface ChartMaintainer {
  name?: string;
  email?: string;
  url?: string;
}
export interface ChartDependency {
  name?: string;
  version?: string;
  repository?: string;
}
export interface ChartMetadata {
  name: string;
  version: string;
  app_version?: string;
  description?: string;
  api_version?: string;
  type?: string;
  kube_version?: string;
  home?: string;
  icon?: string;
  deprecated?: boolean;
  keywords?: string[];
  sources?: string[];
  maintainers?: ChartMaintainer[];
  dependencies?: ChartDependency[];
  annotations?: Record<string, string>;
}
export interface ChartResponse {
  metadata: ChartMetadata | null;
  metadata_error?: string;
  values: string;
  values_truncated: boolean;
  values_error?: string;
}

export const chartKeys = {
  detail: (org: string, repo: string, tag: string) =>
    ["chart", org, repo, tag] as const,
};

// useChart fetches the Helm chart detail for a tag. `enabled` should be gated
// by the caller so it only fires for Helm artifacts + when the Chart tab is
// active. A 404 (core client not wired) resolves to null so the caller renders
// an empty state instead of an error — same posture as useReferrers.
export function useChart(
  org: string,
  repo: string,
  tag: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: chartKeys.detail(org, repo, tag),
    queryFn: async (): Promise<ChartResponse | null> => {
      try {
        const { data } = await apiClient.get<ChartResponse>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(
            repo,
          )}/tags/${encodeURIComponent(tag)}/chart`,
        );
        return data;
      } catch (err) {
        if (isAxiosError(err) && err.response?.status === 404) return null;
        throw err;
      }
    },
    staleTime: 30_000,
    enabled: enabled && Boolean(org && repo && tag),
  });
}
```

- [ ] **Step 2: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: PASS. (Confirm `referrers.ts` uses the same `isAxiosError` import path; if it uses a different helper, match it.)

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/api/chart.ts
git commit -m "feat(frontend): useChart API hook for Helm chart detail (FUT-022)"
```

---

## Task 6: FE — `chart-panel.tsx` component + tests

**Files:**
- Create: `frontend/src/components/tags/chart-panel.tsx`
- Create: `frontend/src/components/tags/__tests__/chart-panel.test.tsx`

Context: mirror `frontend/src/components/tags/referrers-panel.tsx` for the loading/error/empty structure, the design tokens (`var(--color-*)`), `EmptyState`, `ErrorState`, `Skeleton`, and the existing `CopyButton` component (grep for its import path).

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/components/tags/__tests__/chart-panel.test.tsx`:

```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChartPanel } from "../chart-panel";
import * as api from "@/lib/api/chart";

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ChartPanel org="acme" repo="web" tag="1.0.0" active />
    </QueryClientProvider>,
  );
}

describe("ChartPanel", () => {
  it("renders metadata + values", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: {
          name: "web",
          version: "1.0.0",
          app_version: "2.0.0",
          description: "the web chart",
          dependencies: [{ name: "pg", version: "12.x", repository: "oci://r" }],
        },
        values: "replicaCount: 1\n",
        values_truncated: false,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText("web")).toBeInTheDocument();
    expect(screen.getByText(/the web chart/)).toBeInTheDocument();
    expect(screen.getByText(/replicaCount/)).toBeInTheDocument();
    expect(screen.getByText("pg")).toBeInTheDocument();
  });

  it("shows a truncation banner when values_truncated", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: {
        metadata: { name: "web", version: "1.0.0" },
        values: "a: 1\n",
        values_truncated: true,
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText(/truncated/i)).toBeInTheDocument();
  });

  it("renders an empty state when chart detail is not enabled (null)", () => {
    vi.spyOn(api, "useChart").mockReturnValue({
      data: null,
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof api.useChart>);
    renderPanel();
    expect(screen.getByText(/not (available|enabled)/i)).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd frontend && npm run test -- chart-panel`
Expected: FAIL — `../chart-panel` module not found.

- [ ] **Step 3: Implement `chart-panel.tsx`**

Build `ChartPanel({ org, repo, tag, active }: { org: string; repo: string; tag: string; active: boolean })`:
- Call `useChart(org, repo, tag, active)`.
- `isLoading` → `Skeleton` rows.
- `isError` → `ErrorState` with a retry.
- `data === null` → `EmptyState` (icon `Ship`), copy: "Chart detail isn't available" / "registry-core chart inspection is not enabled."
- Otherwise render:
  - **Metadata card:** `metadata.name` + `version` heading, `app_version` badge, `description`; a definition list for `api_version` / `type` / `kube_version` / `deprecated` (pill) and `home` / `icon` / `sources[]` as external links; `keywords[]` as chips. If `metadata === null`, show `metadata_error` inline instead of the card.
  - **Dependencies table** (name / version / repository) — render only when `dependencies?.length`.
  - **Maintainers** (name + mailto/url) — render only when `maintainers?.length`.
  - **values.yaml block:** a `<pre className="font-mono …">` in a bordered, `overflow-x-auto` container with a `CopyButton value={data.values}`; when `values_truncated`, a banner "Showing the first 256 KB — values.yaml was truncated."; when `values_error` and no values, an inline note with the error text.

Use the same token classes and section shells as `referrers-panel.tsx`. Every subcomponent gets a comment.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd frontend && npm run test -- chart-panel`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/tags/chart-panel.tsx frontend/src/components/tags/__tests__/chart-panel.test.tsx
git commit -m "feat(frontend): ChartPanel component for Helm chart detail (FUT-022)"
```

---

## Task 7: FE — wire the Chart tab (gated on `artifact_type`)

**Files:**
- Modify: `frontend/src/routes/_authenticated.repositories.$org.$repo_.tags.$tag.tsx`

Context: `TAG_TAB_VALUES` (line 29) is the allowlist; `tagRow` (derived from `useTags`, line 82) carries `artifact_type` (already on the FE `Tag` type). Tabs are rendered around lines 154–197.

- [ ] **Step 1: Add "chart" to the tab allowlist**

```ts
const TAG_TAB_VALUES = ["security", "history", "layers", "signing", "referrers", "chart"] as const;
```

- [ ] **Step 2: Compute the Helm gate + import the panel**

Near the top of the component, after `tagRow` is derived:

```tsx
// FUT-022 — the Chart tab only exists for Helm artifacts. artifact_type is
// derived server-side from the manifest's config.mediaType.
const isHelm = tagRow?.artifact_type === "helm";
```

Add the import: `import { ChartPanel } from "@/components/tags/chart-panel";`

- [ ] **Step 3: Render the trigger + content conditionally**

After the Referrers `TabsTrigger` (line ~158):

```tsx
{isHelm ? <TabsTrigger value="chart">Chart</TabsTrigger> : null}
```

After the Referrers `TabsContent` (line ~197):

```tsx
{isHelm ? (
  <TabsContent value="chart">
    <ChartPanel
      org={org}
      repo={repo}
      tag={tag}
      active={activeTab === "chart"}
    />
  </TabsContent>
) : null}
```

Use whatever the local variable for the current tab is (the file already tracks it for the controlled `Tabs`; reuse it for `active`).

- [ ] **Step 4: Run all four FE gates**

Run: `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`
Expected: all PASS. (The `TAG_TAB_VALUES` guard already prevents a `?tab=chart` deep-link on a non-Helm tag from selecting a missing tab.)

- [ ] **Step 5: Commit**

```bash
git add "frontend/src/routes/_authenticated.repositories.\$org.\$repo_.tags.\$tag.tsx"
git commit -m "feat(frontend): Chart tab on tag detail, gated on artifact_type (FUT-022)"
```

---

## Task 8: Docs + tracker close-out

**Files:**
- Modify: `docs/SERVICES.md` (BFF route table + core `GetBlob`)
- Modify: `status.md` (prepend a resolution row)
- Modify: `FE-STATUS.md` (new FE-API row for the Chart tab)
- Modify: `futures.md` (FUT-022 → Helm detail shipped; note deferred `/artifacts` + referrer-rendering scope)

- [ ] **Step 1: Update `docs/SERVICES.md`**

Add `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart` to the registry-management route table, and add `CoreService.GetBlob` to the registry-core gRPC section (note: generic, size-capped, used by the BFF chart route).

- [ ] **Step 2: Prepend a row to `status.md`**

One row: date `2026-07-06`, "FUT-022 Helm chart detail page — Chart tab (Chart.yaml metadata + values.yaml) via generic CoreService.GetBlob + BFF chart endpoint", PR link placeholder.

- [ ] **Step 3: Update `FE-STATUS.md`**

Add the next `FE-API-0NN` row for the Chart tab (pick the next free number).

- [ ] **Step 4: Update `futures.md`**

Mark FUT-022 as shipped for the Helm-detail scope; explicitly note the deferred remainder (generic `/artifacts` landing page; richer referrer rendering; `helm template`/provenance) so the tracker reflects reality.

- [ ] **Step 5: Commit**

```bash
git add docs/SERVICES.md status.md FE-STATUS.md futures.md
git commit -m "docs(fut-022): tracker + SERVICES close-out for Helm chart detail"
```

---

## Final verification (before opening the PR)

- [ ] `cd services/core && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
- [ ] `cd services/management && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
- [ ] `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`
- [ ] `git log --oneline` shows 8 focused commits on `feat/fut-022-helm-chart-detail`.
- [ ] Optional live check: rebuild `registry-core` + `registry-management` containers, push a chart with `helm push`, open the tag → Chart tab renders metadata + values.

Then dispatch the security-agent + qa-agent + code-review-agent review batch (worktree-isolated, read-only) per the project cadence, fix must-fixes inline, and open the PR.
