# FUT-022 — Helm chart detail page (design)

> **Status:** design approved 2026-07-06. Scope-narrowed from the original
> `futures.md` FUT-022 (“OCI artifacts as first-class citizens”) after
> discovering most of that item’s plumbing already shipped (see §1).

**Goal:** On the tag-detail page, render a first-class **Chart** tab for Helm
artifacts — the `Chart.yaml` metadata panel plus the chart’s `values.yaml`
inline (the `helm show values` experience) — without the operator pulling the
chart or speaking the OCI wire protocol.

**Posture:** single-tenant (`DEPLOYMENT_MODE=single`). No multi-tenant surface.

---

## 1. What already exists (why this scope is narrow)

The original FUT-022 note assumed a “small metadata proto extension for
mediaType discovery” plus a generic `/artifacts` browse view. Almost all of
that already shipped and is **not** part of this work:

- `Tag.artifact_type` + `Manifest.artifact_type` discriminators
  (`image`/`helm`/`signature`/`sbom`/`other`), derived from `config.mediaType`
  at push time and stored (`proto/metadata/v1/metadata.proto`).
- `Manifest.config_media_type` stored + partial-indexed.
- BFF `GET /repositories?artifact_type=helm` filter (allowlisted) and the FE
  `RepoArtifactFilter`.
- `/helm` route + sidebar entry — a chart-focused repository catalogue.
- Referrers tab + classifier on tag detail (PR #282), including the
  `CoreService` gRPC surface and the BFF→core optional-service wiring.

**What is genuinely missing — and is this spec:** the *deep per-chart detail*.
There is no way today to see a chart’s metadata or values without `helm pull`.

---

## 2. Architecture & data flow

A **“Chart” tab** on the existing tag-detail route
(`frontend/src/routes/_authenticated.repositories.$org.$repo_.tags.$tag.tsx`),
rendered only when the current tag’s `artifact_type === "helm"`. This mirrors
the Referrers tab added in #282 and reuses the tag data the page already loads
(`useTags` → `tagRow`, whose `artifact_type` field already exists on the FE
`Tag` type — no FE type change needed to gate the tab).

One BFF endpoint backs the tab:

```
GET /api/v1/repositories/{org}/{repo}/tags/{tag}/chart
```

**Response (200):**
```json
{
  "metadata": {
    "name": "myapp", "version": "1.2.3", "app_version": "2.0.0",
    "description": "…", "api_version": "v2", "type": "application",
    "kube_version": ">=1.24.0", "home": "https://…", "icon": "https://…",
    "deprecated": false,
    "keywords": ["web", "api"],
    "sources": ["https://github.com/…"],
    "maintainers": [{"name": "…", "email": "…", "url": "…"}],
    "dependencies": [{"name": "postgresql", "version": "12.x", "repository": "oci://…"}],
    "annotations": {"category": "Database"}
  },
  "values": "replicaCount: 1\nimage:\n  repository: nginx\n…",
  "values_truncated": false,
  "values_error": ""
}
```

`metadata` is `null` if the config blob is missing/unparseable (with a top-level
`metadata_error`); `values` is `""` with a non-empty `values_error` if the
content layer can’t be read. The two are independent — a malformed chart still
returns whatever half succeeded.

### Where the Helm logic lives — BFF, not core

Mirrors the referrers split and keeps `registry-core`’s gRPC a **thin, generic
read surface**:

1. **BFF** resolves the tag → manifest via `registry-metadata`
   `GetManifest` → `raw_json` (the same resolution the referrers/manifest routes
   already use). It parses `raw_json` locally to read:
   - `config.digest` + `config.mediaType`
   - the Helm **content layer** digest (the layer whose `mediaType` is
     `application/vnd.cncf.helm.chart.content.v1.tar+gzip`).
2. **Guard:** `config.mediaType == application/vnd.cncf.helm.config.v1+json`.
   Otherwise → `400 "not a Helm chart"`.
3. **BFF** calls a **new generic `CoreService.GetBlob`** gRPC twice:
   - config blob → parse `Chart.yaml` JSON → `metadata`.
   - content-layer blob → gunzip + untar → extract `*/values.yaml` → `values`.
4. BFF shapes the combined JSON and returns it.

`registry-core` already owns blob access (`Registry.GetBlob(ctx, tenantID,
digest, io.Writer)` streams from storage). The new gRPC is a thin size-capped
adapter over it — reusable by a future raw-manifest inspector or other artifact
viewers, so it is deliberately **not** Helm-specific.

```
FE Chart tab ──HTTP──> registry-management (BFF)
                           │  1. GetManifest(tag)  ──gRPC──> registry-metadata
                           │  2. parse raw_json (config + content-layer digests)
                           │  3. GetBlob(config.digest)  ──gRPC──> registry-core ──> storage
                           │  4. GetBlob(contentLayer.digest) ──gRPC──> registry-core ──> storage
                           │  5. parse Chart.yaml JSON + gunzip/untar values.yaml
                           └──JSON──> FE
```

---

## 3. Proto — `CoreService.GetBlob`

Add to `proto/core/v1/core.proto` (additive; regenerate committed stubs):

```proto
service CoreService {
  rpc ListReferrers(ListReferrersRequest) returns (ListReferrersResponse);
  // GetBlob returns the raw bytes of a blob by digest, capped at max_bytes.
  // Generic (not Helm-specific): the BFF uses it to fetch a Helm chart's
  // config + content-layer blobs, but any internal reader can use it.
  rpc GetBlob(GetBlobRequest) returns (GetBlobResponse);
}

// GetBlobRequest identifies a blob by tenant + digest. max_bytes caps the
// returned payload so a caller can't pull an arbitrarily large blob into a
// unary gRPC message; the server refuses (FailedPrecondition) once a blob
// exceeds it rather than truncating silently.
message GetBlobRequest {
  string tenant_id = 1;
  string digest    = 2;      // "sha256:<hex64>"
  int64  max_bytes = 3;      // hard ceiling; 0 = server default (see below)
}

message GetBlobResponse {
  bytes  data       = 1;
  int64  size       = 2;     // == len(data)
  string media_type = 3;     // reserved; empty today (storage doesn't persist it)
}
```

**Why unary + cap, not server-streaming:** Helm charts are small (KB–low MB).
A unary response with an explicit `max_bytes` ceiling is simpler than a
streaming API and sufficient. The BFF raises its core-client
`MaxCallRecvMsgSize` to 16 MB to fit the caps in §5. If a generic blob viewer
later needs arbitrary-size blobs, add a streaming `GetBlobStream` then — YAGNI
now.

**core gRPC handler** (`services/core/internal/handler/grpc_core.go`,
extends the existing `CoreHandler`):

- Validate `tenant_id` (required), `digest` (required, matches the shared
  `digestRE` `^sha256:[a-f0-9]{64}$`).
- Resolve the effective cap: `min(req.max_bytes || defaultBlobCap, hardBlobCap)`
  where `defaultBlobCap = 10 MiB` and `hardBlobCap = 16 MiB`.
- Stream `Registry.GetBlob` into a **cap-enforcing writer** (`limitedBuffer`)
  that returns a sentinel error once it would exceed the cap.
- Map errors: `service.ErrNotFound` → `codes.NotFound`; cap exceeded →
  `codes.FailedPrecondition "blob exceeds max_bytes"`; anything else →
  `codes.Internal` (logged with `slog.ErrorContext`, opaque to caller).
- Extend the handler’s thin interface seam (currently `referrerLister`) with a
  `blobGetter` method `GetBlob(ctx, tenantID, digest, w io.Writer) (int64, error)`
  so the handler stays unit-testable with a fake (no live storage). Combine the
  two into one `coreReader` interface, or add a second interface — keep the seam
  local to the handler.

---

## 4. BFF — `handleGetChart`

New file `services/management/internal/handler/chart.go` (own file so it doesn’t
collide with `handler.go`/`referrers.go`), following `referrers.go` verbatim for
structure:

- **Route:** `GET /repositories/{org}/{repo}/tags/{tag}/chart`, registered next
  to the referrers route in `handler.go`.
- **nil-core guard:** `if h.core == nil { writeError(404, "route disabled") }`
  — same optional-service gate as referrers, so the FE hides the tab cleanly
  when `CORE_GRPC_ADDR` is unset.
- **Validation:** `validateOrgName` / `validateRepoName` / `validateTagName`
  (CLAUDE.md §7 allowlists) before any gRPC call.
- **Repo + tag resolution:** `h.findRepo` → `h.meta.GetManifest` with the tag as
  `reference` (returns `Manifest{raw_json, ...}`). Use `GetManifest` (not
  `GetTag`) because we need `raw_json`.
- **Parse `raw_json`** (`services/management/internal/handler/chartparse.go` —
  pure functions, separately testable):
  - `parseManifestConfigAndLayer(raw []byte) (configDigest, configMediaType, contentDigest string, err error)`.
  - Reject non-Helm config mediaType → `400`.
- **Fetch + parse** (also in `chartparse.go`, pure where possible):
  - `parseChartMetadata(configJSON []byte) (ChartMetadata, error)` — unmarshal
    the Helm config JSON (Chart.yaml).
  - `extractValuesYAML(tgz []byte, cap int) (values string, truncated bool, err error)`
    — gunzip, walk tar, return the first entry matching `*/values.yaml` at chart
    root (single path segment before `values.yaml`), size-capped.
- **5s outgoing deadline** on each core call (`chartTimeout = 5 * time.Second`,
  matching §6 convention).
- **Partial tolerance:** build the response even if one half fails; set
  `metadata_error` / `values_error` accordingly; only 5xx if *both* the metadata
  path errors *and* it’s not a client-shaped error.

**Wire types** (snake_case JSON, matching the BFF contract):

```go
type ChartResponse struct {
    Metadata        *ChartMetadata `json:"metadata"`
    MetadataError   string         `json:"metadata_error,omitempty"`
    Values          string         `json:"values"`
    ValuesTruncated bool           `json:"values_truncated"`
    ValuesError     string         `json:"values_error,omitempty"`
}
type ChartMetadata struct {
    Name         string              `json:"name"`
    Version      string              `json:"version"`
    AppVersion   string              `json:"app_version,omitempty"`
    Description  string              `json:"description,omitempty"`
    APIVersion   string              `json:"api_version,omitempty"`
    Type         string              `json:"type,omitempty"`
    KubeVersion  string              `json:"kube_version,omitempty"`
    Home         string              `json:"home,omitempty"`
    Icon         string              `json:"icon,omitempty"`
    Deprecated   bool                `json:"deprecated,omitempty"`
    Keywords     []string            `json:"keywords,omitempty"`
    Sources      []string            `json:"sources,omitempty"`
    Maintainers  []ChartMaintainer   `json:"maintainers,omitempty"`
    Dependencies []ChartDependency   `json:"dependencies,omitempty"`
    Annotations  map[string]string   `json:"annotations,omitempty"`
}
type ChartMaintainer struct { Name, Email, URL string }   // json name/email/url, omitempty
type ChartDependency struct { Name, Version, Repository string } // json name/version/repository, omitempty
```

---

## 5. Safety caps (the risk lives here)

| Fetch | Cap | On exceed |
|---|---|---|
| Config blob (Chart.yaml) | 1 MiB | `metadata_error = "config blob too large"`, no metadata |
| Content layer (.tgz) `GetBlob max_bytes` | 10 MiB | `values_truncated = true`, skip extraction |
| Extracted `values.yaml` | 256 KiB | truncate to cap, `values_truncated = true` |
| Tar entries scanned | 2000 | stop; `values_error` if not found by then |
| BFF core-client `MaxCallRecvMsgSize` | 16 MiB | (fits the 10 MiB content cap) |

**Tar hardening** (in `extractValuesYAML`):
- Skip any entry whose cleaned path contains `..` or is absolute.
- Only match a path shaped `<single-segment>/values.yaml` (chart-root values,
  not a subchart’s `charts/foo/values.yaml`).
- Only read `tar.TypeReg` entries; ignore dirs/symlinks/devices.
- Bound total bytes read from the gzip stream to the content cap even if the
  archive lies about entry sizes (wrap the tar reader in an `io.LimitReader`).

---

## 6. Frontend

- **`frontend/src/lib/api/chart.ts`** — `useChart(org, repo, tag)` (react-query,
  `enabled` only when the tab is active), plus `ChartMetadata` / `ChartResponse`
  / `ChartMaintainer` / `ChartDependency` types. 404 → treated as
  “not enabled” (tab hidden), not an error toast (mirror `referrers.ts`).
- **`frontend/src/components/tags/chart-panel.tsx`** — `ChartPanel({org, repo, tag})`:
  - **Metadata card:** name + version + appVersion header, description, and a
    definition list (apiVersion, type, kubeVersion, home/icon/sources as links,
    deprecated pill, keywords as chips).
  - **Dependencies table** (name / version / repository) — omitted when empty.
  - **Maintainers list** (name + mailto/url) — omitted when empty.
  - **values.yaml block:** collapsible `<pre>` in mono, with a `CopyButton`
    (reuse existing) and a truncation banner when `values_truncated`. No
    external YAML highlighter — plain mono, consistent with existing raw-JSON
    rendering elsewhere.
  - Loading → skeletons; error (non-404) → `ErrorState` with retry; 404 →
    render nothing (tab is already gated, this is belt-and-suspenders).
- **Tag route** (`…tags.$tag.tsx`):
  - Add `"chart"` to `TAG_TAB_VALUES`.
  - `const isHelm = tagRow?.artifact_type === "helm"`.
  - Render `<TabsTrigger value="chart">Chart</TabsTrigger>` and the matching
    `<TabsContent>` only when `isHelm`. Keep the `TAG_TAB_VALUES` guard so a
    `?tab=chart` deep-link on a non-Helm tag falls back to the default tab.

---

## 7. Error handling summary

| Condition | Result |
|---|---|
| `CORE_GRPC_ADDR` unset (`h.core == nil`) | `404 "route disabled"` → FE hides tab |
| Invalid org/repo/tag path | `400` |
| Repo missing / cross-tenant | `404 "repository not found"` |
| Tag/manifest missing | `404 "tag not found"` |
| Config mediaType ≠ Helm | `400 "not a Helm chart"` |
| Config blob unparseable | `200` with `metadata: null` + `metadata_error` |
| Content layer missing/oversize/bad gzip | `200` with `values: ""` + `values_error` / `values_truncated` |
| core `GetBlob` `NotFound` | `404` (blob GC’d out from under the manifest) |
| core `GetBlob` other error | `500 "failed to read chart"` (logged) |

---

## 8. Testing

- **core** (`grpc_core_test.go`): `GetBlob` happy path (fake returns N bytes),
  `NotFound`, cap exceeded → `FailedPrecondition`, digest validation.
- **BFF** (`chart_test.go` + `chartparse_test.go`):
  - `parseManifestConfigAndLayer` — valid Helm manifest, non-Helm config → error,
    missing content layer.
  - `parseChartMetadata` — full Chart.yaml, minimal Chart.yaml, garbage → error.
  - `extractValuesYAML` — real gzip+tar fixture **built in-test** (`archive/tar`
    + `compress/gzip`) containing `mychart/values.yaml`; oversize → truncated;
    subchart-only values → not found; `..`/absolute path entry ignored.
  - `handleGetChart` — helm happy path (fake core returns config JSON + tgz
    bytes), not-helm `400`, nil-core `404`, oversize content → `values_truncated`,
    malformed Chart.yaml → `metadata_error` with values still present.
- **FE** (`chart-panel.test.tsx`): renders metadata + dependencies + values;
  truncation banner; empty deps/maintainers omitted; loading skeletons;
  non-404 error → ErrorState; tab gating (`artifact_type` non-helm → no Chart
  tab) tested at the route level or via the panel’s own guards.

---

## 9. Files

**Create**
- `services/management/internal/handler/chart.go` — `handleGetChart`.
- `services/management/internal/handler/chartparse.go` — pure parse/extract fns.
- `services/management/internal/handler/chart_test.go`, `chartparse_test.go`.
- `frontend/src/lib/api/chart.ts`.
- `frontend/src/components/tags/chart-panel.tsx`.
- `frontend/src/components/tags/__tests__/chart-panel.test.tsx`.

**Modify**
- `proto/core/v1/core.proto` (+ regenerated `proto/gen/go/core/v1/*`).
- `services/core/internal/handler/grpc_core.go` (+ `_test.go`) — `GetBlob` RPC +
  interface seam.
- `services/management/internal/server/server.go` — raise core-client
  `MaxCallRecvMsgSize` to 16 MiB (only if not already sufficient).
- `services/management/internal/handler/handler.go` — register the chart route.
- `frontend/src/routes/_authenticated.repositories.$org.$repo_.tags.$tag.tsx` —
  Chart tab (gated on `artifact_type === "helm"`).

**Docs/trackers** (close-out): `status.md`, `FE-STATUS.md`, `futures.md`
(FUT-022 → Helm detail shipped; note the generic `/artifacts` + values scope
that remains deferred), `docs/SERVICES.md` (BFF route table + core `GetBlob`).

---

## 10. Out of scope (deferred)

- Generic `/artifacts` mediaType-aware landing page (redundant with `/helm` +
  `artifact_type` chips under single-tenant).
- Richer referrer rendering (SBOM package tables, signature verification inline)
  — the other half of the original FUT-022; separate spec if picked up.
- Chart `templates/` rendering / `helm template` dry-run.
- Provenance (`.prov`) signature verification.
- Streaming `GetBlobStream` for arbitrarily-large blobs.
