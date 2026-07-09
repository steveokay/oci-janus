# FUT-023 — Ephemeral PR-scoped Registries (Phase 1) — Design

> **Status:** Approved design (2026-07-08). Phase 1 of FUT-023.
> **Scope:** Namespace lifecycle only — GitHub, immediate cascade-delete,
> optional config-driven promote-on-merge. Keyless OIDC push is **Phase 2**
> (deferred; see §11).
> **Posture:** single-tenant (`DEPLOYMENT_MODE=single`).

---

## Implementation notes / drift from design (Phase 1 PR A, 2026-07-09)

Two facts diverged from this design body during implementation. The body
below is left as-authored; these are the authoritative corrections:

1. **Migration is `00020_pr_registry.sql`, not `00019`.** §4 / §14 name the
   migration `00019_pr_registry.sql`, but `00019` was already taken by
   FUT-021 on `main` by the time this branch landed. The shipped file is
   `services/metadata/migrations/00020_pr_registry.sql`.
2. **The `Outcome` enum values ship with an `OUTCOME_` prefix.** §6 sketches
   the enum as `IGNORED`, `PROVISIONED`, `PROMOTED_AND_TORN_DOWN`,
   `TORN_DOWN`, `DISABLED`. buf lint (`ENUM_VALUE_PREFIX`) requires each
   value to be prefixed with the enum name, so the shipped proto uses
   `OUTCOME_IGNORED`, `OUTCOME_PROVISIONED`, `OUTCOME_PROMOTED_AND_TORN_DOWN`,
   `OUTCOME_TORN_DOWN`, `OUTCOME_DISABLED` (plus the existing
   `OUTCOME_UNSPECIFIED = 0`). Behaviour and status mapping are unchanged.

---

## 1. Goal

Auto-provision a disposable registry namespace for each open pull request,
and tear it down when the PR closes. CI pushes its build to
`pr-<repo>-<N>/*`; on merge the images can be promoted to a real target org;
on close the whole namespace disappears in a single cascade delete.

One sentence: **a GitHub PR webhook drives the create/promote/delete
lifecycle of a per-PR org.**

---

## 2. Why this shape (decisions already made)

These were settled during brainstorming (2026-07-08). Recorded here so the
implementer doesn't re-litigate them.

| # | Decision | Rationale |
|---|---|---|
| D1 | **Namespace = a dedicated ephemeral org per PR** (`pr-<repo>-<N>`), not a repo-prefix or a path convention. | RBAC scopes are only `org/*` or `org/repo`-exact (confirmed via the promotion gate + the OIDC exchange, which reads a service account's static `allowed_scopes`). Only an org-level namespace gives clean "push to your PR namespace only" scoping without leaking write on the whole source org. Cleanup is a single `DELETE FROM organizations` that FK-cascades to repos/manifests/tags/blob_links (`00001_initial_schema.sql:18,30,42,59`). |
| D2 | **GitHub only** in Phase 1. | One payload shape + one HMAC scheme (`X-Hub-Signature-256`, HMAC-SHA256). GitLab's `X-Gitlab-Token` plaintext scheme + different payload is a clean follow-up. |
| D3 | **Immediate cascade-delete** on close/merge (no grace window). | Matches the "ephemeral" intent; avoids a sweep worker + `deleted_at` column in Phase 1. |
| D4 | **Promote-on-merge is optional + config-driven.** | If `promote_target_org` is set, a merge promotes the namespace's tags to that org (reusing the shipped FUT-020 `PromoteTag`) before teardown; otherwise merge behaves like close (delete only). |
| D5 | **State + secret + HMAC verification live in `services/metadata`;** `services/management` is a thin forwarder. | `services/management` is a stateless BFF with no DB. metadata already owns orgs/repos. The KEK-sealed webhook secret must never cross the wire as plaintext, and the HMAC is computed over the raw request body — so the service holding the secret must be the one that verifies. |
| D6 | **CI authenticates pushes with an existing scoped API key** in Phase 1. | Keyless OIDC (deriving `pr-<N>/*` scope from the signed GitHub `ref` claim) is the Phase-2 target; it touches the security-critical token-exchange path and is deliberately deferred. |

### Why not per-PR OIDC in Phase 1 (the load-bearing finding)

GitHub Actions' OIDC token uses the **same `sub` claim for every PR in a
repo** (`repo:<owner>/<repo>:pull_request`). It does not vary by PR number —
the number only appears in the signed `ref` claim (`refs/pull/<N>/merge`),
which the shipped exchange (`services/auth/internal/service/oidc_exchange.go`)
does not read. So real per-PR isolation requires extending the exchange to
derive scope from `ref`. That is Phase 2. Phase 1 ships the lifecycle with a
manually-configured scoped credential and does not touch the auth boundary.

---

## 3. Architecture

```
        GitHub  ──POST /webhooks/scm/github/pr──►  registry-management
        (PR event, X-Hub-Signature-256)            (thin receiver, no secret)
                                                          │ gRPC
                                                          │ HandlePREvent(
                                                          │   tenant, "github",
                                                          │   raw_body, signature)
                                                          ▼
                                                   registry-metadata
                                                   • unseal webhook secret (KEK)
                                                   • verify HMAC over raw_body
                                                   • parse pull_request payload
                                                   • provision / promote / teardown
                                                   • emit pr.namespace.* events
                                                          │
                        ┌─────────────────────────────────┼───────────────────┐
                        ▼                                   ▼                   ▼
              GetOrCreateOrganization              PromoteTag (FUT-020)   DeleteOrganization
              (opened/reopened)                    (merged + target set)  (cascade delete)

        Admin config:  FE  ──GET/PUT /api/v1/pr-registry/config──►  management ──gRPC──► metadata
        Audit:  pr.namespace.provisioned / pr.namespace.torn_down  ──RabbitMQ──►  registry-audit
```

**Unit boundaries**

- **`registry-management` receiver** — public HTTP surface. Knows nothing
  about the secret. Reads raw body + signature header, resolves the bootstrap
  tenant, forwards to metadata, maps the result to an HTTP status. Also hosts
  the two admin config routes (authenticated, admin-gated).
- **`registry-metadata` PR-registry module** — owns the config table, the
  `pr_namespaces` table, HMAC verification, GitHub payload parsing, and the
  org lifecycle. Isolated in a new `internal/prregistry` package + a
  `repository/pr_registry.go` data-access file.
- **`libs/rabbitmq/events`** — two new typed events.
- **`frontend`** — an admin config panel + a read-only active-namespaces list.

---

## 4. Data model (metadata migrations)

New migration `services/metadata/migrations/00019_pr_registry.sql` (goose;
paired down migration; grants in the same file — the #290 lesson).

### 4.1 `pr_registry_config` (one row per tenant)

```sql
CREATE TABLE pr_registry_config (
    tenant_id           UUID PRIMARY KEY,
    enabled             BOOLEAN     NOT NULL DEFAULT FALSE,
    -- AES-256-GCM sealed under PR_REGISTRY_KEY_HEX. NULL = no secret set yet.
    webhook_secret_enc  BYTEA,
    kek_version         SMALLINT    NOT NULL DEFAULT 1,
    -- When set, a merged PR promotes the namespace's tags into this org
    -- before teardown. NULL = merge just deletes (D4).
    promote_target_org  TEXT,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by          UUID
);
```

### 4.2 `pr_namespaces` (lifecycle tracking)

```sql
CREATE TABLE pr_namespaces (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL,
    -- ON DELETE SET NULL (NOT cascade): the org is deleted on teardown, but
    -- the pr_namespaces row must SURVIVE as a torn-down lifecycle record.
    -- A cascade would erase the audit trail the moment the org is deleted.
    org_id        UUID        REFERENCES organizations(id) ON DELETE SET NULL,
    provider      TEXT        NOT NULL,           -- 'github' (Phase 1)
    source_repo   TEXT        NOT NULL,           -- 'owner/repo'
    pr_number     INTEGER     NOT NULL,
    org_name      TEXT        NOT NULL,           -- 'pr-<repo>-<N>'
    status        TEXT        NOT NULL DEFAULT 'active'
                              CHECK (status IN ('active','torn_down')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    torn_down_at  TIMESTAMPTZ,
    UNIQUE (tenant_id, provider, source_repo, pr_number)
);
```

> Teardown order: within one tx, `UPDATE pr_namespaces SET status='torn_down',
> torn_down_at=now(), org_id=NULL WHERE id=$1` and then
> `DELETE FROM organizations WHERE id=$2`. The explicit `org_id=NULL` in the
> UPDATE plus `ON DELETE SET NULL` means the lifecycle row is preserved
> regardless of statement order.

### 4.3 Grants

`registry-metadata` connects as its schema owner today (no low-privilege
runtime role split, unlike audit). Confirm during implementation; if a
dedicated role exists, `GRANT SELECT, INSERT, UPDATE, DELETE` on both tables
in this migration. If not, no grant statements are needed — but the migration
must not assume a role that doesn't exist.

---

## 5. KEK / config env

New metadata env var, mirroring `NOTIFY_WEBHOOK_KEY_HEX`:

- **`PR_REGISTRY_KEY_HEX`** — 64 hex chars (32 bytes). Validated at startup:
  - unset/empty → PR-registry feature **disabled** (metadata still boots; the
    inbound route returns `404`, and `PutPRRegistryConfig` with a secret
    returns `FailedPrecondition`).
  - present but not exactly 32 bytes → **fail closed at startup** (config
    validation error, like the MFA/webhook KEKs).

Documented in `services/metadata/.env.example` + `docs/SERVICES.md` §5 +
CLAUDE.md service-catalogue row for metadata.

---

## 6. Proto (metadata/v1)

Add to `proto/metadata/v1/metadata.proto` (additive — new fields/messages/RPCs
only, never renumber existing fields):

```proto
// PR-registry config (FUT-023).
message PRRegistryConfig {
  string tenant_id          = 1;
  bool   enabled            = 2;
  bool   has_secret         = 3;   // write-only secret; never echoed
  string promote_target_org = 4;
  google.protobuf.Timestamp updated_at = 5;
}

message GetPRRegistryConfigRequest { string tenant_id = 1; }

message PutPRRegistryConfigRequest {
  string tenant_id          = 1;
  bool   enabled            = 2;
  // Empty string = keep existing secret; non-empty = re-seal new secret.
  string webhook_secret     = 3;
  string promote_target_org = 4;
  string updated_by         = 5;
}

// Inbound SCM event. metadata verifies the HMAC internally, so the raw
// body + signature are passed through untouched.
message HandlePREventRequest {
  string tenant_id = 1;
  string provider  = 2;   // 'github'
  bytes  raw_body  = 3;
  string signature = 4;   // value of X-Hub-Signature-256 ('sha256=<hex>')
  string event     = 5;   // value of X-GitHub-Event ('pull_request'|'ping'|...)
}

message HandlePREventResponse {
  // Outcome for the caller to map onto an HTTP status + for logging.
  enum Outcome {
    OUTCOME_UNSPECIFIED = 0;
    IGNORED             = 1;  // ping, non-PR event, action we don't act on
    PROVISIONED         = 2;  // org created (opened/reopened)
    PROMOTED_AND_TORN_DOWN = 3;
    TORN_DOWN           = 4;
    DISABLED            = 5;  // feature off / no config → 404 at the edge
  }
  Outcome outcome   = 1;
  string  org_name  = 2;   // provisioned/torn-down namespace, when applicable
}

service MetadataService {
  // ... existing RPCs ...
  rpc GetPRRegistryConfig(GetPRRegistryConfigRequest) returns (PRRegistryConfig);
  rpc PutPRRegistryConfig(PutPRRegistryConfigRequest) returns (PRRegistryConfig);
  rpc HandlePREvent(HandlePREventRequest) returns (HandlePREventResponse);
  rpc DeleteOrganization(DeleteOrganizationRequest) returns (google.protobuf.Empty);
}

message DeleteOrganizationRequest {
  string tenant_id = 1;
  string org_id    = 2;
}

// Active-namespaces list for the FE (§10). Read-only.
message ListPRNamespacesRequest {
  string tenant_id  = 1;
  string status     = 2;  // 'active' (default) | 'torn_down' | '' (all)
  int32  page_size  = 3;
  string page_token = 4;
}

message PRNamespace {
  string provider    = 1;
  string source_repo = 2;
  int32  pr_number   = 3;
  string org_name    = 4;
  string status      = 5;
  google.protobuf.Timestamp created_at   = 6;
  google.protobuf.Timestamp torn_down_at = 7;
}

message ListPRNamespacesResponse {
  repeated PRNamespace namespaces      = 1;
  string               next_page_token = 2;
}
```

Add the matching RPC to `MetadataService`:

```proto
  rpc ListPRNamespaces(ListPRNamespacesRequest) returns (ListPRNamespacesResponse);
```

Regenerate stubs: `cd proto && buf generate`.

---

## 7. metadata behaviour (`internal/prregistry`)

### 7.1 HMAC verification (`verify.go`)

- GitHub signs `HMAC-SHA256(secret, raw_body)` and sends
  `X-Hub-Signature-256: sha256=<hex>`.
- Unseal `webhook_secret_enc` with the KEK (`libs/crypto/aes`).
- Compute the digest, compare with `hmac.Equal` (constant time).
- Fail closed: config missing / `enabled=false` / secret unset / KEK unset →
  return a sentinel that the handler maps to `DISABLED` (→ HTTP 404 at the
  edge, so a probe can't distinguish "no integration" from "bad signature").
- Signature mismatch → `PermissionDenied`.

### 7.2 GitHub payload parsing (`github.go`)

Parse only the fields needed (ignore the rest):

```go
type githubPREvent struct {
    Action      string `json:"action"`   // opened, reopened, closed, synchronize, ...
    Number      int    `json:"number"`
    PullRequest struct {
        Merged bool `json:"merged"`
    } `json:"pull_request"`
    Repository struct {
        FullName string `json:"full_name"` // "owner/repo"
        Name     string `json:"name"`      // "repo"
    } `json:"repository"`
}
```

`X-GitHub-Event: ping` or any non-`pull_request` event → `IGNORED` (200/204).

### 7.3 Namespace name derivation (`name.go`)

`orgName = "pr-" + sanitize(repository.name) + "-" + strconv(number)`

- `sanitize`: lowercase; replace any char outside `[a-z0-9-]` with `-`;
  collapse repeats; trim leading/trailing `-`.
- Truncate the repo portion so the whole name satisfies the org regex
  `^[a-z0-9-]{2,64}$`. If derivation can't produce a valid name, return
  `InvalidArgument` (logged, `IGNORED` at the edge — never 500).

### 7.4 Dispatch (`service.go`)

| GitHub `action` | Behaviour |
|---|---|
| `opened`, `reopened` | `GetOrCreateOrganization(orgName)` (idempotent upsert) → upsert `pr_namespaces` (status `active`, capture `org_id`) → emit `pr.namespace.provisioned` → `PROVISIONED`. |
| `closed` with `merged=false` | Look up namespace row; `DeleteOrganization(org_id)` (cascade); mark row `torn_down`; emit `pr.namespace.torn_down` → `TORN_DOWN`. No-op (idempotent) if already torn down or never provisioned. |
| `closed` with `merged=true` | If `promote_target_org` set: promote (see §7.5), then delete + mark + emit → `PROMOTED_AND_TORN_DOWN`. If not set: same as merged=false → `TORN_DOWN`. |
| `synchronize`, `edited`, `labeled`, ... | `IGNORED`. |

Idempotency: every branch tolerates re-delivery (GitHub retries). Provision is
an upsert; teardown checks the row's current status. All lifecycle work for a
single event runs in one metadata transaction where practical; the promote
loop (multiple `PromoteTag` calls) runs before the delete so a promote failure
aborts teardown (the namespace survives for retry).

### 7.5 Promote-on-merge (§7.4 merged branch)

For the namespace's org, enumerate its repositories (`ListRepositories` scoped
to `org_id`) and, per repo, its tags (`ListTags`). For each `(repo, tag)`:

`PromoteTag(src = pr-org/repo:tag, dst = promote_target_org/repo:tag,
create_if_missing = true, actor = "" (system), note = "PR #<N> merge")`

- `repo` on the destination keeps its base name (the pr-org is dropped; the
  repo name inside the namespace is already the plain image name the CI chose).
- Reuses the shipped `PromoteTag` transaction verbatim — no new promotion
  logic. Blobs are content-addressed + tenant-scoped, so no blob copy.
- A `FailedPrecondition` from an immutable destination tag is logged and
  **skipped** (one immutable tag shouldn't block the rest of the merge
  promotion); the teardown still proceeds. This is the one place we don't
  fail the whole operation — documented so it's not read as a bug.

---

## 8. management receiver + admin routes

### 8.1 Inbound receiver

`POST /webhooks/scm/github/pr` — registered **without** `authMW`
(unauthenticated; HMAC is the only gate, verified downstream in metadata).

- `http.MaxBytesReader(w, r.Body, maxBodyBytes)` — cap the body (GitHub PR
  payloads are well under the existing `maxBodyBytes`; reuse it).
- Read `X-GitHub-Event` and `X-Hub-Signature-256` headers.
- Resolve tenant = bootstrap tenant (single-tenant; the `SingleTenantInjector`
  / deployment-metadata path already available to management).
- Call `metadata.HandlePREvent(tenant, "github", body, signature, event)`.
- Map outcome → status: `DISABLED` → **404**; `PermissionDenied` (bad sig) →
  **401**; `IGNORED` → **204**; `PROVISIONED`/`TORN_DOWN`/
  `PROMOTED_AND_TORN_DOWN` → **200** with a small JSON body (`{outcome, org}`).
- Never echo internal error detail; log with the delivery id
  (`X-GitHub-Delivery`) for correlation.

### 8.2 Admin config routes (authenticated, admin-gated)

Mirror the notification-webhook BFF handlers:

- `GET  /api/v1/pr-registry/config` → `metadata.GetPRRegistryConfig` →
  `{enabled, has_secret, promote_target_org, webhook_url, updated_at}`.
  `webhook_url` is a convenience: the fully-qualified public receiver URL the
  admin pastes into GitHub (derived from a `PLATFORM_HOST`-style config).
- `PUT  /api/v1/pr-registry/config` → `metadata.PutPRRegistryConfig`.
  `FailedPrecondition` (KEK unset) → **409**.

Both gated by the existing global-admin check (reuse the same helper the
notification-webhook + email-transport admin routes use).

---

## 9. Events + audit

`libs/rabbitmq/events/events.go` — two new routing keys + payloads:

```go
const (
    RoutingPRNamespaceProvisioned = "pr.namespace.provisioned"
    RoutingPRNamespaceTornDown    = "pr.namespace.torn_down"
)

type PRNamespaceProvisionedPayload struct {
    TenantID   string
    Provider   string
    SourceRepo string
    PRNumber   int
    OrgName    string
}

type PRNamespaceTornDownPayload struct {
    TenantID   string
    Provider   string
    SourceRepo string
    PRNumber   int
    OrgName    string
    Promoted   bool   // true when merged + target promoted
    TargetOrg  string // set when Promoted
}
```

`services/audit` `mapEvent` gains a case per routing key → an `audit_events`
row (`action = "pr.namespace.provisioned"` / `"...torn_down"`,
`resource = "<org_name>"`, `actor = "system"`, `outcome = "success"`,
metadata = raw payload). This satisfies the spec-lint invariant (every event
type maps or carries `// audit: skip`).

---

## 10. Frontend

- **Admin config panel** — a new `PRRegistryPanel` under Settings (alongside
  the notification-webhook + email-transport panels; exact tab chosen during
  implementation — likely a new **Integrations** settings tab or the existing
  workspace settings). Mirrors `NotificationWebhookPanel`: enable toggle,
  read-only copy-able webhook URL, write-only secret field
  (`has_secret` → "secret set" hint), promote-target-org dropdown (sourced
  from the caller's visible orgs like the promote dialog), admin-gated
  (renders null for non-admins). New `lib/api/pr-registry.ts` hooks
  (`usePRRegistryConfig`, `useUpdatePRRegistryConfig`).
- **Active namespaces list** — a read-only table of `status='active'`
  `pr_namespaces` (provider, source repo, PR #, org name, created). Backed by
  a `GET /api/v1/pr-registry/namespaces` BFF route → a metadata
  `ListPRNamespaces` RPC. Minimal: no actions in Phase 1 (teardown is
  webhook-driven), but a manual "tear down" button is a reasonable stretch if
  cheap.

FE gates before push (CLAUDE.md §15.1): `lint`, `typecheck`, `test`, `build`.

---

## 11. Out of scope (Phase 2+)

- **Keyless OIDC push** — extend `oidc_exchange.go` to derive `pr-<N>/*` scope
  from the signed GitHub `ref` claim, removing the manual scoped-API-key step.
  This is the security-critical piece deliberately deferred (§2 D6).
- **GitLab** (and Bitbucket) providers.
- **Grace window / soft teardown** — inspect a failed PR's images before
  hard delete.
- **Manual namespace teardown from the FE** (unless trivially added in §10).
- **Per-namespace storage quota override**, PR-comment status back-links.

---

## 12. Testing

- **metadata unit** — name derivation (`sanitize`, truncation, invalid →
  error), GitHub payload parse (opened/closed-merged/closed-unmerged/
  synchronize/ping), HMAC verify (valid / tampered body / wrong secret /
  disabled / KEK-unset), dispatch idempotency (double-deliver opened, double
  teardown), promote-on-merge fan-out (target set vs unset; immutable-dest
  skip).
- **metadata integration** (testcontainers PG16) — provision creates the org +
  namespace row; teardown deletes the org (cascade) + leaves a `torn_down`
  row (via `ON DELETE SET NULL`); merged-with-target promotes then tears down.
- **management** — receiver forwards raw body + signature unchanged; header
  gating (`ping` → 204, missing signature → 401/404); outcome→status mapping;
  admin config routes incl. `409` on KEK-unset + happy path; unauthenticated
  route is reachable without a JWT but authenticated config routes are not.
- **frontend** — panel admin-gates (null for non-admin), secret is write-only,
  enable/target round-trip, namespaces list renders + empty state.

---

## 13. Security notes

- The inbound route is public + unauthenticated; **HMAC is the entire trust
  boundary.** Constant-time compare, fail-closed on any config gap, body-size
  cap, and a generic `404` when disabled so the endpoint's existence isn't a
  probe oracle. No secret ever crosses the management↔metadata wire (raw body
  + signature go *to* metadata; the secret stays *in* metadata).
- Org auto-creation is bounded: only via this HMAC-verified path, only under
  the `pr-` prefix, only after org-name regex validation.
- Single-tenant: all namespaces belong to the bootstrap tenant; no cross-tenant
  surface.
- Deleting an org is destructive + irreversible — gated behind a verified
  merge/close event, idempotent, and audit-logged via `pr.namespace.torn_down`.
- `PR_REGISTRY_KEY_HEX` follows the established KEK rules (fail-closed wrong
  length; unset disables). It is **not** swept by `rotate-kek` yet — same
  known gap as the notification-webhook/email KEKs (tracked under RED-FU-015);
  note it in `futures.md` rather than solving it here.

---

## 14. Affected surfaces (summary)

| Area | Change |
|---|---|
| `services/metadata/migrations` | `00019_pr_registry.sql` (+down, +grants) |
| `services/metadata/internal/prregistry` | new pkg: verify, github parse, name, dispatch |
| `services/metadata/internal/repository/pr_registry.go` | config + namespaces data access; `DeleteOrganization`; `ListPRNamespaces` |
| `services/metadata/internal/handler/grpc.go` | 4 new RPC handlers |
| `services/metadata/internal/config` | `PR_REGISTRY_KEY_HEX` load + validate |
| `services/metadata/internal/server` | decode KEK, wire prregistry into the handler |
| `proto/metadata/v1/metadata.proto` (+gen) | 6 messages, 4 RPCs |
| `services/management/internal/handler` | inbound receiver + 3 admin/list routes + route registration |
| `libs/rabbitmq/events/events.go` | 2 events |
| `services/audit/internal/eventconsumer` | 2 `mapEvent` cases |
| `frontend` | `PRRegistryPanel`, `pr-registry.ts` hooks, namespaces list, tests |
| docs | `SERVICES.md`, CLAUDE.md metadata row, `.env.example`, `futures.md` (FUT-023 Phase 1 shipped + rotate-kek gap), `status.md` |
