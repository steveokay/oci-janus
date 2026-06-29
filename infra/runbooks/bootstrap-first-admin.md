# Bootstrap the first admin

> **Audience:** operators deploying a fresh OCI Janus stack — first install, new
> tenant, or disaster-recovery rebuild. Local dev setup uses the same flow.

Starting in REDESIGN-001 (Phase 3.1 / RM-008, 2026-06-27), the deployment ships
**without** a baked-in admin account. The previous dev-seed migration that
created `admin@dev.local` with a known argon2 hash has been deprecated — it was
shipping a known credential in the production Docker image (Top-5 #5 security
finding).

The replacement is a one-shot CLI subcommand: `registry-auth bootstrap`.

---

## Local dev (Docker Compose)

```bash
# 1. Bring up the stack (Postgres + auth + tenant + the rest)
docker compose -f infra/docker-compose/docker-compose.yml up -d

# 2. Wait for healthchecks to pass (~30s)
docker compose -f infra/docker-compose/docker-compose.yml ps

# 3. Create the admin user (idempotent — safe to re-run)
make dev-bootstrap
```

That creates `admin@dev.local` with password `Admin1234!` on tenant
`Development` (UUID `98dbe36b-…` — same as the legacy dev-seed so existing dev
workflows keep working without re-learning credentials).

### What `docker compose up -d` does on its own

As of RED-FU-007 (PR #184, 2026-06-29) the compose stack ships a
`registry-bootstrap` one-shot container that runs automatically and seeds
the *tenant-side* rows so the Phase 3.4 services can start in single mode:

- `tenants` row (`id=98dbe36b-…`, `name=Development`, `slug=development`)
- `tenant_policies` row with defaults
- `deployment_metadata.bootstrap_tenant_id` = `98dbe36b-…`

The compose seed deliberately **does not** create the admin user — the auth
bootstrap CLI is still needed for that (it owns the argon2 hashing + audit
trail). Step 3 above runs the CLI via `docker exec` into the auth container.

### Using different credentials in dev

Because `registry-bootstrap` already wrote `bootstrap_tenant_id=98dbe36b-…`,
any CLI invocation against the running stack **must** pass that same UUID via
`--tenant-id` — otherwise single-mode's idempotency check rejects the call
with `exit 2`. The CLI also needs the two DB DSNs in its env. Full example:

```bash
echo 'YourPassword' | MSYS_NO_PATHCONV=1 docker exec -i \
    -e AUTH_DB_DSN="postgres://registry:registry@postgres:5432/registry_auth?sslmode=prefer" \
    -e TENANT_DB_DSN="postgres://registry:registry@postgres:5432/registry_tenant?sslmode=prefer" \
    -e DEPLOYMENT_MODE=single \
    docker-compose-registry-auth-1 \
    /server bootstrap \
    --admin-email you@example.com \
    --admin-username you \
    --admin-password-stdin \
    --tenant-id 98dbe36b-ef28-4903-b25c-bff1b2921c9e \
    --tenant-name "MyOrg"
```

- `MSYS_NO_PATHCONV=1` is only needed on Git Bash / MSYS shells on Windows;
  Linux + macOS + WSL can omit it. It stops the shell rewriting `/server` to
  a host path.
- The `--tenant-name` value WILL be ignored if the tenant row already exists
  (the CLI uses `INSERT INTO tenants … ON CONFLICT (id) DO NOTHING`) — but
  the admin user is still created against the existing tenant. To force a
  fresh tenant name in dev, tear the volume down first: `docker compose down -v`.
- The tenant UUID is printed on stdout on success.

The first compose-stack run records the tenant id in
`deployment_metadata.bootstrap_tenant_id`; subsequent bootstrap CLI runs in
`DEPLOYMENT_MODE=single` against a non-matching `--tenant-id` are rejected.

---

## Production / self-hosted

The same CLI ships inside the `registry-auth` image. Recommended pattern is to
run it as a one-shot container after migrations + before opening external
traffic:

```bash
# Assuming AUTH_DB_DSN, TENANT_DB_DSN, DEPLOYMENT_MODE are set in the
# registry-auth deployment's environment.
kubectl run registry-auth-bootstrap --rm -i --tty \
    --image=ghcr.io/steveokay/oci-janus/registry-auth:vX.Y.Z \
    --restart=Never \
    --env-from=secret/registry-auth-env \
    --command -- /server bootstrap \
        --admin-email admin@yourcompany.com \
        --admin-username admin \
        --admin-password-stdin \
        --tenant-name "YourCompany"
# Type the password, then Ctrl-D. The CLI prints the generated UUIDs on
# success and exits 0.
```

For Helm-managed deployments, package the same call as a one-shot
`Job` with `helm.sh/hook: post-install` so it runs after the schema
migrations complete.

---

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Bootstrap succeeded (or idempotent re-run on a matching tenant id). |
| 1 | Infrastructure error — DB unreachable, transaction failed, etc. Retry after fixing the cause. |
| 2 | Operator-input error — admin already exists, single-mode tenant conflict, invalid email/username, empty password. Do NOT auto-retry; inspect logs and fix the input. |

---

## Idempotency contract

- **Same admin email + same tenant** → exit 2, "admin already exists". You can
  delete the admin out-of-band via SQL and re-run.
- **`DEPLOYMENT_MODE=single` + different `--tenant-id`** → exit 2, "deployment
  already bootstrapped". `single` mode supports exactly one tenant; the
  `bootstrap_tenant_id` is locked in the first time.
- **`DEPLOYMENT_MODE=multi` + different `--tenant-id`** → succeeds. Multi-tenant
  deployments can bootstrap additional tenants via the same CLI.

---

## Related

- REDESIGN-001 plan: `.claude/plans/2026-06-26-single-tenant-redesign.md` Phase 3.1.
- The compose-stack `registry-bootstrap` one-shot container that seeds the
  tenant-side rows automatically is documented inline in
  `infra/docker-compose/docker-compose.yml` (`registry-bootstrap` service
  block). It runs against `DEPLOYMENT_MODE=single` only — production deploys
  still rely on this CLI runbook end-to-end.
- The platform-admin role assignment migrated from the legacy
  `(admin, org, '*')` magic-string marker to the typed
  `users.is_global_admin` column in Phase 5.1 (PR #134, 2026-06-28). The
  CLI now sets `is_global_admin=true` on the seeded admin user directly —
  the operator UX is unchanged.
- Audit event: bootstrap is **not yet captured** in the audit pipeline.
  Phase 6.3 (PR #130) added the broader event catalogue but the bootstrap
  flow is still operator-supervised and unlogged — recommended interim
  practice is to record the CLI invocation in your deployment changelog
  until a dedicated `auth.bootstrap.completed` event lands.
