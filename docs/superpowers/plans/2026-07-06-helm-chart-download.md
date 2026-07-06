# Helm Chart Download Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A one-click "Download chart (.tgz)" on the Chart tab that streams a Helm chart's packaged content layer to the browser, byte-identical to `helm pull`.

**Architecture:** A new server-streaming `CoreService.GetBlobStream` gRPC streams a blob from storage in chunks (constant memory, no size cap). The `registry-management` BFF adds a streaming download endpoint that resolves the tag → Helm content-layer digest (reusing FUT-022's `parseManifestConfigAndLayer`), opens the stream, and `io.Copy`s it to the HTTP response with `Content-Disposition`. The React Chart panel gets a download button using the existing authenticated-blob-download pattern.

**Tech Stack:** Go 1.25 (grpc server-streaming, buf), React + TanStack Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-07-06-helm-chart-download-design.md`

**Branch:** `feat/helm-chart-download` (checked out; spec committed).

---

## Conventions for every task

- **Per-module Go commands** (match Docker/CI):
  - core: `cd services/core && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
  - management: `cd services/management && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
  - A pre-existing `admin_tenants_test.go:162` vet lock-copy warning in management is NOT yours — vet still exits 0.
- **Proto regen:** `cd proto && buf generate` (from inside `proto/` — never repo root, which rescans stale `.claude/worktrees/*` copies).
- **Frontend (`npm` not on PATH in Git Bash — use PowerShell tool):** `Set-Location frontend; npm run lint` / `npm run typecheck` / `npm run test` / `npm run build`. All four before any FE push.
- **Comments:** every new func/type gets a godoc/JSDoc comment. Match neighbour density (`chart.go`, `chart-panel.tsx`).
- **Commit trailer:** end each commit body with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Never** stage `.claude/scheduled_tasks.lock` (harness state).

---

## Task 1: proto — `CoreService.GetBlobStream`

**Files:**
- Modify: `proto/core/v1/core.proto`
- Regenerate: `proto/gen/go/core/v1/core.pb.go`, `core_grpc.pb.go`

- [ ] **Step 1: Add the streaming RPC + chunk message**

In `service CoreService { ... }`, add after `GetBlob`:

```proto
  // GetBlobStream streams a blob's raw bytes in chunks, for downloads that may
  // exceed the unary GetBlob size cap (e.g. a chart .tgz). Reuses GetBlobRequest
  // (max_bytes is ignored — streaming is unbounded by design; the blob is a
  // stored artifact the caller already has pull access to).
  rpc GetBlobStream(GetBlobRequest) returns (stream GetBlobChunk);
```

Add at the end of the file:

```proto
// GetBlobChunk is one slice of a streamed blob's bytes.
message GetBlobChunk {
  bytes data = 1;
}
```

- [ ] **Step 2: Regenerate stubs**

Run: `cd proto && buf generate`
Expected: no errors; `git status` shows `proto/gen/go/core/v1/core.pb.go` + `core_grpc.pb.go` modified with a new streaming client method (`GetBlobStream(ctx, *GetBlobRequest, ...) (CoreService_GetBlobStreamClient, error)`), the server interface method, and the `GetBlobChunk` type.

- [ ] **Step 3: Verify both modules build**

Run: `cd services/core && GOWORK=off go build ./...` then `cd services/management && GOWORK=off go build ./...`
Expected: PASS (new symbols resolve; nothing calls them yet).

- [ ] **Step 4: Commit**

```bash
git add proto/core/v1/core.proto proto/gen/go/core/v1/
git commit -m "feat(proto): add CoreService.GetBlobStream server-streaming RPC"
```

---

## Task 2: core — `GetBlobStream` handler + chunk writer

**Files:**
- Modify: `services/core/internal/handler/grpc_core.go`
- Modify: `services/core/internal/handler/grpc_core_test.go`

Context: `CoreHandler.registry` is a `coreReader` interface that ALREADY has `GetBlob(ctx, tenantID, digest string, w io.Writer) (int64, error)` (added in FUT-022) — reuse it, no interface change. `(*service.Registry).GetBlob` streams storage into `w` chunk-by-chunk, so a writer that forwards each `Write` as a `stream.Send` yields a genuine stream. `digestRE` and `service.ErrNotFound` are available (used by the existing `GetBlob` handler). Follow TDD.

- [ ] **Step 1: Write the failing tests**

Add to `services/core/internal/handler/grpc_core_test.go`:

```go
// fakeBlobStream is a minimal corev1.CoreService_GetBlobStreamServer for
// unit-testing GetBlobStream without a live gRPC server. It collects Sent
// chunks; only Send + Context are exercised by the handler, so the embedded
// grpc.ServerStream (nil) is never touched.
type fakeBlobStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent [][]byte
}

func (f *fakeBlobStream) Send(c *corev1.GetBlobChunk) error {
	f.sent = append(f.sent, append([]byte(nil), c.GetData()...))
	return nil
}

func (f *fakeBlobStream) Context() context.Context {
	if f.ctx != nil {
		return f.ctx
	}
	return context.Background()
}

func TestGetBlobStream_streamsBytes(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{blob: []byte("chart-tgz-bytes")})
	fs := &fakeBlobStream{}
	err := h.GetBlobStream(&corev1.GetBlobRequest{
		TenantId: "t1", Digest: "sha256:" + strings.Repeat("a", 64),
	}, fs)
	if err != nil {
		t.Fatalf("GetBlobStream: %v", err)
	}
	var got []byte
	for _, c := range fs.sent {
		got = append(got, c...)
	}
	if string(got) != "chart-tgz-bytes" {
		t.Fatalf("reassembled %q", got)
	}
}

func TestGetBlobStream_badDigest_invalidArgument(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{})
	err := h.GetBlobStream(&corev1.GetBlobRequest{TenantId: "t1", Digest: "nope"}, &fakeBlobStream{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestGetBlobStream_notFound(t *testing.T) {
	h := NewCoreHandler(&fakeReferrerLister{blobErr: service.ErrNotFound})
	err := h.GetBlobStream(&corev1.GetBlobRequest{
		TenantId: "t1", Digest: "sha256:" + strings.Repeat("a", 64),
	}, &fakeBlobStream{})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}
```

`grpc` (`google.golang.org/grpc`) must be imported in the test file — add it if missing. `context`, `strings`, `codes`, `status`, `service`, `corev1` are already imported by the existing `GetBlob` tests.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd services/core && GOWORK=off go test ./internal/handler/ -run TestGetBlobStream -v`
Expected: FAIL — `GetBlobStream` undefined on `*CoreHandler`.

- [ ] **Step 3: Implement the handler + chunk writer**

Add to `services/core/internal/handler/grpc_core.go`:

```go
// grpcChunkWriter forwards each Write to the gRPC stream as a GetBlobChunk, so
// Registry.GetBlob can stream storage bytes straight to the client with no
// intermediate buffering.
type grpcChunkWriter struct {
	stream corev1.CoreService_GetBlobStreamServer
}

func (w grpcChunkWriter) Write(p []byte) (int, error) {
	// Copy p: the storage layer may reuse the chunk slice after Write returns,
	// and Send may serialise it asynchronously.
	if err := w.stream.Send(&corev1.GetBlobChunk{Data: append([]byte(nil), p...)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// GetBlobStream streams a blob's raw bytes to the client in chunks. Validates
// tenant_id + digest exactly like GetBlob (max_bytes is ignored — streaming is
// unbounded). Like GetBlob it authorises on tenant + digest only; callers MUST
// gate repo access first (the BFF download route does via findRepo).
func (h *CoreHandler) GetBlobStream(req *corev1.GetBlobRequest, stream corev1.CoreService_GetBlobStreamServer) error {
	if req.GetTenantId() == "" {
		return status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if !digestRE.MatchString(req.GetDigest()) {
		return status.Error(codes.InvalidArgument, "digest must match sha256:<hex64>")
	}
	ctx := stream.Context()
	if _, err := h.registry.GetBlob(ctx, req.GetTenantId(), req.GetDigest(), grpcChunkWriter{stream: stream}); err != nil {
		if errors.Is(err, service.ErrNotFound) {
			return status.Error(codes.NotFound, "blob not found")
		}
		slog.ErrorContext(ctx, "GetBlobStream: read failed",
			"tenant_id", req.GetTenantId(), "digest", req.GetDigest(), "error", err)
		return status.Error(codes.Internal, "failed to read blob")
	}
	return nil
}
```

(`errors`, `slog`, `codes`, `status`, `corev1`, `service` are already imported by the existing `GetBlob` handler.)

- [ ] **Step 4: Run tests + build**

Run: `cd services/core && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
Expected: PASS (all `TestGetBlobStream*` green, existing tests still green).

- [ ] **Step 5: Commit**

```bash
git add services/core/internal/handler/grpc_core.go services/core/internal/handler/grpc_core_test.go
git commit -m "feat(core): GetBlobStream server-streaming handler"
```

---

## Task 3: BFF — streaming download endpoint

**Files:**
- Create: `services/management/internal/handler/chart_download.go`
- Create: `services/management/internal/handler/chart_download_test.go`
- Modify: `services/management/internal/handler/handler.go` (register route)
- Modify: `services/management/internal/handler/referrers_test.go` (extend `fakeCoreServer`)

Context: `handleGetChart` (`chart.go`) is the exact prefix to mirror — `h.core == nil → 404`, `validateOrgName/validateRepoName/validateTagName`, `findRepo`, `meta.GetManifest(tag) → raw_json`, `parseManifestConfigAndLayer`. `helmConfigMediaType` + `parseManifestConfigAndLayer` are from `chartparse.go`. The chart route is registered in `handler.go` (search `tags/{tag}/chart`). **The download handler uses `r.Context()` directly (NOT a 5s `WithTimeout`) — the client `DeadlineInterceptor` is unary-only, so a server-stream is not capped, and large charts must not be cut off.** Follow TDD.

- [ ] **Step 1: Extend `fakeCoreServer` with `GetBlobStream`**

In `services/management/internal/handler/referrers_test.go`, add this method (it reuses the existing `blobs`/`blobErr`/`blobErrs` fields; splits into 2 chunks to exercise streaming):

```go
// GetBlobStream streams a canned blob for the chart-download route. Reuses the
// same blobs/blobErr/blobErrs fields as GetBlob; sends the bytes in two chunks
// so tests exercise multi-chunk reassembly.
func (s *fakeCoreServer) GetBlobStream(req *corev1.GetBlobRequest, stream corev1.CoreService_GetBlobStreamServer) error {
	if e, ok := s.blobErrs[req.GetDigest()]; ok {
		return e
	}
	if s.blobErr != nil {
		return s.blobErr
	}
	data, ok := s.blobs[req.GetDigest()]
	if !ok {
		return status.Error(codes.NotFound, "blob not found")
	}
	mid := len(data) / 2
	if err := stream.Send(&corev1.GetBlobChunk{Data: data[:mid]}); err != nil {
		return err
	}
	return stream.Send(&corev1.GetBlobChunk{Data: data[mid:]})
}
```

- [ ] **Step 2: Write the failing handler tests**

Create `services/management/internal/handler/chart_download_test.go`. Reuse the `referrers_test.go` env harness (the one that builds `*Handler` + the fake core client + the metadata fake) and the helm-manifest helper from `chart_test.go` (build the manifest JSON inline as in the FUT-022 chart tests). Tests:

```go
func TestHandleDownloadChart_nilCore_404(t *testing.T) {
	// Handler with core == nil → 404. Mirror chart_test.go's nil-core setup.
}

func TestHandleDownloadChart_notHelm_400(t *testing.T) {
	// meta returns an OCI image manifest (config mediaType != helm). Assert 400.
}

func TestHandleDownloadChart_noContentLayer_400(t *testing.T) {
	// meta returns a helm config manifest whose layers[] has NO helm content
	// layer. Assert 400.
}

func TestHandleDownloadChart_helm_streamsBytes(t *testing.T) {
	// meta returns a helm manifest; fakeCore.blobs[contentDigest] = []byte("PK...tgz").
	// Assert: 200, Content-Type == "application/gzip",
	// Content-Disposition contains `filename="<repo>-<tag>.tgz"`,
	// body bytes == the canned bytes.
}

func TestHandleDownloadChart_coreNotFound_404(t *testing.T) {
	// helm manifest; fakeCore.blobErrs[contentDigest] = status.Error(codes.NotFound, "gone").
	// Assert 404.
}
```

Use `httptest.NewRecorder()`; assert `rec.Code`, `rec.Header().Get("Content-Type")`, `rec.Header().Get("Content-Disposition")`, and `rec.Body.Bytes()`. Build the request with the org/repo/tag path values + tenant context exactly as the referrers/chart handler tests do.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run TestHandleDownloadChart -v`
Expected: FAIL — `handleDownloadChart` undefined.

- [ ] **Step 4: Implement `chart_download.go`**

Create `services/management/internal/handler/chart_download.go`:

```go
// Package handler — chart_download.go
//
// Chart download — GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart/download
//
// Streams a Helm chart's packaged content layer (.tgz) to the browser, byte-
// identical to `helm pull`. Resolves the tag -> manifest -> Helm content-layer
// digest (reusing chartparse.go), then streams registry-core's GetBlobStream
// straight to the HTTP response. Auth + repo gate match the chart route. Lives
// in its own file so concurrent edits don't collide with chart.go.
package handler

import (
	"io"
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// handleDownloadChart streams the tag's Helm chart .tgz to the client.
func (h *Handler) handleDownloadChart(w http.ResponseWriter, r *http.Request) {
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

	mf, err := h.meta.GetManifest(r.Context(), &metadatav1.GetManifestRequest{
		RepoId:    repo.GetRepoId(),
		TenantId:  tenantID,
		Reference: tagName,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	_, cfgMediaType, contentDigest, err := parseManifestConfigAndLayer(mf.GetRawJson())
	if err != nil {
		writeError(w, http.StatusBadRequest, "unreadable manifest")
		return
	}
	if cfgMediaType != helmConfigMediaType || contentDigest == "" {
		writeError(w, http.StatusBadRequest, "not a Helm chart")
		return
	}

	// Stream with the request context directly. Do NOT wrap in a short timeout:
	// GetBlobStream is a server stream, the client DeadlineInterceptor is
	// unary-only, and a large chart download must not be cut off.
	stream, err := h.core.GetBlobStream(r.Context(), &corev1.GetBlobRequest{
		TenantId: tenantID,
		Digest:   contentDigest,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to download chart")
		return
	}

	// Read the first chunk before writing headers so a NotFound / error maps to
	// a clean HTTP status instead of a truncated 200.
	first, err := stream.Recv()
	if err != nil && err != io.EOF {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.NotFound:
				writeError(w, http.StatusNotFound, "chart blob not found")
				return
			case codes.InvalidArgument:
				writeError(w, http.StatusBadRequest, "invalid download request")
				return
			}
		}
		writeError(w, http.StatusBadGateway, "failed to download chart")
		return
	}

	// repoName + tagName are already allowlist-validated, so the header value
	// can't carry CR/LF or quotes.
	filename := repoName + "-" + tagName + ".tgz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)

	if first != nil {
		if _, werr := w.Write(first.GetData()); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err == io.EOF {
		return // empty single-shot stream
	}

	for {
		chunk, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Headers already sent — can't change the status; log + truncate.
			slog.Error("chart download: mid-stream read failed", "err", rerr, "digest", contentDigest)
			return
		}
		if _, werr := w.Write(chunk.GetData()); werr != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 5: Register the route**

In `services/management/internal/handler/handler.go`, immediately after the chart route (`…/tags/{tag}/chart`), add:

```go
	mux.Handle("GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart/download", authMW(http.HandlerFunc(h.handleDownloadChart)))
```

- [ ] **Step 6: Run tests + build**

Run: `cd services/management && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
Expected: PASS (all `TestHandleDownloadChart*` green).

- [ ] **Step 7: Commit**

```bash
git add services/management/internal/handler/chart_download.go services/management/internal/handler/chart_download_test.go services/management/internal/handler/handler.go services/management/internal/handler/referrers_test.go
git commit -m "feat(management): streaming Helm chart download endpoint"
```

---

## Task 4: FE — download hook + button

**Files:**
- Modify: `frontend/src/lib/api/chart.ts` (add `useDownloadChart`)
- Modify: `frontend/src/components/tags/chart-panel.tsx` (Download button)
- Modify: `frontend/src/components/tags/__tests__/chart-panel.test.tsx` (button tests)

Context: mirror `useDownloadReport` in `frontend/src/lib/api/compliance-reports.ts` — a `useMutation` that fetches the blob (`responseType: "blob"`), builds an object URL, clicks a temporary `<a download>`, and revokes. `chart-panel.tsx` already imports `CopyButton` and lucide icons; add the lucide `Download` icon. `sonner`'s `toast` is used across the app.

- [ ] **Step 1: Add `useDownloadChart` to `chart.ts`**

Append to `frontend/src/lib/api/chart.ts`:

```ts
import { useMutation } from "@tanstack/react-query";
import { AxiosError } from "axios";

// useDownloadChart fetches a Helm chart's .tgz as an authenticated blob and
// triggers a browser download (a plain <a href> can't carry the JWT). Mirrors
// useDownloadReport (compliance-reports.ts): blob -> object URL -> anchor click.
// The suggested filename is "<repo>-<tag>.tgz", matching the BFF's
// Content-Disposition.
export function useDownloadChart() {
  return useMutation<void, Error, { org: string; repo: string; tag: string }>({
    mutationFn: async ({ org, repo, tag }) => {
      try {
        const res = await apiClient.get<Blob>(
          `/repositories/${encodeURIComponent(org)}/${encodeURIComponent(
            repo,
          )}/tags/${encodeURIComponent(tag)}/chart/download`,
          { responseType: "blob" },
        );
        const url = window.URL.createObjectURL(res.data);
        const a = document.createElement("a");
        a.href = url;
        a.download = `${repo}-${tag}.tgz`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.setTimeout(() => window.URL.revokeObjectURL(url), 1_000);
      } catch (e) {
        if (e instanceof AxiosError && e.response?.status === 400) {
          throw new Error("This tag isn't a Helm chart.");
        }
        throw new Error("Couldn't download the chart. Check the BFF logs.");
      }
    },
  });
}
```

(Adjust the imports to merge with the existing `chart.ts` import block — `useQuery` is already imported from `@tanstack/react-query`; add `useMutation` to that import, and add `AxiosError` if not already imported. If `chart.ts` already imports `AxiosError` in another form, reuse it.)

- [ ] **Step 2: Typecheck**

PowerShell: `Set-Location frontend; npm run typecheck`
Expected: PASS.

- [ ] **Step 3: Write the failing button test**

Add to `frontend/src/components/tags/__tests__/chart-panel.test.tsx`:

```tsx
it("renders a download button that triggers the download mutation", async () => {
  const mutate = vi.fn();
  vi.spyOn(api, "useDownloadChart").mockReturnValue({
    mutate,
    isPending: false,
  } as unknown as ReturnType<typeof api.useDownloadChart>);
  vi.spyOn(api, "useChart").mockReturnValue({
    data: { metadata: { name: "web", version: "1.0.0" }, values: "a: 1\n", values_truncated: false },
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof api.useChart>);
  renderPanel();
  const btn = screen.getByRole("button", { name: /download/i });
  btn.click();
  expect(mutate).toHaveBeenCalledWith({ org: "acme", repo: "web", tag: "1.0.0" });
});
```

Also add `useDownloadChart` to the existing `useChart` mocks in the other tests (they don't render the button path unless mocked) — the simplest fix is a top-level `vi.spyOn(api, "useDownloadChart").mockReturnValue({ mutate: vi.fn(), isPending: false } as ...)` in a `beforeEach`, so every test that renders the panel has a stub. Add that `beforeEach`.

- [ ] **Step 4: Run test to verify it fails**

PowerShell: `Set-Location frontend; npm run test -- chart-panel`
Expected: FAIL — no button matching `/download/i` (or `useDownloadChart` not exported yet).

- [ ] **Step 5: Add the Download button to `chart-panel.tsx`**

In the values card header (next to the existing `CopyButton`), add a download button:

```tsx
import { Download } from "lucide-react";
import { toast } from "sonner";
import { useChart, useDownloadChart } from "@/lib/api/chart";
// ...
// inside ChartPanel, after `const { data, ... } = useChart(...)`:
const download = useDownloadChart();
// ...
// in the values card header row, beside <CopyButton value={data.values} …/>:
<button
  type="button"
  onClick={() =>
    download.mutate(
      { org, repo, tag },
      { onError: (e) => toast.error(e.message) },
    )
  }
  disabled={download.isPending}
  className="inline-flex items-center gap-1.5 rounded-md border border-[var(--color-border)] px-2.5 py-1 text-xs font-medium text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-sunken)] disabled:opacity-50"
>
  <Download className="size-3.5" aria-hidden />
  {download.isPending ? "Downloading…" : "Download chart"}
</button>
```

Merge the imports into the existing lines (don't duplicate the `useChart` import; add `useDownloadChart` to it). Place the button so it's rendered on the real-chart path (where `data.values` / metadata renders), never on the loading/error/empty branches.

- [ ] **Step 6: Run the button test + full FE gates**

PowerShell, all four:
```
Set-Location frontend; npm run lint
Set-Location frontend; npm run typecheck
Set-Location frontend; npm run test
Set-Location frontend; npm run build
```
Expected: all PASS (chart-panel button test green; existing tests green).

- [ ] **Step 7: Commit**

```bash
git add frontend/src/lib/api/chart.ts frontend/src/components/tags/chart-panel.tsx frontend/src/components/tags/__tests__/chart-panel.test.tsx
git commit -m "feat(frontend): Download chart button on the Chart tab"
```

---

## Task 5: Docs + tracker close-out

**Files:**
- Modify: `docs/SERVICES.md` (new route + `GetBlobStream` RPC)
- Modify: `status.md` (prepend resolution row)
- Modify: `FE-STATUS.md` (next FE-API row)
- Modify: `futures.md` (mark the FUT-022 `GetBlobStream`/streaming-download deferred item shipped)

- [ ] **Step 1: `docs/SERVICES.md`** — under registry-core's CoreService gRPC section (added in FUT-022) add `GetBlobStream(GetBlobRequest) → stream GetBlobChunk` — "server-streaming blob fetch for downloads that exceed the unary GetBlob cap; used by the BFF chart-download route." Under registry-management add `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart/download` — "streams a Helm chart's .tgz (Content-Disposition attachment); 404 when core unset, 400 when the tag isn't a Helm chart."

- [ ] **Step 2: `status.md`** — prepend a newest-first row: date `2026-07-06`, "Helm chart download — one-click .tgz download on the Chart tab via streaming CoreService.GetBlobStream + BFF download endpoint (byte-identical to helm pull)", branch `feat/helm-chart-download`, PR placeholder.

- [ ] **Step 3: `FE-STATUS.md`** — add the next `FE-API-0NN` row (grep for the highest; FUT-022 used 055): Download-chart button on the Chart tab.

- [ ] **Step 4: `futures.md`** — in the FUT-022 entry's deferred list, mark the streaming `GetBlobStream` / chart-download item as SHIPPED 2026-07-06 (branch `feat/helm-chart-download`).

- [ ] **Step 5: Commit**

```bash
git add docs/SERVICES.md status.md FE-STATUS.md futures.md
git commit -m "docs: tracker + SERVICES close-out for Helm chart download"
```

---

## Final verification (before opening the PR)

- [ ] `cd services/core && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
- [ ] `cd services/management && GOWORK=off go test ./... && GOWORK=off go vet ./... && GOWORK=off go build ./...`
- [ ] `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build` (PowerShell)
- [ ] `git log --oneline` shows 5 focused commits on `feat/helm-chart-download`.
- [ ] Optional live check: rebuild `registry-core` + `registry-management`, open `dev/nginx:1.2.3` Chart tab → click "Download chart" → `nginx-1.2.3.tgz` downloads and `helm install`s cleanly.

Then run the security-agent + qa-agent + code-review-agent review batch (worktree-isolated, read-only), fix must-fixes inline, open the PR.
