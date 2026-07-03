[//]: # (FUT-001 — federated workload identity. Pair this with docs/AUTH.md for the broader token model and docs/SERVICES.md §2 for the auth service surface.)

# Federated Workload Identity (OIDC Trust)

> Canonical reference for the FUT-001 federated workload identity feature.
> Read this when you're wiring a CI pipeline (GitHub Actions / GitLab CI /
> Buildkite) to push or pull from the registry **without a stored secret**,
> when you're an operator configuring the trust boundary, or when you're
> extending the exchange flow in `services/auth`.
>
> For the human-facing token model (JWT key ring, API keys, Bearer
> dispatch) see [`docs/AUTH.md`](AUTH.md). For how a CI job actually
> plugs the minted token into `docker login` / `helm`, see
> [`docs/CREDENTIAL-HELPERS.md`](CREDENTIAL-HELPERS.md). For narrowing
> what a minted token can do, see [`docs/TOKEN-POLICIES.md`](TOKEN-POLICIES.md).

---

## TL;DR

- **The problem:** CI runners need registry credentials, but a long-lived
  API key pasted into a CI secret is a standing liability — it leaks, it
  never rotates, and revoking it means editing every pipeline.
- **The fix:** the CI platform already hands each job a short-lived,
  cryptographically-signed **OIDC JWT** describing *who is running*
  (`repo:acme/api:ref:refs/heads/main`). The runner POSTs that token to
  `POST /auth/token/workload` and gets back a **15-minute registry JWT**
  scoped to a service account. **Nothing is stored** — the OIDC JWT is the
  credential, and it's already minted fresh per job by the CI platform.
- **The trust model:** an operator registers a trust row binding
  `(issuer_url, audience, subject_pattern glob)` → a **service account**.
  A token is only exchanged if all three match *and* its signature
  verifies against the issuer's published JWKS.
- **The boundary:** `OIDC_ALLOWED_ISSUERS` is a deploy-time allowlist.
  Empty means the feature is **off** (fail-closed) — self-hosters name
  the IdPs they trust; there is no "trust the world" default.
- **Fail-closed everywhere:** unknown issuer, JWKS outage, expired token,
  disabled SA — every reject collapses to a generic `401`. The exact
  reason lives only in the audit trail, never in the response body.

---

## 1. Where this fits

The registry already accepts three credential shapes at its edge: RS256
login JWTs, `key.`-prefixed API keys, and (via this feature) workload
tokens minted from an external OIDC JWT. Workload identity is a
credential *bootstrap* — it converts a trust the CI platform vouches for
into a native registry JWT that flows through the same validation path as
any other token.

```
┌───────────────┐   OIDC JWT (per-job, signed by CI IdP)
│  CI runner    │─────────────────────────────────────────────┐
│ (GH Actions,  │                                              │
│  GitLab, …)   │                                              ▼
└───────────────┘                                    POST /auth/token/workload
                                                              │
                                          ┌───────────────────▼────────────────────┐
                                          │             registry-auth               │
                                          │  1. rate-limit (issuer, subject)         │
                                          │  2. issuer allowlist gate                │
                                          │  3. match a trust row                    │
                                          │  4. fetch + cache issuer JWKS            │
                                          │  5. verify RS256 signature               │
                                          │  6. check SA not disabled                │
                                          │  7. mint 15-min registry JWT             │
                                          └───────────────────┬────────────────────┘
                                                              │ { access_token, expires_in: 900,
                                                              │   token_type: "Bearer" }
                                                              ▼
                                          docker login / helm / oras with the JWT
                                          → registry-core validates it like any token
```

---

## 2. The trust model

A trust is one row in `oidc_trust_configs` (migration
`20260701000001_oidc_trust_configs.sql`). It answers one question: *"when
a token from issuer X, minted for audience Y, whose subject matches
pattern Z arrives — which service account does it become?"*

| Column | Meaning |
|---|---|
| `service_account_id` | The SA the minted token is scoped to. `ON DELETE CASCADE` — deleting the SA drops its trusts atomically. |
| `issuer_url` | The OIDC issuer (`iss` claim). Must be `https://` and covered by `OIDC_ALLOWED_ISSUERS`. |
| `audience` | Exact match against the token's `aud` claim. |
| `subject_pattern` | A glob matched against the token's `sub` claim (see §5). |
| `jwks_cache_ttl_seconds` | Per-issuer key-cache TTL. `0` = repo default (3600); otherwise bounded `[60, 86400]`. |
| `last_used_at` | Best-effort writeback on each successful exchange — surfaces stale trusts. |

The tuple `(tenant_id, issuer_url, subject_pattern)` is **UNIQUE**: a
given IdP subject maps to exactly one service account, so a
misconfiguration can never silently fan one CI runner out to two SAs.

The minted token inherits the SA's `allowed_scopes` (mapped to
per-repository access), carries `source=workload_oidc`, records the
originating `trust_id`, and has empty `Roles` — it is a
`service_account` principal, not an admin.

---

## 3. Configuration

### 3.1 `OIDC_ALLOWED_ISSUERS` (required to enable)

A comma-separated list of trusted issuer URL **prefixes**. This is the
security boundary, evaluated at *both* trust-create and every exchange —
removing an issuer from the env stops all its trusts minting on the next
exchange, no DB change needed.

```env
# services/auth/.env
OIDC_ALLOWED_ISSUERS=https://token.actions.githubusercontent.com,https://gitlab.com
```

Rules enforced by the code:

- **Empty = feature off (fail-closed).** With no allowlist, the admin
  RPCs return `Unimplemented` and `POST /auth/token/workload` returns
  `503 "workload token exchange is not configured"`. Nothing is exchanged.
- **Prefix match with a URL-path boundary (SEC-057).** A prefix of
  `https://token.actions.githubusercontent.com` matches that issuer and
  any `…/path` under it, but **not**
  `https://token.actions.githubusercontent.com.evil.com` — the character
  after the matched prefix must be `/` or end-of-string. This is what lets
  a single GitLab entry (`https://gitlab.com`) cover per-project issuer
  paths while rejecting lookalike-domain suffix attacks.
- **Scheme required.** Entries without an `http(s)://` scheme are dropped
  at parse time as almost-certain typos (and could never match a real
  `https://` issuer anyway).

### 3.2 `jwks_cache_ttl_seconds` (per-trust, optional)

Bounds how long a fetched key set is trusted before re-fetch. Validated
at create/update time (SEC-060):

- `0` — sentinel for "use the repo default", which is **3600s**.
- Otherwise must be within **`[60, 86400]`** (1 minute to 24 hours).

The floor stops a tiny TTL turning the service into a JWKS-refetch
amplifier against the IdP; the ceiling stops an enormous TTL pinning a
possibly-retired key set for the life of the deployment. Out-of-band
values are rejected with `InvalidArgument`.

---

## 4. The exchange flow, step by step

`OIDCTrustService.ExchangeWorkloadToken` (in
`services/auth/internal/service/oidc_exchange.go`) runs these gates in
order. The HTTP handler wraps it with a rate-limit pre-check.

0. **Rate limit (HTTP layer).** Keyed on `(issuer, subject)` from the
   *unverified* claims: 100 requests / 60s in Redis. Runs **before** any
   JWKS fetch or signature work so a compromised CI can't burn server
   cycles. The key is `sha256(iss \x00 sub)` so a hostile multi-MB `sub`
   can't bloat a Redis key (SEC-061). **Fail-open** on Redis errors — the
   rate limit is an optimisation, not the security boundary. Over budget
   → `429` with a `Retry-After` header.
1. **Parse unverified.** Read `iss` / `sub` / `aud` to find a candidate
   trust. No signature trust yet — this only decides *which* JWKS to fetch.
2. **Issuer allowlist gate.** `iss` must pass `OIDC_ALLOWED_ISSUERS`.
   Runs before any DB lookup so an attacker firing arbitrary issuers can't
   drive per-request DB load.
3. **Match a trust.** `ListByIssuer(iss)` (indexed), ordered
   `created_at DESC`. The first trust whose `audience == aud` **and** whose
   `subject_pattern` matches `sub` wins; the newest trust takes precedence
   on pattern overlap. No match biases the audit reason: `subject_mismatch`
   if some trust matched the audience, else `audience_mismatch`.
4. **Fetch + cache JWKS** for the issuer (see §6).
5. **Verify the signature.** RS256 is required **explicitly**
   (`WithValidMethods`), so an `alg: none` or HS256 downgrade is rejected.
   The `kid` header selects the RSA key from the JWKS. `exp` in the past →
   `expired`; `nbf` in the future → `not_yet_valid`; anything else →
   `signature_invalid`.
6. **Check the service account.** A disabled SA short-circuits even after
   a valid signature — operators expect "disable the SA" to kill workload
   tokens immediately.
7. **Mint.** Build access from the SA's `allowed_scopes` and issue a
   15-minute RS256 registry JWT (`IssueWorkloadToken`), signed by the same
   key ring as login tokens.
8. **Writeback** `last_used_at` (best-effort — a failure doesn't block the
   response).
9. **Audit** `auth.workload_token.exchanged` (best-effort).

### Reject reasons

Every failure returns the **same** generic `401 unauthorized` to the
caller — the body never leaks which gate failed. The precise
classification is emitted *only* to the `auth.workload_token.rejected`
audit event, so forensics can tell "wrong issuer" from "wrong subject"
while an attacker can't enumerate by response:

| Reason (audit only) | Cause |
|---|---|
| `issuer_not_allowed` | `iss` not in `OIDC_ALLOWED_ISSUERS` |
| `audience_mismatch` | `aud` matched no trust for that issuer |
| `subject_mismatch` | audience matched but `sub` matched no pattern |
| `signature_invalid` | RS256 verify failed (incl. unknown `kid`, bad parse) |
| `expired` | `exp` in the past |
| `not_yet_valid` | `nbf` in the future |
| `sa_disabled` | matched trust's SA is disabled |

A DB outage (step 3) or JWKS/IdP outage (step 4) is **not** the caller's
fault — those surface as `503 idp unreachable` (`codes.Unavailable`) so
the CI retries with backoff rather than treating a blip as an auth denial.

---

## 5. Subject-pattern glob semantics

`subject_pattern` is matched against the `sub` claim by a purpose-built,
anchored glob (`oidc_subject.go`). The `/` boundary is load-bearing —
it mirrors how GitHub Actions / GitLab / Buildkite document their OIDC
subject filters:

| Token | Matches |
|---|---|
| `*` | zero or more chars **excluding** `/` |
| `**` | zero or more chars **including** `/` |
| `?` | exactly one char **excluding** `/` |
| any other char | literal (brackets included — no character-class support) |

The whole `sub` must consume the whole pattern (anchored at both ends).
`validateGlobSyntax` rejects an empty pattern and any run of 3+ `*`
(`***` is ambiguous) at create/update time.

Examples for a GitHub Actions `sub` of
`repo:acme/api:ref:refs/heads/main`:

| Pattern | Matches? | Why |
|---|---|---|
| `repo:acme/api:ref:refs/heads/main` | ✅ | exact |
| `repo:acme/api:*` | ❌ | `*` won't cross the `/` in `refs/heads` |
| `repo:acme/api:**` | ✅ | `**` crosses `/` |
| `repo:acme/api:ref:refs/heads/*` | ✅ | trailing segment has no `/` |
| `repo:acme/*:**` | ❌ | `*` won't cross the `/` in `acme/api` |
| `repo:acme/**:**` | ✅ | `**` spans `acme/api` |

> **Tightness matters.** `repo:acme/**:**` authorises *every* ref and
> workflow in `acme/api`. If you only trust `main`, pin the ref:
> `repo:acme/api:ref:refs/heads/main`.

---

## 6. The per-issuer JWKS cache

`oidc_jwks.go` holds a process-wide, mutex-guarded cache keyed by issuer.

- **Discovery.** `{issuer}/.well-known/openid-configuration` → read
  `jwks_uri` → fetch the JWKS. Only RSA keys are parsed (RS256 is the
  dominant CI-IdP shape); non-RSA entries are skipped.
- **TTL & coalescing.** Hits within TTL serve from memory. The mutex is
  held across the fetch so a thundering herd on one issuer collapses to a
  single network round-trip.
- **Fail-closed.** Stale entries are **never** served past TTL. An IdP
  outage surfaces as `Unavailable` (retryable) rather than trusting keys
  that may have been rotated out upstream.
- **16-issuer cap (SEC-048 analog).** Adding a 17th distinct issuer evicts
  the oldest-fetched entry, bounding memory against a flood of trust configs.

### SSRF hardening

The discovery document is attacker-influenceable (a hostile IdP controls
its body), so `jwks_uri` is untrusted:

- **Same-origin pin (SEC-058).** `jwks_uri` must share the issuer's scheme
  **and** host (case-insensitive, port-inclusive). This blocks a hostile
  discovery doc pointing `jwks_uri` at an RFC-1918 address, a cloud
  metadata endpoint, an alternate port, or a different public host.
- **No redirects (SEC-058).** The HTTP client returns the 30x as-is
  instead of chasing a `Location` off the vetted host.
- **Bounded I/O (SEC-059 / SEC-062).** Response bodies capped at 1 MiB via
  `io.LimitReader`; a 5s overall timeout plus 3s TLS-handshake and 3s
  response-header sub-timeouts stop a hostile server stalling the exchange.

---

## 7. Admin API (managing trusts)

Five RPCs on the auth service (`grpc_oidc_trust.go`); operators normally
reach them through the dashboard or the management BFF, which owns the
RBAC gate (the gRPC layer trusts its caller, per platform convention).

| RPC | Notes |
|---|---|
| `ListOIDCTrusts` | All trusts for the workspace. No pagination — realistic counts are ~10s. |
| `CreateOIDCTrust` | Validates display_name/audience/issuer non-empty, issuer allowlisted + `https://`, glob syntax, TTL bounds, and that the SA exists, belongs to the workspace, and isn't disabled. Duplicate tuple → `AlreadyExists`. |
| `UpdateOIDCTrust` | Mutates **only** `display_name`, `subject_pattern`, `jwks_cache_ttl_seconds`. |
| `DeleteOIDCTrust` | Removes the row (also fires via SA-delete cascade). |
| `ExchangeWorkloadToken` | The public path — also exposed over HTTP (§8). |

> **`issuer_url`, `audience`, and `service_account_id` are append-only.**
> To re-point any of them you must **Delete + Create**, so an IdP-bound
> identity can never be silently swung onto a different SA by an edit.

Each successful mutation emits an audit event
(`auth.oidc_trust.created` / `.updated` / `.deleted`).

---

## 8. The public exchange endpoint

```
POST /auth/token/workload
```

- **No Bearer required** — the OIDC JWT *is* the credential.
- **Two input forms**, body taking precedence over the header so a stale
  header can't override an explicit body:
  - Body: `{"oidc_jwt": "<jwt>"}` (body capped at 8 KiB)
  - Header: `Authorization: Bearer <jwt>`
- **Response** (OAuth 2.0 §5.1 shape):

  ```json
  { "access_token": "<15-min RS256 registry JWT>",
    "expires_in": 900,
    "token_type": "Bearer" }
  ```

- **Errors** are JSON `{"error": "..."}`: `400 missing oidc_jwt`,
  `401 unauthorized` (any reject, incl. malformed token),
  `429 rate limit exceeded` (+ `Retry-After`),
  `503` (feature off, or IdP/DB unreachable).

---

## 9. Worked example — GitHub Actions

**One-time operator setup.** Create a service account with the scopes the
pipeline needs, then register a trust binding GitHub's issuer + your
chosen audience + a subject pinned to the repo and branch:

```bash
# POST via the management BFF (RBAC-gated). issuer_url must be covered by
# OIDC_ALLOWED_ISSUERS; subject_pattern is matched against the sub claim.
curl -X POST https://registry.example.com/api/v1/access/oidc-trust \
  -H "Authorization: Bearer $ADMIN_JWT" \
  -H "Content-Type: application/json" \
  -d '{
    "service_account_id": "b3f1...-sa-that-can-push-acme-api",
    "display_name": "acme/api main branch",
    "issuer_url": "https://token.actions.githubusercontent.com",
    "audience": "registry.example.com",
    "subject_pattern": "repo:acme/api:ref:refs/heads/main"
  }'
```

**The workflow.** Grant `id-token: write`, ask GitHub for a JWT with an
audience that matches the trust, exchange it, and log in — **no secret in
the repo**:

```yaml
# .github/workflows/publish.yml
permissions:
  id-token: write        # lets the job mint an OIDC JWT
  contents: read

jobs:
  push:
    runs-on: ubuntu-latest
    steps:
      - name: Exchange OIDC JWT for a registry token
        id: token
        run: |
          # GitHub injects ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN for id-token: write.
          # The audience MUST equal the trust's `audience`.
          OIDC_JWT="$(curl -sSf \
            -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=registry.example.com" \
            | jq -r '.value')"

          REG_JWT="$(curl -sSf \
            -X POST https://registry.example.com/auth/token/workload \
            -H 'Content-Type: application/json' \
            -d "{\"oidc_jwt\": \"$OIDC_JWT\"}" \
            | jq -r '.access_token')"

          echo "::add-mask::$REG_JWT"
          echo "reg_jwt=$REG_JWT" >> "$GITHUB_OUTPUT"

      - name: Log in and push
        run: |
          echo "${{ steps.token.outputs.reg_jwt }}" \
            | docker login registry.example.com -u workload --password-stdin
          docker push registry.example.com/acme/api:${{ github.sha }}
```

The same shape works for **GitLab CI** (`CI_JOB_JWT_V2` /
`id_tokens`, issuer `https://gitlab.com`) and **Buildkite** (OIDC token
via `buildkite-agent oidc request-token`) — only the issuer and the
`sub` format differ. See [`docs/CREDENTIAL-HELPERS.md`](CREDENTIAL-HELPERS.md)
for a helper that wraps the exchange + `docker login` in one step.

---

## 10. Security posture summary

| Property | How it's enforced |
|---|---|
| No stored secret | The credential is the CI platform's per-job OIDC JWT, minted fresh each run. |
| Explicit trust boundary | `OIDC_ALLOWED_ISSUERS` allowlist, empty = feature off (fail-closed). |
| Lookalike-domain defence | URL-path-boundary prefix match (SEC-057). |
| No `alg` downgrade | RS256 required explicitly; `none` / HS256 rejected. |
| SSRF defence | `jwks_uri` same-origin pin + no-redirect client + 1 MiB body cap + sub-phase timeouts (SEC-058/059/062). |
| No stale keys | JWKS cache fail-closed past TTL; bounded `[60, 86400]` (SEC-060). |
| Bounded memory | 16-issuer cache cap (SEC-048 analog); hashed rate-limit keys (SEC-061). |
| Short blast radius | 15-minute minted token, empty `Roles`, scoped to one SA's `allowed_scopes`. |
| Immediate revocation | disabling the SA rejects on the next exchange (`sa_disabled`). |
| No information leak | every reject → generic `401`; reason lives only in the audit event. |
| Full audit trail | `auth.oidc_trust.{created,updated,deleted}` + `auth.workload_token.{exchanged,rejected}`. |

---

## 11. Reference: code map

| Concern | File |
|---|---|
| Admin CRUD + validation + audit | `services/auth/internal/service/oidc_trust.go` |
| Exchange flow + reject reasons | `services/auth/internal/service/oidc_exchange.go` |
| Per-issuer JWKS cache + SSRF pin | `services/auth/internal/service/oidc_jwks.go` |
| Issuer allowlist (SEC-057) | `services/auth/internal/service/oidc_issuer.go` |
| Subject glob matcher | `services/auth/internal/service/oidc_subject.go` |
| Token minting + TTL constant | `services/auth/internal/service/auth.go` (`IssueWorkloadToken`) |
| Public HTTP endpoint + rate limit | `services/auth/internal/handler/http_workload_token.go` |
| gRPC handlers (5 RPCs) | `services/auth/internal/handler/grpc_oidc_trust.go` |
| Repository | `services/auth/internal/repository/oidc_trust.go` |
| Trust table schema | `services/auth/migrations/20260701000001_oidc_trust_configs.sql` |
| Proto contract | `proto/auth/v1/auth.proto` (`OIDCTrust`, `ExchangeWorkloadToken*`) |
| Env var | `services/auth/internal/config/config.go` (`OIDC_ALLOWED_ISSUERS`) |

---

## 12. Limitations & follow-ups

| Limitation | Notes | Tracked |
|---|---|---|
| RSA / RS256 keys only | EC (ES256) JWKS entries are skipped; add once a non-RSA issuer enters a trust list. | Follow-up |
| Cache mutex serialises across issuers | Fine for the typical 1–2 issuer self-host; a `singleflight.Group` upgrade is noted for heavy multi-issuer use. | REM-023 |
| Sliding rate-limit window | `INCR`+`EXPIRE` slides the TTL forward per hit (keep-alive fixed-window), not a strict boundary reset. | REM-023 |
| Audience is exact-match only | No glob on `aud` — one trust per audience value. | By design |
| Actor on trust mutations | The gRPC layer doesn't yet thread the admin actor into the audit event (BFF captures it in a future task). | Follow-up |

---

> **Last updated:** see `git log -- docs/WORKLOAD-IDENTITY.md`.
> **Found a gap?** This doc is the canonical reference — any divergence
> between the code and this file is the file's bug. Open an issue with the
> label `auth-sso`.
