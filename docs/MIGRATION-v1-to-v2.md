<!--
  docs/MIGRATION-v1-to-v2.md — operator-facing upgrade guide for moving an
  existing OCI Janus deployment from the pre-REDESIGN-001 platform ("v1") to
  the post-REDESIGN-001 platform ("v2"). Read this before upgrading a running
  install. New installs should skip straight to README + infra/runbooks/.

  Sources of authority:
    - .claude/plans/2026-06-26-single-tenant-redesign.md (Phase 8.1)
    - docs/adr/0025…0030
    - infra/runbooks/bootstrap-first-admin.md
  Last updated: REDESIGN-001 Phase 8 (2026-06-30).
-->

# Migrating from v1 to v2 (REDESIGN-001)

> **tl;dr.** Both modes share the same three steps: `docker compose down`
> → run the bootstrap CLI once → `docker compose up -d`. **Multi mode** is
> the same plus a tenant-create flow after bootstrap. **In either mode** we
> recommend (optionally) setting `MTLS_PEER_CN_ALLOWLIST` per gRPC server
> after you've confirmed every legitimate caller's certificate CN — see
> Step 6. No active re-encryption pass is required (see Step 3). Downtime:
> 5–15 min, dominated by snapshot time.

For operators upgrading an existing v1 deployment. New installs: start at
[`README.md`](../README.md). v1 = any commit before REDESIGN-001 lands; v2 =
a release tagged `v2.0.0-rc1` or later.

---

## Before you start

1. **Snapshot Postgres.** Each service owns its own database.

   ```bash
   for db in registry_auth registry_tenant registry_metadata registry_proxy \
             registry_webhook registry_audit registry_scanner registry_gc; do
     docker exec docker-compose-postgres-1 \
       pg_dump -U registry -Fc "$db" > "backup-${db}-$(date +%F).dump"
   done
   ```

   Kubernetes: use your existing managed-Postgres snapshot mechanism (RDS,
   GCP automated backups, Velero, etc.).

2. **Snapshot the blob store.** MinIO / S3 / GCS / Azure bucket data is the
   authoritative copy of every blob + manifest. Use your provider's snapshot
   or `mc mirror` to a recovery bucket.

3. **Pre-check running v1 versions** (record what's deployed so rollback has
   a target tag):

   ```bash
   docker images | grep registry-
   # Kubernetes equivalent: kubectl get deploy -l app.kubernetes.io/part-of=oci-janus -o yaml | grep image:
   ```

4. **Git-tag the v1 commit** so rollback is unambiguous:

   ```bash
   git tag -a pre-redesign-001 -m "Last commit before v2 upgrade"
   git push origin pre-redesign-001
   ```

5. **Decide single vs. multi mode now** (changes Step 4) by reading the
   breaking-change summary below.

---

## What changed in v2

Full design rationale lives in [`docs/adr/`](adr/). Operator-relevant deltas:

- **ADR-0025** — `DEPLOYMENT_MODE` defaults to `single`. Multi-tenant
  operators MUST set `DEPLOYMENT_MODE=multi` explicitly.
  ([adr/0025](adr/0025-single-tenant-default-deployment-mode.md))
- **ADR-0026** — Platform-admin is now the typed `users.is_global_admin`
  column. Existing magic-string `(admin, org, '*')` grants migrated
  automatically. ([adr/0026](adr/0026-is-global-admin-typed-primitive.md))
- **ADR-0027** — Per-tenant `auth_providers` gone; SSO is one global
  `global_sso_config` row. See Step 5.
  ([adr/0027](adr/0027-global-sso-config.md))
- **ADR-0028** — Dev-seed admin migration deleted. First admin is created via
  the `registry-auth bootstrap` CLI.
  ([adr/0028](adr/0028-bootstrap-cli-replaces-dev-seed.md),
  [bootstrap runbook](../infra/runbooks/bootstrap-first-admin.md))
- **ADR-0029** — AES-256-GCM ciphertexts carry a `Version = 0x01` prefix; new
  writes are v1, decrypt falls back to legacy. **No re-encrypt pass required
  for v1→v2** (see Step 3).
  ([adr/0029](adr/0029-aes-gcm-version-byte-prefix.md))
- **ADR-0030** — `audit_events` gains hash-chain columns (`chain_seq`,
  `row_hash`, `prev_row_hash`); writes go via `registry_audit_app`
  INSERT-only. ([adr/0030](adr/0030-audit-hash-chain-tip-from-chain-seq.md))
- **RM-001** — Custom domains (`tenant_domains`, DNS TXT verification,
  per-domain ACME) removed. The `goose up` step drops the table. If v1
  served traffic on a custom hostname, terminate TLS for that hostname at
  your existing reverse proxy / load balancer in v2.

Complete decision log: [`CLAUDE.md` §14](../CLAUDE.md#14-decision-log).
Phase ledger:
[`.claude/plans/2026-06-26-single-tenant-redesign.md`](../.claude/plans/2026-06-26-single-tenant-redesign.md).

---

## Step 1 — Stop the platform

Quiesce all traffic first.

**Docker Compose:**

```bash
docker compose -f infra/docker-compose/docker-compose.yml down
```

This stops containers but leaves named volumes intact. Do **not** add `-v` —
that wipes the data you just snapshotted.

**Kubernetes:** scale every platform Deployment to 0 (leave Postgres / MinIO
StatefulSets running):

```bash
kubectl scale deploy -l app.kubernetes.io/part-of=oci-janus --replicas=0
kubectl wait --for=delete pod -l app.kubernetes.io/part-of=oci-janus --timeout=120s
```

---

## Step 2 — Run migrations

Each service runs `goose up` against its own database at boot — pull the v2
image, start the container, migrations run, server starts. **You do not need
to run `goose` manually** in the normal upgrade path. The Phase 5.5
`drop_auth_providers` and RM-001 `drop_tenant_domains` migrations are
applied automatically.

> **CRITICAL.** The legacy dev-seed admin migration is **gone** (ADR-0028 /
> RM-008). If you skip Step 4, the platform will start with zero admin users
> and you will not be able to log into the dashboard. Run the bootstrap CLI
> in Step 4 **before** you let traffic into the gateway.

If you operate a "migrations-first, boot-second" pipeline (out-of-band `Job`
running `goose up` before scaling deployments up), keep doing it — the
on-boot path is idempotent and will skip with "no migrations pending."

---

## Step 3 — Re-encrypt secrets (skip)

**There is no active re-encryption step for the v1→v2 upgrade.**

ADR-0029 introduced an AES-256-GCM `Version = 0x01` byte prefix.
`libs/crypto/aes.Decrypt` is "try v1, then fall back to legacy" — legacy
ciphertexts written before the upgrade continue to decrypt cleanly; the next
time the row is written it goes out as v1. So existing `auth_login_sessions`,
upstream proxy creds, OAuth `client_secret`, and SAML SP private key rows
keep decrypting with no operator intervention; new writes are tagged v1
automatically; a future KEK rotation can flip the version byte and migrate
gradually without coordinated downtime.

The dedicated "rotate the KEK and re-encrypt every row" tool is out of scope
for v2 itself — document it as a future operator workflow when KEK rotation
lands. Background: [`infra/runbooks/secret-rotation.md`](../infra/runbooks/secret-rotation.md).

---

## Step 4 — Set `DEPLOYMENT_MODE` and bootstrap

This is the only step you cannot skip.

### Decide the mode

| Operator profile | Set |
|---|---|
| Self-hosted single-tenant install (the new default) | `DEPLOYMENT_MODE=single`, or leave unset |
| Existing multi-tenant deployment, want to keep multi-tenant | `DEPLOYMENT_MODE=multi` (required — default flipped) |

Set the env var on every service that calls `loader.LoadDeploymentMode()` —
easiest path is to set it once at the Compose `.env` level or as a global
ConfigMap entry. Loader: `libs/config/loader/loader.go:229`.

### Run the bootstrap CLI

Required for the first start of either mode, and for provisioning additional
tenants in multi mode. CLI is built into the `registry-auth` image. Flag
names (verified against `services/auth/internal/bootstrap/bootstrap.go`):

```
/server bootstrap \
    --admin-email      <email>          (required)
    --admin-username   <username>       (required, ^[a-zA-Z0-9_-]{3,64}$)
    --admin-password-stdin              (required; read pw from stdin)
    --tenant-name      "<display name>" (required)
    --tenant-id        <uuid>           (optional in multi; pinned in single)
```

Docker Compose one-liner (single mode, fresh install):

```bash
printf 'YourStrongPassword\n' | docker exec -i docker-compose-registry-auth-1 \
    /server bootstrap \
    --admin-email admin@yourcompany.com \
    --admin-username admin \
    --admin-password-stdin \
    --tenant-name "YourCompany"
```

Exit codes: `0` = success or idempotent re-run, `1` = infrastructure error
(retry after fixing), `2` = operator-input error (DO NOT auto-retry —
inspect logs first).

Full CLI flag detail, idempotency rules, single-mode tenant-id pinning, and
the Kubernetes `kubectl run` pattern live in
[`infra/runbooks/bootstrap-first-admin.md`](../infra/runbooks/bootstrap-first-admin.md).

---

## Step 5 — Re-create SSO config (optional)

If v1 had per-tenant SSO via `auth_providers`, the Phase 5.5 migration
(`services/auth/migrations/20260628000003_drop_auth_providers.sql`) dropped
that table on `goose up`. **Existing OAuth + SAML configuration is not
migrated automatically.** Re-create it once in v2 via the dashboard
(`/settings/sso` after logging in as the Step 4 admin) or via SQL into
`global_sso_config` (schema:
`services/auth/migrations/20260628000001_global_sso_config.sql`).

OAuth `client_secret` and SAML SP private key are AES-256-GCM-encrypted at
write time using the ADR-0029 versioned format — `services/auth/internal/repository/global_sso_config.go`
handles it; you don't encrypt them manually. If v1 had multiple SSO providers
per tenant, pick one; single-tenant deploys typically have one IdP anyway,
which is the motivation for ADR-0027.

---

## Step 6 — mTLS hot reload + peer-CN allowlist (recommended)

Phase 6.9 added hot reload to `libs/auth/mtls.ServerTLSConfig` /
`ClientTLSConfig` — cert-manager renewals are picked up at the next TLS
handshake without restarts. No operator action required.

Phase 6.10 added the peer-CN allowlist interceptor, **opt-in per server** via
`MTLS_PEER_CN_ALLOWLIST` (CSV, e.g. `registry-core,registry-management`).
Empty/unset = no per-peer enforcement (backwards-compatible Option A);
non-empty = only the listed CNs may call this server, rejections increment
`registry_grpc_peer_cn_denied_total{method, reason}`.

Recommended rollout on production: (1) leave the env unset, deploy v2, watch
logs for legitimate caller CNs over a steady-state period; (2) build the
allowlist from observed CNs; (3) set `MTLS_PEER_CN_ALLOWLIST` per server and
roll; (4) watch `registry_grpc_peer_cn_denied_total` for unexpected denies
and add any missed CNs back.

Spec + rationale: [`CLAUDE.md` §7](../CLAUDE.md#7-authentication--security).
Implementation: `libs/middleware/grpc/peer_cn.go`.

---

## Step 7 — Restart

**Docker Compose:**

```bash
docker compose -f infra/docker-compose/docker-compose.yml up -d
docker compose -f infra/docker-compose/docker-compose.yml logs -f \
  registry-auth registry-management
```

Watch for:

- Each service's `goose up` line reporting migrations applied (or "no
  migrations pending" on idempotent re-runs).
- `registry-auth` log line confirming the bootstrap admin user is present.
- The `ci-spec-lint` job (Phase 7.3, PR #212) green on the v2 image tag — it
  asserts the shipped code still matches CLAUDE.md's claims.

**Kubernetes:**

```bash
kubectl scale deploy -l app.kubernetes.io/part-of=oci-janus --replicas=1
kubectl rollout status deploy -l app.kubernetes.io/part-of=oci-janus
```

---

## Verification

Smoke-test the upgraded platform end-to-end:

1. **OCI conformance** — `make test-conformance` in `services/core` runs the
   OCI Distribution Spec v1.1 conformance suite against the running registry.
   See [`docs/TESTING.md`](TESTING.md).
2. **Manual round-trip** — push an image, trigger a scan, sign it (Cosign),
   pull from a clean Docker host, delete the tag, then run a GC sweep
   (`POST /admin/gc/run-now` from the dashboard or management API).
3. **Deployment-info probe** — confirm the right mode is reported:

   ```bash
   curl -sS https://<your-gateway>/api/v1/deployment-info | jq .
   # Expect: { "mode": "single", … } or { "mode": "multi", … }
   ```

   Handler: `services/management/internal/handler/deployment_info.go`.
4. **Audit hash-chain sanity** — verify the per-tenant chain head advances
   when you perform an action (`audit_events` ordered by `chain_seq`).
   Implementation: `services/audit/internal/repository/hashchain.go`.

---

## Rollback procedure

Use the snapshots from "Before you start." Rollback is destructive of any v2
writes between the upgrade and the rollback.

```bash
# 1. Stop v2
docker compose -f infra/docker-compose/docker-compose.yml down

# 2. Restore each Postgres database
for db in registry_auth registry_tenant registry_metadata registry_proxy \
          registry_webhook registry_audit registry_scanner registry_gc; do
  docker exec -i docker-compose-postgres-1 \
    pg_restore -U registry -d "$db" --clean --if-exists \
    < "backup-${db}-$(date +%F).dump"
done

# 3. Restore the blob bucket from its snapshot (provider-specific:
#    mc mirror, aws s3 sync, gsutil rsync, az storage blob copy)

# 4. Redeploy the v1 image tag from "Before you start"
git checkout pre-redesign-001
docker compose -f infra/docker-compose/docker-compose.yml up -d
```

**Note on `audit_events`.** Rows written under v2 carry the hash-chain columns
(`chain_seq`, `row_hash`, `prev_row_hash`) added by Phase 6.12. v1 code
doesn't know those columns exist, and the migration that adds them
(`services/audit/migrations/20260630120000_audit_hash_chain.sql`) is
reversible per [`CLAUDE.md` §11](../CLAUDE.md#11-database-conventions).
Restoring from the v1 Postgres dump in step 2 drops the v2 rows entirely,
which is the cleanest path. If you cannot restore from a dump, manual
rollback is "downgrade each service image to the v1 tag, then run
`goose down` against the v2-applied migrations." Test in staging first —
significantly slower + riskier than restoring a snapshot.

---

## Where to go next

- [`docs/adr/README.md`](adr/README.md) — full ADR index.
- [`infra/runbooks/`](../infra/runbooks/) — bootstrap, secret rotation,
  MinIO encryption, disaster recovery.
- [`CLAUDE.md`](../CLAUDE.md) — canonical rules + service catalogue.
