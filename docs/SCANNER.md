# SCANNER.md — Vulnerability Scanner Plugin Reference

> **Audience:** operators choosing or swapping a vulnerability scanner;
> developers writing a new adapter (e.g. Grype, customer-supplied
> engine); reviewers asking "how does `registry-scanner` actually do its
> job."
>
> **Status:** REM-011 + REM-014 — four adapters shipped (dev-stub,
> Trivy, Grype, Clair). All work end-to-end against `docker compose
> --profile scanner up -d`. The admin UI for adapter selection
> (`/admin/scanner`) shipped in REM-011 Phase 2 (FE-API-044..047).
> Per-tenant scan policies
> (FE-API-018) + compliance reports (FE-API-019) ride on top — the
> scanner respects `block_on_severity` rules and renders SPDX SBOMs +
> PDF reports per scan.

---

## 1. Architecture in one paragraph

`registry-scanner` does not contain any CVE-detection logic of its own.
It is an orchestrator: it consumes RabbitMQ events (`push.completed` or
`scan.queued`), fetches the image layers from `registry-storage`, stages
them into a temp directory, and then invokes an **adapter binary** as a
subprocess. The adapter is whatever satisfies the JSON-RPC contract
defined in [§3 below](#3-the-jsonrpc-plugin-contract). One adapter is
active at a time, selected via `SCANNER_PLUGIN_PATH`. Swapping scanners
— Trivy → Grype → customer-supplied — is one env-var change plus a
restart.

This is CLAUDE.md decision #5 ("external-process JSON-RPC, no Go
plugins") materialized.

---

## 2. Adapters shipped today

All four ship inside the `registry-scanner` Docker image; pick one by
setting `SCANNER_PLUGIN_PATH` before bringing the profile up. The
`/admin/scanner` dashboard route renders the same selector backed by
the live `SCANNER_PLUGIN_CHOICES` env var so an operator can swap
adapters without redeploying.

| Adapter | Path inside image | Real CVE detection? | When to use |
|---|---|---|---|
| `dev-stub` | `/usr/local/bin/scanner-dev-stub` | **No** — returns 4 hardcoded findings (1 CRITICAL / 1 HIGH / 1 MEDIUM / 1 LOW) regardless of input | Local UI work, demos, CI smoke tests. Lets you exercise the full trigger → pending → complete → findings flow without waiting on a vuln DB download. |
| `trivy-adapter` | `/usr/local/bin/scanner-trivy-adapter` | **Yes** — translates the JSON-RPC request into a `trivy rootfs` invocation against the staged layers | Real vulnerability scanning. First scan is slower (Trivy DB download), subsequent scans are fast. Default in dev compose when `--profile scanner` is selected without an explicit override. |
| `grype-adapter` | `/usr/local/bin/scanner-grype-adapter` | **Yes** — runs `grype dir:<staged-layers>` and translates Grype JSON to the platform's finding shape | Alternative to Trivy. Same input/output contract; useful when Grype's vuln DB matches your downstream tooling, or for cross-adapter comparison runs. |
| `clair-adapter` | `/usr/local/bin/scanner-clair-adapter` | **Yes** — embeds an HTTP layer-server bridging Clair v4's pull-style API to the platform's stage-then-scan contract (REM-014) | Use when your existing Clair deployment is the source of truth for vulnerability data. Needs `--profile clair` so the `clair` + `clair-db` containers come up; configured via `CLAIR_*` env vars (allowlisted by the subprocess env filter). |

Source: `infra/scanner-plugins/dev-stub/main.go`,
`infra/scanner-plugins/trivy-adapter/main.go`,
`infra/scanner-plugins/grype-adapter/main.go`,
`infra/scanner-plugins/clair-adapter/main.go`.

### Zero-config dev

```sh
docker compose --profile scanner up -d registry-scanner
```

That's it. The image's `ENV SCANNER_PLUGIN_PATH` defaults to
`scanner-dev-stub`, and the entrypoint auto-computes the
`SCANNER_PLUGIN_CHECKSUM` against the baked binary so neither env var
needs to be set explicitly.

### Switch to real Trivy

```sh
SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-trivy-adapter \
  docker compose --profile scanner up -d --force-recreate registry-scanner
```

The entrypoint detects the swap, re-derives the checksum against the
new binary, and starts the service. No restart of any other registry
service is required.

> **Note (Windows / Git-Bash):** prefix with `MSYS_NO_PATHCONV=1` so the
> shell doesn't mangle the absolute path into a Windows path.

### Production override (out-of-band checksum)

In dev the entrypoint auto-fills the checksum from the binary in the
image. In production you should pin it explicitly so a supply-chain
attack on the image build is caught:

```yaml
environment:
  SCANNER_PLUGIN_PATH: /usr/local/bin/scanner-trivy-adapter
  SCANNER_PLUGIN_CHECKSUM: 8961abbcbc67d6d9e15589abea99f55b4a83dff0af29d38e5d83005595526789
```

The service refuses to start if `sha256sum(SCANNER_PLUGIN_PATH) !=
SCANNER_PLUGIN_CHECKSUM` (`services/scanner/internal/plugin/process.go`).

---

## 3. The JSON-RPC plugin contract

Every adapter is a subprocess that satisfies a one-shot, newline-
delimited JSON-RPC request/response loop:

```
                          stdin                          stdout
orchestrator ─────── rpcRequest ───────► adapter ────► rpcResponse ───►
                       (1 request)                       (1 response,
                                                          then exit)
```

The contract is defined in
`services/scanner/internal/plugin/process.go` and the data types in
`libs/scanner/plugin/plugin.go`. Both adapters this repo ships satisfy
it; if you're writing a new one, you only need to match the wire
shapes below.

### Request (stdin)

```json
{
  "id": "<opaque request id, echo back as-is>",
  "method": "scan",
  "params": {
    "tenant_id": "<UUID>",
    "manifest_digest": "sha256:<hex>",
    "layers": [
      {"Digest": "sha256:<hex>", "MediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "Size": 12345}
    ],
    "image_path": "/tmp/registry-scan-XXXXXX"
  }
}
```

- `image_path` is a host-local directory the orchestrator has
  pre-populated with one file per layer, named by the layer digest's
  hex part (no `sha256:` prefix). Read files from there; you never need
  storage credentials.
- `layers` is the manifest's layer descriptor list in order. Use this
  ordering when flattening into a rootfs.

### Response (stdout)

Success:

```json
{
  "id": "<the same id from the request>",
  "result": {
    "scanner_name": "trivy",
    "scanner_version": "0.52.0",
    "findings": [
      {
        "CVE": "CVE-2024-1234",
        "Severity": "CRITICAL",
        "Package": "openssl",
        "Version": "3.0.7",
        "FixedIn": "3.0.13",
        "Description": "...",
        "References": ["https://nvd.nist.gov/vuln/detail/CVE-2024-1234"]
      }
    ],
    "severity_counts": {"CRITICAL": 1, "HIGH": 0, "MEDIUM": 0, "LOW": 0}
  }
}
```

Failure:

```json
{"id": "<request id>", "error": "short error message"}
```

The `result` field key names are case-sensitive and MUST match exactly.
`scanner_name` and `scanner_version` are echoed into the dashboard so
operators can see which engine produced the findings.

### Subprocess environment

The orchestrator strips host secrets before invoking your adapter. Only
these variables are forwarded:

- `PATH`, `HOME`, `TMPDIR`, `TMP`, `TEMP`
- `USER`, `USERNAME`
- `XDG_CACHE_HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`
- Anything with the `TRIVY_` or `GRYPE_` prefix

If your adapter needs additional config, namespace it under one of
those prefixes (e.g. `TRIVY_CACHE_DIR`, `GRYPE_DB_PATH`).

### Resource caps

- `stdout` is capped at **10 MiB** by the orchestrator
  (`io.LimitReader`). If your adapter emits more, the response will be
  truncated and unmarshalling will fail.
- Per-scan timeout is `SCANNER_JOB_TIMEOUT_SECS` (default 600s). The
  orchestrator sends `SIGKILL` via `exec.CommandContext` when it
  expires.

---

## 4. Writing a new adapter

The dev-stub is intentionally small — read
[`infra/scanner-plugins/dev-stub/main.go`](../infra/scanner-plugins/dev-stub/main.go)
first; it's ~150 LOC and demonstrates the full contract with no
external CVE engine to distract from the wire shape.

The Trivy adapter is the realistic template if you're wrapping a real
engine — read
[`infra/scanner-plugins/trivy-adapter/main.go`](../infra/scanner-plugins/trivy-adapter/main.go).

Quick checklist when adding `infra/scanner-plugins/grype-adapter/` (or
your own):

1. New directory with `go.mod` (separate module so it stays out of the
   workspace; the build is reproducible from stdlib only).
2. `main.go` implementing the JSON-RPC loop above.
3. Add a build step to `services/scanner/Dockerfile` (mirror the
   `WORKDIR /build/infra/scanner-plugins/trivy-adapter` block) and a
   `COPY --from=builder` line into the final image.
4. Operators point `SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-<your-adapter>`
   and the entrypoint takes care of the checksum.

No changes to the gRPC API, no migrations, no proto regen needed —
adapter swap is purely an env-var flip.

---

## 5. Scan policies + compliance reports (FE-API-018 / FE-API-019)

The adapter contract is artifact-agnostic — it scans whatever layers
are staged. Two pieces of operator-facing policy sit *on top* of the
scan results to decide what to do with them:

**Per-tenant scan policies (`scan_policies` table).** A repo (or its
parent org) can declare a `block_on_severity` rule (`critical`, `high`,
`medium`, `low`, or `none`). When the scanner finishes, if any finding
meets-or-exceeds the configured severity threshold, the scanner emits
`manifest.quarantined` via the metadata RPC — services/core then fails
pulls of that manifest with `451 Unavailable For Legal Reasons` and an
explanatory body. The policy is configured via the dashboard's
per-repo Settings tab (FE-API-049) or via the management BFF directly:

```
GET    /api/v1/repositories/{org}/{repo}/scan-policy
PUT    /api/v1/repositories/{org}/{repo}/scan-policy   {"block_on_severity":"high"}
DELETE /api/v1/repositories/{org}/{repo}/scan-policy   # falls back to org-level / off
```

Org-level policies cascade down — a repo with no policy inherits its
org's setting. Org policies in turn inherit a workspace-wide default
(off in dev, recommended `high` in prod). `none` at any layer disables
the gate without changing the scan itself; the scan still runs and
the dashboard still shows findings.

**Compliance reports (`compliance_reports` table).** On every
completed scan the scanner writes an SPDX JSON 2.3 SBOM and a
plain-text PDF report and persists them under
`/var/lib/registry-scanner/reports/<tenant>/<repo>/<manifest>.{json,pdf}`.
The async worker uses `FOR UPDATE SKIP LOCKED` to safely claim jobs
across multiple replicas. Reports are served via the management BFF:

```
GET /api/v1/repositories/{org}/{repo}/tags/{tag}/sbom            # SPDX JSON
GET /api/v1/repositories/{org}/{repo}/tags/{tag}/compliance.pdf  # PDF
```

The PDF renderer is hand-rolled (no third-party PDF library — kept the
dependency footprint shallow) and emits one section per finding with
package name, version, CVE id, severity, and remediation hint.

---

## 6. End-to-end verification

The acceptance criteria for REM-011 are:

1. `docker compose --profile scanner up -d` starts `registry-scanner`
   with zero operator config and the dev-stub fires successfully.
2. `POST /tags/{tag}/scan` produces a `scan_results` row within ~30s.
3. Auto-scan via `push.completed` produces a row within ~30s of a
   `docker push`.
4. Swap-plugin smoke test passes — replace `SCANNER_PLUGIN_PATH` +
   checksum, restart, scans still land.

Reproduce (1) and (2) with curl after `docker compose --profile
scanner up -d registry-scanner`:

```sh
TOKEN=$(curl -sS -X POST http://localhost:8080/api/v1/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"Admin1234!dev","tenant_id":"98dbe36b-ef28-4903-b25c-bff1b2921c9e"}' \
  | python -c "import sys,json; print(json.load(sys.stdin)['token'])")

# trigger
curl -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8091/api/v1/repositories/dev/alpine/tags/latest/scan

# poll
curl -H "Authorization: Bearer $TOKEN" \
  http://localhost:8091/api/v1/repositories/dev/alpine/tags/latest/scan
```

You should see `status: complete` within ~10s on the dev-stub adapter
(or ~30-90s on Trivy with a cold cache, depending on image size +
network).

---

## 7. Known limitations + follow-ups

These are not blockers for REM-011 Phase 1 but should be tracked when
extending the surface:

- **Trivy adapter layer flatten ignores whiteouts.** A package installed
  in layer N and removed in layer N+1 is still reported. Documented
  overcount; a correct overlayfs replay is a follow-up.
- **No multi-platform manifest handling in the adapter.** The
  orchestrator resolves a single manifest before invoking the adapter,
  so this only matters if an adapter wants to scan all platforms in an
  image-index. Out of scope today.
- **`pull.image` events aren't emitted** by the audit consumer, so
  `?metric=pulls` on the analytics endpoint is flat zero. Independent
  gap, tracked separately.
- **Adapter-swap UI shipped** (REM-011 Phase 2, FE-API-044..047). The
  `/admin/scanner` (Settings › Scanning) page lets a platform admin pick
  from the pre-installed adapters (backed by `SCANNER_PLUGIN_CHOICES`, see
  §2) and trigger a test scan without redeploying. The binaries still must
  be baked into the image — the UI never uploads executable code. An
  env-var change + restart remains the equivalent CLI path.

---

> **Source of truth:** the wire contract lives in code, not docs. If
> you change `process.go` or `libs/scanner/plugin/plugin.go`, update the
> "Request"/"Response" sections of this file in the same commit.
