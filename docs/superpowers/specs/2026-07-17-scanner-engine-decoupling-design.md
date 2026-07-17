# Scanner engine decoupling — thin wrapper sidecars

> **Status:** design approved 2026-07-17. Ready for implementation planning.
> **Origin:** parked idea after REM-019 P2 merged — see `memory/project-scanner-plugin-decoupling`.
> **Scope owner:** registry-scanner + infra/scanner-plugins.

## 1. Problem

Bumping a scanner engine (trivy `0.52 → 0.65 → 0.71`, grype schema bumps) currently
requires a **full `registry-scanner` image rebuild + redeploy**, because both engine
CLIs are baked into that image (`services/scanner/Dockerfile` stages
`FROM aquasec/trivy:0.71.2` and `FROM anchore/grype:v0.93.0` and `COPY`s the
binaries in). Engine bumps are frequent and CVE-driven, so this is a recurring
pain out of all proportion to "change a version number."

**Goal:** make engine *version bumps* possible without rebuilding the scanner
image. The *set* of engines (trivy, grype, clair, dev-stub) is stable — operator-
installable *new* engines at runtime is explicitly **not** a goal (see §8).

## 2. The reframe — three separable things are baked in

Grounded in `services/scanner/Dockerfile` + `services/scanner/internal/registry/registry.go`:

1. **Adapter shim** (`scanner-trivy-adapter`, `scanner-grype-adapter`) — stdlib-only
   JSON-RPC bridge, ~MBs, **ours**, rarely changes. Keep baked.
2. **Engine CLI** (`trivy`, `grype`) — ~50–100MB, **third-party**, frequent CVE-driven
   updates. **This is the pain.** Move out.
3. **Vuln DB** (~1GB) — already externalized to cache volumes; DB pre-warm fixed in
   REM-019 P2. Moves with the engine.

Discovery + hot-swap already exist: `registry.New()` globs `SCANNER_ADAPTER_DIR` for
`scanner-*`, SHA256-checksums each, and REM-011 P2 hot-swaps the active adapter via
`/settings/scanning`. None of that changes.

## 3. Decision

**Option A — sidecar the engines (network protocol, not `exec`)**, realized as
**A1 — thin HTTP wrapper sidecars.**

Decisions locked during brainstorming:
- **Update model:** bump-versions-only (not runtime-installable new engines). → Option A, not the OCI-pull install registry (Option B).
- **Topology:** co-located sidecars on the private `registry` network (like clair today), not shared standalone engine services.
- **Realization:** A1 wrapper sidecars, chosen over A2 (native `trivy server` — removes DB but not the client CLI, and grype has no equivalent) and A3 (engine pulls from registry-core by digest — cleaner future, adds auth surface for no gain when co-located).

The clair adapter (`infra/scanner-plugins/clair-adapter`) already proves the
network-to-engine pattern in-repo (embedded HTTP layer server on :9099, Clair runs
as its own co-located container). A1 generalizes that pattern to the CLI engines.

**A1 dissolves the "grype has no server mode" wrinkle** the parked notes flagged:
our own wrapper gives *both* engines an identical network API.

## 4. Architecture

```
┌─────────────────────── registry-scanner (baked, stable) ───────────────────────┐
│  orchestrator ── stages raw layer blobs → /scan-work/<scan-id>/blobs/<hex>       │
│       │ exec (unchanged JSON-RPC over stdin/stdout, SHA256-checksummed shim)     │
│       ▼                                                                          │
│  scanner-trivy-adapter  (SHIM — ours, tiny, stays baked)                         │
│    1. flatten staged blobs → rootfs at /scan-work/<scan-id>/rootfs  (reused)     │
│    2. POST http://trivy-engine:8085/scan {rootfs:"/scan-work/<id>/rootfs"}       │
│    3. translate returned engine JSON → contract findings  (reused)               │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                        shared volume   │   HTTP (private `registry` net)
                        /scan-work (ro) ▼
        ┌──────────────── trivy-engine sidecar (bump-per-CVE, independent image) ──┐
        │  scanner-engine-server  (WRAPPER — ours, ~120 lines stdlib)              │
        │    POST /scan {rootfs} → exec `trivy rootfs <rootfs> --skip-db-update`   │
        │                        → return raw trivy JSON                            │
        │  FROM aquasec/trivy:0.71.2   (the ONLY thing that changes on a bump)     │
        │  owns the DB cache volume + the REM-019 P2 pre-warm on entrypoint        │
        └───────────────────────────────────────────────────────────────────────────┘
```

### Components & owners

1. **The shim** (`infra/scanner-plugins/trivy-adapter`, `grype-adapter`) — *ours,
   stays baked in the scanner image.* Keeps every line it has today **except** the
   `exec.Command(<engine>, …)` call, which becomes an HTTP POST to the engine
   sidecar. Still does layer-flatten (`extractLayer`/`untarTo`) and findings-
   translation (`translateFindings`). Still SHA256-checksummed at boot (SEC-003
   trust root **unchanged** for the shim). Reads `TRIVY_ENGINE_URL` /
   `GRYPE_ENGINE_URL` from env; unset ⇒ misconfiguration ⇒ scan fails cleanly.

2. **The wrapper** (`infra/scanner-plugins/engine-server`, new) — *ours, one shared
   program.* A stdlib HTTP server:
   - `POST /scan {scan_id, rootfs}` → exec the engine CLI named by `ENGINE_CMD`
     with a per-engine arg template → return the engine's raw JSON on success, a
     structured error otherwise.
   - `GET /healthz` → execs `<engine> --version`; used by compose/K8s liveness and
     lets the shim distinguish "unreachable" from "scan errored."
   - Baked into both `trivy-engine` and `grype-engine` images, parameterized by env.
   - Carries clair-adapter's path-traversal guards verbatim: `rootfs` must resolve
     under the read-only shared mount.

3. **The engine sidecar images** (new Dockerfiles) — *thin, independently versioned.*
   `FROM aquasec/trivy:<pin>@sha256:…` (or `anchore/grype:<pin>@sha256:…`) as a
   stage, plus the wrapper binary. Owns the vuln-DB cache volume and the REM-019 P2
   DB pre-warm (moved out of the scanner entrypoint). Bumping the engine = change
   the one `FROM` pin + rebuild this small image.

**Why one shared wrapper, not two:** trivy and grype differ only in argv and the
`--version` parse. One `scanner-engine-server` with `ENGINE_CMD` + a per-engine arg
template keeps "exec a CLI, return stdout JSON" in exactly one testable place, and
makes a future 4th CLI engine a config line, not new code.

## 5. Data flow & shared volume

Today the shim flattens layers into `os.MkdirTemp("")` — local to the scanner
container, invisible to a sidecar. The change: stage/flatten under a **shared
volume** both containers mount.

- Scanner mounts `/scan-work` **read-write** (stages blobs + flattens rootfs).
- Each engine sidecar mounts `/scan-work` **read-only** (only reads the rootfs).
- Compose: named volume `scan_work` on both services. K8s: an `emptyDir` shared
  within the pod (co-located ⇒ free + ephemeral).
- Path convention `/scan-work/<scan-id>/rootfs`; identical mount path in both
  containers ⇒ the sidecar `exec trivy rootfs /scan-work/<scan-id>/rootfs` just works.
- Cleanup stays in the shim (`defer os.RemoveAll` of the per-scan dir under
  `/scan-work` after the engine responds).

**Chosen over** the clair-style "serve blobs over HTTP, engine flattens" because:
(a) keeps the already-tested flatten logic in the shim rather than duplicating it
into the wrapper; (b) rootfs bytes never cross the network; (c) the wrapper stays
trivial. Read-only mount neutralizes "sidecar writes back."

### HTTP request/response

```
POST /scan   { "scan_id": "...", "rootfs": "/scan-work/<id>/rootfs" }
200 → { "engine":"trivy", "version":"0.71.2", "raw": <engine's native JSON> }
5xx → { "error": "trivy exited 2: <stderr>" }
```

The shim runs its **existing** `translateFindings` on `raw` — finding shape, dedup,
and severity counts stay byte-for-byte identical. Zero downstream change.

## 6. Failure modes

The new distributed boundary introduces exactly one new class; it must be
observable, not silent.

| Situation | Today (`exec`) | After A1 | Surfaced as |
|---|---|---|---|
| Engine ran, real problem (exit ≠ 0) | scan `failed` w/ stderr | wrapper 5xx w/ stderr; shim propagates | `failed` + engine stderr (same as today) |
| Engine binary missing/corrupt | checksum/exec error | wrapper `/healthz` fails; image-digest pin is the guard | `failed`, engine-side |
| **Sidecar down / unreachable (NEW)** | impossible | shim HTTP call gets conn-refused/timeout | `failed` w/ distinct `engine_unreachable` reason |
| Sidecar slow / hung | `exec` hang → orchestrator timeout | shim HTTP client deadline (mirrors clair timeouts) | `failed` on deadline; doesn't pin a worker |

`engine_unreachable` distinguishes "your deploy is broken" from "this image
genuinely failed to scan." The shim tags it; it threads to `/settings/scanning` so
the active adapter shows **degraded (engine sidecar unreachable)**. Rides existing
rails — the orchestrator already models `failed`/`unknown` scan results (REM-019
touched exactly this). **No new scan-status enum** — a clearer error string + an
adapter-health readout.

**Startup ordering:** compose `depends_on` with the engine's `/healthz` healthcheck;
the shim tolerates a not-yet-ready sidecar by surfacing `engine_unreachable`
(retryable) rather than crashing. No hard boot coupling.

## 7. Trust-root shift & Dockerfile surgery

**Trust-root shift (deliberate).** Today the trust root for an engine is the SHA256
checksum of the `exec`'d binary (SEC-003 / PENTEST-019) plus the in-image guarantee
that the baked trivy came from the same build. Once trivy runs in a separate
container that in-image guarantee no longer covers it. Replacement:

- **Shim** → trust root **unchanged.** Still a baked, SHA256-checksummed binary.
  SEC-003 does not weaken.
- **Engine binary** → trust root becomes **image-digest pinning.** Engine-sidecar
  images pin the upstream by digest (`FROM aquasec/trivy:0.71.2@sha256:…`), and
  compose/Helm reference the built engine-sidecar image by digest too. Strictly
  stronger than a mutable tag.
- **No cosign here.** Cosign verification of engine images was the Option B
  dogfooding endgame — out of scope for this bump-versions-only design. Digest
  pinning is the trust root; cosign is a named future upgrade (§8).
- The wrapper's `/scan` endpoint has **no auth**, exactly like clair's :9099 layer
  server — binds only on the private `registry` network (no host port mapping) and
  only accepts a path-validated `rootfs` under the read-only shared mount.

**What leaves `services/scanner/Dockerfile`:**

```diff
- FROM aquasec/trivy:0.71.2 AS trivy          # entire stage gone
- FROM anchore/grype:v0.93.0 AS grype         # entire stage gone
- COPY --from=trivy /usr/local/bin/trivy ...  # engine binaries gone
- COPY --from=grype /grype ...
- ENV TRIVY_CACHE_DIR=/trivy-cache            # DB caches move to sidecars
- ENV GRYPE_DB_CACHE_DIR=/grype-cache
- VOLUME ["/trivy-cache", "/grype-cache"]
- # entrypoint.sh trivy/grype pre-warm blocks  → move to sidecar entrypoints
+ # scanner keeps: server, healthcheck, all 4 SHIM adapters, /scan-work mount
```

The scanner image gets smaller (loses two engine binaries + ~2GB DB volume
responsibility); its entrypoint simplifies (REM-019 P2 pre-warm relocates to the
engine sidecars where the DB now lives). `dev-stub` (no engine) and `clair` (already
a sidecar) are unaffected.

**New files:**
- `infra/scanner-plugins/engine-server/main.go` (+ `main_test.go`) — shared wrapper.
- `infra/scanner-plugins/engine-server/Dockerfile.trivy`, `Dockerfile.grype` — thin
  sidecar images (or one parameterized Dockerfile with an `ENGINE` build-arg —
  chosen to keep CI path-filters clean).
- `services/scanner/scripts/engine-entrypoint.sh` — DB pre-warm, moved from the
  scanner entrypoint.

**Compose + Helm:**
- `infra/docker-compose`: add `trivy-engine` + `grype-engine` services (private
  network, `/healthz` healthcheck, own DB cache volumes), add shared `scan_work`
  volume, set `TRIVY_ENGINE_URL` / `GRYPE_ENGINE_URL` on the scanner, drop the
  scanner's engine-DB volumes.
- Helm scanner chart: sidecar containers (or a paired Deployment) + the shared
  `emptyDir`.

## 8. Scope, phasing, test plan

### Phasing — trivy first (default active adapter, best proof)

- **Phase 1 — trivy-engine (the pattern).** Wrapper program + `trivy-engine` image +
  repoint the trivy shim to HTTP + shared `scan_work` volume + compose wiring + move
  trivy DB pre-warm to the sidecar + `engine_unreachable` surfacing +
  `/settings/scanning` degraded readout. Live-verify a real scan through the sidecar.
  Carries all the net-new code (wrapper, shared volume, failure model, Helm shape).
- **Phase 2 — grype-engine (the repeat).** Reuse the identical wrapper
  (`ENGINE_CMD=grype` + arg template + `--version` parse), add `grype-engine` image +
  compose service, repoint the grype shim, move grype DB pre-warm. Mechanical once
  Phase 1 lands.

Each phase is its own gated PR with tracker hygiene (status.md row, status-tracker.md
move).

### Out of scope (named, not silently dropped) → each gets a `futures.md` line

- Cosign verification of engine images (Option B dogfooding endgame).
- A3 registry-pull model (engine pulls from registry-core by digest).
- Operator-installable *new* engines at runtime (Option B install registry + a
  persisted `scanner_adapters` table).
- Clair (already a sidecar).

### Test plan

- **Wrapper unit tests** (`engine-server/main_test.go`): `/scan` happy path with a
  fake engine script echoing canned JSON; engine-exits-nonzero → 5xx with stderr;
  rootfs path outside the mount → rejected (traversal guard); `/healthz` maps engine
  `--version` success/failure. A shell stub as `ENGINE_CMD` keeps it hermetic — no
  real trivy needed.
- **Shim tests** (existing trivy-adapter harness): the HTTP-call branch against an
  `httptest` server standing in for the sidecar — asserts flatten→POST→translate, and
  that conn-refused/timeout yields the distinct `engine_unreachable` error. Existing
  `translateFindings` tests untouched (that logic didn't move).
- **Image build check:** the engine Dockerfiles build in CI (new
  `ci-scanner-engines.yml`, path-filtered on `infra/scanner-plugins/engine-server/**`,
  modeled on the existing per-service workflows).
- **Live verification** (per rebuild-full-vertical rule): rebuild scanner +
  trivy-engine, `docker push` a real image, confirm the scan runs through the sidecar
  with identical findings to a pre-change baseline; then kill the trivy-engine
  container and confirm the scan reports `engine_unreachable` and `/settings/scanning`
  shows the adapter degraded — proving the new failure mode is observable.

### Non-goals reaffirmed

No change to the JSON-RPC plugin contract, the orchestrator, scan-status enums, the
FE scan UI, or end-user behavior. This is a packaging/deployment refactor whose only
visible deliverable is "bump an engine without rebuilding the scanner." End users
scan images exactly as before; the operator swaps one pinned engine-image digest
instead of rebuilding registry-scanner.
