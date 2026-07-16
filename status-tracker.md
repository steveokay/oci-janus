# status-tracker.md — Open Remediation + Hardening Work

> **What this file is for:** the curated set of currently-open
> remediation (`REM-NNN`) and security (`SEC-NNN` / `PENTEST-NNN`)
> items, plus partial / blocked surfaces. **Lean by design.**
>
> **Workflow:**
> 1. New item surfaces → add a short entry here (rationale, scope, link to branch / PR when in flight).
> 2. Work happens on a feature branch as usual.
> 3. When the work is **complete** (merged + verified): **remove the entry from this file** and **append a resolution note to [`status.md`](status.md)** (the completed-work log). One entry per item; PR / commit hash / date.
> 4. This file stays short. [`status.md`](status.md) accumulates the audit trail.
>
> **Forward-looking backlog:** see [`futures.md`](futures.md) for
> prioritised work that hasn't started yet (Tier 1 / 2 / 3 items
> without active branches).
>
> **Security disclosures:** see [`security.md`](security.md) — the
> full per-CVE lifecycle (`SEC-*` IDs + resolution dates). Only
> currently-open security items are duplicated here.

---

## Open remediation items

### RED-FU-015 follow-ups — KEK rotation tool post-merge hardening

**Status:** OPEN (non-blocking; mostly cleared). RED-FU-015 shipped in PR #249; the bulk of the should-fix follow-ups shipped in **PR #262 (2026-07-04)**: SEC-071/072/073 (lock-free verify, stdout/stderr split, equal-key guard), code-review #3 (`--to-version` bounds), code-review #5 (`signal.NotifyContext(SIGINT,SIGTERM)`), and the QA flag-plumbing test gaps (`cli_test.go` covering mutual-exclusion / bounds / bad-missing-equal keys / missing DSN / `--generate`, plus `Rekey`/`OnNewKey` empty+nil ciphertext and a `selectSQL` FOR-UPDATE lock-clause assertion). Resolution rows in [`status.md`](status.md).

**Still OPEN (all LOW/INFO, non-blocking):**
- **SEC-074 (INFO):** plaintext buffer in `Rekey` not zeroed. Accepted as best-effort defense-in-depth — consistent with the existing `libs/crypto/aes` posture, not a regression. Left open deliberately (Go GC makes true wiping unreliable).
- **Code-review #2 (latent):** `EncodingHexText` NULL cell would fail to scan in a *multi-column* table (`string` can't take SQL NULL); safe today because webhook's hex-TEXT column is the sole column in its spec. Use `*string`/`pgtype.Text` before adding a second hex-TEXT column.
- **Code-review #6:** rotate path opens the pool via `pgxpool.New(os.Getenv(dsn))` directly, bypassing `loader.DBConfig`, so the §11 / SEC-022 `sslmode=disable` rejection + pool tuning don't apply. Deliberately mirrors the `bootstrap` subcommand pattern, but the sslmode guard is silently skipped for rotations.
- **Audit VerifyChain unit-coverage gap (from #265 QA review):** the `VerifyChain` walk (incl. the SEC-051 pre-chain counting) is exercised only in the Docker-gated integration lane; the standard unit lane covers `canonicaliseJSON` only. A future refactor extracting the walk to accept an in-memory row slice would allow pure unit coverage.
- **Orphaned audit integration tests (from #316 QA review, pre-existing):** `services/audit/Makefile`'s `test-integration` target is scoped to `./internal/testutil/integration/...`, so the `//go:build integration` tests in `services/audit/internal/rotatekek/` (both the pre-existing `TestRotate_AuditTwoColumns` from #249 and the new `TestRotate_Notify*` from #316) never run via `make test-integration` — only if an operator invokes `go test -tags integration ./internal/rotatekek/...` directly. Widen the audit `test-integration` glob to `./...` (or relocate the tests) so the rotation happy-paths run in the Docker lane. The fast `TestRun_MutuallyExclusiveSelectors` (the only genuinely-new production logic) does run in CI.

---

### REM-014 — Lint findings unmasked by Go 1.25 toolchain upgrade

**Surfaced:** 2026-06-28 after PR #156 (`fix(ci): goinstall golangci-lint`) made golangci-lint reachable past its typecheck stage. Prior to #156 the action's bundled Go 1.24 binary couldn't parse Go 1.25 source, so every linter was short-circuited; PR #156 fixed that, which unmasked a real backlog.

**Status:** OPEN. CI is temporarily unblocked via `.golangci.yml` exclusions (`gosec G115`, `gosec G306`, `gocritic exitAfterDefer + whyNoLint + style/performance tags`, `unused`/`gosec`/`dupl`/`gocritic` on `_test.go`). The exclusions are the right call for noise (proto generated structs, graceful-shutdown patterns, table-driven test fixtures), but the suppressed findings still include real surface that warrants follow-up.

**Known surface (sampled from `feat/redesign-3.x-single-tenant-guards` CI run):**

| Linter | Site | Triage |
|---|---|---|
| `gosec G115` int→int32/uint32 conversion | `services/webhook/internal/handler/grpc.go:296,297,354`, `services/scanner/internal/{worker,handler}/...`, `libs/crypto/argon2/argon2.go:73` | Bounded inputs (page sizes, time-since millis); confirm each via a small audit + add a `min(x, math.MaxInt32)` guard if any source can grow unbounded. |
| `gosec G306` file write perms | `services/scanner/internal/reportworker/worker.go:138,141` | Tempdir is service-private. Tighten to 0600 if/when reports are persisted outside the service's own filesystem. |
| `errcheck` unchecked error | `services/scanner/internal/worker/worker.go:932` (`p.Enqueue`) | One-line fix — capture + log the error. |
| `exhaustive` partial switch | `libs/errors/codes/codes.go:67` | Fixed at config level via `default-signifies-exhaustive: true` (the switch has a `default:`). Verify no real coverage hole. |

**Owner:** TBD. Per-service cleanup PRs welcome; mark each finding nolint+REM-014 if intentional.

**Progress:**

| Service | Status | PR | Notes |
|---|---|---|---|
| `services/tenant` | ✅ CLOSED | (earlier PR) | gofmt/goimports drift in `internal/handler/{grpc,grpc_test}.go`; `continue-on-error: true` dropped on `ci-tenant.yml` `lint:` as proof. |
| `services/management` | ✅ CLOSED | PR #254 | gofmt sweep of the handler package (2 introduced by #253 + 3 pre-existing). |
| `libs` + `auth` + `audit` + `core` + `mcp` + `metadata` | ✅ CLOSED | PR #255 (`chore/rem-014-gofmt-rot-sweep`) | 12 gofmt-drifted files + 6 real linter findings (gocritic elseif, gosimple S1005/S1009, staticcheck SA9003 empty branch, gosec G101 false-positive nolint'd, unparam nolint'd). This was the rot keeping main's `ci-core` lint red — which gated `build` → `conformance`, so OCI conformance had stopped running on main. Lint is blocking on every per-service workflow (no lint job carries `continue-on-error` anymore). |
| Remaining (proxy, webhook, scanner, signer, storage, gateway, gc) | ✅ clean as of 2026-07-04 | — | `gofmt -l` clean repo-wide after the sweep; their CI lint was already green. Residual REM-014 surface = the `.golangci.yml` exclusions (gosec G115/G306 etc.) listed above, not per-service red. |

---

### REM-019 — Scanner trivy adapter exits with code 1 (Phase 2: underlying failure)

**Surfaced:** 2026-06-24 during scan smoke testing.
**Phase 1 (DONE, PR #70):** all four adapters now mirror their RPC
error to stderr before exit; orchestrator parses stdout RPC error
even on non-zero exit. This was the "stop debugging blind" half.
**Phase 2 (OPEN):** the underlying trivy invocation still fails.
The next smoke test against
`dev/rabbitmq:3.13-management-alpine` should now print the real
error in either the `stderr` or `stdout_error` field of the
orchestrator log. Once that error string lands, file the targeted
fix (likely candidates: missing Trivy DB in the cache volume —
boot pre-warm uses Grype not Trivy; raw gzipped layer vs OCI
layout; distroless scratch-dir / tmpdir perms).

**Workaround for users right now:** in `/admin/scanner`, swap the
active adapter to the dev stub. REM-011 P2's in-memory swap means
no container restart is needed.

---

### REM-020 — CI pipeline reshape (rethink + rework)

**Surfaced:** 2026-06-29 during PR #160. The proto-touching PR
unmasked multiple latent CI pathologies in a single afternoon — none
are new, all are pre-existing rot — making it clear the pipeline
needs a deliberate reshape, not just per-incident patches.

**Why now:** REM-014 (lint backlog), REM-015 (auth lint-queries),
REM-016 (Go stdlib CVEs) and the PR #156-#158 BE CI infrastructure
reset already filed individual failures. REM-020 is the umbrella that
turns "patch each fire as it surfaces" into "reshape the pipeline so
the fires stop starting." Without it the next proto-touching PR
re-discovers the same potholes.

**Pain points surfaced this session:**

| # | What's broken | Evidence | Impact |
|---|---|---|---|
| 1 | `ci-proto.yml` breaking-check used `cd proto && buf breaking --against '.git#branch=main'` — looked for `.git` inside `proto/` (doesn't exist) | PR #160 round 1 — fixed in commit `dc9cb8c` / `4612578` | Every proto PR for months silently failed this check; nobody noticed because it's been red on every PR |
| 2 | `actions/checkout` default shallow clone doesn't fetch main, so `branch=main` fails even after fix #1 | PR #160 round 2 — required `fetch-depth: 0` + `branch=origin/main` | Hidden dependency on checkout config; every CI tool that compares against main needs this |
| 3 | Per-service `go.sum` rot — Docker build fails with `missing go.sum entry for pgxpool` whenever `libs/config/loader` indirect deps change | PR #160 needed tidies in `services/tenant` AND `services/core`; memory note `current_sprint_status.md` flagged 2 services pre-existing | Every proto-touching PR re-discovers this. Workspace mode (`go.work`) hides it locally; Dockerfile's `GOWORK=off` exposes it |
| 4 | `ci-core.yml` path-filtered on `proto/**` so a tenant-only RPC addition triggers the full core pipeline (build, conformance) | PR #160 — core ran even though no core code changed | Wasted runner minutes; obscures which service's CI is "actually" testing the change; means proto PRs hit 2× the failure surface |
| 5 | `continue-on-error: true` sprawl — `lint` (REM-014), `security` (REM-016), at one point `breaking` was silently broken too. Red marks in `gh pr checks` no longer mean "merge blocker" | Every PR has 1-3 red marks that have to be triaged "is this a real blocker?" | Erodes signal-to-noise; new contributors can't tell if their PR is broken without reading workflow YAML |
| 6 | 13 nearly-identical `ci-<svc>.yml` files. Drift between them is invisible until something breaks (PR #156 fixed golangci-lint version mismatch across all 13) | 13 × ~100 LOC YAML files, one per service | Drift bombs. DRY violation. Easy to fix one but miss the same fix in 12 others |
| 7 | No Docker BuildKit cache mount on `go build`. Every CI build re-downloads every dep. ~5min per build job | Observed in PR #160 build logs — `go: downloading github.com/...` for ~150 deps every run | ~1h/day of runner time per active branch |
| 8 | No `setup-go` cache, no go-mod cache shared across jobs in the same workflow | Same downloads in lint job → test job → build job | Adds ~30-60s per stage |
| 9 | No central "pipeline health" view. Hard to know if main is currently green across all 13 services. Each PR resurfaces failures that exist on main too | Discovered REM-015 and REM-016 only after they leaked into PR #155's red-CI merge | Failures persist until a PR makes them visible |
| 10 | `services/core`'s `conformance` job has been red on **every main run since at least 2026-06-25** (verified via `gh run list --workflow ci-core.yml --branch main`). Root cause: every Dockerfile does `COPY go.work go.work.sum ./` but `go.work.sum` is `.gitignore`d. `docker compose build` fails before the test even runs. CLAUDE.md §2 explicitly says "`go.work.sum` is committed alongside `go.work`" — reality disagrees | PR #160 — conformance red; every recent PR (#155, #156, #157, #158) merged with this red | ✅ CLOSED by PR #255 (go.work.sum committed). A "real" blocker that nobody treats as a blocker. Erodes the meaning of "CI is green." Fix: remove `go.work.sum` from `.gitignore`, commit the file, OR drop the line from every Dockerfile. The CLAUDE.md spec wins — 1-line `.gitignore` edit + commit the existing 22KB file. Lives under REM-020 sub-fix #5 (per-service tidy sweep) since it's the same go-workspace category |
| 11 | **NEW rot (found by the PR #270 qa-agent, 2026-07-05):** `ci-core.yml` `conformance` is red again on main — but a *different* root cause than #10. Failure is `dependency failed to start: container docker-compose-registry-auth-1 is unhealthy` — the auth container builds fine then fails its startup healthcheck, so conformance never runs. Entered main with commit `13a2fee` = **PR #267 (TOTP MFA merge)**; prior run (`3e52929`, 07-04 20:10) was green. **Confirmed root cause:** the conformance job builds `services/auth/.env` from `.env.example` (ships `MFA_SECRET_KEY_HEX=` empty) + appends only JWT keys, and `registry-auth`'s `config.validate()` (PR #267) fails CLOSED unless `MFA_SECRET_KEY_HEX` decodes to exactly 32 bytes → auth exits at boot. | PR #267 merge (`13a2fee`) | ✅ **CLOSED by PR #271** (`9e2a376`, 2026-07-05) — conformance job now generates an ephemeral 32-byte MFA KEK inline and appends it to `services/auth/.env`; `ci-core.yml` also added to its own path filter (+ `workflow_dispatch`) so pipeline edits self-validate. Conformance verified green on the PR run (both push + pull_request). Unblocks the v2.0.0 tag. |

**Proposed reshape (sketch — confirm before execution):**

1. **DRY the 13 `ci-<svc>.yml` files** into one reusable workflow
   (`.github/workflows/_be-service-ci.yml`) consumed by 13 thin
   per-service callers that pass `service: <name>`. One place to
   change the toolchain, the lint config, the cache strategy.
2. **Fix the build caching** — `actions/setup-go` with `cache:
   true`, BuildKit cache-mount on `RUN go build`, hash-keyed on
   `go.sum`. Expected: ~5min build → ~30s on cached PRs.
3. **Standardise checkout** — `fetch-depth: 0` everywhere a tool
   compares against `main`. Add a workflow lint that fails CI if a
   workflow references `branch=main` without `fetch-depth: 0`.
4. **Kill the path-filter cross-trigger** — narrow `ci-core.yml`'s
   `proto/**` filter to only the proto subtrees core actually
   consumes (e.g. `proto/storage/**`, `proto/metadata/**`).
   Tenant-only proto changes shouldn't run core CI.
5. **Per-service `go mod tidy` sweep** — one-shot PR that runs
   tidy on all 13 services + libs to close the pre-existing rot.
   Then add a `tidy-check` job (`go mod tidy && git diff --exit-code`)
   so future PRs can't merge with stale go.sum.
   🟡 **IN FLIGHT — PR TBD on `fix/ci-rem-020-tidy-sweep`.** Tidied 11
   services + libs (services/auth deferred until PR #162 merges to
   avoid branch conflict; auth tidy ships as a follow-up). Added
   `.github/workflows/ci-tidy-check.yml` — matrix workflow over all 14
   modules with `go mod tidy && git diff --exit-code` per module.
6. **Sunset `continue-on-error: true` once REM-014/015/016 close** —
   per-service cleanup PRs are already designed to drop these
   flags; REM-020 just tracks the campaign.
7. **`main` health board** — a single workflow that runs
   `gh run list --branch main --workflow ci-*.yml` nightly +
   pings if any are red. Catches drift before it leaks into a PR.

**Status:** OPEN — sketch above. Each numbered item ships as its
own PR; REM-020 stays open until they all close. No immediate
priority over the in-flight REDESIGN-001 work, but **interleaved**
— each REDESIGN-001 PR that runs into a CI pothole files a fix
inline and ticks the relevant REM-020 sub-item.

**Owner:** TBD. PR #160 already shipped fixes for sub-items #1, #2,
and partial #5 (tenant + core tidies).

## Open security items

The full audit log lives in [`security.md`](security.md). Only items that remain OPEN are tracked here for ongoing attention.

| ID | Severity | Title | Status | Notes |
|---|---|---|---|---|
| **PENTEST-030** | LOW | Per-endpoint test-dispatch throttle missing on webhook `Test` action | OPEN | `handleTestWebhook` (`services/management/internal/handler/webhooks.go:348`) only checks `requireWebhookAdmin` then forwards. No per `(tenant_id, endpoint_id)` Redis bucket or daily budget. Per-user 20 rps still amplifies. Tracked for a global rate-limit pass. |
| **PENTEST-033** | LOW | Postman dev passwords still inlined | PARTIAL | Login uses `{{password}}` (`type: secret`) — done. Still open: (a) `NewUser1234!` baked into `createUser` request body at `registry-management.postman_collection.json:114`; (b) dev tenant UUID `98dbe36b-…` defaulted in the env file. Cosmetic cleanup. |

---

## Partial / blocked surfaces

### S11 Retention slices 3 + 4 (✅ DONE — REM-013 closed 2026-07-03)

- **Slice 3** (FE-API-040): "Run now" trigger + 5s status polling on the Retention tab, pending-delete pills on Tags tab, and the per-repo Run history panel — all **live** (REM-013 gaps 1 + 2 shipped).
- **Slice 4** (FE-API-039): org-default Retention surface + cross-link from inherited per-repo policies **live**; the dashboard storage-breakdown "Reclaimed via retention" savings stat shipped as REM-013 gap 3 (PR #253).

All retention FE surfaces now render real data end-to-end. The only outstanding retention item is the optional, non-FE-blocking per-rule "considered/kept/graced/hard-deleted" run breakdown (parked — see the REM-013 close-out row in `status.md`).

---

## Post-OSS launch hygiene

Surfaced by PR #42 (Apache 2.0 OSS launch, 2026-06-23). These items aren't bug fixes — they're the contributor-onboarding surface that should exist before the repo gets meaningful inbound traffic.

| ID | Item | Effort | Why |
|---|---|---|---|
| **HYG-001** | README hero screenshot / dashboard GIF | ~30 min | Biggest first-impression lever on the repo page. People decide whether to read the README in ~5 seconds based on the visual. |
| **HYG-005** | 3-5 `good first issue` labels populated | ~1h | The single biggest lever for first-time contributors. People can't contribute if they don't know where to start. Pick 3-5 small items from this tracker or futures.md and label them. |
| **HYG-006** | Architecture diagram image (replace ASCII in README §2) | ~1h | Cleaner first impression than the ASCII diagram. Excalidraw / draw.io export → committed PNG. |

> HYG-002 / HYG-003 / HYG-004 shipped in PR #44 (2026-06-23); HYG-007 (Discussions) + HYG-008 (private vulnerability reporting) shipped 2026-07-12 — see [`status.md`](status.md).

---

## Review batch — 2026-06-23

Three review agents (design / quality / architecture) did a deep cross-cutting review.
**74 findings total** — 24 design (`DSGN-*`), 28 code quality (`QA-*`), 22 architecture
(`ARCH-*`). Full per-finding detail with file paths + line numbers lives in:

- [`.claude/reviews/design-review-2026-06-23.md`](.claude/reviews/design-review-2026-06-23.md)
- [`.claude/reviews/quality-review-2026-06-23.md`](.claude/reviews/quality-review-2026-06-23.md)
- [`.claude/reviews/architecture-review-2026-06-23.md`](.claude/reviews/architecture-review-2026-06-23.md)

Curated P0/P1/P2 backlog lives in [`futures.md`](futures.md) under the
"Review batch — 2026-06-23" section. Pick from there as work cycles open up.

---

## Backlog (not in this file)

Prioritised feature work that hasn't been picked up yet lives in [`futures.md`](futures.md). The tracker doesn't duplicate them — once an item gets picked up + assigned a REM / FE-API number + put on a branch, it migrates here.

Quick pointer to the largest open backlog items (see `futures.md` for full detail):

- **Tier 1 #1** — MFA (TOTP step-up) — **core SHIPPED 2026-07-05** (PR #267 + SEC-078/079/080 fixes #267/#268) + **active-session list + per-row revoke SHIPPED 2026-07-05** (PR #270, squash `91f42f4`; resolution rows in [`status.md`](status.md)). **Residual open:** WebAuthn/hardware keys only — see the trimmed Tier-1 #1 in [`futures.md`](futures.md)
- **Tier 1 #5** — SCIM v2 provisioning — ~1.5 weeks
- **Tier 1 #3 Phase 3** — multi-key quorum + Fulcio binding — ~1-2 weeks
- **FUT-010** — RBAC + FE-RBAC polish pass — ~1 sprint
- **FUT-011** — New-user onboarding flow end-to-end via FE (paired with DEPLOY-001) — ~half day + docs
- **DEPLOY-001** — SaaS vs self-hosted deployment docs + tenant-persona testing — ~half day
- Smaller Tier 2 items: FUT-007-FE, FUT-008, etc.
- Remaining DSGN: DSGN-002 / -008 / -009 / -018 / -023 / -024 (6 of 24 still open from the 2026-06-23 review batch)
- **System review batch 2026-07-05** — FUT-071 (air-gap export/import), FUT-072 (vuln diff between tags), FUT-073 (per-token rate limit on core data plane), FUT-074 (quota fail-open observability), FUT-075 (test-debt truth-up: `libs/scanner` + TESTING.md claims) + FUT-067 amended with an on-demand hash-chain-verify UI action — see the `futures.md` section of the same name

---

## How to use this file

- **One bullet per open item.** Lean by design — if this file passes ~10 sections something is wrong with the workflow.
- **When work ships:**
  1. Remove the entry from this file.
  2. Append a resolution note to [`status.md`](status.md) (one entry per item, with PR / commit hash / date).
- **New surfacings** get an entry here first; once the work is in flight, link the branch / PR; once it ships, move to `status.md`.
- **`futures.md`** is the natural place for things that haven't started yet — not yet picked up, not yet on a branch. This tracker is for things that are *open work*, not *future ideas*.

```
                  ┌──────────────────┐
   ─surfacing──►  │ status-tracker.md│ ──ships──►  status.md
                  │  (in flight)     │              (completed log)
                  └──────────────────┘
                          ▲
                          │ pickup
                          │
                  ┌──────────────────┐
                  │   futures.md     │
                  │  (backlog ideas) │
                  └──────────────────┘
```

---

> **Last updated:** 2026-07-16 — **Signing coverage rollup SHIPPED (no new open REM/SEC).** New read-only BFF `GET /api/v1/signing/coverage` (pure orchestration in `services/management` — no proto/migration) + a live **Security → Signing** tab (was a placeholder): per-repo signed-tag % over a bounded recent-tag window, recent signers, and trusted-key allowlist health (surfaces the "enforced but empty allowlist → any signature passes" soft spot). Cosign-only; **visibility only** — changes no admission decision, distinct from the deferred admission Phase 3 (quorum / rotation / keyless) tracked in `futures.md`. DONE row + rationale in [`status.md`](status.md); `docs/SIGNING.md` §9 + README + `futures.md` updated. Branch `feat/signing-coverage-rollup`. Prior update 2026-07-15 — **FUT-088 UI paper-cut batch + repo Settings General section shipped (no new open REM/SEC).** Cleared four FUT-088 paper-cuts: **#1** real time-range on the API-key activity feed (PR #372 — the auth `/access/activity` handler now parses `?since=<RFC3339>` and threads it into the audit `GetNotificationsRequest.since` that *already existed*; FE `sinceForRange` computes a real bound per 24h/7d/30d window — replaces the fake limit-as-time proxy that also 400'd on the 30d chip's `limit=500`; live-verified counts move monotonically 30d=16>7d=8>1h=0), **#3** retention-grace window single-sourced from GC (#371), **#6** MCP connect card in Settings › Integrations (#370), and **batch-1** org bulk-scan + a stale TODO (#369); items #2/#4/#7a were already-fixed on re-triage. Also closed the **repo Settings General section** — rename + transfer across auth/metadata/management/FE as four gated PRs (#363/#364/#365/#367; storage is `repo_id`-keyed so the only cross-service concern was the RBAC scope-string rewrite via the new `auth.RewriteRepoRoleScopes`), plus **Tier-2 #3 image-diff-between-tags** and the **FUT-020 re-sign-on-promote** follow-up. All DONE rows + rationale in [`status.md`](status.md); FUT-088/FUT-020 backlog bullets ticked in `futures.md`. Remaining FUT-088 tail: **#7c** unused-hooks sweep (low). **Next pickup candidates unchanged: the 2026-07-12 system-gap audit Tier-1 items (FUT-080..FUT-084 in `futures.md`).** Prior update 2026-07-13 — **tracker truth-up (no new feature work).** Resolved the SEC-087 inconsistency flagged in the 2026-07-12 system-gap audit: the `ListRepositories` artifact-types tenant-scope fix already shipped inline in PR #321 (verified in-code at `services/metadata/internal/repository/repository.go:289-291`) but `security.md` still read OPEN — flipped SEC-087 → RESOLVED + banner note; `futures.md` preamble + FUT-089 bullet ticked. Also truth-upped the README storage-driver overclaim (listed all 5 drivers; only `minio` + `filesystem` are implemented — the `minio` driver is S3-compatible so AWS S3 works today, native GCS/Azure on the roadmap per `docs/integrations/storage.md`). No open MEDIUM/HIGH security items remain. **Next pickup candidates: the 2026-07-12 system-gap audit Tier-1 items (FUT-080..FUT-084 in `futures.md`)** — compliance-report data wiring, missing event publishers, MCP client repair, Helm management subchart, and FE SSO wiring. Prior update 2026-07-12 — **quick-win batch shipped** (subagent-driven + 3-agent review batch): **HYG-007** (GitHub Discussions) + **HYG-008** (private vulnerability reporting) enabled; **FUT-079 item 1** — show-password toggle (PR #328); **REM-018-followup** — actor display name (PR #329); **FUT-009** — service-account-as-signing-identity (PR #330). Review batch found one MEDIUM — **SEC-088** (review label SEC-330-A: UUID-shaped free-form `signer_id` could forge SA provenance) — **fixed inline before merging #330**. DONE rows in [`status.md`](status.md); SEC-088 in [`security.md`](security.md); HYG-007/008 removed from the hygiene table above and FUT-009 from the backlog pointer. Deferred should-fixes now tracked in `futures.md`: a two-page pagination test-fake + FE custom-mode payload test (FUT-009), a REM-018 comment-wording tidy, and the pre-existing `admin_tenants_test.go:162` copylocks `go vet` main-rot (REM-014 territory). Earlier same day — filed **FUT-079 — Auth/forms UX polish bundle** (Tier 3) in `futures.md` (Group C), first item a "show password" reveal toggle for the 8 `type="password"` fields; growable bundle so further UX gaps append as sibling bullets (PR #325). No REM/SEC/status.md row — backlog-filing only, per the FUT-078 precedent. Prior update 2026-07-08 — **FUT-019 webhook notification channel SHIPPED** (branch `feat/fut-019-webhook-channel`, PR #291; DONE row in `status.md`, backlog note in `futures.md`). Bell + Email + Webhook columns of Settings › Notifications are all now live. No new open REM/SEC entry — the review batch's accepted non-blocking follow-ups are logged in `futures.md` (FUT-019 block), and the rotate-kek channel-secret sweep gap is cross-linked under the RED-FU-015 follow-ups above (KEK-rotation territory). Prior update 2026-07-02 — closed **SEC-066** as **WON'T FIX / not applicable**: it is a multi-tenant-only exploit (forge `tenant_id` to attack another tenant), and the platform's supported posture is single-tenant (REDESIGN-001) where no second tenant exists. The proposed shared `PeerTenantCheck`/`PeerActorCheck` interceptor is withdrawn; multi-mode operators rely on `MTLS_PEER_CN_ALLOWLIST` (documented). This also disposes of SEC-069's inherited `actor_id`-spoof residual (same compromised-internal-peer threat, same allowlist mitigation). Earlier same day: cleared **SEC-057..062** (FUT-001 OIDC/JWKS hardening, resolved #244) and **SEC-068/069/070** (access-review defence-in-depth, resolved #243) from the open security items table; both batches now carry RESOLVED status + resolution notes in `security.md` and DONE rows in `status.md`. SEC-057 remediation #3 (compose trailing slashes) was **withdrawn as incorrect** — see the `security.md#SEC-057` note. Open security items remaining: PENTEST-030/033 (LOW) + the tail SEC-051..054 follow-ups noted above (no open MEDIUM/HIGH security items). Earlier same day: retired REM-026 (FUT-004 shipped #227 + hotfix #228); cleared SEC-064/065/067 (resolved #226/#228 — statuses were stale). (Prior to #244, SEC-057..062 had been synced *into* this table on 2026-07-02 from the Fable review absorption; #244 resolved them the same day.) Prior update 2026-06-30: REDESIGN-001 entry trimmed to a soak-window residual after `v2.0.0-rc1` cut + pushed (tag `4dd3e63` → commit `f0896ff`, PR #219). Calendar-only remainder: soak ≥ 2026-07-07 then tag `v2.0.0`. Tail SEC items + 4 RED-FU items deferred per the residual block above.
> **Maintainer:** see `git log -- status-tracker.md`.
