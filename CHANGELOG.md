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
  Prerequisite for the planned KEK-rotation tool (futures.md
  RED-FU-015). (Decision #29, Phase 6.4.)
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

- **RED-FU-015 (HIGH)** — KEK rotation tool. Phase 6.4 shipped the
  version byte; the rotation CLI itself is the planned next pickup
  after v2.0.0 ships.
- **RED-FU-016 (LOW)** — SAML library upgrade `crewjam/saml` v0.4 →
  v0.5.x. No forcing function on the v0.4 line; revisit on advisory.
- **RED-FU-017 (LOW)** — Audit checkpoint signing. Adds a third
  signature layer over the hash-chain; parked pending operator demand.
- **RED-FU-018 (PARKED)** — Scanner in-process sandbox (seccomp /
  landlock / cgroup / netns). Container-boundary posture in the new
  runbook covers ~80% of the original threat. Revisit on container
  runtime CVE trigger.

[Unreleased]: https://github.com/steveokay/oci-janus/compare/v2.0.0-rc1...HEAD
[2.0.0-rc1]: https://github.com/steveokay/oci-janus/releases/tag/v2.0.0-rc1
