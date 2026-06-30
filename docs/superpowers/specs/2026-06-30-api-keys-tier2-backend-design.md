# `/api-keys` Tier 2 backend — design

> **What this spec covers:** the backend (plus thin FE wiring) for the
> four Tier 2 access-surface features whose preview FE shells already
> live under `/api-keys/{trust,helpers,policies,review}` with dummy
> data: **FUT-001** federated workload identity, **FUT-002** credential
> helpers, **FUT-003** token policies, **FUT-004** access review.
>
> **Status:** approved 2026-06-30 (see brainstorming transcript). The
> implementation plan lives in
> [`../plans/2026-06-30-api-keys-tier2-backend-plan.md`](../plans/2026-06-30-api-keys-tier2-backend-plan.md).
>
> **Sequencing:** one PR per feature, smallest-first —
> FUT-002 → FUT-001 → FUT-003 → FUT-004. FUT-004 hard-depends on
> FUT-003's `api_keys.last_used_at` updater; everything else is
> independent.

---

## Why now

`FE-API-048` shipped the `/api-keys` hub + `service_accounts` table
(2026-06-22). The four sub-routes — `trust`, `helpers`, `policies`,
`review` — were shipped as preview surfaces (FE-API-048 T24+) with
disabled controls + amber `<PreviewBanner>` chips pointing at the
respective `FUT-NNN` futures.md anchor. The FE shells aren't broken;
they're stubs that have been waiting for the backend.

This spec lifts all four from preview to live. Each PR removes the
`<PreviewBanner>` from one route + wires the now-real RPCs.

---

## Cross-cutting decisions (approved during brainstorming)

| # | Decision | Rationale |
|---|---|---|
| 1 | **One PR per feature**, smallest-first | Smaller diffs, faster review, every PR fires its own 3-agent batch per `feedback_review_agents_batch`. |
| 2 | **Generic OIDC** for FUT-001 — `issuer_url`/`audience`/`subject_pattern` columns, no provider enum | Same shape as Vault JWT auth + AWS STS web-identity. Adding a new IdP is config, not code. |
| 3 | **Grandfather until rotation** for FUT-003 policy enforcement | Matches the preview UI's promise (`"keys created before the policy is applied are grandfathered until they next rotate"`). Lowest blast radius. Idle-revoke still applies to all keys (that's the policy's whole point). |
| 4 | **Nudge-only** for FUT-004 access review | FUT-003 idle-revoke is the auto-action; FUT-004 is the human-in-the-loop review. Matches the preview's `Revoke` / `Keep` / `Snooze 30d` button layout. |

These decisions are durable. If a follow-up change needs to revisit any
of them, update this spec and ADR-NNN the change.

---

## Feature 1 — FUT-002 Credential helpers (~3h, BFF-only)

### Goal

Render copy-paste-ready `docker login` / k8s Secret / Terraform / GHA
snippets parameterised on the operator's selected service account +
the actual registry hostname. No new persistence; no new gRPC RPCs;
everything composes from existing `/api/v1/workspace/me` data plus
one tiny new BFF endpoint.

### Backend

- **New BFF route:** `GET /api/v1/registry-info` →
  `{registry_host: string, supports_oci_v1_1: bool}`. The registry
  hostname comes from a new `PLATFORM_HOST` env on `services/management`
  (already present on `services/gateway` per RM-001 single-mode
  hostname posture). `supports_oci_v1_1` is hardcoded `true`.
- **Auth:** `requireAuth` only — any authenticated user can see helpers
  for their own service accounts.
- **No proto change. No migration.**

### Frontend

- `HelpersPreview.tsx` (currently 205 LOC, all dummy) becomes
  `HelpersPanel.tsx`. Drop the `<PreviewBanner>`.
- Add a `<Select>` at the top driven by `useServiceAccounts()` (hook
  already exists post-FE-API-048).
- Snippets render from a `buildSnippets({hostname, saName, format})`
  pure helper — easy to unit-test.
- Copy buttons keep their existing functional behaviour (they already
  work on the preview).
- Route file: `_authenticated.api-keys.helpers.tsx` swaps the preview
  import for the live panel.

### Tests

- Unit: `buildSnippets` for each of the 4 formats; verify hostname +
  SA name substitution is correct and that no dummy fallback ever
  reaches the snippet body.
- FE: existing `HelpersPreview.test.tsx` adapted for the live shape;
  add a test that confirms `<PreviewBanner>` is no longer rendered.
- BFF: `GET /api/v1/registry-info` returns 200 + the env-derived
  hostname; returns 500 (fail-loud) when `PLATFORM_HOST` is empty in
  `OTEL_ENVIRONMENT=production`.

### Out of scope

- Bash/Powershell/curl snippets (only the four formats already in the
  preview ship in v1; new formats can be follow-ups).
- Per-snippet "test this in your browser" runner.

---

## Feature 2 — FUT-001 Federated workload identity (~2 days)

### Goal

CI runners (GitHub Actions, GitLab CI, Buildkite, anything OIDC-capable)
exchange a workload OIDC token for a short-lived registry JWT bound
to a specific service account — no static API key required.

### Schema

```sql
-- services/auth/migrations/20260701000001_oidc_trust_configs.sql

CREATE TABLE oidc_trust_configs (
  id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                UUID        NOT NULL,
  service_account_id       UUID        NOT NULL REFERENCES service_accounts(id) ON DELETE CASCADE,
  display_name             TEXT        NOT NULL,
  issuer_url               TEXT        NOT NULL,
  audience                 TEXT        NOT NULL,
  subject_pattern          TEXT        NOT NULL,
  jwks_cache_ttl_seconds   INTEGER     NOT NULL DEFAULT 3600,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at             TIMESTAMPTZ,
  CONSTRAINT oidc_trust_unique_subject UNIQUE (tenant_id, issuer_url, subject_pattern)
);

CREATE INDEX idx_oidc_trust_tenant ON oidc_trust_configs (tenant_id);
CREATE INDEX idx_oidc_trust_sa     ON oidc_trust_configs (service_account_id);
```

- `subject_pattern` is a **glob**, not a regex (`?` / `*`). Easier to
  validate at write time + easier for operators to reason about.
  Example: `repo:steveokay/oci-janus:ref:refs/heads/*` covers any
  branch.
- `jwks_cache_ttl_seconds` lets a paranoid operator force re-fetch on
  every token exchange (`= 0`) without a config change. Default 3600s.

### Issuer allowlist (security gate)

New env `OIDC_ALLOWED_ISSUERS` on `services/auth` — CSV of issuer URL
prefixes the deployment trusts (e.g. `https://token.actions.githubusercontent.com,https://gitlab.com,https://agent.buildkite.com`).
Empty/unset = trust-creation rejected entirely (fail-closed default for
self-hosters who don't know what their CI runners are using yet).

**Why:** without an allowlist a tenant admin could point a trust at an
attacker-controlled IdP and mint registry tokens. The check is enforced
at `CreateOIDCTrust` time AND re-checked at `ExchangeWorkloadToken` time
(an issuer removed from the allowlist after creation stops minting).

### RPCs (4 admin + 1 exchange)

`proto/auth/v1/auth.proto` gains:

```protobuf
service AuthService {
  // ... existing RPCs ...
  rpc ListOIDCTrusts(ListOIDCTrustsRequest) returns (ListOIDCTrustsResponse);
  rpc CreateOIDCTrust(CreateOIDCTrustRequest) returns (OIDCTrust);
  rpc UpdateOIDCTrust(UpdateOIDCTrustRequest) returns (OIDCTrust);
  rpc DeleteOIDCTrust(DeleteOIDCTrustRequest) returns (google.protobuf.Empty);
  rpc ExchangeWorkloadToken(ExchangeWorkloadTokenRequest) returns (ExchangeWorkloadTokenResponse);
}

message OIDCTrust {
  string id                   = 1;
  string tenant_id            = 2;
  string service_account_id   = 3;
  string display_name         = 4;
  string issuer_url           = 5;
  string audience             = 6;
  string subject_pattern      = 7;
  int32  jwks_cache_ttl_seconds = 8;
  google.protobuf.Timestamp created_at   = 9;
  google.protobuf.Timestamp updated_at   = 10;
  google.protobuf.Timestamp last_used_at = 11;
}

message ExchangeWorkloadTokenRequest {
  string oidc_jwt = 1;  // raw token from the CI runner
}

message ExchangeWorkloadTokenResponse {
  string access_token = 1;     // short-lived RS256 JWT
  int32  expires_in   = 2;     // seconds (default 900 = 15 min)
  string token_type   = 3;     // always "Bearer"
}
```

### Token exchange flow

```
CI runner          services/auth                      registry-core
   │                     │                                 │
   ├ POST /auth/token/workload                             │
   │  Authorization: Bearer <oidc_jwt>                     │
   │  (or body: {oidc_jwt: "..."})                         │
   │                     │                                 │
   │                     ├─ parse JWT header, fetch + cache issuer JWKS
   │                     ├─ verify signature
   │                     ├─ check iss in OIDC_ALLOWED_ISSUERS prefixes
   │                     ├─ check aud == config.audience
   │                     ├─ check sub matches config.subject_pattern
   │                     ├─ check exp > now, nbf <= now
   │                     ├─ load service_account, ensure enabled
   │                     ├─ mint RS256 JWT (TTL 15min, claims from SA)
   │                     │     - sub: SA shadow user id
   │                     │     - tenant_id
   │                     │     - access: SA's effective scopes
   │                     │     - principal_kind: "service_account"
   │                     │     - source: "workload_oidc"
   │                     │     - trust_id (for audit join)
   │                     ├─ UPDATE last_used_at on trust row
   │                     ├─ emit auth.workload_token.exchanged audit
   │ <─ 200 {access_token, expires_in: 900, token_type: "Bearer"}
   │                     │                                 │
   ├ docker login -u oidc -p <access_token> <registry>     │
   │                     │                                 │
   ├ docker pull org/repo:tag ─────────────────────────────►│
   │                                                  (normal flow)
```

### JWKS cache

In-process `map[issuer_url]*cachedJWKS{keys, fetched_at}`,
mutex-guarded. Background goroutine refreshes expired entries every
60s. Fail-closed on JWKS fetch failure (existing entry kept and used
until TTL expiry, then exchange fails with `Unavailable`). Bounded
size (32 issuers) — a self-hoster will never hit this; a multi-mode
deployment with > 32 issuers needs to grow the cap consciously.

### BFF + auth-service routes

Admin surfaces live on `services/management` (the usual BFF pattern):

- `GET /api/v1/access/oidc-trust` — list trusts for the tenant
- `POST /api/v1/access/oidc-trust` — create
- `PATCH /api/v1/access/oidc-trust/:id` — update
- `DELETE /api/v1/access/oidc-trust/:id` — delete

The exchange endpoint lives directly on `services/auth` HTTP
(mirrors the existing `/auth/token` exchange route — bypasses the
management BFF because CI runners shouldn't need to know about a
BFF):

- `POST /auth/token/workload` — public (no Bearer); body `{oidc_jwt}`.
  Reachable through `services/gateway` at the deployment's
  `PLATFORM_HOST`.

Admin routes gated by `requireTenantAdmin` (uses the global-admin
fast-path that Phase 5.1 wired). Exchange route has its own rate
limiter (per-issuer + per-subject) keyed in Redis with a 60s window
+ 100 req cap — defence against a compromised CI that tries to mint
unlimited tokens.

### Audit events

- `auth.oidc_trust.created` / `.updated` / `.deleted` — actor +
  trust id + before/after for the changed field
- `auth.workload_token.exchanged` — trust id, issuer, subject
  (truncated to 256 chars), SA id; ALWAYS emitted (success only —
  failures emit `auth.workload_token.rejected` with reason enum:
  `issuer_not_allowed` / `audience_mismatch` / `subject_mismatch` /
  `signature_invalid` / `expired` / `sa_disabled`)

### Frontend

- `TrustPreview.tsx` → `TrustPanel.tsx`; drop `<PreviewBanner>`
- Add `<CreateOIDCTrustDialog>` triggered by the now-enabled
  "New trust relationship" button. Fields: display name, service
  account (select), issuer URL, audience, subject pattern, optional
  JWKS cache TTL override. Validation: subject_pattern parses as a
  glob; issuer URL must be HTTPS.
- Trust cards render real data; "Last verified" pulls from
  `last_used_at` (or `"never"` if NULL).
- Each card gets a kebab menu: `Edit` / `Delete`.

### Tests

- Unit: glob matcher (`subjectMatches`), issuer-allowlist matcher,
  JWKS cache TTL behaviour, JWT verifier (golden vectors for
  GHA + GitLab + Buildkite issuer formats).
- Integration (testcontainers): full exchange flow against a stub
  IdP `httptest.Server` that serves a JWKS + signs JWTs with a known
  key. Verify both happy path and every rejection reason.
- Security regression tests:
  - issuer not in allowlist → `Unauthenticated` with no error leak
  - audience mismatch → same
  - subject_pattern empty → reject at create time
  - subject_pattern matches but SA disabled → `Unauthenticated`,
    `auth.workload_token.rejected` with `sa_disabled`
  - JWT signed by a different key → `Unauthenticated`,
    `signature_invalid`
  - expired JWT → `Unauthenticated`, `expired`
- FE: `TrustPanel.test.tsx` — render with 0/1/N trusts; create dialog
  happy path + glob-validation error path; a11y snapshot.

### Out of scope

- WebAuthn / hardware-key auth for IdPs (deferrable; futures.md
  Tier 1 #1 covers this for human MFA, not workload).
- Trust-config import/export (operator types it in once).
- Per-trust scope clamping beyond what the SA already has (the SA's
  scope IS the clamp).

---

## Feature 3 — FUT-003 Token policies (~2 days)

### Goal

Workspace-wide guardrails on API key TTL, rotation cadence, and
idle revocation. Operator picks the dials once in `/api-keys/policies`;
backend enforces at create + a background job sweeps for idle.

### Schema

```sql
-- services/auth/migrations/20260702000001_token_policies.sql

CREATE TABLE token_policies (
  tenant_id              UUID        PRIMARY KEY,
  max_ttl_days           INTEGER,             -- NULL = no cap
  rotation_interval_days INTEGER,             -- NULL = no force-rotation
  idle_revoke_days       INTEGER,             -- NULL = no idle-revoke
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_by_user_id     UUID                 -- for audit join
);

ALTER TABLE api_keys ADD COLUMN last_used_at    TIMESTAMPTZ;
ALTER TABLE api_keys ADD COLUMN rotation_due_at TIMESTAMPTZ;

CREATE INDEX idx_api_keys_idle_check
  ON api_keys (tenant_id, last_used_at)
  WHERE revoked_at IS NULL;
```

### `last_used_at` updater (load-bearing)

Every successful `ValidateAPIKey` debounces an `UPDATE api_keys SET
last_used_at = now() WHERE id = $1` write via Redis
`SET NX EX 300 lastused:debounce:<key_id>`. If the SET wins, the
UPDATE runs in a goroutine (fire-and-forget; failure is logged but
non-fatal — the next request will retry). 5-minute debounce matches
how `manifests.last_pulled_at` is handled today; gives FUT-004 enough
granularity to spot stale keys without writing on every CI request.

**Redis-down posture: fail-OPEN** (skip the debounce, run the UPDATE
inline). The cache is an optimisation, not a security boundary.

### Enforcement at `CreateAPIKey`

```go
if policy.MaxTTLDays != nil {
    requested := req.ExpiresAt.Sub(now).Hours() / 24
    if int(requested) > *policy.MaxTTLDays {
        return nil, status.Errorf(codes.InvalidArgument,
            "requested TTL %dd exceeds workspace max %dd", int(requested), *policy.MaxTTLDays)
    }
}
if policy.RotationIntervalDays != nil {
    apiKey.RotationDueAt = now.AddDate(0, 0, *policy.RotationIntervalDays)
}
```

Existing keys: `rotation_due_at` is NULL until they're touched by a
rotate operation (or a future migration that backfills based on
created_at + policy when both exist). Grandfathered.

### Background worker — `idle_revoke`

`services/auth/internal/worker/idle_revoke.go`:

```go
// Runs every 1h. Idempotent.
for {
    select {
    case <-ticker.C:
        if policy.IdleRevokeDays == nil { continue }
        rows, _ := db.Query(`
            UPDATE api_keys
            SET revoked_at = now(),
                revoke_reason = 'idle_revoked'
            WHERE tenant_id = $1
              AND revoked_at IS NULL
              AND last_used_at < now() - ($2 || ' days')::interval
            RETURNING id, owner_user_id
        `, tenantID, *policy.IdleRevokeDays)
        // emit auth.key_revoked per row
    case <-ctx.Done():
        return
    }
}
```

`FOR UPDATE SKIP LOCKED` not needed because UPDATE is the lock; no
two workers race for the same row. In multi-mode this runs once per
tenant via a `FOR UPDATE SKIP LOCKED` on a `tenants` cursor.

### RPCs

```protobuf
rpc GetTokenPolicy(GetTokenPolicyRequest) returns (TokenPolicy);
rpc PutTokenPolicy(PutTokenPolicyRequest) returns (TokenPolicy);
```

Both `tenant_id`-scoped. `Put` is upsert.

### BFF

- `GET /api/v1/access/token-policy` — workspace-admin gated
- `PUT /api/v1/access/token-policy` — workspace-admin gated

### Audit events

- `auth.token_policy.changed` — actor + before/after diff per field
- `auth.key_revoked` (existing) — gains `reason` field:
  `manual` / `idle_revoked` / `rotation_lapsed`

### Frontend

- `PoliciesPreview.tsx` → `PoliciesPanel.tsx`. Drop `<PreviewBanner>`,
  enable the three numeric inputs + Apply button. Wire to
  `useTokenPolicy()` + `usePutTokenPolicy()` hooks.
- "Allow per-key override" checkbox in the preview is dropped — per
  brainstorming Decision #3, grandfathering is the override mechanism;
  no per-key flag.
- "Force rotation" help text changes from `"Owners receive an email
  reminder 14 days before expiry."` to `"You'll see a reminder in the
  bell feed 14 days before expiry."` until FUT-019 Phase 3 lands the
  email channel.

### Tests

- Unit: enforcement logic (TTL cap rejection, rotation_due_at
  computation, idle-revoke SQL generation with NULL handling).
- Integration (Postgres testcontainer): policy upsert; CreateAPIKey
  rejection when TTL exceeds cap; idle_revoke worker against a seeded
  set of keys with varying last_used_at.
- Security: PutTokenPolicy from a non-admin user → 403.

### Out of scope

- Per-service-account policy (workspace-wide only in v1).
- Auto-revoke when `rotation_due_at` lapses — FUT-004 surfaces lapsed
  rotations as a review item; the operator decides to rotate or revoke.
  Auto-action is FUT-003's `idle_revoke` only.
- Email reminders 14 days before expiry. The PoliciesPreview's inline
  text currently mentions email reminders; that string changes to
  `"You'll see a reminder in the bell feed 14 days before expiry."`
  in this PR. Email channel waits on FUT-019 Phase 3 (blocked on
  v2.0.0 final tag).

---

## Feature 4 — FUT-004 Access review (~1 day)

### Goal

Periodically surface API keys that haven't been used recently;
operator chooses Revoke / Keep / Snooze 30d. Nudges arrive via the
existing notification feed.

### Schema

```sql
-- services/auth/migrations/20260703000001_api_keys_review_snooze.sql

ALTER TABLE api_keys ADD COLUMN review_snoozed_until TIMESTAMPTZ;
```

No new table. Staleness threshold = `token_policies.idle_revoke_days`
if set, else a fixed default of `90`.

### Background worker — `access_review`

Weekly cron on `services/auth`. For each tenant:

```sql
SELECT id, name, owner_user_id, last_used_at, rotation_due_at
FROM api_keys
WHERE tenant_id = $1
  AND revoked_at IS NULL
  AND (review_snoozed_until IS NULL OR review_snoozed_until < now())
  AND (
        -- stale: not used recently
        (last_used_at < now() - ($2 || ' days')::interval)
        OR
        -- rotation lapsed: FUT-003 set rotation_due_at, the deadline passed
        (rotation_due_at IS NOT NULL AND rotation_due_at < now())
      )
```

For each row: emit `auth.access_review_due` audit event with
`reason` enum (`idle` / `rotation_lapsed`) + create
`notification_events` rows for owner + workspace admin. A key
satisfying both conditions emits one row with the more urgent
reason (`rotation_lapsed` wins).

This is why FUT-003's `rotation_due_at` column is load-bearing for
FUT-004 — without it the rotation cadence policy would be silently
ignored.

### RPCs

```protobuf
rpc ListStaleKeys(ListStaleKeysRequest) returns (ListStaleKeysResponse);
rpc SnoozeAPIKeyReview(SnoozeAPIKeyReviewRequest) returns (APIKey);
```

`ListStaleKeys` returns the same shape as `ListAPIKeys` + a
`suggested_action` enum (`REVOKE` / `KEEP` / `SNOOZE`) derived from
heuristics: `last_used_at < idle_revoke_days - 14d` → `REVOKE`;
`last_used_at` within 14d of threshold → `KEEP`; explicit snooze
already active → `SNOOZE`.

### BFF

- `GET /api/v1/access/review/stale` — workspace-admin OR owner of the
  key. Owners see their own keys; admins see all.
- `POST /api/v1/access/review/snooze` — body `{key_id, days}` —
  validate days ∈ [1, 90].
- Revoke flows through the existing `DELETE /api/v1/api-keys/:id`.

### Audit events

- `auth.access_review_due` — actor=`system`, payload includes key id
  + days idle
- `auth.access_review.snoozed` — actor + key id + snoozed_until
- `auth.key_revoked` (existing) — used for the manual Revoke action

### Frontend

- `ReviewPreview.tsx` → `ReviewPanel.tsx`. Drop `<PreviewBanner>`,
  enable the three action buttons per row + the "Send review reminders
  to owners" footer button. Wire to `useStaleKeys()` +
  `useSnoozeKey()` + the existing `useRevokeAPIKey()` hooks.

### Tests

- Unit: `suggestedAction` helper covers all three branches.
- Integration: seed a key with `last_used_at = now() - 45d`, run the
  worker, assert audit event + notification_events row; snooze the
  key, re-run the worker, assert nothing fires.
- Security: snooze with `days > 90` → InvalidArgument; snooze for
  another tenant's key → NotFound (don't reveal existence).

### Out of scope

- Bulk-revoke ("revoke all 12 stale keys") — single-key only in v1.
- Email channel for the reminders (waits on FUT-019 Phase 3).
- Owner-defined per-key snooze defaults (workspace-wide threshold
  only).

---

## Tracker + memory updates per PR

Each of the 4 PRs:

1. Adds a `REM-NNN` row to `status-tracker.md` when work starts
   (linking the branch).
2. Moves the row to `status.md` on merge with PR number + commit
   hash + date.
3. In `futures.md`, the `FUT-NNN` anchor gets a `**DONE — see
   status.md (REM-NNN row)**` stub instead of full removal — preserves
   the design history that motivated the work.

## Cross-cutting tests

- All 4 PRs run the 3-agent review batch
  (`security-agent` + `qa-agent` + `code-review-agent` in parallel)
  before `gh pr create`, per `feedback_review_agents_batch`.
- All 4 PRs satisfy the per-service `make build && make test && make
  lint` gate per CLAUDE.md §15.

---

## Open questions resolved during brainstorming

1. ✅ **OIDC issuer allowlist** (`OIDC_ALLOWED_ISSUERS` env) — fail-closed
   default (empty = no trusts can be created). Operator names the
   issuers they trust once at deploy time.
2. ✅ **`last_used_at` debounce window** — 5 minutes (mirrors
   `manifests.last_pulled_at`). Tighter starves the cache; looser
   starves FUT-004.
3. ✅ **FUT-004 default staleness threshold** — `token_policies.idle_revoke_days`
   if set, else `90` days. Single dial.

Open items for future revision (NOT blockers):

- **WebAuthn workload identity** — non-OIDC IdPs (hardware-attested
  workload keys). Defer until an operator asks for it.
- **Per-SA policies** — different rotation cadence for `prod-deploy`
  vs `dev-ci`. Defer until the workspace-wide v1 ships + we have data
  on real operator workflows.
- **Bulk-revoke** in FUT-004 — wait for v1 friction reports.
