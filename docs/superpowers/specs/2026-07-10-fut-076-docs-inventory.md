# FUT-076 — Documentation Readiness Inventory

> **Purpose:** the master "don't miss anything" map for the FUT-076 live-docs
> initiative. Built 2026-07-10 from a 6-agent code+docs sweep. It captures the
> **complete surface that must be documented** (every API route, UI page,
> integration, env var, MCP tool, event) plus the **gap worklist** for the
> existing `docs/`. When we write the user-facing docs site + UI/end-user guide,
> this is the checklist.
>
> **Truth lives in code.** Every section cites the source files so a doc author
> verifies against the code, never against this snapshot.

---

## 0. Headline findings

1. **14 services on disk, only 13 catalogued.** `services/mcp` (registry-mcp) is a real shipped service but is **missing from `docs/SERVICES.md` §4 and CLAUDE.md** (both say "13 services"). → doc fix.
2. **No OpenAPI/Swagger spec exists anywhere.** The ~110 BFF routes + the registry-auth HTTP routes are the only source of truth. Generating an OpenAPI 3 spec is the natural backbone for the "docs link" + API reference.
3. **The API is split across two services.** The dashboard/CLI hit **both** `registry-management` (BFF, `/api/v1/*` business routes) **and** `registry-auth` directly (login, SSO, MFA, profile, sessions, API keys, service accounts, workload tokens). A docs "API reference" must cover both.
4. **Docs skew developer/operator-facing.** There is **no getting-started/quickstart page and no UI/end-user guide** today. The reference layer (`docs/*.md`) is strong; the onboarding + UI layer must be written from scratch (FUT-076 slices 2 + 3).
5. **Doc staleness clusters** in: SSO (SAML.md + SERVICES.md §2 still describe the dropped per-tenant `auth_providers` table vs. the global `global_sso_config` model — ADR-0027/RM-003), removed custom domains (RM-001), and the newest KEK env vars missing from the operator deployment docs.

---

## 1. System at a glance

- **14 Go services** (`services/*`): auth, core, storage, metadata, proxy, scanner, signer, webhook, audit, gc, tenant, gateway, management (BFF), **mcp**.
- **Deployment modes** (`DEPLOYMENT_MODE`): `single` (default, OSS self-host, one bootstrap tenant) / `multi` (SaaS posture). Wire format + schema identical; single-mode injects the bootstrap tenant id everywhere.
- **Frontend:** React/TypeScript SPA (`frontend/`), TanStack Router, served by nginx in compose at `localhost:3000`.
- **Data plane:** OCI Distribution `/v2/` API on `registry-core`; Docker token auth on `registry-auth` `/auth/token`.

---

## 2. HTTP API surface (the "docs link" / API-reference target)

**No OpenAPI spec exists** — action item: generate one (hand-authored or codegen) and serve it (e.g. `/openapi.json` + a Swagger/Redoc page). Neither service currently serves `/docs`, `/openapi.json`, `/version`, or (for the BFF) `/metrics`.

### 2.1 registry-management BFF — `/api/v1/*` (~110 routes)

Source: `services/management/internal/handler/*.go` (routes in `handler.go`). Listen `HTTP_ADDR` (default `:8085`). Every authenticated route runs behind `RequireAuth` (validates Bearer via `registry-auth.ValidateToken`) + a 20 rps/user rate limiter; role checks happen inside handlers. **Tenant id is always from the JWT, never the body.** Optional route groups 404 "route disabled" when their gRPC-addr env is unset (see §7).

| Area | Routes (method path) | Auth |
|---|---|---|
| **Health/Meta** | `GET /healthz` (public); `GET /api/v1/deployment-info` (public); `GET /api/v1/registry-info`; `GET /api/v1/stats`; `GET /api/v1/stats/storage`; `GET /api/v1/stats/analytics`; `GET /api/v1/workspace/me`; `GET /api/v1/me/abilities`; `GET /api/v1/notifications` | mixed |
| **Repositories** | `GET/POST /api/v1/repositories`; `GET/PATCH/DELETE /api/v1/repositories/{org}/{repo}`; `POST /api/v1/admin/orgs/{org}/claim` | auth + role |
| **Tags/Artifacts** | `GET /…/tags`; `DELETE /…/tags/{tag}`; `DELETE /…/tags` (bulk); `GET /…/tags/{tag}/manifest`; `…/referrers`; `…/chart`; `…/chart/download`; `…/sbom`; `…/builds`; `POST /…/tags/{tag}/promote`; `GET /…/promotions`; `POST/DELETE /…/tags/{tag}/pin` | auth + writer/admin |
| **Access/RBAC** | `GET/POST /api/v1/orgs/{org}/members`, `DELETE …/{assignmentID}`; repo members trio; `GET/POST/PATCH/DELETE /api/v1/access/oidc-trust[/{id}]`; `GET/PUT /api/v1/access/token-policy`; `GET /api/v1/access/review/stale`; `POST /api/v1/access/review/snooze` | auth + admin |
| **Scanning** | `GET/POST /…/tags/{tag}/scan`; `POST /…/{repo}/scan`; `POST /api/v1/orgs/{org}/scan`; `GET /api/v1/security/{overview,vulnerabilities,scans,remediation}`; `GET/PUT /api/v1/security/policies`; report routes `POST /security/reports/generate`, `GET /security/reports[/{id}][/download/{pdf,sbom}]`; org/repo scan-policy CRUD; `POST /…/quarantine/lift`; `GET/POST /api/v1/scan-by-digest/{digest}` | auth + role; scanner-gated |
| **Signing** | `GET /…/tags/{tag}/signature`; `POST /…/tags/{tag}/sign`; repo trusted-keys CRUD; `GET /…/recent-signers`; `GET /api/v1/signatures-by-digest/{digest}`; `POST /api/v1/sign-by-digest/{digest}` | auth + repo-admin; signer-gated |
| **GC/Retention** | `GET /api/v1/admin/gc/{status,runs}`, `POST /api/v1/admin/gc/run`; repo retention policy CRUD + `dry-run` + `preview` + `run` + `runs[/{id}]`; org retention policy CRUD | auth + admin; gc-gated |
| **Proxy cache** | `GET /api/v1/proxy/cache[/stats][/{id}]`, `DELETE …/{id}`; per-upstream scan/sign policy CRUD; `GET /api/v1/proxy/cache/{scan,sign}-policies` | workspace-admin; proxy-gated |
| **Notifications** | `GET/PATCH /api/v1/users/me/notification-preferences`; `GET/PUT /api/v1/notifications/email-transport`, `POST …/test`, `GET …/email-deliveries`; `GET/PUT /api/v1/notifications/webhook-config`, `POST …/test` | self / email-admin |
| **Webhooks (outbound)** | `GET/POST /api/v1/webhooks`, `PATCH/DELETE …/{id}`, `GET …/{id}/deliveries[/{delivery_id}]`, `POST …/{id}/test`, `POST …/{id}/rotate-secret` | auth + admin; webhook-gated |
| **PR-registry (FUT-023)** | `POST /webhooks/scm/github/pr` (**public**, HMAC downstream); `GET/PUT /api/v1/pr-registry/config`; `GET /api/v1/pr-registry/namespaces` | public / platform-admin |
| **Tenant/Users** | `GET /api/v1/tenant/users`, `POST …/invite`, `POST/DELETE …/{id}/disable`, `POST …/{id}/elevate/{org}`; `PUT /api/v1/admin/tenants/{id}/quota`; `GET/PATCH /api/v1/admin/tenants[/{id}]`; `POST/DELETE /api/v1/admin/tenants` (**multi-mode only** → 405 in single) | tenant/platform-admin |
| **Scanner admin** | `GET /api/v1/admin/scanners[/active,/health]`, `PATCH …/active`, `POST …/test` | platform-admin; scanner-gated |
| **Analytics/Activity** | `GET /…/{repo}/activity`; `GET /…/{repo}/analytics` | auth |
| **Audit export (SIEM)** | `GET/PUT/DELETE /api/v1/workspace/me/audit-export`, `POST …/test`, `POST …/drain` | workspace-admin |

### 2.2 registry-auth — direct HTTP (login / SSO / MFA / profile / sessions / keys / SAs / workload)

Source: `services/auth/internal/handler/{http,sso,saml,http_mfa,http_service_accounts,http_workload_token,http_sessions,http_users_me,http_access_activity}.go`. **Two Bearer forms:** `Bearer <RS256 JWT>` and `Bearer key.<uuid>.<64-hex>` (API key → empty roles, so admin routes cleanly 403).

| Area | Routes | Auth |
|---|---|---|
| **Auth/Login** | `GET,POST /auth/token` (Docker RFC-7235 token, Basic auth, 300s OCI JWT); `POST /api/v1/login` (3-way: `{token}` / `{mfa_required,challenge_token}` / `{mfa_setup_required,setup_token}`; 423 on lockout); `POST /api/v1/login/mfa`; `POST /api/v1/logout`; `POST /api/v1/token/refresh`; `POST /api/v1/users` (create user, admin) | public / auth / admin |
| **SSO OAuth** | `GET /api/v1/auth/providers` (public list); `GET /auth/oauth/{id}/start`; `GET /auth/oauth/{id}/callback` → `302 {next}?sso_token=<jwt>` | public |
| **SSO SAML** | `GET /auth/saml/{id}/start`; `POST /auth/saml/{id}/acs` → `302 …?sso_token=`; `501 NOTCONFIGURED` when SP cert/key unset | public |
| **MFA/TOTP** | `GET /api/v1/users/me/mfa`; `POST …/mfa/enroll`; `POST …/mfa/verify`; `DELETE …/mfa`; `POST …/mfa/backup-codes/regenerate` | auth / mfa_setup token |
| **Profile** | `GET/PATCH /api/v1/users/me`; `POST /api/v1/users/me/password`; `POST /api/v1/users/me/onboarding/complete` | auth |
| **Sessions** | `GET /api/v1/users/me/sessions`; `DELETE …/sessions/{sid}`; `POST …/sessions/revoke-others` | auth (JWT) |
| **API keys** | `POST/GET /api/v1/apikeys`; `DELETE /api/v1/apikeys/{id}` | auth / admin (SA path) |
| **Service accounts** | `GET/POST /api/v1/service-accounts`; `GET/PATCH/DELETE …/{id}`; `POST …/{id}/scopes/preflight`; `GET/POST …/{id}/api-keys`, `DELETE …/{id}/api-keys/{keyID}` | admin; `501` when unwired |
| **Workload identity (FUT-001)** | `POST /auth/token/workload` (OIDC JWT → registry token; **the OIDC JWT is the credential**) | public |
| **Access activity** | `GET /api/v1/access/activity` | auth |
| **JWKS/Meta** | `GET /.well-known/jwks.json` (public); `GET /healthz`; `GET /metrics` (`:9090`); gRPC `grpc.health.v1` | public |

*No OpenID discovery endpoint is served (registry-auth is an OIDC **consumer**, not provider). No `/readyz`.*

### 2.3 registry-core — OCI Distribution `/v2/` (the data plane)

Push/pull/delete/list/referrers per OCI Distribution Spec v1.1. `checkAccess` on every handler; tag-immutability preflight on `PutManifest`; signature/CVSS admission gates. Docker clients authenticate via the `registry-auth` token flow (`WWW-Authenticate: Bearer realm=<AUTH_REALM>`). Document as "the registry endpoint you `docker login` / `docker push` against."

---

## 3. Dashboard UI map (for the UI/end-user guide — FUT-076 slice 3)

Source: `frontend/src/routes/*` (TanStack file routes), `components/shell/{sidebar,topbar,app-shell}.tsx`. Auth gate in `_authenticated.tsx`.

**Public:** `/login` (SSO buttons + credential form + 2-step MFA challenge/forced-enrol).

**Sidebar groups (operator mental-model, not microservice):**
- **Registry:** `/` Dashboard (KPIs, analytics, first-run → `/getting-started`), `/getting-started` (6-step wizard), `/repositories`, `/repositories/{org}/{repo}` (tabs: Tags·Members·Retention·Promotions·Settings), `/repositories/{org}/{repo}/tags/{tag}` (tabs: Security·Push history·Layers·Signing·Referrers·Chart[Helm]), `/helm`, `/workspace/proxy-cache[/{id}]`.
- **Security:** `/security/{overview,vulnerabilities,scans,signing,remediation,policies,reports}`.
- **Governance:** `/activity`, `/workspace/audit-export` (SIEM).
- **Integrations:** `/webhooks[/{id}]`.
- **Access:** `/members` (orgs), `/orgs/{org}/{members,settings}`, `/tenant/users`, `/api-keys` (+ `/service-accounts`, `/activity`, `/trust`, `/helpers`, `/policies`, `/review`).
- **Settings (cog):** `/settings/{workspace,scanning,housekeeping,notifications,integrations,platform}` — tab visibility is role+mode gated (Integrations = global-admin; Platform = multi + global-admin; Scanning/Housekeeping = single-mode admins).
- **Profile:** `/profile` (identity, password, personal API keys, MFA, sessions).

**Topbar (R→L):** email-activity menu · notifications bell · theme toggle · user menu (Profile / Sign out; SA principals get a non-clickable bot avatar).

**Deployment-mode differences:** Platform tab multi-only; Scanning/Housekeeping single-only; tenant UUID chip multi-only; tenant switcher / plan badge / custom-domain surfaces all absent (REDESIGN-001 + RM-001).

**Placeholders (no backend yet):** `/security/signing` rollup, repo Settings "General".

---

## 4. Integrations catalog (9 surfaces — FUT-076 slice 4)

| # | Integration | Select/enable | Key config | Code |
|---|---|---|---|---|
| 1 | **Storage** | `STORAGE_DRIVER` | **Only `minio` (+ S3-compatible via MinIO endpoint) and `filesystem` are implemented**; `s3`/`gcs`/`azure` validate but fail at init. `STORAGE_MINIO_*`. | `services/storage`, `libs/storage/driver` |
| 2 | **SSO** | `global_sso_config` table (SQL/seed — **no REST admin**, RM-003) | OAuth (Google/GitHub/MS/generic-OIDC, PKCE S256) + SAML 2.0 SP. `SSO_CREDENTIAL_KEY_HEX`, `SSO_BASE_URL`, `SSO_SAML_TRUST_EMAIL`, `SAML_SP_CERT/KEY_PATH`. One IdP per kind. | `services/auth/…/sso.go,saml.go`; `docs/SAML.md`, `docs/AUTH.md` |
| 3 | **Scanners** | `SCANNER_PLUGIN_PATH` + `SCANNER_PLUGIN_CHECKSUM` | External-process JSON-RPC adapter (dev-stub/trivy/grype/clair). Scan policies (`block_on_severity`) + SPDX/PDF reports. | `services/scanner`, `libs/scanner/plugin`; `docs/SCANNER.md` |
| 4 | **Signing** | `SIGNER_KEY_BACKEND` (`env`/`vault`; KMS wired-not-shipped) | **Cosign only** (Notary v2 deferred). Vault Transit is the documented posture. `require_signature` admission + trusted-key allowlist. | `services/signer`; `docs/SIGNING.md` |
| 5 | **Outbound webhooks** | per-endpoint in `webhook_endpoints` | HMAC `X-Registry-Signature: sha256=…`; HTTPS-only + SSRF blocklist. `CREDENTIAL_KEY_HEX`. | `services/webhook` |
| 6 | **Notifications** | per-tenant config tables | Bell (always) + Email (Resend/SMTP/Gmail, `NOTIFY_EMAIL_KEY_HEX`, needs `AUTH_GRPC_ADDR` for recipient resolution) + Org webhook (`NOTIFY_WEBHOOK_KEY_HEX`). | `services/audit` |
| 7 | **SCM PR registries (FUT-023)** | `pr_registry_config` (admin UI) | GitHub PR webhook `X-Hub-Signature-256`; `PR_REGISTRY_KEY_HEX`; ephemeral `pr-<repo>-<N>` org lifecycle. | `services/metadata/…/prregistry`, `services/management` |
| 8 | **Pull-through cache** | `upstream_registries` table | Upstream creds AES-GCM (`CREDENTIAL_KEY_HEX`); digest-verified; `store.queued` retry. | `services/proxy` |
| 9 | **Observability** | `OTEL_EXPORTER` (`stdout/jaeger/tempo/datadog`) | OTLP/TLS to `OTEL_ENDPOINT`; Prometheus `/metrics` on `:9090`. | `libs/observability` |

**HMAC header conventions to document clearly:** inbound GitHub → `X-Hub-Signature-256`; all outbound (webhook + notification) → `X-Registry-Signature`. Both `sha256=<hex>` HMAC-SHA256.

---

## 5. MCP connectivity (FUT-076 slice 5)

`registry-mcp` (`services/mcp`) — MCP server on the official Go SDK. **stdio** (default) or **http** transport (`MCP_TRANSPORT`; HTTP default `:8092`). stdio invariant: logs go to **stderr only** (stdout is JSON-RPC). No DB/RabbitMQ/mTLS — it's a pure HTTP client of the BFF.

**12 read-only tools** (`registry_` prefixed): `list_repositories`, `list_tags`, `get_manifest`, `list_service_accounts`, `list_stale_keys`, `get_scan_report`, `list_signatures`, `list_audit_events` (500-cap), `list_promotions`, `ping`, `version`, `get_deployment_info`. Source: `services/mcp/internal/tools/*`.

**Config/auth:** `MCP_MANAGEMENT_URL`, `MCP_API_KEY` (SA key `key.<uuid>.<hex>`), `MCP_TENANT_ID`. Two auth planes: (a) MCP→BFF via the API key + `X-Tenant-ID`; (b) client→MCP is **unauthenticated** by protocol (stdio owns the process; HTTP must sit behind a proxy). Seed doc: `docs/MCP.md` (covers the 12 tools + Claude Desktop/Cursor setup; gaps: Linux path, native-binary invocation, `:8087`/`:8092` reconciliation).

---

## 6. Async events (RabbitMQ) — reference

Exchange `registry.events` (topic, durable); DLX `registry.dlx`. Envelope `events.Event{id,type,tenant_id,occurred_at,version,payload}`. Publishers confirm-mode; consumers manual-ack; 7-day TTL; no secrets in payloads. The **audit trail is the near-universal consumer** (`mapEvent`; spec-lint enforces every routing key maps or is `// audit: skip`). Full catalogue: `docs/EVENTS.md` + `libs/rabbitmq/events/events.go`. Key families: `push.*`, `pull.image`, `manifest/tag.deleted`, `scan.*`, `webhook.*`, `gc.run.*`, `cache.populated`/`store.queued`, `image.signed`/`image.promoted`, `repo.cvss_policy.changed`, `tenant.*`, `rbac.role_*`, `retention.*`, `service_account.lifecycle`, `auth.{oidc_trust,workload_token,token_policy,key_revoked,access_review}.*`, `pr.namespace.*`.

**Dead-code note:** `RoutingTenantDomainVerified = "tenant.domain.verified"` still exists in `events.go` but custom domains were removed (RM-001) — nothing publishes it. → code cleanup (remove the constant + the EVENTS.md row together), not a docs-only edit.

---

## 7. Config & env reference (FUT-076 slice 6)

**Shared (every service, `libs/config/loader.BaseConfig`):** `LOG_LEVEL/FORMAT`, `GRPC_ADDR`(:50051), `HTTP_ADDR`(:8080), `METRICS_ADDR`(:9090), `MTLS_REQUIRED` + `MTLS_{CA_CERT,CERT,KEY}_PATH` + `MTLS_PEER_CN_ALLOWLIST`, `OTEL_*`, `DEPLOYMENT_MODE`, `TENANT_GRPC_ADDR`. DB services add `DB_DSN` (sslmode=require) + pool tuning + `DB_DSN_REPLICA`.

**BFF optional-route gates** (unset → those routes 404): `TENANT_GRPC_ADDR`, `WEBHOOK_GRPC_ADDR`, `SIGNER_GRPC_ADDR`, `SCANNER_GRPC_ADDR`, `GC_GRPC_ADDR`, `PROXY_GRPC_ADDR`, `CORE_GRPC_ADDR`, `PLATFORM_ADMIN_TENANT_ID`, `PUBLIC_BASE_URL`, `PLATFORM_HOST`; `DEPLOYMENT_MODE` gates tenant create/delete.

**KEK inventory** (all AES-256-GCM, 64-hex, swept by `rotate-kek`; most fail **closed** on bad length):

| KEK env var | Service | Seals |
|---|---|---|
| `SSO_CREDENTIAL_KEY_HEX` | auth | OAuth client secrets / SAML |
| `MFA_SECRET_KEY_HEX` | auth | TOTP secrets (**required**, 32 bytes) |
| `CREDENTIAL_KEY_HEX` | webhook, proxy | webhook HMAC secrets / upstream creds |
| `NOTIFY_EMAIL_KEY_HEX` | audit | Resend/SMTP creds |
| `NOTIFY_WEBHOOK_KEY_HEX` | audit | notification webhook secret |
| `AUDIT_EXPORT_SECRETS_KEY_HEX` | audit | SIEM export creds |
| `PR_REGISTRY_KEY_HEX` | metadata | PR-registry webhook secret |

Per-service specifics: see the agent-sourced detail — auth (JWT ring/trio, Redis, SSO, MFA), storage (`STORAGE_*`), core (upstream gRPC addrs + `AUTH_REALM` + `PULL_EVENT_SAMPLE_RATE`), scanner (plugin path/checksum/workers), signer (backends + Vault/KMS), audit (notify KEKs + `AUTH_GRPC_ADDR` + `PLATFORM_HOST`), gc (modes + grace + advisory-lock DSN), proxy (upstream timeouts), mcp (tiny). **Gotcha:** several config packages expose vars **not in `.env.example`** (audit `AUDIT_EXPORT_SECRETS_KEY_HEX`/`RABBITMQ_MGMT_URL`, gc `GC_ADVISORY_LOCK_DB_DSN`/`RETENTION_GRACE_*`, shared `MTLS_PEER_CN_ALLOWLIST`/`OTEL_INSECURE`/`TRUSTED_PROXY_CIDRS`) — the env reference must read the config structs, not just `.env.example`.

---

## 8. Existing `docs/` — coverage map + gap worklist

**Current & strong (reuse as reference layer):** AUTH.md (MFA/sessions/KEK-rotation — best-maintained), WORKLOAD-IDENTITY.md, CREDENTIAL-HELPERS.md, TOKEN-POLICIES.md, ADMISSION.md, IMAGE-PROMOTION.md, MCP.md, SIEM-EXPORT.md, DATABASE.md, OBSERVABILITY.md, GRPC-CONVENTIONS.md, HARDENING-CHECKLIST.md, MIGRATION-v1-to-v2.md, EVENTS.md (FUT-023 covered), DEPLOYMENT.md, CI-CD.md.

**Doc-fix worklist (prioritized):**

| # | Pri | File | Fix |
|---|---|---|---|
| 1 | P0 | SERVICES.md | Add **§14 registry-mcp** + TOC row (service exists, uncatalogued). |
| 2 | P0 | CLAUDE.md + README.md | "13 services" → **14**; add registry-mcp to the §4 catalogue table. |
| 3 | P0 | SERVICES.md §2 | Replace the dropped per-tenant `auth_providers(tenant_id,…)` schema + `/api/v1/admin/sso/*` CRUD with the **global `global_sso_config`** model (ADR-0027/RM-003). |
| 4 | P0 | SAML.md | Reconcile with global SSO — rewrite the per-tenant `auth_providers`/`tenant_id` schema+API to `global_sso_config`; fix the §9 cert-rotation runbook referencing a metadata endpoint the doc says isn't implemented. |
| 5 | P1 | SERVICES.md §12/§13 | Remove custom-domain surfaces (RM-001): `/workspace/me/domains` routes, `ListTenantDomains/VerifyDomainNow/SetPrimaryDomain/DeleteDomain` gRPC, "custom domain provisioning" purpose line. |
| 6 | P1 | SELF-HOSTING.md | Remove custom domains from headline + pitfalls; refresh "Last updated". |
| 7 | P2 | SELF-HOSTING.md + DEPLOYMENT.md | Add the new KEK env vars (`PR_REGISTRY_KEY_HEX`, `NOTIFY_EMAIL_KEY_HEX`, `NOTIFY_WEBHOOK_KEY_HEX`, `MFA_SECRET_KEY_HEX`) or a pointer to where they're defined. |
| 8 | P2 | infra/runbooks/kek-rotation.md | Add the 3 new KEK domains (metadata PR-registry, audit email + webhook). |
| 9 | P2 | ACCESS-REVIEW.md | "Send reminders → deferred to FUT-019 / not wired" is stale — FUT-019 email shipped (#288). |
| 10 | P3 | SCANNER.md | Reconcile §2 vs §7 on the `/admin/scanner` adapter-swap UI (it **shipped** — FE-API-044..047). |
| 11 | P3 | SIGNING.md + README/CLAUDE.md | Reconcile "Cosign + Notary v2" claim — only Cosign is implemented; Notary v2 deferred. |
| 12 | P3 | README.md + CLAUDE.md | Feature lists under-represent TOTP MFA + PR-registry + notification channels — add by name. |
| 13 | P3 | TESTING.md | Coverage table stamped 2026-06-21 — refresh or mark measured. |
| 14 | cleanup | EVENTS.md + events.go | Remove dead `tenant.domain.verified` routing key (constant + doc row together — code change, not docs-only). |

---

## 9. New user-facing docs to write (FUT-076 slices 1–3, 6)

None of these exist today:
- **Docs-site scaffold + publish pipeline** (slice 1) — pick a generator, fold `docs/*.md` in as reference, publish on merge.
- **Getting-started / quickstart** (slice 2) — install → bootstrap admin → `docker login` → push/pull → see in UI. Seeds: README quick-start, `infra/runbooks/bootstrap-first-admin.md`, SELF-HOSTING.md.
- **UI / dashboard walkthrough** (slice 3) — page-by-page from §3, with screenshots. **This is the next task.**
- **Docs landing page / IA** — there is no index inside `docs/` today (only the README table); the site needs one.
- **Published API reference** (slice 6) — from §2 (both services) + an OpenAPI spec; wire the "docs link" on the BFF.
- **Integrations catalog pages** (slice 4) — from §4. **MCP "connect your agent" guide** (slice 5) — promote `docs/MCP.md`.

---

## 10. Cross-cutting doc actions

- **Generate an OpenAPI 3 spec** (BFF + auth) → serve `/openapi.json` + Redoc/Swagger; this is the backbone of the "docs link on BFF."
- **Add a BFF docs endpoint** (`/docs` or link in `deployment-info`) pointing at the published site.
- **Screenshots/GIFs** for the UI guide + README hero (HYG-001) + architecture diagram (HYG-006).
- Keep the site **self-contained + versioned to releases**.

---

> Generated from a 6-agent sweep on 2026-07-10 (BFF API, registry-auth API, UI pages, integrations+env, MCP+events, docs freshness). Re-verify against code before publishing any figure.
