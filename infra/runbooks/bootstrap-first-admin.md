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

# 3. Run the bootstrap target (idempotent — safe to re-run)
make dev-bootstrap
```

That creates `admin@dev.local` with password `Admin1234!` on tenant
`Development` (UUID `98dbe36b-…` — same as the legacy dev-seed so existing dev
workflows keep working without re-learning credentials).

To use different credentials, run the CLI directly:

```bash
echo 'YourPassword' | docker exec -i docker-compose-registry-auth-1 \
    /server bootstrap \
    --admin-email you@example.com \
    --admin-username you \
    --admin-password-stdin \
    --tenant-name "MyOrg"
```

The tenant UUID is generated (per Phase 0 Q-003) and printed on stdout. The
first run records it in `deployment_metadata.bootstrap_tenant_id`; subsequent
bootstrap runs in `DEPLOYMENT_MODE=single` are rejected if they try to create a
different tenant.

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
- The Phase 5.1 plan migrates the platform-admin role assignment (the
  `(admin, org, '*')` magic marker the CLI currently grants) to a typed
  `users.is_global_admin` column. Once that ships, the CLI grant changes
  shape but the operator UX stays the same.
- Audit event: bootstrap is not yet captured in the audit pipeline; Phase 6.3
  will add an `auth.bootstrap.completed` event.
