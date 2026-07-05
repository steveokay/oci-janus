# Active Session List + Per-Row Revoke — Design

> **Status:** approved design (brainstorming complete). Next step: implementation plan via `writing-plans`.
> **Feature:** Tier-1 #1 residual ("MFA + session management") — the session-management half.
> **Branch:** `feat/mfa-session-list`
> **Date:** 2026-07-05
> **Posture:** single-tenant (`DEPLOYMENT_MODE=single`).

## Goal

Give a signed-in user a list of their active sessions — device, IP, last active — on
`/settings/account`, with a per-row **Revoke** button and a **"Sign out all other
sessions"** action. This closes the session-management half of Tier-1 #1 (the TOTP
MFA half shipped in PR #267–#269).

## Motivating finding (why this is not just "wire up a table")

The `futures.md` note said this feature "backs onto the existing `auth_login_sessions`
table." **That premise is wrong.** `auth_login_sessions` is short-lived SSO
redirect-dance CSRF/PKCE state (single-use, 10-minute expiry) — it does **not** track
user sessions. There is no session/device tracking today.

The token model is fully stateless:

- Access JWT: RS256, **300-second TTL**, held in memory by the FE (zustand store).
- Kept alive by `POST /api/v1/token/refresh`, which validates a live JWT and issues a
  replacement **with a fresh JTI every time**.
- Revocation is Redis-based: `revoke:jti:<id>` (one token) and `revoke:user:<id>`
  (whole principal). Both are consulted in `ValidateToken` and are **fail-closed**
  (Redis unreachable ⇒ deny).

Because the JTI rotates on every refresh, there is no stable handle to anchor "the
session on device X." So the feature requires introducing a **stable session id
(`sid`)** that survives refreshes, threaded login → JWT claim → refresh → validation.

## Decisions (locked during brainstorming)

| Decision | Choice | Rationale |
|---|---|---|
| Session store | **Postgres source-of-truth + Redis hot-path** | Durable, auditable list survives a Redis flush; matches the platform's per-service-Postgres + audit-chain posture. Redis does only the fail-closed `revoke:sid` gate and the `last_active` debounce. |
| Which logins create sessions | **Interactive only** — password `/login`, `/login/mfa` completion, SSO callback | API-key / service-account (`Bearer key.*`) and the OCI `/v2` token flow are machine identities tracked elsewhere; they must not flood the human-session list. |
| `last_active` update | **Debounced per-request via Redis (60s)** | Mirrors the FUT-003 API-key `last_used` pattern: at most one DB write per 60s per session, one Redis op on the hot path. |
| Session expiry | **Idle window + absolute max** | Idle = `token_policies.idle_revoke_days` (default 14d) since `last_active`; absolute max = 30d since `created_at`; whichever first. Reuses existing policy config. |
| Device label | **Light UA parse** | Short human label ("Chrome on macOS", "Docker CLI") from a small in-house parser (browser + OS family, no external dep); raw UA also stored for the tooltip. |
| Sessions DB | **auth service DB** | Auth owns identity and already owns MFA + token policy. |
| Absolute max / idle default | **30d absolute / 14d idle** | Confirmed during brainstorming. |

## Architecture

```
POST /login (password)  ─┐
POST /login/mfa (final) ─┼─► mint sid ─► INSERT user_sessions(sid,…) ─► JWT{…, sid}
SSO callback            ─┘

Every authed request across all services ─► registry-auth.ValidateToken(gRPC/HTTP)
    ├─ existing: revoke:jti / revoke:user checks (fail-closed)
    ├─ NEW: if sid != "" and revoke:sid:<sid> present ⇒ deny (fail-closed)
    └─ NEW: debounced last_active (SETNX sid_active:<sid> 60s ⇒ on miss, UPDATE last_active_at)

POST /token/refresh ─► ValidateToken ─► IssueToken carrying claims.Sid verbatim (JTI rotates)

Self-service (auth HTTP, requireAuth):
    GET    /api/v1/users/me/sessions                 → list live sessions (current flagged)
    DELETE /api/v1/users/me/sessions/{sid}           → revoke one (ownership-checked)
    POST   /api/v1/users/me/sessions/revoke-others   → revoke all but current
```

## 1. Data model

New goose migration in `services/auth/migrations/` — `YYYYMMDDHHMMSS_user_sessions.sql`.

```sql
CREATE TABLE user_sessions (
    sid            UUID PRIMARY KEY,
    user_id        UUID NOT NULL,
    tenant_id      UUID NOT NULL,           -- bootstrap tenant in single mode
    device_label   TEXT NOT NULL,           -- "Chrome on macOS" (parsed)
    user_agent     TEXT NOT NULL,           -- raw UA, for the tooltip / detail
    ip             INET NOT NULL,           -- from the trusted-proxy clientIP() logic
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,    -- created_at + absolute max (30d)
    revoked_at     TIMESTAMPTZ              -- NULL = live
);

CREATE INDEX idx_user_sessions_user_live
    ON user_sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_user_sessions_expires
    ON user_sessions (expires_at);
```

Down migration drops the table. `tenant_id` is retained in both deployment modes
(single mode populates it with the bootstrap tenant id), per CLAUDE.md §9.

**Redis keys**

- `revoke:sid:<sid>` — the fail-closed revocation gate. Set on revoke with
  `TTL = remaining absolute lifetime` (bounded ≤ 30d), so the entry auto-expires
  exactly when the session could no longer exist (SEC-005 TTL-coupling philosophy).
- `sid_active:<sid>` — 60s debounce marker for `last_active` writes.

## 2. Session lifecycle

### Claim + issuance

- Add `Sid string \`json:"sid,omitempty"\`` to `service.Claims`.
- `IssueToken` gains a trailing `sid string` parameter (empty ⇒ no session). Every
  existing caller is updated in the same commit (the same shape as when `amr` was
  added). Non-session paths (API-key dispatch, OCI `/v2` token, workload OIDC) pass
  `""`.
- The interactive login handlers generate a `sid` (`uuid.New()`), resolve the
  device label from `User-Agent`, capture the client IP via the existing
  trusted-proxy `clientIP(r)` helper (SEC-009), `INSERT` the `user_sessions` row,
  and mint the JWT with the `sid` claim.
  - Password login: `login` handler.
  - MFA login: `IssueMFACompletedToken` (also used by forced-enrol completion — a
    forced-enrol completion is an interactive login, so it gets a session too).
  - SSO: the SSO callback token-issue path.
- The MFA **challenge** and **setup** tokens are typed, short-lived, and carry **no**
  `sid` (they are not sessions). `ValidateToken` already rejects typed tokens.

### Refresh

`RefreshToken` copies `claims.Sid` verbatim into the replacement token. The JTI still
rotates; the `sid` is the stable anchor.

### last_active (debounced)

In `ValidateToken`, when `claims.Sid != ""`:

1. `SETNX sid_active:<sid> "1" EX 60`.
2. On the once-per-60s miss (key already present ⇒ no-op), skip. On set (first in the
   window) ⇒ enqueue an async `UPDATE user_sessions SET last_active_at = now() WHERE
   sid = $1`. Reuses the `lastUsedUpdater` debounce shape from FUT-003.

`last_active` therefore tracks activity at ~60s granularity with at most one DB write
per session per minute. Redis-down here is **fail-open** (last_active is telemetry, not
a security boundary) — matching the API-key `last_used` posture, and distinct from the
`revoke:sid` gate which is fail-closed.

### Expiry

A session is **live** when:

```
revoked_at IS NULL
AND expires_at > now()                                   -- absolute max (30d)
AND last_active_at > now() - <idle_window>               -- idle (token_policies.idle_revoke_days, default 14d)
```

The list query filters on this predicate, so an idle-expired-but-not-yet-swept row never
appears in the list regardless of sweep timing. A lightweight periodic sweep
(`DELETE FROM user_sessions WHERE expires_at < now() OR last_active_at < now() - <idle window>`)
garbage-collects long-dead rows so the table stays small. The sweep cadence reuses the
existing background-worker pattern in the auth service.

## 3. Revocation

- **Gate:** `ValidateToken` adds, alongside the existing `revoke:user` check, a
  `revoke:sid:<sid>` check (only when `sid != ""`). Present ⇒ deny. **Fail-closed** on
  a Redis error, identical to the `revoke:user` posture. Once set, the current token is
  denied on its next request and refresh cannot roll it — the session dies within
  ≤300s, effectively immediately.
- **Revoke one** — `DELETE /users/me/sessions/{sid}`: verifies the `sid` belongs to the
  caller (`WHERE sid = $1 AND user_id = $2`); a non-matching sid returns **404** (no
  cross-user enumeration). Sets `revoked_at = now()` and
  `SET revoke:sid:<sid> "1" EX <remaining life>`.
- **Revoke others** — `POST /users/me/sessions/revoke-others`: revokes every live
  session for the caller **except** `claims.Sid` (the current one). The "sign out
  everywhere else" button.
- Revoking the **current** session is permitted (an explicit "sign out this device");
  the FE treats the response as a logout.

## 4. API surface

Auth-service HTTP, self-service, mounted next to `/users/me/mfa` and `requireAuth`-gated
with a **normal access token** (a setup token must not manage sessions — same boundary
the MFA disable handler enforces):

| Method + path | Behaviour |
|---|---|
| `GET /api/v1/users/me/sessions` | Live sessions for the caller. Each row: `sid`, `device_label`, `user_agent`, `ip`, `created_at`, `last_active_at`, and `current: bool` (true when `sid == claims.Sid`). |
| `DELETE /api/v1/users/me/sessions/{sid}` | Revoke one owned session. 204 on success, 404 if not owned/not found. |
| `POST /api/v1/users/me/sessions/revoke-others` | Revoke all live sessions except the current. Returns the count revoked. |

No management-BFF or gRPC hop: sessions follow the MFA self-service pattern (the FE
calls the auth service directly through the `/users` dev proxy / gateway route). IPs,
UAs, and sids are non-secret but the list is scoped strictly to the caller's own
`user_id`; nothing is logged that isn't already in the request.

## 5. Frontend

A **Sessions card** on `/settings/account`, below the MFA card, following the existing
API-keys table pattern (`frontend/src/components/...`):

- Columns: **Device** (parsed label, raw UA in a tooltip) · **IP** · **Last active**
  (relative time) · a **"This device"** badge on the current row · a per-row
  **Revoke** button.
- A **"Sign out all other sessions"** action above the table.
- Revoking the current session logs the user out (clears the auth store, redirects to
  `/login`).
- New `frontend/src/lib/api/sessions.ts` (types + fetchers) + TanStack Query hooks
  (`useSessions`, `useRevokeSession`, `useRevokeOtherSessions`) with query invalidation.
- Standard skeleton / empty / error / loaded states (no `—` fallbacks), per the Beacon
  design rules.

## 6. Error handling

- Redis down on the `revoke:sid` gate ⇒ **deny** (fail-closed), logged + metric,
  matching `revoke:user`.
- Redis down on the `last_active` debounce ⇒ **fail-open** (skip the update); never
  blocks a request.
- A session-`INSERT` failure at login is **fatal to that login** (the user retries) —
  we must not issue a `sid` claim without a backing row, or the gate/list would be
  inconsistent. (Contrast: MFA post-enrol token issue is non-fatal because the factor
  was already enabled; here the row *is* the session.)
- Revoke of a non-owned/absent `sid` ⇒ 404, uniform, no enumeration.
- Malformed `sid` path param ⇒ 400 before any DB call.

## 7. Testing

- **Service unit:** session row created on each interactive login path; `sid` survives
  `RefreshToken`; `revoke:sid` gate denies (and fail-closes on Redis error);
  `last_active` debounce writes at most once/60s and fail-opens; ownership-scoped
  revoke; revoke-others excludes current.
- **Handler:** list returns only the caller's live sessions with the current flagged;
  revoke one (204) + cross-user 404; revoke-others count; setup-token rejected (401).
- **Migration:** up/down apply cleanly against the integration Postgres.
- **Frontend:** card renders list, current-session badge, revoke flow + invalidation,
  sign-out-others, empty/error states.

## 8. Out of scope (explicitly deferred)

- **WebAuthn / hardware keys** — the other Tier-1 #1 residual; separate spec.
- **Admin view of *other users'* sessions** — this feature is self-service only
  (`/users/me/sessions`). A platform-admin cross-user session view can follow if needed.
- **Geo/IP enrichment** (city/country lookup) — show the raw IP only; no external geo
  service (CSP/SSRF posture + no new dependency).
- **Named/labelled sessions** ("rename this device") — YAGNI.

## 9. Affected components

- `services/auth`: migration; `repository/session.go` (new); `service` Claims + `IssueToken`
  + `RefreshToken` + `ValidateToken` gate/debounce + session create/list/revoke;
  `handler/http_sessions.go` (new) + route registration; UA-parse helper; background sweep.
- `frontend`: `lib/api/sessions.ts`, a Sessions card component + hooks, wired into
  `/settings/account`.
- Docs: `docs/AUTH.md` (session model + revoke:sid gate), `docs/SERVICES.md` §2 (routes),
  `.env.example` if any new knob is introduced (none expected — idle reuses
  `token_policies.idle_revoke_days`; absolute max is a code constant unless we choose to
  make it configurable during planning).
