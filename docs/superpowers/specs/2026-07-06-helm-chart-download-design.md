# Helm chart download (design)

> **Status:** design approved 2026-07-06. Follows the FUT-022 Helm chart detail
> page (Chart tab). Implements the `GetBlobStream` item deferred in the FUT-022
> spec §10.

**Goal:** Let an operator download a Helm chart's packaged `.tgz` from the Chart
tab with one click — byte-identical to `helm pull`, without a terminal.

**Posture:** single-tenant (`DEPLOYMENT_MODE=single`).

---

## 1. Scope — what's new vs. already shipped

The original "download + install helper" idea is mostly already built:

- **Install/pull walkthrough — DONE.** `PullCommandCard` (rendered on the
  tag-detail page at
  `frontend/src/routes/_authenticated.repositories.$org.$repo_.tags.$tag.tsx:159`)
  + `pullCommandFor()` (`frontend/src/lib/format.ts`) already emit the full
  artifact-type-aware Helm walkthrough: `helm registry login` →
  `helm pull oci://host/org/repo --version <tag> --plain-http` →
  `helm install my-release …`, with local-host `--plain-http` detection. **Not
  rebuilding.**
- **Manifest → content-layer-digest resolution + the Helm gate — DONE**
  (`parseManifestConfigAndLayer`, `helmContentMediaType`, FUT-022
  `chartparse.go`).
- **Registry host surfacing — DONE** (`/api/v1/deployment-info` → `platformHost`).

**Genuinely new work: the download itself** — a streaming blob path from
storage to the browser, in three pieces (§3).

---

## 2. Why streaming (not the existing unary `GetBlob`)

FUT-022 added a unary `CoreService.GetBlob` that buffers the whole blob into one
gRPC message, capped at 16 MiB (10 MiB effective). That's right for
config/values inspection but wrong for a download: a chart `.tgz` with bundled
subcharts can exceed the cap, and buffering a whole artifact into one message
is wasteful. A **server-streaming** RPC serves any size in constant memory. This
is exactly the `GetBlobStream` deferred in FUT-022 spec §10.

---

## 3. Architecture

```
FE "Download chart" button ──HTTP GET──▶ registry-management (BFF)
   (authenticated blob fetch)               │ 1. findRepo (tenant-scoped pull gate)
                                            │ 2. GetManifest(tag) → raw_json
                                            │ 3. parseManifestConfigAndLayer → contentDigest + gate helm
                                            │ 4. GetBlobStream(contentDigest) ──gRPC──▶ registry-core ──▶ storage
                                            └ 5. io.Copy chunks → http.ResponseWriter (streamed, Flushed)
```

### 3.1 `CoreService.GetBlobStream` (server-streaming gRPC)

Add to `proto/core/v1/core.proto` (additive; regenerate committed stubs):

```proto
service CoreService {
  rpc ListReferrers(ListReferrersRequest) returns (ListReferrersResponse);
  rpc GetBlob(GetBlobRequest) returns (GetBlobResponse);
  // GetBlobStream streams a blob's raw bytes in chunks, for downloads that may
  // exceed the unary GetBlob size cap (e.g. a chart .tgz). Reuses GetBlobRequest
  // (max_bytes is ignored — streaming is unbounded by design; the blob is a
  // stored artifact the caller already has pull access to).
  rpc GetBlobStream(GetBlobRequest) returns (stream GetBlobChunk);
}

// GetBlobChunk is one slice of a streamed blob's bytes.
message GetBlobChunk {
  bytes data = 1;
}
```

**core handler** (`services/core/internal/handler/grpc_core.go`, extends
`CoreHandler`):

- Validate `tenant_id` (required) + `digest` (shared `digestRE`). Same as
  `GetBlob`. `max_bytes` is ignored.
- Adapt the existing `(*service.Registry).GetBlob(ctx, tenantID, digest,
  io.Writer)` (which streams storage → writer) with a tiny writer that forwards
  each `Write` as a `stream.Send`:

  ```go
  // grpcChunkWriter forwards each Write to the gRPC stream as a GetBlobChunk,
  // so Registry.GetBlob can stream storage bytes straight to the client with no
  // intermediate buffering.
  type grpcChunkWriter struct {
      stream corev1.CoreService_GetBlobStreamServer
  }
  func (w grpcChunkWriter) Write(p []byte) (int, error) {
      // Copy p: the storage layer may reuse the chunk slice after Write returns,
      // and Send serialises asynchronously.
      if err := w.stream.Send(&corev1.GetBlobChunk{Data: append([]byte(nil), p...)}); err != nil {
          return 0, err
      }
      return len(p), nil
  }
  ```
- Map errors: `service.ErrNotFound` → `codes.NotFound`; else log + `codes.Internal`.
- The `coreReader` interface already has `GetBlob(ctx, tenantID, digest, io.Writer)`
  — the handler reuses it; no interface change needed.

### 3.2 BFF download endpoint

New file `services/management/internal/handler/chart_download.go`:

- **Route:** `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart/download`,
  registered next to the chart route in `handler.go`.
- **nil-core guard:** `if h.core == nil { writeError(404, "route disabled") }`.
- **Validation + resolution:** identical prefix to `handleGetChart` —
  `validateOrgName`/`validateRepoName`/`validateTagName`, `findRepo`,
  `meta.GetManifest(tag)` → `raw_json`, `parseManifestConfigAndLayer`. Gate:
  `configMediaType == helmConfigMediaType` **and** `contentDigest != ""`, else
  `400 "not a Helm chart"`.
- **Stream:** open `h.core.GetBlobStream(ctx, &corev1.GetBlobRequest{TenantId,
  Digest: contentDigest})`. Set headers **before** the first write:
  - `Content-Type: application/gzip`
  - `Content-Disposition: attachment; filename="<repo>-<tag>.tgz"` (repo/tag are
    already validated against the allowlists, so the header value is safe)
  - `X-Content-Type-Options: nosniff`
  Then loop `stream.Recv()` → `w.Write(chunk.GetData())` → flush via
  `http.Flusher` if available. On a mid-stream gRPC error, log it; the client
  sees a truncated body (headers already sent — can't change the status). On the
  **first** `Recv()` error, map before writing any body: `NotFound` → 404,
  `InvalidArgument` → 400, else 502.
- **Deadline:** no fixed short deadline on the whole stream (downloads legitimately
  take time); rely on the request context + client interceptor. A large chart is
  bounded by storage, not by us.

### 3.3 FE "Download chart" button

- **`frontend/src/lib/api/chart.ts`** — add `useDownloadChart()`, mirroring
  `useDownloadReport` (`compliance-reports.ts`): a `useMutation` that
  `apiClient.get(url, { responseType: "blob" })`, builds an object URL, clicks a
  temporary `<a download="<repo>-<tag>.tgz">`, and revokes the URL. Error map:
  404 → "not available", 400 → "not a Helm chart", `*` → "download failed". A
  plain `<a href>` can't carry the JWT, so the authenticated-fetch pattern is
  required.
- **`frontend/src/components/tags/chart-panel.tsx`** — a "Download chart (.tgz)"
  button (lucide `Download` icon) in the values card header (next to the copy
  button) or the metadata card header. Calls `useDownloadChart().mutate(...)`,
  shows a pending spinner, and a `sonner` toast on error. Only rendered when
  `data.metadata` or `data.values` is present (i.e. a real chart).

---

## 4. Filename

`<repo>-<tag>.tgz` (e.g. `nginx-1.2.3.tgz`). Cheap, no extra config-blob fetch,
correct whenever the repo name equals the chart name (the common case). The
downloaded **bytes** are byte-identical to `helm pull` regardless of the
suggested filename. Deriving the true `Chart.yaml` name/version would need an
extra blob fetch for a cosmetic filename — not worth it (YAGNI).

---

## 5. Error handling

| Condition | Result |
|---|---|
| `CORE_GRPC_ADDR` unset (`h.core == nil`) | `404 "route disabled"` → FE hides/disables button gracefully |
| Invalid org/repo/tag | `400` |
| Repo missing / cross-tenant | `404 "repository not found"` |
| Tag/manifest missing | `404 "tag not found"` |
| Not a Helm chart (config mediaType or no content layer) | `400 "not a Helm chart"` |
| core `GetBlobStream` first `Recv` = `NotFound` | `404` (blob GC'd from under the manifest) |
| core `GetBlobStream` first `Recv` = other error | `502 "failed to download chart"` |
| Mid-stream error after headers sent | truncated body + server log (can't rewrite status) |

---

## 6. Security

- **Auth:** same `authMW` + `findRepo` pull-access gate as the chart route — no
  weaker path. A user who can view the Chart tab can already pull the chart via
  `helm pull`; this is the same data over the same authorization.
- **Tenant isolation:** `tenantID` from `middleware.TenantIDFromContext`, threaded
  into `findRepo`, `GetManifest`, and `GetBlobStream` → `blobKey(tenantID,
  digest)`. No cross-tenant path.
- **Header injection:** `Content-Disposition` filename is built from `repo` + `tag`,
  both already validated against the CLAUDE.md §7 allowlists (`^[a-z0-9]…`,
  `^[a-zA-Z0-9_][\w.-]*`), so no CR/LF or quote injection is possible.
- **DoS / memory:** streaming is constant-memory (no whole-artifact buffer). The
  artifact is already stored; re-serving it to an authorized puller is not a new
  exposure. No decompression happens on the download path (raw bytes only), so
  the FUT-022 gzip-bomb concern does not apply here.
- **`GetBlobStream` authz:** like `GetBlob`, authorizes on tenant + digest only
  (documented contract: caller must gate repo access first). The sole caller
  gates via `findRepo`. Carry the same doc-comment.

---

## 7. Testing

- **core** (`grpc_core_test.go`): `GetBlobStream` happy path (fake `coreReader`
  writes N bytes → assert chunks reassemble to the original), `NotFound` →
  `codes.NotFound`, bad digest → `InvalidArgument`. Use a small in-test
  server-stream fake that collects `Send`ed chunks.
- **BFF** (`chart_download_test.go`): reuse the `referrers_test.go` harness +
  `fakeCoreServer`. Extend the fake with a `GetBlobStream` method that streams a
  canned byte slice keyed by digest. Tests: helm happy path (assert 200,
  `Content-Type: application/gzip`, `Content-Disposition` filename, body ==
  canned bytes), nil-core 404, not-helm 400, no-content-layer 400, tag-not-found
  404, core first-Recv NotFound → 404.
- **FE** (`chart-panel.test.tsx`): the download button renders for a real chart;
  clicking calls the mutation (mock `useDownloadChart`); button absent/disabled
  on the not-enabled (null) state. (Object-URL/anchor mechanics are mocked, as in
  the existing report/sbom download tests.)

---

## 8. Files

**Create**
- `services/management/internal/handler/chart_download.go` + `_test.go`
- (FE hook + button live in existing files, below)

**Modify**
- `proto/core/v1/core.proto` (+ regenerated `proto/gen/go/core/v1/*`)
- `services/core/internal/handler/grpc_core.go` (+ `_test.go`) — `GetBlobStream`
  handler + `grpcChunkWriter`
- `services/management/internal/handler/handler.go` — register the download route
- `services/management/internal/handler/referrers_test.go` — extend `fakeCoreServer`
  with `GetBlobStream`
- `frontend/src/lib/api/chart.ts` — `useDownloadChart`
- `frontend/src/components/tags/chart-panel.tsx` — Download button
- `frontend/src/components/tags/__tests__/chart-panel.test.tsx` — button tests

**Docs/trackers** (close-out): `status.md`, `FE-STATUS.md` (next FE-API row),
`futures.md` (mark the FUT-022 `GetBlobStream` deferred item shipped),
`docs/SERVICES.md` (new route + `GetBlobStream` RPC).

---

## 9. Out of scope (deferred)

- Downloading arbitrary (non-Helm) OCI artifacts / image layers — images use
  `docker pull`; a single-file download doesn't map to a multi-layer image.
- True `Chart.yaml`-derived download filename (extra blob fetch for cosmetics).
- Provenance (`.prov`) download / signature verification on download.
- Resumable / range-request downloads (charts are small; YAGNI).
