# CUSTOM-DOMAINS.md — Per-tenant custom domain reference

> **Audience:** operators wiring their own hostname (`registry.acme.com`,
> `janus.athelos.co`) to the platform, plus developers extending the
> verification or routing pipeline.
>
> **Status:** FE-API-007 + FE-API-027 shipped — full CRUD, DNS TXT
> verification, atomic primary-swap, background worker with 24h/48h
> notification cadence. Wildcard-zone collision guard active. Per
> `docs/SERVICES.md` §12 (registry-tenant) for the schema + RPC contract.

---

## 1. Mental model

By default every tenant is reachable on a subdomain of the platform's
wildcard zone (`<slug>.registry.example.com`). A custom domain lets a
tenant point their own hostname at the registry so their Docker / Helm
clients use a name they control. Common reasons:

- White-labelling for enterprise customers
- Certificate / domain ownership compliance
- Cleaner CI configs (`docker login registry.acme.com` vs.
  `docker login acme.registry.example.com`)
- Migrating from a self-hosted registry without breaking existing
  image references

The platform stores one or more custom domains per tenant, marks
exactly one as `primary`, and the gateway routes incoming requests
based on `Host:` header lookup. The platform-derived host is always
available as a fallback — deleting a custom primary doesn't break the
tenant, it just reverts the workspace to the platform host.

---

## 2. End-to-end flow

```
1. /workspace/domains → "Register a domain"
       │
       │  Operator enters: janus.athelos.co
       ▼
2. Backend responds with:
       verification_token: <64-char hex>
       txt_record_name:    _registry-verify.janus.athelos.co
       │
       ▼
3. Operator adds a TXT record at their DNS provider
   (Cloudflare, Route53, …):
       Name:    _registry-verify.janus.athelos.co
       Type:    TXT
       Value:   <the token from step 2>
       │
       ▼
4. Either wait for the background poll (~5–20 min loop)
   or click "Verify Now" in the dashboard for an inline
   accelerator.
       │
       │  services/tenant resolves the TXT, matches the
       │  token, flips `verified = true`. If this is the
       │  first verified domain for the tenant it's
       │  auto-promoted to primary in the same tx.
       ▼
5. Operator can promote another verified domain to
   primary at any time. The promotion is one atomic tx
   (SELECT verified → demote-all → promote-target
   RETURNING) so no observable state has two primaries.
       │
       ▼
6. registry-gateway sees Host: janus.athelos.co on
   incoming requests, hits a 60s Redis cache, falls
   back to a registry-tenant gRPC lookup on miss, and
   injects X-Tenant-ID for downstream services.
```

---

## 3. Adding the DNS record (Cloudflare walkthrough)

Cloudflare's "Name" field is **relative to the zone**, which is the
single most common source of misconfiguration. If your zone is
`athelos.co` and the system asks for `_registry-verify.janus.athelos.co`,
the right Name field value is `_registry-verify.janus` — Cloudflare
appends `.athelos.co` automatically.

| Field | Value |
|---|---|
| **Type** | `TXT` |
| **Name** | `_registry-verify.janus` (zone is `athelos.co`) |
| **Content** | The 64-char token from the registration response — no quotes, no whitespace, exact case |
| **Proxy status** | DNS only (gray cloud) — TXT records are never proxied anyway |
| **TTL** | Auto / 5 min |

**Always check Cloudflare's "Will create" preview** before saving — it
should read `_registry-verify.janus.athelos.co`. If it shows
`_registry-verify.athelos.co` or `janus.athelos.co`, your Name field
is wrong.

In addition to the verification TXT, you'll also need an **A or CNAME**
pointing the actual hostname at the gateway:

| Field | Value |
|---|---|
| **Type** | `CNAME` (or `A` if pointing at an IP) |
| **Name** | `janus` |
| **Content** | `gateway.registry.example.com` (or the gateway's external IP) |
| **Proxy status** | DNS only (recommended — let the platform terminate TLS) |
| **TTL** | Auto |

The TXT record can be deleted once verification succeeds, but leaving
it in place is harmless — re-verification (e.g. after a domain
expires and gets re-registered) still uses the same record name.

### Verifying propagation from your terminal

```bash
# Public DNS — should return the token in quotes
nslookup -type=TXT _registry-verify.janus.athelos.co 1.1.1.1
# expect: "<your-token>"

# Or with dig if you have it
dig +short TXT _registry-verify.janus.athelos.co
```

If the lookup returns nothing → wait 1-5 minutes after saving in
Cloudflare and try again. If it returns the wrong value → check that
you pasted the token correctly. If it returns the right value but
the dashboard still won't verify → file a bug, that's our problem.

### Other DNS providers

The Name field semantics differ by provider:

| Provider | Name field convention | Example value |
|---|---|---|
| Cloudflare | Relative to zone | `_registry-verify.janus` |
| Route53 | Absolute FQDN | `_registry-verify.janus.athelos.co.` (trailing dot) |
| AWS Route53 (Hosted Zone UI) | Just the leftmost part | `_registry-verify.janus` |
| Google Cloud DNS | Absolute FQDN | `_registry-verify.janus.athelos.co.` |
| Namecheap / GoDaddy | Relative | `_registry-verify.janus` |

When in doubt, set the record and immediately run a `nslookup` /
`dig` against the FQDN. If the lookup returns the token, the system
will verify. If not, the Name field is wrong.

---

## 4. Verification cadence

The worker in `services/tenant` runs a single in-process loop with
these defaults (overridable via env vars):

| Behavior | Default | Env var |
|---|---|---|
| Poll an unverified domain | Every 5-20 min (jittered) | `DOMAIN_POLL_INTERVAL_MIN`, `DOMAIN_POLL_INTERVAL_MAX` |
| Email "still pending" reminder | 24h after registration | `DOMAIN_NOTIFY_24H_AFTER` |
| Email "we'll stop polling" | 48h after registration | `DOMAIN_NOTIFY_48H_AFTER` |
| Stop polling | 48h | (hard-coded; re-registration resets the timer) |

"Verify Now" (dashboard button) is an inline accelerator — it does a
synchronous TXT lookup at click time. If the lookup doesn't find the
token yet, the response carries `verified: false` and the FE renders
a "Verification still pending — TXT record not visible yet" info
toast. Genuine failures (DB error, etc.) propagate as a real error
toast.

---

## 5. Primary domain semantics

Exactly one domain per tenant carries `is_primary = true` at any time.
Implementation:

- A partial unique index `tenant_domains_primary_idx ON tenant_domains
  (tenant_id) WHERE is_primary = true` makes the constraint
  enforceable at the database layer.
- `SetPrimaryDomain` is one atomic transaction:
  `SELECT verified → demote-all-others → promote-target RETURNING`.
  No observable state ever has two primaries.
- Auto-promotion: when the FIRST domain for a tenant is verified,
  the same transaction also flips it to `is_primary = true`.
- Workspace `host` (FE-API-007's `/workspace/me` response):
  - If any domain has `is_primary=true`: that's `host`.
  - Else: the platform-derived host `<slug>.<PLATFORM_BASE_DOMAIN>`.

The dashboard banner shows "platform host" vs "custom host" badges
based on this so operators can tell at a glance which surface is live.

### Deleting the primary domain

The platform-derived host is always available, so deleting the
primary is supported. The flow:

1. Operator clicks Delete on the primary row.
2. FE renders an extra warning in the confirmation dialog: "this is
   your primary domain — removing it falls the workspace back to
   the platform-derived host."
3. On confirm, the tenant service deletes the row and sends back the
   gRPC metadata `x-janus-was-primary: true`. The BFF translates that
   to the HTTP response header `X-Janus-Warning: primary-domain-removed`.
4. FE surfaces the warning on the success toast: "Removed
   `janus.athelos.co`. Workspace primary fell back to the
   platform-derived host."
5. Workspace's `host` field on the next `/workspace/me` poll updates
   to the platform-derived hostname; the dashboard URL doesn't change
   for operators logged in via the platform host, but anyone with the
   custom hostname bookmarked needs to update their config.

---

## 6. Services involved

| Service | Role |
|---|---|
| `registry-management` (BFF, port 8091) | REST routes under `/api/v1/workspace/me/domains*`. Workspace-admin gated. Proxies to tenant service via gRPC. |
| `registry-tenant` (port 8090, gRPC 50060) | Owns `tenants` + `tenant_domains` tables, the verification poll worker, the DNS TXT validator, the primary-swap transaction. |
| `registry-gateway` | At request time, looks up `Host:` header → tenant_id via a Redis cache (60s TTL) populated from `registry-tenant`. Injects `X-Tenant-ID`. Unverified domains return 404 cleanly — no leak of unverified domain existence. |

---

## 7. HTTP API surface (BFF routes)

All routes under `/api/v1/workspace/me/domains*`. Workspace-admin
(any `admin`/`owner` role on any org in the tenant) is required for
every mutation; reads are also admin-gated because the response
includes notification timestamps + next-poll cursor that leak
operational state.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/workspace/me/domains` | List all domains for the tenant |
| `POST` | `/workspace/me/domains` | Register a new domain; body `{"domain":"…"}` |
| `POST` | `/workspace/me/domains/{domain}/verify` | Inline DNS TXT check (Verify Now button) |
| `PATCH` | `/workspace/me/domains/{domain}` | Mutate (e.g. `{"is_primary":true}` to promote) |
| `DELETE` | `/workspace/me/domains/{domain}` | Remove the domain. Sets `X-Janus-Warning: primary-domain-removed` when the removed row was primary. |

`X-Janus-Warning` is exposed via `Access-Control-Expose-Headers` so
browser callers can read it on cross-origin requests.

---

## 8. Database schema

`services/tenant/migrations/`:

```sql
CREATE TABLE tenant_domains (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  domain               TEXT NOT NULL UNIQUE,       -- the FQDN
  verification_token   TEXT NOT NULL,              -- 32-byte hex
  verified             BOOLEAN NOT NULL DEFAULT FALSE,
  is_primary           BOOLEAN,                    -- nullable; promoted via tx
  registered_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  verified_at          TIMESTAMPTZ,
  next_poll_after      TIMESTAMPTZ NOT NULL,       -- worker schedule
  notified_24h         BOOLEAN NOT NULL DEFAULT FALSE,
  notified_48h         BOOLEAN NOT NULL DEFAULT FALSE
);

-- Only one row per tenant can be primary at any time.
CREATE UNIQUE INDEX tenant_domains_primary_idx
    ON tenant_domains (tenant_id) WHERE is_primary = TRUE;
```

Two important constraints:

- **`UNIQUE (domain)`** — a domain can only belong to one tenant ever.
  Trying to register a domain that's already taken returns a generic
  `AlreadyExists` (the system intentionally doesn't disclose which
  tenant owns it; that would be a cross-tenant information leak).
- **Partial unique on `WHERE is_primary`** — only one primary per
  tenant at a time, enforced at the DB layer.

---

## 9. Common failure modes

| Symptom | Root cause | Fix |
|---|---|---|
| "Couldn't register, try again" | Tenant row missing in `services/tenant`'s DB (was a bug fixed in PR #32) | Should be impossible after #32; if it recurs, file a bug |
| "Couldn't run verification, try again later" | TXT record not propagated yet, AND backend reported it as an error instead of a "pending" status (fixed in PR #34) | Verify the TXT with `nslookup` first; the FE will show "still pending" on retry |
| "Verified" never flips even though TXT is set | TXT lives at the wrong name. Most common cause: Cloudflare Name field misconfiguration | `nslookup -type=TXT _registry-verify.<your-domain> 1.1.1.1` — if it returns nothing, fix the Name field; if it returns the wrong value, repaste the token |
| Worker says "we'll stop polling" after 48h | The 48h cap kicked in. The worker stops on its own; click Verify Now to reset, or delete + re-register | Click Verify Now; if still not propagating, delete the row and register again |
| `cannot register domain within the platform-managed wildcard space` | You tried to register a subdomain of `PLATFORM_BASE_DOMAIN` — reserved for the platform's own subdomain assignment | Pick a different hostname (`<your-org>.registry.example.com` is auto-assigned per tenant slug — you don't need to "register" the platform host) |
| Delete button disabled in the table | Was a guard against deleting primary domains (relaxed in branch `docs+fix/custom-domains`) | After that PR merges, deletion is always allowed; primary deletion surfaces a warning + falls back to the platform host |

---

## 10. Configuration reference

### `registry-tenant`

| Variable | Default | Purpose |
|---|---|---|
| `DB_DSN` | required | Postgres DSN for `registry_tenant` |
| `PLATFORM_BASE_DOMAIN` | `registry.localhost` | Platform wildcard zone — tenants get `<slug>.<this>` automatically; custom domains in this zone are rejected |
| `DOMAIN_POLL_INTERVAL_MIN` | `5m` | Lower bound on the worker's per-domain poll cadence |
| `DOMAIN_POLL_INTERVAL_MAX` | `20m` | Upper bound — jittered between min/max |
| `DOMAIN_NOTIFY_24H_AFTER` | `24h` | When the "still pending" reminder fires |
| `DOMAIN_NOTIFY_48H_AFTER` | `48h` | When the "we'll stop polling" final reminder fires |
| `GRPC_ADDR` | `:50051` | gRPC bind (in-container) — mapped to host :50060 in dev compose |
| `HTTP_ADDR` | `:8080` | HTTP health endpoint — host :8090 in dev compose |
| `MTLS_CA_CERT_PATH` / `MTLS_CERT_PATH` / `MTLS_KEY_PATH` | required | mTLS to the rest of the platform |
| `OTEL_*` | per platform defaults | Tracing + metrics |

### `registry-gateway`

| Variable | Purpose |
|---|---|
| `TENANT_GRPC_ADDR` | Where to ask `registry-tenant` when a Host header doesn't match the wildcard zone |
| `REDIS_ADDR` | Where the `Host: → tenant_id` cache lives (60s TTL) |
| `PLATFORM_BASE_DOMAIN` | Must match the tenant service's value — used for wildcard vs. custom-domain decision |

---

## 11. Dev quickstart

```bash
# 1. Login as dev admin
JWT=$(curl -s -X POST http://localhost:8080/api/v1/login \
  -d '{"username":"admin","password":"Admin1234!dev","tenant_id":"98dbe36b-ef28-4903-b25c-bff1b2921c9e"}' \
  | jq -r .token)

# 2. Register a domain
curl -X POST http://localhost:8091/api/v1/workspace/me/domains \
  -H "Authorization: Bearer $JWT" \
  -d '{"domain":"registry.dev.localhost"}'
# Response includes verification_token + txt_record_name

# 3. Add the TXT record at your DNS provider (or skip for dev —
#    you can also just demote / delete the row directly via the API).

# 4. Click Verify Now (or wait for the worker)
curl -X POST http://localhost:8091/api/v1/workspace/me/domains/registry.dev.localhost/verify \
  -H "Authorization: Bearer $JWT"
# verified: false → TXT not yet visible; click again after propagation
# verified: true  → ready to promote / use

# 5. Promote
curl -X PATCH http://localhost:8091/api/v1/workspace/me/domains/registry.dev.localhost \
  -H "Authorization: Bearer $JWT" \
  -d '{"is_primary":true}'

# 6. List / inspect
curl -X GET http://localhost:8091/api/v1/workspace/me/domains \
  -H "Authorization: Bearer $JWT"

# 7. Delete (deletion of a primary falls back to platform host)
curl -X DELETE http://localhost:8091/api/v1/workspace/me/domains/registry.dev.localhost \
  -H "Authorization: Bearer $JWT" -i
# Look for X-Janus-Warning: primary-domain-removed in the response headers
```

### Dev shortcut — direct DB fixups

When you don't have real DNS available and just want to flip a row's
state to test downstream code (gateway routing, dashboard rendering),
you can poke the DB directly. **Don't do this in prod.**

```bash
# Mark a domain verified without the TXT dance
docker exec docker-compose-postgres-1 psql -U registry -d registry_tenant -c "
UPDATE tenant_domains
   SET verified = TRUE, verified_at = now()
 WHERE domain = 'registry.dev.localhost';"

# Demote a primary so you can delete it via the UI (or just delete the row)
docker exec docker-compose-postgres-1 psql -U registry -d registry_tenant -c "
UPDATE tenant_domains SET is_primary = FALSE WHERE domain = '<…>';"

# Nuke everything for this tenant (cascades cleanly)
docker exec docker-compose-postgres-1 psql -U registry -d registry_tenant -c "
DELETE FROM tenant_domains WHERE tenant_id = '98dbe36b-ef28-4903-b25c-bff1b2921c9e';"
```

---

> **Last updated:** see `git log -- docs/CUSTOM-DOMAINS.md`.
> **Questions / changes?** This file is the canonical operator
> reference for the custom-domain feature. Update it whenever the
> registration flow, validation rules, or worker cadence changes.
