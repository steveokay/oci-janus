# ADMISSION.md — Image Admission Policies

> **Audience:** operators configuring what a repository will and won't
> admit on push/pull; developers touching the `services/core` OCI
> handlers or the metadata repo-flag columns behind them.
>
> **Scope:** the three admission *gates* that `registry-core` enforces
> on the OCI distribution layer — **CVSS-gated admission** (FUT-021),
> **signed-image admission** (futures.md Tier 1 #3), and **tag
> immutability** (futures.md Tier 1 #2). This doc covers *how a push or
> pull is blocked*. The machinery that *produces* the inputs those gates
> read lives elsewhere: see [`SIGNING.md`](./SIGNING.md) for how
> signatures + trusted keys get created, and [`SCANNER.md`](./SCANNER.md)
> for how scan results + severity counts get produced.

---

## TL;DR

- Three independent gates compose on a repository. Each is off by
  default and flipped on via a repo column (all set through
  `PATCH /api/v1/repositories/{org}/{repo}` on the management BFF).
- **Tag immutability** gates the **push** side (`PutManifest`) — a tag
  re-point is rejected with `400 MANIFEST_INVALID`.
- **Signed-image admission** and **CVSS-gated admission** gate the
  **pull** side (`GetManifest` / `HeadManifest`) — an unsigned or
  too-vulnerable manifest is rejected with `403 DENIED`.
- Every gate **fails OPEN on infrastructure blips** (metadata/signer/
  scanner unreachable, or no scan result yet) and **fails CLOSED only on
  a definitive policy violation**. A transient outage never cascades
  into a registry-wide push/pull outage.
- All three sit on the OCI distribution code path, so they apply
  identically to container images and Helm charts (anything that goes
  through `PutManifest` / `GetManifest`).

---

## 1. The three gates at a glance

| Gate | Side | Handler | Trigger column | Denied status | Sentinel error |
|---|---|---|---|---|---|
| Tag immutability | **push** | `PutManifest` → `checkTagImmutable` | `repositories.immutable_tags` OR `tags.immutable` | `400 MANIFEST_INVALID` | `ErrTagImmutable` |
| Signed-image admission | **pull** | `GetManifest`/`HeadManifest` → `checkSignatureAdmission` | `repositories.require_signature` (+ `repository_trusted_keys`) | `403 DENIED` | `ErrSignatureRequired` |
| CVSS-gated admission | **pull** | `GetManifest`/`HeadManifest` → `checkCVSSAdmission` | `repositories.max_cvss_score` | `403 DENIED` | `ErrCVSSThresholdExceeded` |

All three live in `services/core/internal/service/registry.go`; the
HTTP status mapping is in `services/core/internal/handler/http.go`; the
sentinel errors in `services/core/internal/service/errors.go`.

> **Not covered here:** the **pull-time quarantine** gate (`451
> Unavailable For Legal Reasons`, FE-API-050). That is a *different*
> scanner-fed mechanism: a per-tenant `scan_policies.block_on_severity`
> rule causes the scanner to set `manifest.quarantined` after a scan,
> and `GetManifest` refuses quarantined manifests with `451`. See
> [`SCANNER.md` §5](./SCANNER.md#5-scan-policies-compliance-reports-fe-api-018-fe-api-019).
> CVSS admission (this doc) is a *pull-time threshold check against the
> stored scan result* driven by the repo's `max_cvss_score` column, and
> denies with `403` — the two are orthogonal and can both be on at once.

---

## 2. Tag immutability (push gate)

**What triggers it.** A `PutManifest` where the reference is a **tag**
(not a digest) that already exists AND points at a *different* digest
than the incoming push. Digest-addressed pushes are content-addressable
and can never move a tag, so they skip the check entirely.

**Two layers** (migration `00014_tag_immutability.sql`):

- `repositories.immutable_tags BOOLEAN` — repo-wide "no tag re-pushes".
- `tags.immutable BOOLEAN` — per-tag pin; freezes one tag even on an
  otherwise-mutable repo.

`checkTagImmutable` evaluates the per-tag pin **first** (it wins
regardless of the repo flag), then falls back to the repo-wide flag.

**Allowed even when a flag is on:**

- **New tag** — the tag doesn't exist yet. Immutability gates *re-writes*,
  not initial creation.
- **Idempotent re-push** — same tag, same digest. Not an operator-visible
  "move", so it's a no-op, not a rejection.

**Enforced before the metadata write.** The check runs before
`metadata.PutManifest` so a rejected push leaves no manifest row behind
— the rejection is legible end-to-end.

**Denial.** `ErrTagImmutable` → `400 MANIFEST_INVALID` (OCI Distribution
Spec §4.2.2), body `"tag <name> is immutable; ..."`.

**Decision flow:**

```
PutManifest(reference)
  reference is a digest?           → yes → admit (can't move a tag)
  tag doesn't exist?               → yes → admit (new tag)
  incoming digest == current?      → yes → admit (idempotent re-push)
  tags.immutable == true?          → yes → REJECT 400 MANIFEST_INVALID
  repositories.immutable_tags==true→ yes → REJECT 400 MANIFEST_INVALID
  else                             → admit
GetTag / GetRepository RPC error   → admit (fail-OPEN, warn log)
```

---

## 3. Signed-image admission (pull gate)

Full lifecycle — how signatures and trusted keys are produced, the
operator workflows (docker + Helm), and the two-phase design — is
documented in [`SIGNING.md` §8](./SIGNING.md#8-signed-image-admission-futuresmd-tier-1-3).
This section covers only the *enforcement* in `registry-core`.

**What triggers it.** `GetManifest` / `HeadManifest` on a repo with
`repositories.require_signature = TRUE` (migration
`00015_repository_require_signature.sql`). The gate runs **after** the
manifest fetch so a genuine 404 stays a 404 rather than leaking
"signature required" for a non-existent digest.

**Phase 1 — any signature passes.** `checkSignatureAdmission` calls
`signer.ListSignatures(manifest_digest)`; zero rows → reject.

**Phase 2 — per-repo trusted-key allowlist** (`repository_trusted_keys`,
migration `00016`). When the repo has a non-empty allowlist, the
recorded signature `key_id`s are intersected with the approved set; an
empty intersection rejects. An **empty allowlist falls back to Phase 1**
("any signature passes") by design, so an operator can flip
`require_signature` on first and pin keys incrementally.

**Fail-OPEN scenarios (warn/allow, never reject):**

1. `GetRepository` RPC error (metadata blip).
2. `require_signature` is false (default).
3. Signer not wired — `SIGNER_GRPC_ADDR` unset, so `r.signer == nil`
   (dev-stack convenience; production always sets it).
4. `ListSignatures` RPC error (signer blip).
5. `ListRepositoryTrustedKeys` RPC error (metadata blip on the Phase-2 read).

**Denial.** `ErrSignatureRequired` → `403 DENIED`, body `"repository
requires a signed manifest; sign the image or turn require_signature
off"`.

**Decision flow:**

```
GetManifest → (manifest exists)
  require_signature == false?      → admit
  signer client nil?               → admit (fail-OPEN, warn)
  ListSignatures error?            → admit (fail-OPEN, warn)
  zero signatures?                 → REJECT 403 DENIED
  trusted-keys list empty?         → admit (Phase 1 fallback)
  any sig key_id in allowlist?     → admit
  else                             → REJECT 403 DENIED
```

---

## 4. CVSS-gated admission (pull gate — FUT-021)

This is the gate that closes the scanner → admission loop: a manifest
whose scan found vulnerabilities at/above a configured severity is
blocked at pull time. It reads the scan result produced by the pipeline
in [`SCANNER.md`](./SCANNER.md); it does **not** run a scan itself.

**Configuration.** `repositories.max_cvss_score INTEGER` (migration
`00019_repositories_max_cvss.sql`):

- **NULL (default)** — no gate; pull path behaves as pre-FUT-021.
- **0–100** — activates the gate at that threshold. A DB `CHECK`
  constraint (`max_cvss_range`) enforces the 0–100 range at the storage
  layer as defence-in-depth alongside the handler-side validation.

Set it via `PATCH /api/v1/repositories/{org}/{repo}` with
`{"max_cvss_score": 70}` (set) or `{"max_cvss_score": null}` (clear).
The BFF is a three-state field: key absent → leave unchanged; JSON null
→ clear; integer → set (validated `[0,100]`, else `400`). The flip is
plumbed through `metadata.UpdateRepositoryCVSSPolicy` and emits a
`repo.cvss_policy.changed` audit event.

**How the scan result feeds the gate.** `checkCVSSAdmission` calls
`metadata.GetScanResult(tenant, repo, manifest_digest)` and derives a
"top CVSS" integer from the result's `severity_counts` map. The scanner's
`plugin.Finding` shape does **not yet carry a numeric CVSS score**, so v1
derives top CVSS from the highest populated severity band using fixed
v3.1 band midpoints (`topCVSSFromSeverity`):

| Highest band present | Derived top CVSS |
|---|---|
| CRITICAL | 100 |
| HIGH | 89 |
| MEDIUM | 69 |
| LOW | 39 |
| none (clean scan) | 0 |

Rejection is **strict `>`** (not `>=`): `top_cvss > threshold` denies.
The midpoints sit *below* the band ceilings so a threshold pinned at a
band value *admits* that band. Practical thresholds:

| `max_cvss_score` | Blocks |
|---|---|
| 100 | nothing (opt-in default posture) |
| 89 | CRITICAL only |
| 69 | HIGH + CRITICAL |
| 39 | MEDIUM + HIGH + CRITICAL |

> **Caveat — carried by the code, not invented here:** because top CVSS
> is derived from severity *bands*, not raw per-CVE scores, the gate is
> coarser than a true numeric CVSS check. Reading the raw
> `plugin.Finding.CVSS` score is an explicit **Phase 2 follow-up** noted
> in `checkCVSSAdmission`; once findings carry the numeric score the
> function switches to reading it directly and this band table goes away.

**Fail-OPEN scenarios (allow, log, never reject):**

1. `GetRepository` RPC error → warn + allow.
2. `max_cvss_score` is NULL → allow (no gate; scan lookup skipped).
3. `GetScanResult` returns `NotFound` → info + allow. This is the
   first-pull case: the manifest was just pushed and the scanner hasn't
   finished yet, so operators aren't blocked on scanner queue depth.
4. `GetScanResult` other RPC error (scanner/metadata blip) → warn + allow.

> The code comment notes a fail-CLOSED-on-unreachable env toggle as a
> possible follow-up; **as written today the scanner-unreachable path is
> fail-OPEN**, matching the signature gate. There is no env var to flip
> it closed in the current code.

**Denial.** `ErrCVSSThresholdExceeded`, wrapped with numeric context
(`"top CVSS 100 exceeds threshold 70"`) → `403 DENIED`, body
`"repository CVSS admission policy: <wrapped message>"`. The numeric
detail is passed through verbatim so CI tooling can parse it and decide
waive / patch / rebuild without a second call.

**Decision flow:**

```
GetManifest → (manifest exists)
  GetRepository error?             → admit (fail-OPEN, warn)
  max_cvss_score == NULL?          → admit (no gate)
  GetScanResult NotFound?          → admit (fail-OPEN, info — no scan yet)
  GetScanResult error?             → admit (fail-OPEN, warn)
  topCVSS(severity_counts) > thr?  → REJECT 403 DENIED
  else                             → admit
```

---

## 5. Composite: what must be true to be admitted

The gates are independent and compose. A push and a pull traverse
different gates.

**Push (`PutManifest`) is admitted when:**

- the tag is new, OR addressed by digest, OR the re-push is the same
  digest, OR neither `tags.immutable` nor `repositories.immutable_tags`
  is set on a moving tag.

**Pull (`GetManifest` / `HeadManifest`) is admitted when ALL hold:**

1. The manifest exists (else `404 MANIFEST_UNKNOWN`).
2. **Signature gate** passes — `require_signature=false`, OR a recorded
   signature exists (and matches the trusted-key allowlist if non-empty),
   OR a fail-OPEN blip.
3. **CVSS gate** passes — `max_cvss_score` NULL, OR derived top CVSS ≤
   threshold, OR a fail-OPEN blip (including "no scan yet").
4. The manifest is not **quarantined** (else `451`; separate mechanism,
   see [`SCANNER.md`](./SCANNER.md)).

**Ordering matters for the error surfaced.** In `GetManifest` the
signature check runs **before** the CVSS check, so a repo requiring both
signed *and* scan-clean images surfaces the signature error first (no
signature at all is treated as the more structural red flag than a HIGH
finding). The quarantine `451` check runs after both.

The gates are orthogonal in configuration too — `signed + mutable` and
`unsigned + immutable` are both valid combinations, as are any mix of
signature/CVSS/immutability flags.

---

## 6. File reference

| File | Role |
|---|---|
| `services/core/internal/service/registry.go` | `checkTagImmutable` (push), `checkSignatureAdmission` + `checkCVSSAdmission` (pull), `topCVSSFromSeverity` band mapping |
| `services/core/internal/service/errors.go` | `ErrTagImmutable`, `ErrSignatureRequired`, `ErrCVSSThresholdExceeded` sentinels |
| `services/core/internal/service/cvss_admission_test.go` | Unit tests pinning the six CVSS invariants + band table |
| `services/core/internal/handler/http.go` | Error → OCI status mapping (`400 MANIFEST_INVALID` / `403 DENIED` / `451`) |
| `services/core/internal/config/config.go` | `SIGNER_GRPC_ADDR` — unset ⇒ signature gate fails OPEN |
| `services/metadata/migrations/00014_tag_immutability.sql` | `repositories.immutable_tags` + `tags.immutable` columns |
| `services/metadata/migrations/00015_repository_require_signature.sql` | `repositories.require_signature` column |
| `services/metadata/migrations/00016_repository_trusted_keys.sql` | `repository_trusted_keys` allowlist table (Phase 2) |
| `services/metadata/migrations/00019_repositories_max_cvss.sql` | `repositories.max_cvss_score` column + `max_cvss_range` CHECK |
| `proto/metadata/v1/metadata.proto` | `Repository.max_cvss_score`; `UpdateRepositoryCVSSPolicy` / `UpdateRepositorySignaturePolicy` RPCs; `RepositoryTrustedKey` |
| `services/management/internal/handler/handler.go` | BFF `PATCH /repositories/{org}/{repo}` — three-state `max_cvss_score` + `require_signature` + immutability plumbing |

---

## 7. Related docs & open work

- [`SIGNING.md`](./SIGNING.md) — signature + trusted-key production; the
  two-phase signed-image admission design and operator workflows.
- [`SCANNER.md`](./SCANNER.md) — scan-result production; the separate
  `block_on_severity` quarantine (`451`) gate.
- **CVSS Phase 2** — read the raw `plugin.Finding.CVSS` numeric score
  instead of deriving from severity bands (tracked in the
  `checkCVSSAdmission` doc comment).
- **Fail-CLOSED CVSS mode** — an env toggle to reject pulls when the
  scanner is unreachable is floated in the code comment but **not
  implemented today**; the current posture is fail-OPEN.

---

> **Source of truth is the code.** The gates live in
> `services/core/internal/service/registry.go` and the repo columns in
> the metadata migrations. If this file and the code disagree, the file
> is the bug.
