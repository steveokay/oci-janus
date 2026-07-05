# Changelog

All notable changes to this project will be documented in this file. Format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> v2.0.0 is the first tagged release. The "v1" referenced below is the
> pre-REDESIGN-001 state of the codebase (multi-tenant SaaS posture) that
> existed in operator deployments before this release was cut. Operators
> upgrading from a v1 deployment must read
> [`docs/MIGRATION-v1-to-v2.md`](docs/MIGRATION-v1-to-v2.md) end-to-end
> before running the upgrade.

---

## [Unreleased]

_Nothing yet._

## [2.0.0] — 2026-07-07

First tagged release of the single-tenant (REDESIGN-001) posture. Supersedes
`v2.0.0-rc1`; the notable changes below are everything merged on top of rc1
during the soak window (2026-06-30 → 2026-07-07). Operators upgrading from a
pre-REDESIGN v1 deployment must read
[`docs/MIGRATION-v1-to-v2.md`](docs/MIGRATION-v1-to-v2.md) first.

### Added

- **Machine-identity suite (FUT-001..004)** — the four `/api-keys/*` surfaces
  went from preview to live: **federated workload identity** (OIDC-trust
  exchange for CI runners — GitHub Actions / GitLab / Buildkite / generic OIDC —
  swapping a workload token for a short-lived registry JWT, no static key;
  PR #224); **token policies** (workspace-wide `max_ttl_days` /
  `rotation_interval_days` / `idle_revoke_days` + an hourly idle-revoke worker;
  PR #225); **access review** (weekly stale/rotation-lapsed key flagging with
  per-row Revoke / Keep / Snooze; PR #227); **credential helpers** (hostname-aware
  `docker login` / k8s Secret / Terraform / GHA snippets; PR #221).
- **TOTP multi-factor auth (step-up)** — self-service enrolment (QR + 8 backup
  codes), a two-step stateless-challenge login, and an admin "require MFA"
  policy toggle; AES-256-GCM secret at rest under a dedicated `MFA_SECRET_KEY_HEX`
  KEK (fails closed if unset). SSO accounts exempt. (PRs #267, #268, #269.)
- **Active session management** — a signed-in user sees their live sessions
  (device, IP, last-active) on `/profile` with per-row revoke + "sign out all
  other sessions", backed by a durable `user_sessions` table + a stable `sid`
  JWT claim and a fail-closed `revoke:sid` gate. Client-side pagination keeps the
  table compact. (PRs #270, #277.)
- **OCI Referrers tab** — referrer artifacts attached to an image (Cosign
  signatures, SBOMs, attestations, scan results) are now a first-class tab on the
  tag-detail page instead of raw JSON, via a new `registry-core`
  `CoreService.ListReferrers` gRPC surface. (PR #282.)
- **Image promotion workflow** — `POST /repositories/{org}/{repo}/tags/{tag}/promote`
  atomically copies a tag's manifest to a destination with digest verification +
  audit trail; dashboard dst-org picker with create-if-missing. (PRs #231, #234, #235.)
- **CVSS-gated admission policy (FUT-021)** — repos gain a nullable
  `max_cvss_score`; `GetManifest` refuses a pull when the scan's top CVSS exceeds
  the threshold, closing the scanner → admission loop. (PR #233.)
- **MCP server (read-only)** — exposes the registry to AI assistants (Claude
  Desktop / Cursor / continue.dev) over the Model Context Protocol; read-only
  tools this release. (PR #232.)
- **Retention savings on the dashboard** — a new
  `GCService.GetTenantRetentionSavings` RPC surfaces bytes reclaimed via retention
  on the storage-breakdown card (REM-013 gap 3). (PR #253.)
- **KEK rotation tool (RED-FU-015)** — per-service `rotate-kek` subcommand
  (`registry-auth` / `registry-proxy` / `registry-webhook` / `registry-audit`,
  dispatched before config load like `bootstrap`), backed by the shared
  `libs/crypto/rekey` package (re-encryption core + declarative table-agnostic
  sweep engine + CLI runner). Re-encrypts every KEK-encrypted column from an old
  key to a new one, per-table all-or-nothing, idempotent/resumable via
  trial-decryption. Modes: `--dry-run`, `--verify` (exit 3 if rows remain),
  `--generate`, `--to-version`. Adds a nullable `kek_version SMALLINT` tracking
  column per affected table. Keys are read from `KEK_OLD_HEX` / `KEK_NEW_HEX`
  (never flags). Operator runbook: [`infra/runbooks/kek-rotation.md`](infra/runbooks/kek-rotation.md).
  Note: there is **no single master KEK** — four independent per-service KEKs;
  signer keys stay in Vault/KMS (out of scope). (PR #249.)

### Changed

- **Settings / Profile information architecture** — Profile split out of Settings
  into its own top-right surface (identity, API keys, MFA, sessions); Settings
  retabbed into Workspace / Scanning / Housekeeping / Notifications with
  Vulnerability scanning on its own tab. (PR #274.)
- **`/repositories` lists all repositories**, not only those with pushed images
  (dropped the client-side image filter). (PR #236.)
- **CI: govulncheck consolidated** — the 13 per-service non-blocking `security:`
  jobs were replaced by one scheduled `.github/workflows/ci-security.yml`
  (nightly + `workflow_dispatch`, matrix over all 14 Go modules). Removed the
  muted `scripts/lint-user-queries.sh` step (REM-015). (PR #250.)

### Security

- **OIDC / JWKS hardening (SEC-057..062)** — SSRF and resource-exhaustion
  defences on the federated-identity OIDC/JWKS fetch path (FUT-001). (PR #244.)
- **Password-login kind guard (SEC-075)** + OAuth `email_not_verified` aligned to
  `403 EMAILNOTVERIFIED`. (PR #263.)
- **MFA login-step lockout + challenge-token replay cap (SEC-079)** — the login
  MFA step now checks account lockout before consuming a code and bounds
  submissions per challenge token. (PR #268.)
- **Access-review / token-policy defence-in-depth** — SEC-064 + SEC-067 (FUT-003;
  PR #226), SEC-065 idle-revoke floor + SEC-068 HIGH cross-tenant snooze (FUT-004;
  PRs #228, #243), SEC-069/070 audit drop-on-malformed. (PR #243.)
- **Audit tamper-evidence (SEC-051/052)** — `VerifyChain` now surfaces
  pre-chain-migration rows as unverifiable rather than masking them; added
  canonicalisation tests. (PR #265.)
- **KEK rotation hardening (SEC-071/072/073)** — lock-free verify, stdout/stderr
  split, equal-key guard, `--to-version` bounds, signal-aware cancel. (PR #262.)
- **Spec-lint tightening (SEC-053/054)** — require a reason on every audit-skip
  annotation + tighten the mTLS-gate regex. (PR #264.)
- **Login page reveals account lockout (PENTEST-005 scoped reversal)** — the
  interactive `/login` now returns a distinct `423 ACCOUNT_LOCKED` with a retry
  hint; wrong-password / disabled failures and the `/auth/token` machine endpoint
  still collapse to a generic `401` (no enumeration oracle). Residual exposure
  accepted for the single-tenant posture; see `security.md`. (PR #275.)
- **Go toolchain 1.25.7 → 1.25.11 (REM-016)** — clears the five deferred stdlib
  CVEs (GO-2026-5039/5037 in `net/textproto`+`crypto/x509`, GO-2026-4982/4980/4971)
  across every Go module. `services/auth`'s `russellhaering/goxmldsig` bumped
  v1.3.0 → v1.6.0 fixing **GO-2026-4753** (XML-dsig signature bypass under the
  SAML SP path; logged as SEC-076, resolved same-day). The nightly govulncheck
  sweep (`ci-security.yml`) is now a **blocking** gate — 0 affected
  vulnerabilities across all 15 modules. (PR #256.)

### Fixed

- **API keys were silently revoked before first use** — the idle-revoke and
  access-review workers treated any never-used key as instantly idle; a
  freshly-issued key was revoked on the next hourly tick. Never-used keys are now
  measured from `created_at` (`COALESCE(last_used_at, created_at)`), so a new key
  gets the full idle window. (PR #276.)
- **Clearer duplicate-API-key-name error** — a `409` on a reserved name now says
  so instead of a generic "couldn't create key". (PR #275.)
- **UI polish batch (UIR-1..10)** — the deferred tail of the 2026-07-04 review:
  GC/HealthCard/retention/notification/topbar/badge/SSO-button/PoliciesPanel/copy
  fixes; per-cell pending on the notification matrix; login SSO buttons rendered
  honestly disabled. (PR #279.)
- **Dialogs scroll when content overflows** the viewport (user report). (PR #229.)
- **Audit hash-chain migration** reworked to apply on the partitioned
  `audit_events` parent (REM-022). (PR #223.)
- **OCI conformance restored on `main`** — cleared gofmt/lint rot that had
  silently gated the conformance job, and gave the CI conformance auth container
  an ephemeral `MFA_SECRET_KEY_HEX`. (PRs #254, #255, #271.)
- **Dashboard UI — 30 fixes from the 2026-07-04 four-agent UX review**
  (PRs #257/#258/#259; full inventory in `FE-STATUS.md` → "UI polish review").
  Highlights: access-review key revocation now requires confirmation; the SA
  keys table's expiry column was mislabelled "Last used"; repository search no
  longer reports false "no matches" over unfetched pages; login honors the
  captured `?from=` deep-link; GC run history live-updates after "Run now";
  sidebar gains keyboard focus rings, `aria-current`, and a Dashboard entry;
  repo/tag detail tabs are URL-driven (`?tab=`); destructive dialogs unified
  on `ConfirmDestructiveDialog`; API-key tables show expiry-urgency badges;
  activity feed gains cursor pagination + URL-persisted filters;
  platform-admin actions are disabled (with a hint) for callers without the
  grant instead of failing post-confirmation with a 403.

---

## [2.0.0-rc1] — 2026-06-30

Release candidate for the REDESIGN-001 single-tenant-by-default rewrite. The
RC will soak for at least one week before being re-tagged as `v2.0.0`. No
new features are planned between RC1 and the final tag — only critical-bug
fixes surfaced during soak.

### Breaking changes

These five items are the operator-facing breaks. Each one is covered with
upgrade steps in [`docs/MIGRATION-v1-to-v2.md`](docs/MIGRATION-v1-to-v2.md).

- **`DEPLOYMENT_MODE` defaults to `single`.** v1 operators running the
  multi-tenant SaaS posture **MUST** set `DEPLOYMENT_MODE=multi` on every
  service before the v2 upgrade, otherwise the new
  `SingleTenantInjector` interceptor will reject every request that carries
  a tenant id other than the bootstrap tenant. (Decision #25.)
- **Per-tenant SSO removed; SSO is now global.** The `auth_providers` table
  + per-tenant SSO admin RPCs are gone. Re-configure your IdP through the
  new `global_sso_config` (one OAuth/SAML provider per deployment).
  AES-256-GCM ciphertexts persisted in v1 are read transparently — no
  re-encryption required. (Decision #27, REDESIGN-001 RM-003.)
- **Custom domains removed.** `tenant_domains` CRUD + Host-header tenant
  resolution are gone. Operators routing tenants by hostname must move to
  the per-request tenant header (`X-Registry-Tenant`) or to the single-tenant
  bootstrap. (REDESIGN-001 RM-001.)
- **Dev-seed admin migration removed.** v1 shipped a
  `seed_dev_admin.sql` migration that wrote a hardcoded admin row into
  every deployment — a critical credential leak. Replaced by the new
  `registry-auth bootstrap` CLI subcommand, which **MUST** be run exactly
  once per deployment with `--admin-email --admin-username
  --admin-password-stdin --tenant-name`. Idempotency is enforced via
  `tenant.deployment_metadata.bootstrap_tenant_id`. (Decision #28, Phase 3.1.)
- **`scope_value='*'` no longer mints platform admin.** The legacy
  `(admin, org, '*')` marker scope is rejected by every gate. Use the new
  typed `users.is_global_admin BOOLEAN` primitive via
  `SetGlobalAdmin`. The Phase 5.1 backfill deleted the marker rows; if you
  have a v1 backup with marker grants, re-grant them as
  `is_global_admin = true` after the migration runs. (Decision #26.)

### Added — features

- **Bootstrap CLI (`registry-auth bootstrap`).** One-shot, idempotent
  provisioning of the first tenant + first global-admin user.
- **Global SSO** — OAuth 2.0 + PKCE (Google / GitHub / Microsoft / generic
  OIDC) and SAML 2.0 SP (auto-provisioning, AES-256-GCM-encrypted client
  secrets, `SSO_SAML_TRUST_EMAIL` flag). See [`docs/SAML.md`](docs/SAML.md).
- **Multi-key JWT signing ring** — `JWT_KEY_RING_PATH` loads up to 16
  RS256 keys at startup. JWKS endpoint `/.well-known/jwks.json` enumerates
  every public key so external validators can rotate too. `kid` is stamped
  on every issued token. (Phase 6.5.)
- **mTLS hot reload** — `libs/auth/mtls.ReloadingServerTLSConfig` /
  `ReloadingClientTLSConfig` re-read cert files at every handshake when
  the on-disk `(mtime, size)` changes. cert-manager's atomic rename
  surfaces on the next handshake with no service restart. (Phase 6.9.)
- **Per-server peer-CN allowlist** — `MTLS_PEER_CN_ALLOWLIST` (CSV)
  rejects gRPC peers whose client-cert CN is not on the list.
  `registry_grpc_peer_cn_denied_total` + `..._allowlist_enabled` metrics.
  (Phase 6.10.)
- **AES-256-GCM ciphertext version prefix** — `libs/crypto/aes` writes a
  `0x01` version byte ahead of nonce + ciphertext + tag. Decrypt is "try
  v1, fall back to legacy"; tamper safety preserved by GCM auth tag.
  Prerequisite for the KEK-rotation tool (RED-FU-015; shipped
  post-rc1 — see Unreleased). (Decision #29, Phase 6.4.)
- **Tamper-evident audit hash-chain** — `audit_events` carries
  `chain_seq BIGINT GENERATED ALWAYS AS IDENTITY` + `prev_hash` +
  `row_hash`. Per-tenant chain serialised by `pg_advisory_xact_lock`. Tip
  derived from `audit_events` itself (no writable tip table), so the
  INSERT-only `registry_audit_app` role cannot rewrite the chain.
  `Repository.VerifyChain(ctx, tenantID)` walks the linked list.
  (Decision #30, Phase 6.12.)
- **`SingleTenantInjector` interceptor** — wired on all 11 gRPC servers.
  Injects `bootstrap_tenant_id` into requests missing tenant metadata;
  rejects mismatched tenant ids with `InvalidArgument`. (Phase 3.4.)
- **`registry-tenant.GetDeploymentMetadata` RPC** — feeds every service's
  `SingleTenantInjector` with the bootstrap tenant id at startup.
- **API-key Bearer dispatch in HTTP auth** — `requireAuth` now accepts
  both `Bearer <jwt>` and `Bearer key.<id>.<secret>` shapes. Raw API
  keys carry empty `Roles`, so role-gated handlers return a clean 403.
  (Decision #24, FUT-006.)
- **Argon2 verify cache** — successful API-key verifications cache in
  Redis at `apikey:valid:<keyID>:<sha256-hex-secret>` (TTL 60s). Live DB
  state gates still run on every HIT. Fail-open on Redis-down.
  (Phase 6.7.)
- **Service-account principals as shadow users** — every service account
  auto-provisions a `users.kind='service_account'` row. RBAC/audit/RLS
  paths treat the SA shadow-user id as an opaque actor. Admin gates
  refuse SA principals. (Decision #22, Phase 5.4.)
- **Delegator-dominates rule** — RBAC grant chains require the
  delegator's effective role to dominate the role being delegated.
  (Phase 5.3.)
- **SSO subject-id binding + `SSO_SAML_TRUST_EMAIL` flag** — closes the
  email-pivot account-takeover surface. (Phase 5.5 / 5.6, SEC-040..043.)
- **`tools/spec-lint`** — data-driven CLAUDE.md ↔ code invariant checker
  (13 rules). Runs in CI; fails the build when the docs drift from the
  shipped state. (Phase 7.3.)
- **Operator runbook: scanner isolation** —
  [`infra/runbooks/scanner-isolation.md`](infra/runbooks/scanner-isolation.md)
  documents the container-boundary posture (read-only root, cap-drop,
  RuntimeDefault seccomp, NetworkPolicy egress allowlist) that replaces
  the original Phase 6.11 in-process sandbox. The in-process variant
  remains parked as `futures.md` RED-FU-018.
- **Migration guide** —
  [`docs/MIGRATION-v1-to-v2.md`](docs/MIGRATION-v1-to-v2.md) walks
  operators through every breaking change in order. (Phase 8.1.)
- **README rewrite** — repositioned from "multi-tenant SaaS-grade" to
  "self-hosted OCI registry with optional multi-tenant capability".
  (Phase 8.2.)
- **ADR-0001..0030** — every entry in CLAUDE.md §14 Decision Log now has
  a long-form ADR under `docs/adr/` with "verified by" code pointers.
  (Phase 7.2.)

### Changed

- **`tenant.CreateTenant` returns `FAILED_PRECONDITION` in single mode**
  when a second tenant insert is attempted. Multi mode is unchanged.
- **Audit-event coverage is now CI-enforced** — every event registered in
  `libs/rabbitmq/events` must map to a row in `audit_events` (via a case
  in `mapEvent`) OR carry an explicit `// audit: skip` annotation. A Go
  test fails CI if neither is present. (Phase 6.3.)
- **`registry-audit` runtime role is `registry_audit_app`** — INSERT-only
  on `audit_events`, no UPDATE, no DELETE. `audit_events` carries `FORCE
  ROW LEVEL SECURITY` so even the table owner cannot bypass the policy.
  (Decision #15.)
- **Frontend** — removed tenant switcher, plan/billing UI, custom-domain
  CRUD, per-tenant SSO admin pages. Multi-mode operators re-expose
  these via the `useDeploymentMode()` hook. (Phase 4.1–4.6.)
- **JWT validation fail-closed** — `services/auth` returns deny on Redis
  unreachable for principal-revocation checks (`revoke:user:<id>`).
  (Phase 6.6.)

### Removed

- **`auth_providers` table + per-tenant SSO admin RPCs.** Replaced by
  global SSO. (RM-003.)
- **`tenant_domains` table + custom-domain CRUD + Host-header tenant
  resolution.** (RM-001.)
- **`seed_dev_admin.sql` migration + hardcoded admin row.** Replaced by
  `registry-auth bootstrap`. (Decision #28.)
- **`(admin, org, '*')` marker-scope convention.** Replaced by
  `users.is_global_admin`. (Decision #26.)

### Security

- **SEC-038/039** — every outbound gRPC dial pins `serverName` via
  `loader.BaseConfig.MTLSClientCreds(serverName)`; fail-closed on cert
  load. (#181, #182.)
- **SEC-040..043** — SSO subject-id binding closes the email-pivot ATO
  surface. (#201.)

### Fixed

- **REM-014 lint backlog** closed across all 13 services. (#166.)
- **REM-020 #2** — per-service `go mod tidy` sweep + `ci-tidy-check`
  workflow. (#163.)
- **Scanner Trivy bump** to 0.71.2 + zlib1g CVE allowlisted. (#168.)

### Deferred to a future minor release

These were in scope for REDESIGN-001 but explicitly descoped or anchored
in [`futures.md`](futures.md) at close-out — they will land in a later
2.x release.

- **RED-FU-016 (LOW)** — SAML library upgrade `crewjam/saml` v0.4 →
  v0.5.x. No forcing function on the v0.4 line; revisit on advisory.
- **RED-FU-017 (LOW)** — Audit checkpoint signing. Adds a third
  signature layer over the hash-chain; parked pending operator demand.
- **RED-FU-018 (PARKED)** — Scanner in-process sandbox (seccomp /
  landlock / cgroup / netns). Container-boundary posture in the new
  runbook covers ~80% of the original threat. Revisit on container
  runtime CVE trigger.

[Unreleased]: https://github.com/steveokay/oci-janus/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/steveokay/oci-janus/compare/v2.0.0-rc1...v2.0.0
[2.0.0-rc1]: https://github.com/steveokay/oci-janus/releases/tag/v2.0.0-rc1
