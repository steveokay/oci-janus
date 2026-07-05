# Active Session List + Per-Row Revoke — Implementation Plan

> **✅ SHIPPED — PR #270 (session pagination follow-up in #277). Plan complete; canonical status in `status.md` / `FE-STATUS.md`. Task checkboxes left unticked — this is a subagent-driven execution artifact, not a live tracker.**

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a signed-in user a list of their active sessions (device, IP, last active) on `/settings/account` with per-row revoke + "sign out all other sessions", backed by a durable `user_sessions` table and a stable `sid` JWT claim.

**Architecture:** Introduce a stable session id (`sid`) minted at each interactive login (password / MFA / SSO), persisted in a Postgres `user_sessions` row and embedded in the JWT. The `sid` survives the 300s-TTL JTI-rotating refresh. `ValidateToken` gains a fail-closed `revoke:sid` Redis gate (mirroring the existing `revoke:user` gate) plus a fail-open debounced `last_active` write (mirroring the FUT-003 `last_used` debouncer). Self-service HTTP routes on the auth service (mirroring `/users/me/mfa`) list and revoke sessions.

**Tech Stack:** Go 1.25 (`pgx/v5`, `goose` migrations, `go-redis`), auth service; React + TanStack Query + axios frontend.

**Design doc:** [`docs/superpowers/specs/2026-07-05-active-session-list-design.md`](../specs/2026-07-05-active-session-list-design.md)

---

## Key codebase facts (read before starting)

- **Login mints the token inside the service, not the handler.** `Service.Login` (`services/auth/internal/service/auth.go:729`) returns a `LoginResult` whose `Token` is already signed. So the client IP + User-Agent must be passed *into* the service. We thread a `SessionMeta{IP, UserAgent string}` value, exactly the way `amr []string` was threaded when MFA landed.
- **`IssueToken` is the common signing sink** (`auth.go:338`). Its callers today:
  - `handler/http.go:302` — the OCI `/auth/token` (Docker) path → **not a session**, pass `""`.
  - `auth.go:677` — `RefreshToken` → pass `claims.Sid` (preserve).
  - `auth.go:782` — inside `VerifyLoginMFA` (the MFA login step-up) → **session**.
  - `mfa.go:99` (approx) — `IssueMFACompletedToken` (forced-enrol completion) → **session**.
  - `sso.go:561` — `IssueSSOToken` → **session**.
- **Client IP helper:** `remoteIP(r)` (`handler/http.go:1016`) already implements the SEC-009 trusted-proxy logic. Reuse it.
- **Debounce pattern to mirror:** `lastUsedUpdater` (`service/last_used_debounce.go`) — `SETNX` a debounce key with a TTL, fail-**open** on Redis error, fire-and-forget goroutine with `context.Background()`.
- **Fail-closed revocation gate to mirror:** the `revoke:user:<subject>` block in `ValidateToken` (`auth.go:483`) — `redis.Get`, on error (not `redis.Nil`) return `status.Error(codes.Unavailable, …)`.
- **Background sweep to mirror:** `runLoginSessionCleanup` (`server/server.go:575`) — a `time.NewTicker(60s)` loop that deletes expired `auth_login_sessions` rows.
- **Self-service route + handler pattern to mirror:** `/api/v1/users/me/mfa` handlers in `handler/http_mfa.go`, `requireAuth`-gated, registered in `handler/http.go:208-211`.
- **Test harness:** service unit tests use `NewWithFakesAndRing` + fake repos (`service/service_repo_test.go`) + `newTestRedis(t)` (miniredis). Handler tests use `newTestServer(t)` / `newMFATestServer(t)` + `seedTestUser` + `makeMeRequest` (`handler/http_test.go`, `handler/http_users_me_test.go`). Repo tests are integration-gated (testcontainers Postgres).

---

## File Structure

**Create:**
- `services/auth/migrations/20260706000001_user_sessions.sql` — the table.
- `services/auth/internal/repository/session.go` — `SessionRepository` (all SQL for `user_sessions`).
- `services/auth/internal/service/useragent.go` — `parseDeviceLabel(ua string) string` (pure).
- `services/auth/internal/service/session.go` — `SessionMeta`, `issueSessionToken`, list/revoke service methods, the `sessionActiveUpdater` debouncer.
- `services/auth/internal/handler/http_sessions.go` — the 3 self-service handlers.
- `services/auth/internal/worker/session_sweep.go` — the expiry sweep worker.
- `frontend/src/lib/api/sessions.ts` — types + fetchers + hooks.
- `frontend/src/components/profile/sessions-card.tsx` — the Sessions card. *(exact dir confirmed in Task 15)*

**Modify:**
- `services/auth/internal/service/auth.go` — `Claims.Sid`; `IssueToken` `sid` param; `ValidateToken` gate + touch; `RefreshToken` preserve; `Login` `meta` param.
- `services/auth/internal/service/mfa.go` — `IssueMFACompletedToken` + `VerifyLoginMFA` `meta` param.
- `services/auth/internal/service/sso.go` — `IssueSSOToken` `meta` param.
- `services/auth/internal/handler/http.go` — `login` builds `meta`; route registration.
- `services/auth/internal/handler/http_mfa.go` — `loginMFA` + `mfaVerify` build `meta`.
- the SSO callback handler — build `meta` *(located in Task 8)*.
- `services/auth/internal/server/server.go` — wire `SessionRepository`, the debouncer, and the sweep worker.
- `frontend/src/routes/_authenticated.settings.account.tsx` — slot in `<SessionsCard />`.
- `docs/AUTH.md`, `docs/SERVICES.md`.

---

## Task 1: `user_sessions` migration

**Files:**
- Create: `services/auth/migrations/20260706000001_user_sessions.sql`

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- user_sessions tracks interactive login sessions (password / MFA / SSO) so a
-- user can list and revoke them. The stable sid is embedded in the JWT and
-- survives the 300s-TTL JTI-rotating refresh (the JTI is not stable; the sid is).
CREATE TABLE user_sessions (
    sid            UUID PRIMARY KEY,
    user_id        UUID NOT NULL,
    tenant_id      UUID NOT NULL,
    device_label   TEXT NOT NULL,
    user_agent     TEXT NOT NULL,
    ip             INET NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ
);

-- Live-session lookups for the list + revoke-others paths.
CREATE INDEX idx_user_sessions_user_live
    ON user_sessions (user_id) WHERE revoked_at IS NULL;
-- Sweep by expiry.
CREATE INDEX idx_user_sessions_expires
    ON user_sessions (expires_at);

-- +goose Down
DROP TABLE user_sessions;
```

- [ ] **Step 2: Verify the migration applies**

Run: `cd services/auth && GOWORK=off go build ./...`
Expected: builds (the `embed.FS` picks up the new `.sql`; goose runs it at startup / in integration tests).

- [ ] **Step 3: Commit**

```bash
git add services/auth/migrations/20260706000001_user_sessions.sql
git commit -m "feat(auth): user_sessions table (session list migration)"
```

---

## Task 2: User-Agent → device label parser

**Files:**
- Create: `services/auth/internal/service/useragent.go`
- Test: `services/auth/internal/service/useragent_test.go`

- [ ] **Step 1: Write the failing test**

```go
package service

import "testing"

func TestParseDeviceLabel(t *testing.T) {
	cases := []struct{ ua, want string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36", "Chrome on macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36", "Chrome on Windows"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0", "Firefox on Linux"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", "Safari on macOS"},
		{"docker/24.0.7 go/go1.21.3 git-commit/311b9ff0aa2b os/linux arch/amd64", "Docker CLI"},
		{"", "Unknown device"},
		{"curl/8.4.0", "curl/8.4.0"}, // unknown but non-empty → show a trimmed raw token
	}
	for _, c := range cases {
		if got := parseDeviceLabel(c.ua); got != c.want {
			t.Errorf("parseDeviceLabel(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestParseDeviceLabel`
Expected: FAIL — `undefined: parseDeviceLabel`.

- [ ] **Step 3: Implement the parser**

```go
// Package service — useragent.go: a tiny, dependency-free User-Agent classifier
// that turns a raw UA string into a short human label for the session list
// ("Chrome on macOS", "Docker CLI"). It is deliberately coarse: browser family
// + OS family is all the session UI needs, and a heuristic parser avoids adding
// a UA-parsing dependency. The raw UA is stored alongside for the tooltip.
package service

import "strings"

// parseDeviceLabel returns a short "<client> on <os>" label, or a coarse
// fallback. It never returns an empty string.
func parseDeviceLabel(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return "Unknown device"
	}
	// Non-browser clients first (their UAs don't carry an OS token we surface).
	if strings.HasPrefix(ua, "docker/") || strings.Contains(ua, "docker/") {
		return "Docker CLI"
	}

	client := browserFamily(ua)
	os := osFamily(ua)
	if client == "" {
		// Unknown but non-empty: show the leading token (e.g. "curl/8.4.0"),
		// trimmed, so the row is still identifiable without dumping the whole UA.
		if i := strings.IndexByte(ua, ' '); i > 0 {
			return ua[:i]
		}
		return ua
	}
	if os == "" {
		return client
	}
	return client + " on " + os
}

// browserFamily returns the browser name, or "" if not a recognised browser.
// Order matters: Edge/Chrome both contain "Chrome"; Chrome contains "Safari".
func browserFamily(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Chrome/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		return "Safari"
	default:
		return ""
	}
}

// osFamily returns the OS name, or "" if not recognised.
func osFamily(ua string) string {
	switch {
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "macOS"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		return "iOS"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "Linux"
	default:
		return ""
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestParseDeviceLabel`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/useragent.go services/auth/internal/service/useragent_test.go
git commit -m "feat(auth): User-Agent device-label parser for session list"
```

---

## Task 3: `SessionRepository` (Postgres access)

**Files:**
- Create: `services/auth/internal/repository/session.go`
- Test: `services/auth/internal/repository/session_integration_test.go`

- [ ] **Step 1: Write the repository**

```go
// Package repository — session.go: all SQL for the user_sessions table
// (migration 20260706000001). Sessions anchor the stable sid embedded in the
// JWT; the service layer owns the sid lifecycle. No SQL for this table lives
// outside this file (CLAUDE.md §11).
package repository

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session is one row of user_sessions.
type Session struct {
	SID          uuid.UUID
	UserID       uuid.UUID
	TenantID     uuid.UUID
	DeviceLabel  string
	UserAgent    string
	IP           string // stringified INET
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	RevokedAt    *time.Time
}

// SessionRepository owns user_sessions.
type SessionRepository struct {
	pool *pgxpool.Pool
}

// NewSessionRepository constructs a SessionRepository.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

// Create inserts a new session row.
func (r *SessionRepository) Create(ctx context.Context, s Session) error {
	// Validate the IP parses as an inet so a malformed value can't be stored.
	if _, err := netip.ParseAddr(s.IP); err != nil {
		return fmt.Errorf("session ip %q: %w", s.IP, err)
	}
	const q = `INSERT INTO user_sessions
		(sid, user_id, tenant_id, device_label, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::inet, $7)`
	_, err := r.pool.Exec(ctx, q, s.SID, s.UserID, s.TenantID, s.DeviceLabel, s.UserAgent, s.IP, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// ListLive returns the user's non-revoked, non-expired, non-idle sessions,
// newest-active first. idleCutoff is now()-idleWindow; rows with
// last_active_at at or before it are treated as dead and excluded.
func (r *SessionRepository) ListLive(ctx context.Context, userID uuid.UUID, idleCutoff time.Time) ([]Session, error) {
	const q = `SELECT sid, user_id, tenant_id, device_label, user_agent, host(ip),
		       created_at, last_active_at, expires_at, revoked_at
		FROM user_sessions
		WHERE user_id = $1 AND revoked_at IS NULL
		  AND expires_at > now() AND last_active_at > $2
		ORDER BY last_active_at DESC`
	rows, err := r.pool.Query(ctx, q, userID, idleCutoff)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.SID, &s.UserID, &s.TenantID, &s.DeviceLabel, &s.UserAgent,
			&s.IP, &s.CreatedAt, &s.LastActiveAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeOwned marks one session revoked, but only if it belongs to userID.
// Returns the session's expires_at (for the Redis gate TTL) and true when a row
// was updated; ok=false means the sid was absent or not owned by the caller.
func (r *SessionRepository) RevokeOwned(ctx context.Context, userID, sid uuid.UUID) (expiresAt time.Time, ok bool, err error) {
	const q = `UPDATE user_sessions SET revoked_at = now()
		WHERE sid = $1 AND user_id = $2 AND revoked_at IS NULL
		RETURNING expires_at`
	err = r.pool.QueryRow(ctx, q, sid, userID).Scan(&expiresAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("revoke session: %w", err)
	}
	return expiresAt, true, nil
}

// RevokeOthers marks all of the user's live sessions revoked EXCEPT keepSID,
// returning the (sid, expires_at) of each so the caller can set the Redis gate.
func (r *SessionRepository) RevokeOthers(ctx context.Context, userID, keepSID uuid.UUID) ([]Session, error) {
	const q = `UPDATE user_sessions SET revoked_at = now()
		WHERE user_id = $1 AND sid <> $2 AND revoked_at IS NULL
		RETURNING sid, expires_at`
	rows, err := r.pool.Query(ctx, q, userID, keepSID)
	if err != nil {
		return nil, fmt.Errorf("revoke other sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.SID, &s.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan revoked session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TouchLastActive bumps last_active_at to now() for a live session. A no-op row
// count (revoked/expired/absent sid) is not an error — the debouncer swallows it.
func (r *SessionRepository) TouchLastActive(ctx context.Context, sid uuid.UUID, at time.Time) error {
	const q = `UPDATE user_sessions SET last_active_at = $2
		WHERE sid = $1 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, q, sid, at)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteExpired garbage-collects rows past their absolute expiry or older than
// the idle cutoff. Returns the number of rows deleted. Called by the sweep.
func (r *SessionRepository) DeleteExpired(ctx context.Context, idleCutoff time.Time) (int64, error) {
	const q = `DELETE FROM user_sessions
		WHERE expires_at < now() OR last_active_at < $1`
	tag, err := r.pool.Exec(ctx, q, idleCutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
```

- [ ] **Step 2: Write the integration test**

Mirror the goose-target + testcontainer setup used by other `*_integration_test.go` files in this package (find one with `grep -l "testcontainers" services/auth/internal/repository/*_test.go` and copy its `//go:build integration` tag + PG bootstrap helper). The test body:

```go
//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSessionRepository_lifecycle(t *testing.T) {
	ctx := context.Background()
	pool := newIntegrationPool(t) // existing helper in this package's test harness
	repo := NewSessionRepository(pool)

	userID, other := uuid.New(), uuid.New()
	tenantID := uuid.New()
	mk := func(uid uuid.UUID) uuid.UUID {
		sid := uuid.New()
		if err := repo.Create(ctx, Session{
			SID: sid, UserID: uid, TenantID: tenantID,
			DeviceLabel: "Chrome on macOS", UserAgent: "ua", IP: "203.0.113.7",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return sid
	}
	s1, s2 := mk(userID), mk(userID)
	_ = mk(other)

	idleCutoff := time.Now().Add(-time.Hour)
	live, err := repo.ListLive(ctx, userID, idleCutoff)
	if err != nil || len(live) != 2 {
		t.Fatalf("ListLive: got %d err=%v, want 2", len(live), err)
	}

	// Revoke one, verify it drops out and cross-user revoke is refused.
	if _, ok, _ := repo.RevokeOwned(ctx, other, s1); ok {
		t.Fatal("cross-user revoke must not succeed")
	}
	if _, ok, err := repo.RevokeOwned(ctx, userID, s1); err != nil || !ok {
		t.Fatalf("RevokeOwned: ok=%v err=%v", ok, err)
	}
	live, _ = repo.ListLive(ctx, userID, idleCutoff)
	if len(live) != 1 || live[0].SID != s2 {
		t.Fatalf("after revoke expected only s2, got %+v", live)
	}

	// Revoke others (keep s2) → s2 stays, nothing else live.
	revoked, err := repo.RevokeOthers(ctx, userID, s2)
	if err != nil || len(revoked) != 0 {
		t.Fatalf("RevokeOthers with only s2 live should revoke 0, got %d err=%v", len(revoked), err)
	}
}
```

- [ ] **Step 3: Run build (integration test runs in CI's tagged lane)**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go vet ./internal/repository/`
Expected: clean. (The integration test is `//go:build integration`; the unit lane skips it, CI's integration lane runs it against Postgres.)

- [ ] **Step 4: Commit**

```bash
git add services/auth/internal/repository/session.go services/auth/internal/repository/session_integration_test.go
git commit -m "feat(auth): SessionRepository CRUD for user_sessions"
```

---

## Task 4: `Claims.Sid` + thread `sid` through `IssueToken`

**Files:**
- Modify: `services/auth/internal/service/auth.go` (Claims struct; `IssueToken`; `RefreshToken`; the `VerifyLoginMFA` internal `IssueToken` call)
- Modify: `services/auth/internal/service/mfa.go` (`IssueMFACompletedToken` call)
- Modify: `services/auth/internal/service/sso.go` (`IssueSSOToken` call)
- Modify: `services/auth/internal/handler/http.go:302` (OCI token call)
- Test: `services/auth/internal/service/session_token_test.go` (new)

- [ ] **Step 1: Write the failing test**

```go
package service

import (
	"context"
	"testing"
)

// TestIssueToken_carriesSid asserts the sid param lands in the Sid claim and
// that RefreshToken preserves it verbatim.
func TestIssueToken_carriesSid(t *testing.T) {
	rdb := newTestRedis(t)
	svc := newTokenTestService(t, rdb) // helper: NewWithFakesAndRing single-key ring
	ctx := context.Background()

	tok, err := svc.IssueToken(ctx, uuidNil, uuidNil, nil, nil, false, "human", []string{"pwd"}, "sess-123")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims, err := svc.ValidateToken(ctx, tok)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Sid != "sess-123" {
		t.Fatalf("Sid: got %q want sess-123", claims.Sid)
	}

	refreshed, err := svc.RefreshToken(ctx, tok)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	rc, _ := svc.ValidateToken(ctx, refreshed)
	if rc.Sid != "sess-123" {
		t.Fatalf("refreshed Sid: got %q want sess-123 (must be preserved)", rc.Sid)
	}
}
```

Add a `newTokenTestService` helper (or reuse an existing single-key fake constructor from `service_repo_test.go`; several `*_test.go` files already build one via `NewWithFakesAndRing(nil,nil,nil,nil,rdb,ring)`). `uuidNil` is `uuid.Nil.String()`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestIssueToken_carriesSid`
Expected: FAIL — `too many arguments in call to svc.IssueToken` / `claims.Sid undefined`.

- [ ] **Step 3: Add the claim and the param**

In `auth.go`, add to the `Claims` struct (after `Amr`):

```go
	// Sid is the stable session id (user_sessions.sid) for interactive logins.
	// Unlike the JTI, it is preserved verbatim across RefreshToken so a session
	// can be listed and revoked (revoke:sid gate in ValidateToken) even though
	// the JTI rotates every 300s. Empty for non-session tokens (OCI /v2 Docker
	// tokens, workload OIDC, API-key dispatch).
	Sid string `json:"sid,omitempty"`
```

Change `IssueToken`'s signature to add a trailing `sid string` and set it on the claims:

```go
func (s *Service) IssueToken(ctx context.Context, userID, tenantID string, access []RepositoryAccess, roles []string, isGlobalAdmin bool, principalKind string, amr []string, sid string) (string, error) {
	// … existing body …
	claims := Claims{
		// … existing fields …
		Amr:           amr,
		Sid:           sid,
	}
	// … unchanged …
}
```

Update the four in-tree callers:
- `auth.go` `RefreshToken` (~677): `…, claims.Amr, claims.Sid)` — **preserve**.
- `auth.go` `VerifyLoginMFA` internal call (~782): pass `""` **for now** (Task 7 replaces it with the session sid).
- `mfa.go` `IssueMFACompletedToken` (~99): pass `""` **for now** (Task 7).
- `sso.go` `IssueSSOToken` (~561): pass `""` **for now** (Task 8).
- `handler/http.go:302` (OCI token): pass `""` — Docker tokens are not sessions.

- [ ] **Step 4: Run test to verify it passes + full package builds**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go test ./internal/service/ -run TestIssueToken_carriesSid`
Expected: PASS. Fix any other `IssueToken(` caller the compiler flags by appending `, ""`.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/auth.go services/auth/internal/service/mfa.go services/auth/internal/service/sso.go services/auth/internal/handler/http.go services/auth/internal/service/session_token_test.go
git commit -m "feat(auth): Sid claim threaded through IssueToken + preserved on refresh"
```

---

## Task 5: `SessionMeta` + `issueSessionToken` helper + session-service wiring

**Files:**
- Create: `services/auth/internal/service/session.go`
- Modify: `services/auth/internal/service/repos.go` (add `sessionRepo` interface + field + constructor wiring)
- Test: `services/auth/internal/service/session_test.go`

- [ ] **Step 1: Write the failing test**

```go
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestIssueSessionToken_createsRowAndSid(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions) // helper wiring the fake session repo
	ctx := context.Background()

	userID, tenantID := uuid.New(), uuid.New()
	tok, err := svc.issueSessionToken(ctx, userID, tenantID, nil, false, "human",
		[]string{"pwd"}, SessionMeta{IP: "203.0.113.9", UserAgent: "docker/24.0"})
	if err != nil {
		t.Fatalf("issueSessionToken: %v", err)
	}
	claims, _ := svc.ValidateToken(ctx, tok)
	if claims.Sid == "" {
		t.Fatal("expected a non-empty sid claim")
	}
	row, ok := sessions.bySID[claims.Sid]
	if !ok {
		t.Fatal("expected a session row keyed by the minted sid")
	}
	if row.DeviceLabel != "Docker CLI" || row.IP != "203.0.113.9" {
		t.Fatalf("row metadata wrong: %+v", row)
	}
}
```

Add a `fakeSessionRepo` (map `bySID map[string]repository.Session`, implementing the `sessionRepo` interface from Step 3) and `newSessionTestService` to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestIssueSessionToken`
Expected: FAIL — `svc.issueSessionToken undefined` / `SessionMeta undefined`.

- [ ] **Step 3: Implement**

Create `service/session.go`:

```go
// Package service — session.go: the sid lifecycle for interactive logins. A
// SessionMeta (client IP + User-Agent) captured at the HTTP edge is threaded
// into the token-issuing paths; issueSessionToken mints a sid, persists a
// user_sessions row, and embeds the sid in the JWT. List/revoke live here too.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

const (
	// sessionMaxAge is the absolute lifetime of a session regardless of activity.
	sessionMaxAge = 30 * 24 * time.Hour
	// sessionIdleWindow is the default idle timeout when no token policy is set.
	// (The token policy's idle_revoke_days overrides this when configured.)
	sessionIdleWindow = 14 * 24 * time.Hour
)

// SessionMeta is the client context captured at the HTTP edge for a new session.
type SessionMeta struct {
	IP        string
	UserAgent string
}

// sessionRepo is the narrow interface the service needs from SessionRepository,
// so tests can supply an in-memory fake.
type sessionRepo interface {
	Create(ctx context.Context, s repository.Session) error
	ListLive(ctx context.Context, userID uuid.UUID, idleCutoff time.Time) ([]repository.Session, error)
	RevokeOwned(ctx context.Context, userID, sid uuid.UUID) (time.Time, bool, error)
	RevokeOthers(ctx context.Context, userID, keepSID uuid.UUID) ([]repository.Session, error)
	TouchLastActive(ctx context.Context, sid uuid.UUID, at time.Time) error
}

// issueSessionToken mints a sid, persists the session row, and issues an access
// token carrying the sid. Used only by the interactive login paths (password,
// MFA completion, SSO). A row-insert failure is fatal to the login — we must
// never hand out a sid claim without a backing row.
//
// When the session repo is not wired (s.sessions == nil), sessions are disabled:
// issue a plain token with no sid. This mirrors the codebase's optional-dependency
// idiom (tokenPolicy / lastUsed / redis are all nil-tolerant) and keeps every
// existing login/MFA/SSO unit test — which does not wire a session repo — green.
func (s *Service) issueSessionToken(ctx context.Context, userID, tenantID uuid.UUID, roles []string, isGlobalAdmin bool, kind string, amr []string, meta SessionMeta) (string, error) {
	if s.sessions == nil {
		return s.IssueToken(ctx, userID.String(), tenantID.String(), nil, roles, isGlobalAdmin, kind, amr, "")
	}
	sid := uuid.New()
	if err := s.sessions.Create(ctx, repository.Session{
		SID:         sid,
		UserID:      userID,
		TenantID:    tenantID,
		DeviceLabel: parseDeviceLabel(meta.UserAgent),
		UserAgent:   meta.UserAgent,
		IP:          meta.IP,
		ExpiresAt:   s.now().Add(sessionMaxAge),
	}); err != nil {
		return "", err
	}
	return s.IssueToken(ctx, userID.String(), tenantID.String(), nil, roles, isGlobalAdmin, kind, amr, sid.String())
}

// idleCutoff returns now()-idleWindow, honouring the tenant token policy's
// idle_revoke_days when configured, else the default sessionIdleWindow.
func (s *Service) idleCutoff(ctx context.Context, tenantID uuid.UUID) time.Time {
	window := sessionIdleWindow
	if s.tokenPolicy != nil {
		if p, err := s.tokenPolicy.GetOrDefault(ctx, tenantID); err == nil && p.IdleRevokeDays != nil && *p.IdleRevokeDays > 0 {
			window = time.Duration(*p.IdleRevokeDays) * 24 * time.Hour
		}
	}
	return s.now().Add(-window)
}
```

In `repos.go`: add `sessions sessionRepo` to the `Service` struct, and in every constructor that wires real repos (production `New`/`NewWithKeyRing`) leave it nil unless a `SetSessionRepo` setter is added — **follow the `SetMFAKEK` setter pattern**: add

```go
// SetSessionRepo wires the user_sessions repository. Kept as a setter so the
// JWT-posture constructors stay signature-stable (mirrors SetMFAKEK).
func (s *Service) SetSessionRepo(r sessionRepo) { s.sessions = r }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestIssueSessionToken`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/session.go services/auth/internal/service/repos.go services/auth/internal/service/session_test.go
git commit -m "feat(auth): SessionMeta + issueSessionToken helper + session repo wiring"
```

---

## Task 6: Thread `SessionMeta` into the password `Login` path

**Files:**
- Modify: `services/auth/internal/service/auth.go` (`Login` signature + no-MFA branch)
- Modify: `services/auth/internal/handler/http.go` (`login` builds meta from the request)
- Modify: existing `Login(` test callers (compiler will list them)
- Test: `services/auth/internal/service/session_test.go` (extend)

- [ ] **Step 1: Write the failing test**

```go
func TestLogin_noMFA_createsSession(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc, users := newSessionLoginTestService(t, rdb, sessions) // seeds a password user
	ctx := context.Background()

	tenantID, uid := seedPasswordUser(t, users, "alice", "Str0ng!Password123")
	res, err := svc.Login(ctx, tenantID, "alice", "Str0ng!Password123",
		SessionMeta{IP: "203.0.113.5", UserAgent: "Mozilla/5.0 (Macintosh) Chrome/125.0 Safari/537.36"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Token == "" {
		t.Fatal("expected an access token")
	}
	claims, _ := svc.ValidateToken(ctx, res.Token)
	if claims.Sid == "" || sessions.bySID[claims.Sid] == nil {
		t.Fatal("login must create a session row and stamp its sid")
	}
	_ = uid
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestLogin_noMFA_createsSession`
Expected: FAIL — `too many arguments in call to svc.Login`.

- [ ] **Step 3: Implement**

Change `Login`'s signature to `func (s *Service) Login(ctx context.Context, tenantID uuid.UUID, username, password string, meta SessionMeta) (LoginResult, error)`. In the **no-MFA branch only**, replace the final `s.IssueToken(...)` with:

```go
	tok, terr := s.issueSessionToken(ctx, user.ID, user.TenantID, roles, user.IsGlobalAdmin, user.Kind, []string{"pwd"}, meta)
```

(The MFA-required and setup-required branches still return challenge/setup tokens and create **no** session — the session is created when the *access* token is issued, in Task 7.)

In `handler/http.go` `login`, build the meta and pass it:

```go
	meta := service.SessionMeta{IP: ip, UserAgent: r.UserAgent()}
	res, err := h.svc.Login(r.Context(), tenantID, req.Username, req.Password, meta)
```

(`ip` is already computed via `remoteIP(r)` at the top of the handler.)

Update every `svc.Login(` test caller the compiler flags — pass `service.SessionMeta{}` (or `SessionMeta{}` inside the service package) where the test doesn't care about session metadata.

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go test ./internal/service/ ./internal/handler/ -run "TestLogin"`
Expected: PASS; all existing Login tests still green.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/auth.go services/auth/internal/handler/http.go services/auth/internal/service/session_test.go
git commit -m "feat(auth): create a session on password login"
```

---

## Task 7: Thread `SessionMeta` into the MFA login + forced-enrol paths

**Files:**
- Modify: `services/auth/internal/service/mfa.go` (`IssueMFACompletedToken` + `VerifyLoginMFA` signatures)
- Modify: `services/auth/internal/service/auth.go` (the `VerifyLoginMFA` body if it lives there — confirm location; per grep `VerifyLoginMFA` is at `auth.go:795`)
- Modify: `services/auth/internal/handler/http_mfa.go` (`loginMFA` + `mfaVerify` build meta)
- Modify: `Login` MFA branches are unaffected (no session at challenge time)
- Test: `services/auth/internal/service/session_test.go` (extend)

- [ ] **Step 1: Write the failing test**

```go
func TestVerifyLoginMFA_createsSession(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc, users := newSessionLoginTestService(t, rdb, sessions)
	ctx := context.Background()

	// Reuse the MFA test harness to enrol a user + mint a challenge token, then
	// verify with a correct code and assert a session row was created.
	userID, tenantID, secret := enrolMFAUserForSession(t, svc, users) // helper mirrors enrolMFAUser
	ct, _ := svc.IssueMFAChallengeToken(ctx, userID.String(), tenantID.String())
	tok, err := svc.VerifyLoginMFA(ctx, ct, codeForSecret(t, secret, svc.now()),
		SessionMeta{IP: "198.51.100.4", UserAgent: "docker/24.0"})
	if err != nil {
		t.Fatalf("VerifyLoginMFA: %v", err)
	}
	claims, _ := svc.ValidateToken(ctx, tok)
	if claims.Sid == "" || sessions.bySID[claims.Sid] == nil {
		t.Fatal("MFA login must create a session")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestVerifyLoginMFA_createsSession`
Expected: FAIL — `too many arguments`.

- [ ] **Step 3: Implement**

- `IssueMFACompletedToken` gains a trailing `meta SessionMeta` and swaps its `IssueToken` for `issueSessionToken`:

```go
func (s *Service) IssueMFACompletedToken(ctx context.Context, userID, tenantID uuid.UUID, meta SessionMeta) (string, error) {
	roles := s.loadRoleNames(ctx, userID, tenantID)
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return s.issueSessionToken(ctx, userID, tenantID, roles, u.IsGlobalAdmin, "human", []string{"pwd", "otp"}, meta)
}
```

- `VerifyLoginMFA` gains a trailing `meta SessionMeta` and forwards it to `IssueMFACompletedToken` (or, if it calls `IssueToken` directly at `auth.go:782`, replace that with `issueSessionToken(..., meta)`).
- In `handler/http_mfa.go`: `loginMFA` calls `h.svc.VerifyLoginMFA(r.Context(), req.ChallengeToken, req.Code, service.SessionMeta{IP: remoteIP(r), UserAgent: r.UserAgent()})`. `mfaVerify`'s forced-enrol completion calls `h.svc.IssueMFACompletedToken(r.Context(), userID, tenantID, service.SessionMeta{IP: remoteIP(r), UserAgent: r.UserAgent()})`.

Update all `VerifyLoginMFA(` / `IssueMFACompletedToken(` test callers (SEC-079's `login_mfa_test.go`, `http_mfa_test.go`, `http_login_mfa_test.go`) — pass `SessionMeta{}` where session metadata is irrelevant.

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go test ./internal/service/ ./internal/handler/ -run "MFA"`
Expected: PASS; all existing MFA tests still green.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/mfa.go services/auth/internal/service/auth.go services/auth/internal/handler/http_mfa.go services/auth/internal/service/session_test.go
git commit -m "feat(auth): create a session on MFA login + forced-enrol completion"
```

---

## Task 8: Thread `SessionMeta` into the SSO callback

**Files:**
- Modify: `services/auth/internal/service/sso.go` (`IssueSSOToken` signature)
- Modify: the SSO callback handler that calls `IssueSSOToken` (find via `grep -rn "IssueSSOToken" services/auth/internal/handler/`)
- Test: `services/auth/internal/service/sso_test.go` (extend, if a unit-level SSO issue test exists; otherwise assert via the handler test)

- [ ] **Step 1: Locate the caller**

Run: `grep -rn "IssueSSOToken" services/auth/internal/`
Expected: one production caller in the SSO callback handler + `sso.go` definition.

- [ ] **Step 2: Write/extend the failing test**

Add to `sso_test.go` (uses the existing SSO test harness):

```go
func TestIssueSSOToken_createsSession(t *testing.T) {
	// Build the SSO service with a fake session repo (extend the existing SSO
	// test setup to call auth.SetSessionRepo(fake)), issue a token, and assert a
	// session row exists keyed by the token's sid.
	// … arrange per existing sso_test.go harness …
	tok, err := sso.IssueSSOToken(ctx, user, roles, SessionMeta{IP: "203.0.113.20", UserAgent: "Mozilla/5.0 Firefox/126.0"})
	if err != nil { t.Fatalf("IssueSSOToken: %v", err) }
	claims, _ := authSvc.ValidateToken(ctx, tok)
	if claims.Sid == "" || sessions.bySID[claims.Sid] == nil {
		t.Fatal("SSO login must create a session")
	}
}
```

- [ ] **Step 3: Implement**

`IssueSSOToken` gains a trailing `meta SessionMeta` and calls `s.auth.issueSessionToken(ctx, user.ID, user.TenantID, roles, user.IsGlobalAdmin, user.Kind, []string{"sso"}, meta)`. The SSO callback handler builds `SessionMeta{IP: remoteIP(r), UserAgent: r.UserAgent()}` and passes it.

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go test ./internal/service/ -run "SSO"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/sso.go services/auth/internal/handler/ services/auth/internal/service/sso_test.go
git commit -m "feat(auth): create a session on SSO login"
```

---

## Task 9: `revoke:sid` fail-closed gate in `ValidateToken`

**Files:**
- Modify: `services/auth/internal/service/auth.go` (`ValidateToken`, after the `revoke:user` block)
- Modify: `services/auth/internal/service/session.go` (add `sessionRevokeKey` + `revokeSessionRedis`)
- Test: `services/auth/internal/service/session_test.go` (extend)

- [ ] **Step 1: Write the failing test**

```go
func TestValidateToken_revokedSid_denied(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions)
	ctx := context.Background()

	tok, _ := svc.issueSessionToken(ctx, uuid.New(), uuid.New(), nil, false, "human",
		[]string{"pwd"}, SessionMeta{IP: "203.0.113.1", UserAgent: "x"})
	claims, _ := svc.ValidateToken(ctx, tok)

	// Set the revoke:sid gate → the very next validation must be denied.
	if err := rdb.Set(ctx, "revoke:sid:"+claims.Sid, "1", time.Hour).Err(); err != nil {
		t.Fatalf("seed revoke:sid: %v", err)
	}
	if _, err := svc.ValidateToken(ctx, tok); err == nil {
		t.Fatal("ValidateToken must reject a token whose sid is revoked")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestValidateToken_revokedSid`
Expected: FAIL — the token still validates.

- [ ] **Step 3: Implement**

In `session.go`:

```go
// sessionRevokeKey is the Redis key that marks a session revoked. Consulted
// fail-closed by ValidateToken, mirroring revoke:user.
func sessionRevokeKey(sid string) string { return "revoke:sid:" + sid }
```

In `ValidateToken`, immediately after the `revoke:user` check, add:

```go
	// Session revocation (revoke:sid): a listed session can be killed even though
	// the JTI rotates on every refresh. Fail-CLOSED on a Redis error, exactly
	// like the principal (revoke:user) check above — a Redis outage must not let
	// a revoked session keep validating.
	if claims.Sid != "" {
		sv, serr := s.redis.Get(ctx, sessionRevokeKey(claims.Sid)).Result()
		if serr != nil && !errors.Is(serr, redis.Nil) {
			slog.ErrorContext(ctx, "session revocation check failed; failing closed", "err", serr)
			return nil, status.Error(codes.Unavailable, "session revocation check unavailable")
		}
		if sv != "" {
			return nil, status.Error(codes.Unauthenticated, "session revoked")
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestValidateToken_revokedSid`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/auth.go services/auth/internal/service/session.go services/auth/internal/service/session_test.go
git commit -m "feat(auth): fail-closed revoke:sid gate in ValidateToken"
```

---

## Task 10: Debounced `last_active` in `ValidateToken`

**Files:**
- Modify: `services/auth/internal/service/session.go` (add `sessionActiveUpdater`, mirror `lastUsedUpdater`)
- Modify: `services/auth/internal/service/auth.go` (`ValidateToken` fires the touch on success)
- Modify: `services/auth/internal/server/server.go` (construct the updater)
- Test: `services/auth/internal/service/session_test.go` (extend — assert `touchNow` debounces + fail-opens)

- [ ] **Step 1: Write the failing test**

```go
func TestSessionActiveUpdater_debounces(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	u := newSessionActiveUpdater(rdb, sessions, nil)
	ctx := context.Background()
	sid := uuid.New()

	u.touchNow(ctx, sid) // first: SETNX wins → write
	u.touchNow(ctx, sid) // second inside window: SETNX loses → skip
	if sessions.touchCount[sid] != 1 {
		t.Fatalf("expected exactly 1 debounced touch, got %d", sessions.touchCount[sid])
	}
}
```

Extend `fakeSessionRepo` with `touchCount map[uuid.UUID]int` incremented in its `TouchLastActive`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSessionActiveUpdater`
Expected: FAIL — `newSessionActiveUpdater undefined`.

- [ ] **Step 3: Implement (mirror `lastUsedUpdater`)**

In `session.go`:

```go
// sessionActiveWindow is the debounce interval for last_active writes — one DB
// write per session per minute at most, keeping ValidateToken hot.
const sessionActiveWindow = 60 * time.Second

// sessionActiveUpdater debounces user_sessions.last_active_at writes through
// Redis (SETNX), fail-OPEN on Redis error. Fire-and-forget, exactly like the
// FUT-003 lastUsedUpdater for API keys. last_active is telemetry, not a
// security boundary, so Redis-down here degrades to an inline write, never a deny.
type sessionActiveUpdater struct {
	redis  lastUsedRedis // reuse the existing narrow SetNX interface
	repo   sessionTouchRepo
	logger *slog.Logger
}

type sessionTouchRepo interface {
	TouchLastActive(ctx context.Context, sid uuid.UUID, at time.Time) error
}

func newSessionActiveUpdater(rd lastUsedRedis, repo sessionTouchRepo, logger *slog.Logger) *sessionActiveUpdater {
	if logger == nil {
		logger = slog.Default()
	}
	return &sessionActiveUpdater{redis: rd, repo: repo, logger: logger}
}

// Touch fire-and-forgets a debounced last_active bump. Callers pass
// context.Background() (the request context would cancel on client disconnect).
func (u *sessionActiveUpdater) Touch(ctx context.Context, sid uuid.UUID) { go u.touchNow(ctx, sid) }

func (u *sessionActiveUpdater) touchNow(ctx context.Context, sid uuid.UUID) {
	now := time.Now().UTC()
	if u.redis != nil {
		set, err := u.redis.SetNX(ctx, "sid_active:"+sid.String(), "1", sessionActiveWindow).Result()
		if err == nil && !set {
			return // another tick claimed this window
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			u.logger.Info("session last_active debounce redis error; falling open", "err", err)
		}
	}
	if err := u.repo.TouchLastActive(ctx, sid, now); err != nil {
		u.logger.Warn("session last_active UPDATE failed", "sid", sid, "err", err)
	}
}
```

Add a `sessionActive *sessionActiveUpdater` field on `Service` + a `SetSessionActiveUpdater` setter (mirror `SetMFAKEK`). In `ValidateToken`, just before `return claims, nil`, add:

```go
	if claims.Sid != "" && s.sessionActive != nil {
		if sid, perr := uuid.Parse(claims.Sid); perr == nil {
			s.sessionActive.Touch(context.Background(), sid)
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSessionActiveUpdater`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/session.go services/auth/internal/service/auth.go services/auth/internal/service/session_test.go
git commit -m "feat(auth): debounced last_active updater on the validate path"
```

---

## Task 11: Session list + revoke service methods

**Files:**
- Modify: `services/auth/internal/service/session.go` (`ListSessions`, `RevokeSession`, `RevokeOtherSessions`)
- Test: `services/auth/internal/service/session_test.go` (extend)

- [ ] **Step 1: Write the failing test**

```go
func TestRevokeSession_setsGateAndRow(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions)
	ctx := context.Background()

	userID := uuid.New()
	sid := uuid.New()
	sessions.bySID[sid.String()] = &repository.Session{SID: sid, UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}

	ok, err := svc.RevokeSession(ctx, userID, sid)
	if err != nil || !ok {
		t.Fatalf("RevokeSession: ok=%v err=%v", ok, err)
	}
	if v, _ := rdb.Get(ctx, sessionRevokeKey(sid.String())).Result(); v == "" {
		t.Fatal("RevokeSession must set the revoke:sid gate")
	}
	// Cross-user revoke is refused.
	if ok, _ := svc.RevokeSession(ctx, uuid.New(), sid); ok {
		t.Fatal("cross-user RevokeSession must return ok=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestRevokeSession`
Expected: FAIL — `svc.RevokeSession undefined`.

- [ ] **Step 3: Implement**

```go
// ListSessions returns the caller's live sessions, honouring the tenant idle
// policy. tenantID scopes the idle-window lookup only; rows are filtered by
// user_id in the repo.
func (s *Service) ListSessions(ctx context.Context, userID, tenantID uuid.UUID) ([]repository.Session, error) {
	return s.sessions.ListLive(ctx, userID, s.idleCutoff(ctx, tenantID))
}

// RevokeSession revokes one of the caller's own sessions and sets the fail-closed
// Redis gate with a TTL = the session's remaining lifetime (bounded by the 30d
// max). ok=false means the sid was absent or not owned (→ handler 404).
func (s *Service) RevokeSession(ctx context.Context, userID, sid uuid.UUID) (bool, error) {
	expiresAt, ok, err := s.sessions.RevokeOwned(ctx, userID, sid)
	if err != nil || !ok {
		return false, err
	}
	s.setSessionRevokeGate(ctx, sid, expiresAt)
	return true, nil
}

// RevokeOtherSessions revokes every live session for the caller except keepSID
// (the current one) and gates each. Returns the count revoked.
func (s *Service) RevokeOtherSessions(ctx context.Context, userID, keepSID uuid.UUID) (int, error) {
	revoked, err := s.sessions.RevokeOthers(ctx, userID, keepSID)
	if err != nil {
		return 0, err
	}
	for _, r := range revoked {
		s.setSessionRevokeGate(ctx, r.SID, r.ExpiresAt)
	}
	return len(revoked), nil
}

// setSessionRevokeGate sets revoke:sid with a TTL = remaining session lifetime,
// so the entry self-cleans exactly when the session could no longer exist
// (SEC-005 TTL-coupling). A non-positive TTL (already expired) is skipped.
func (s *Service) setSessionRevokeGate(ctx context.Context, sid uuid.UUID, expiresAt time.Time) {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return
	}
	if err := s.redis.Set(ctx, sessionRevokeKey(sid.String()), "1", ttl).Err(); err != nil {
		slog.ErrorContext(ctx, "set revoke:sid gate failed", "sid", sid, "err", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run "TestRevokeSession|TestRevokeOther|TestListSessions"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/session.go services/auth/internal/service/session_test.go
git commit -m "feat(auth): list + revoke session service methods"
```

---

## Task 12: Self-service HTTP handlers + routes

**Files:**
- Create: `services/auth/internal/handler/http_sessions.go`
- Modify: `services/auth/internal/handler/http.go` (register 3 routes near the `/users/me/mfa` block)
- Test: `services/auth/internal/handler/http_sessions_test.go`

- [ ] **Step 1: Write the failing test**

```go
package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestSessions_listAndRevoke(t *testing.T) {
	srv, tc := newMFATestServer(t) // wires a session repo fake via the test harness
	userID, tenantID := seedTestUser(t, tc, "sess-user", "Str0ng!Password123")
	// Seed two live sessions for this user directly in the fake session repo.
	current := seedSession(t, tc, userID, tenantID)
	other := seedSession(t, tc, userID, tenantID)

	// List → 2 sessions, current one flagged.
	req := makeMeSessionRequest(t, srv, tc, http.MethodGet, "/api/v1/users/me/sessions", nil, userID, tenantID, current)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", resp.StatusCode)
	}
	var got struct{ Sessions []struct{ Sid string `json:"sid"`; Current bool `json:"current"` } `json:"sessions"` }
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got.Sessions))
	}

	// Revoke the other → 204, and it drops from the list.
	del := makeMeSessionRequest(t, srv, tc, http.MethodDelete, "/api/v1/users/me/sessions/"+other.String(), nil, userID, tenantID, current)
	dresp, _ := http.DefaultClient.Do(del)
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status %d, want 204", dresp.StatusCode)
	}
	dresp.Body.Close()
}
```

`makeMeSessionRequest` is `makeMeRequest` but mints the token with a `sid` claim (the current session) — add it to the test file by calling a new service helper `IssueSessionTokenForTest` or by using `issueTestToken` variant that sets `Sid`. `seedSession` inserts a row into the harness's fake session repo and returns its sid. Also add `TestSessions_revokeOthers` and `TestSessions_crossUser_404` and `TestSessions_setupToken_401` (a setup token must not manage sessions).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestSessions`
Expected: FAIL — routes 404 / handler undefined.

- [ ] **Step 3: Implement the handlers**

```go
// Package handler — http_sessions.go: self-service session-list endpoints under
// /api/v1/users/me/sessions (Tier-1 #1 session management). requireAuth-gated
// with a normal access token; a setup token must not manage sessions (same
// boundary the MFA disable handler enforces).
package handler

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// sessionDTO is the wire shape for one session row.
type sessionDTO struct {
	Sid          string `json:"sid"`
	DeviceLabel  string `json:"device_label"`
	UserAgent    string `json:"user_agent"`
	IP           string `json:"ip"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
	Current      bool   `json:"current"`
}

// listSessions implements GET /api/v1/users/me/sessions.
func (h *HTTPHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, tenantID, err := parseUserAndTenant(claims)
	if err != nil {
		slog.ErrorContext(r.Context(), "sessions list: invalid claims", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	rows, err := h.svc.ListSessions(r.Context(), userID, tenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "sessions list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	out := make([]sessionDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionDTO{
			Sid: s.SID.String(), DeviceLabel: s.DeviceLabel, UserAgent: s.UserAgent, IP: s.IP,
			CreatedAt:    s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			LastActiveAt: s.LastActiveAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Current:      s.SID.String() == claims.Sid,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// revokeSession implements DELETE /api/v1/users/me/sessions/{sid}.
func (h *HTTPHandler) revokeSession(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	sid, err := uuid.Parse(r.PathValue("sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid session id")
		return
	}
	ok, err := h.svc.RevokeSession(r.Context(), userID, sid)
	if err != nil {
		slog.ErrorContext(r.Context(), "revoke session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "NOTFOUND", "session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeOtherSessions implements POST /api/v1/users/me/sessions/revoke-others.
func (h *HTTPHandler) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	claims, err := h.requireAuth(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	userID, _, err := parseUserAndTenant(claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	current, err := uuid.Parse(claims.Sid)
	if err != nil {
		// No sid on the caller's token (non-session token) — nothing to keep.
		current = uuid.Nil
	}
	n, err := h.svc.RevokeOtherSessions(r.Context(), userID, current)
	if err != nil {
		slog.ErrorContext(r.Context(), "revoke other sessions failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}
```

Register in `http.go` after the MFA routes (line ~211):

```go
	mux.HandleFunc("GET /api/v1/users/me/sessions", h.listSessions)
	mux.HandleFunc("DELETE /api/v1/users/me/sessions/{sid}", h.revokeSession)
	mux.HandleFunc("POST /api/v1/users/me/sessions/revoke-others", h.revokeOtherSessions)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestSessions`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/handler/http_sessions.go services/auth/internal/handler/http.go services/auth/internal/handler/http_sessions_test.go
git commit -m "feat(auth): self-service session list + revoke HTTP endpoints"
```

---

## Task 13: Expiry sweep worker + server wiring

**Files:**
- Create: `services/auth/internal/worker/session_sweep.go`
- Modify: `services/auth/internal/server/server.go` (construct `SessionRepository`, call `svc.SetSessionRepo` + `svc.SetSessionActiveUpdater`, start the sweep)
- Test: `services/auth/internal/worker/session_sweep_test.go`

- [ ] **Step 1: Write the failing test**

```go
package worker

import (
	"context"
	"testing"
	"time"
)

type fakeSweepRepo struct{ deleted int64; gotCutoff time.Time }

func (f *fakeSweepRepo) DeleteExpired(_ context.Context, idleCutoff time.Time) (int64, error) {
	f.gotCutoff = idleCutoff
	return f.deleted, nil
}

func TestSessionSweep_TickOnce(t *testing.T) {
	repo := &fakeSweepRepo{deleted: 3}
	w := NewSessionSweeper(repo, 14*24*time.Hour, time.Minute, nil)
	if err := w.TickOnce(context.Background()); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if repo.gotCutoff.After(time.Now().Add(-13 * 24 * time.Hour)) {
		t.Fatal("idle cutoff should be ~14 days in the past")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/worker/ -run TestSessionSweep`
Expected: FAIL — `NewSessionSweeper undefined`.

- [ ] **Step 3: Implement (mirror `worker/idle_revoke.go`)**

```go
// Package worker — session_sweep.go: a ticker that garbage-collects expired /
// long-idle user_sessions rows so the table and the session list stay bounded.
// Mirrors runLoginSessionCleanup but for interactive sessions.
package worker

import (
	"context"
	"log/slog"
	"time"
)

type sessionSweepRepo interface {
	DeleteExpired(ctx context.Context, idleCutoff time.Time) (int64, error)
}

// SessionSweeper deletes rows past their absolute expiry or older than
// idleWindow since last activity.
type SessionSweeper struct {
	repo       sessionSweepRepo
	idleWindow time.Duration
	period     time.Duration
	logger     *slog.Logger
}

func NewSessionSweeper(repo sessionSweepRepo, idleWindow, period time.Duration, logger *slog.Logger) *SessionSweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionSweeper{repo: repo, idleWindow: idleWindow, period: period, logger: logger}
}

// TickOnce runs one sweep. Exposed so tests drive it deterministically.
func (w *SessionSweeper) TickOnce(ctx context.Context) error {
	n, err := w.repo.DeleteExpired(ctx, time.Now().Add(-w.idleWindow))
	if err != nil {
		return err
	}
	if n > 0 {
		w.logger.Debug("session sweep: deleted expired sessions", "n", n)
	}
	return nil
}

// Run loops until ctx is cancelled.
func (w *SessionSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(w.period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.TickOnce(ctx); err != nil {
				w.logger.Warn("session sweep failed", "err", err)
			}
		}
	}
}
```

In `server/server.go`, near the `NewUserRepository` wiring (line ~76) and the existing worker/cleanup startup (line ~365-418):

```go
	sessionRepo := repository.NewSessionRepository(pool)
	svc.SetSessionRepo(sessionRepo)
	svc.SetSessionActiveUpdater(service.NewSessionActiveUpdater(rdb, sessionRepo, slog.Default()))
	// … in the goroutine-start block …
	sweeper := worker.NewSessionSweeper(sessionRepo, sessionIdleWindowForServer, time.Hour, slog.Default())
	go sweeper.Run(ctx)
```

(Expose `NewSessionActiveUpdater` as an exported constructor wrapping `newSessionActiveUpdater`, and define `sessionIdleWindowForServer = 14 * 24 * time.Hour` or reuse the service constant via an exported accessor.)

- [ ] **Step 4: Run test + build the whole service**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go test ./internal/worker/ -run TestSessionSweep`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/worker/session_sweep.go services/auth/internal/server/server.go services/auth/internal/service/session.go services/auth/internal/worker/session_sweep_test.go
git commit -m "feat(auth): session expiry sweep worker + server wiring"
```

---

## Task 14: Frontend API module (`sessions.ts`)

**Files:**
- Create: `frontend/src/lib/api/sessions.ts`
- Test: `frontend/src/lib/api/__tests__/sessions.test.tsx`

Pattern source: `frontend/src/lib/api/api-keys.ts` (uses `apiClient`, `<feature>Keys.all` query key, `use<Thing>` / `use<Verb>` hooks, `void qc.invalidateQueries` in `onSuccess`). Note the GET response is the envelope `{ sessions: [...] }` (the Go handler wraps the array), unlike api-keys which returns a bare array — extract `data.sessions`.

- [ ] **Step 1: Write the failing hook test**

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import React from "react";

const get = vi.fn();
const del = vi.fn();
const post = vi.fn();
vi.mock("../client", () => ({ apiClient: { get: (...a: unknown[]) => get(...a), delete: (...a: unknown[]) => del(...a), post: (...a: unknown[]) => post(...a) } }));

import { useSessions } from "../sessions";

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: React.ReactNode }) => React.createElement(QueryClientProvider, { client }, children);
}

beforeEach(() => vi.clearAllMocks());

it("useSessions unwraps the {sessions:[…]} envelope", async () => {
  get.mockResolvedValueOnce({ data: { sessions: [{ sid: "s1", device_label: "Chrome on macOS", ip: "203.0.113.1", user_agent: "ua", created_at: "2026-07-05T10:00:00Z", last_active_at: "2026-07-05T11:00:00Z", current: true }] } });
  const { result } = renderHook(() => useSessions(), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toHaveLength(1);
  expect(result.current.data?.[0].current).toBe(true);
  expect(get).toHaveBeenCalledWith("/users/me/sessions");
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && npx vitest run src/lib/api/__tests__/sessions.test.tsx`
Expected: FAIL — cannot resolve `../sessions`.

- [ ] **Step 3: Implement the API module**

```ts
// Beacon — active session list (Tier-1 #1 session management).
// Wire shape from services/auth GET/DELETE/POST /api/v1/users/me/sessions.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface Session {
  sid: string;
  device_label: string;
  user_agent: string;
  ip: string;
  created_at: string;
  last_active_at: string;
  current: boolean;
}

export const sessionKeys = {
  all: ["sessions"] as const,
};

export function useSessions() {
  return useQuery({
    queryKey: sessionKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<{ sessions: Session[] }>("/users/me/sessions");
      return data.sessions ?? [];
    },
    staleTime: 20_000,
  });
}

export function useRevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (sid: string) => {
      await apiClient.delete(`/users/me/sessions/${encodeURIComponent(sid)}`);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sessionKeys.all });
    },
  });
}

export function useRevokeOtherSessions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ revoked: number }>("/users/me/sessions/revoke-others", {});
      return data.revoked;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sessionKeys.all });
    },
  });
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd frontend && npx vitest run src/lib/api/__tests__/sessions.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/api/sessions.ts frontend/src/lib/api/__tests__/sessions.test.tsx
git commit -m "feat(fe): sessions API module + hooks"
```

---

## Task 15: Frontend Sessions card + settings wiring

**Files:**
- Create: `frontend/src/components/profile/sessions-card.tsx`
- Modify: `frontend/src/routes/_authenticated.settings.account.tsx` (slot `<SessionsCard />` below `<MfaCard />`)
- Test: `frontend/src/components/profile/sessions-card.test.tsx`

Pattern source: `frontend/src/components/profile/api-keys-section.tsx` (Card + state-branch order: `isError`→`ErrorState` with `onRetry={()=>void refetch()}`; empty→`EmptyState`; else table with `loading`). Confirm via `ConfirmDestructiveDialog severity="low"` (`frontend/src/components/ui/confirm-destructive-dialog.tsx`). Current-session logout via `authStore.clear()` + `navigate({to:"/login",replace:true})` (see `frontend/src/lib/api/auth.ts` `logout()` and `topbar.tsx`). Relative time via `formatRelativeDate` + `formatAbsoluteDate` title from `@/lib/format`.

- [ ] **Step 1: Write the failing component test**

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import React from "react";

const revokeMutate = vi.fn();
const revokeOthersMutate = vi.fn();
const sessionsData = { data: [
  { sid: "cur", device_label: "Chrome on macOS", ip: "203.0.113.1", user_agent: "ua", created_at: "2026-07-05T10:00:00Z", last_active_at: "2026-07-05T11:00:00Z", current: true },
  { sid: "oth", device_label: "Firefox on Linux", ip: "203.0.113.9", user_agent: "ua2", created_at: "2026-07-04T10:00:00Z", last_active_at: "2026-07-04T11:00:00Z", current: false },
], isLoading: false, isError: false, refetch: vi.fn() };

vi.mock("@/lib/api/sessions", () => ({
  useSessions: () => sessionsData,
  useRevokeSession: () => ({ mutateAsync: revokeMutate }),
  useRevokeOtherSessions: () => ({ mutateAsync: revokeOthersMutate }),
}));
const toastSuccess = vi.fn();
vi.mock("sonner", () => ({ toast: { success: (...a: unknown[]) => toastSuccess(...a), error: vi.fn() } }));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

import { SessionsCard } from "./sessions-card";

beforeEach(() => vi.clearAllMocks());

it("lists sessions and flags the current device", () => {
  render(<SessionsCard />);
  expect(screen.getByText("Chrome on macOS")).toBeInTheDocument();
  expect(screen.getByText(/this device/i)).toBeInTheDocument();
});

it("revokes a non-current session on confirm", async () => {
  revokeMutate.mockResolvedValueOnce(undefined);
  const user = userEvent.setup();
  render(<SessionsCard />);
  // Click the Revoke button on the non-current (Firefox) row, then confirm.
  await user.click(screen.getAllByRole("button", { name: /revoke/i })[0]);
  await user.click(screen.getByRole("button", { name: /^revoke$|confirm|sign out/i }));
  await waitFor(() => expect(revokeMutate).toHaveBeenCalledWith("oth"));
  expect(navigate).not.toHaveBeenCalled(); // revoking a non-current session must NOT log out
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend && npx vitest run src/components/profile/sessions-card.test.tsx`
Expected: FAIL — cannot resolve `./sessions-card`.

- [ ] **Step 3: Implement the card**

```tsx
import React from "react";
import { toast } from "sonner";
import { useNavigate } from "@tanstack/react-router";
import { Monitor, LogOut } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { formatAbsoluteDate, formatRelativeDate } from "@/lib/format";
import { authStore } from "@/lib/auth/store";
import { useSessions, useRevokeSession, useRevokeOtherSessions, type Session } from "@/lib/api/sessions";

export function SessionsCard(): React.ReactElement {
  const { data, isLoading, isError, refetch } = useSessions();
  const revoke = useRevokeSession();
  const revokeOthers = useRevokeOtherSessions();
  const navigate = useNavigate();
  const [target, setTarget] = React.useState<Session | null>(null);
  const [busy, setBusy] = React.useState(false);

  async function confirmRevoke(): Promise<void> {
    if (!target) return;
    setBusy(true);
    try {
      await revoke.mutateAsync(target.sid);
      if (target.current) {
        // Revoking your own current session is an explicit sign-out.
        toast.success("Signed out.");
        authStore.clear();
        void navigate({ to: "/login", replace: true });
        return;
      }
      toast.success("Session revoked.");
      setTarget(null);
    } catch {
      toast.error("Couldn't revoke the session. Try again.");
    } finally {
      setBusy(false);
    }
  }

  async function signOutOthers(): Promise<void> {
    try {
      const n = await revokeOthers.mutateAsync();
      toast.success(n > 0 ? `Signed out ${n} other session${n === 1 ? "" : "s"}.` : "No other sessions.");
    } catch {
      toast.error("Couldn't sign out other sessions. Try again.");
    }
  }

  const sessions = data ?? [];
  const hasOthers = sessions.some((s) => !s.current);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardDescription className="!text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--color-fg-subtle)]">
              Active sessions
            </CardDescription>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">Devices currently signed in to your account.</p>
          </div>
          {hasOthers ? (
            <Button variant="ghost" size="sm" onClick={() => void signOutOthers()}>
              <LogOut className="size-3.5" />Sign out others
            </Button>
          ) : null}
        </div>
      </CardHeader>
      <CardContent>
        {isError ? (
          <ErrorState title="Couldn't load sessions" description="Something went wrong fetching your sessions." onRetry={() => void refetch()} />
        ) : !isLoading && sessions.length === 0 ? (
          <EmptyState icon={<Monitor className="size-5" />} title="No active sessions" description="You have no other signed-in devices." />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Device</TableHead>
                <TableHead>IP</TableHead>
                <TableHead>Last active</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {isLoading
                ? [0, 1].map((i) => (
                    <TableRow key={i}>
                      <TableCell><Skeleton className="h-3 w-40" /></TableCell>
                      <TableCell><Skeleton className="h-3 w-24" /></TableCell>
                      <TableCell><Skeleton className="h-3 w-20" /></TableCell>
                      <TableCell />
                    </TableRow>
                  ))
                : sessions.map((s) => (
                    <TableRow key={s.sid}>
                      <TableCell>
                        <span className="text-sm text-[var(--color-fg)]" title={s.user_agent}>{s.device_label}</span>
                        {s.current ? <Badge tone="success" className="ml-2">This device</Badge> : null}
                      </TableCell>
                      <TableCell className="font-mono text-xs text-[var(--color-fg-muted)]">{s.ip}</TableCell>
                      <TableCell>
                        <span className="text-xs text-[var(--color-fg)]" title={formatAbsoluteDate(s.last_active_at)}>
                          {formatRelativeDate(s.last_active_at)}
                        </span>
                      </TableCell>
                      <TableCell className="text-right">
                        <Button variant="ghost" size="sm" onClick={() => setTarget(s)}
                          className="text-[var(--color-danger)] hover:bg-[var(--color-danger)]/10">
                          {s.current ? "Sign out" : "Revoke"}
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
            </TableBody>
          </Table>
        )}
      </CardContent>

      <ConfirmDestructiveDialog
        open={target !== null}
        onOpenChange={(o) => { if (!o) setTarget(null); }}
        severity="low"
        title={target?.current ? "Sign out this device?" : "Revoke this session?"}
        description={target?.current
          ? "You'll be signed out of this device immediately."
          : `Revoke the session on ${target?.device_label ?? "this device"}? That device will be signed out.`}
        confirmLabel={target?.current ? "Sign out" : "Revoke"}
        loading={busy}
        onConfirm={confirmRevoke}
      />
    </Card>
  );
}
```

- [ ] **Step 4: Wire into the account route**

In `frontend/src/routes/_authenticated.settings.account.tsx`, add the import and slot the card between `<MfaCard … />` and `<ReplayOnboardingFooter />`:

```tsx
import { SessionsCard } from "@/components/profile/sessions-card";
// …
      <MfaCard onEnroll={…} onDisable={…} onRegenerate={…} />
      <SessionsCard />
      <ReplayOnboardingFooter />
```

- [ ] **Step 5: Run tests + the 4 CI gates**

Run:
```
cd frontend && npx vitest run src/components/profile/sessions-card.test.tsx src/lib/api/__tests__/sessions.test.tsx
npm run lint && npm run typecheck && npm run test && npm run build
```
Expected: new tests PASS; all 4 gates green (CLAUDE.md §15.1).

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/profile/sessions-card.tsx frontend/src/routes/_authenticated.settings.account.tsx frontend/src/components/profile/sessions-card.test.tsx
git commit -m "feat(fe): active sessions card on /settings/account"
```

---

## Task 16: Docs

**Files:**
- Modify: `docs/AUTH.md` (session model + `revoke:sid` gate)
- Modify: `docs/SERVICES.md` §2 (the three routes)

- [ ] **Step 1: Add the AUTH.md section**

Add a "Sessions" subsection near the TOTP MFA section documenting: the stable `sid` claim minted on interactive login (password/MFA/SSO); persistence in `user_sessions`; preservation across refresh; the fail-closed `revoke:sid` gate in `ValidateToken` (alongside `revoke:user`); the debounced `last_active`; idle (`token_policies.idle_revoke_days`) + 30d absolute expiry; and that machine identities (API keys, OCI `/v2` tokens, workload OIDC) carry no `sid` and create no session.

- [ ] **Step 2: Add the SERVICES.md routes**

Under registry-auth's route list, add:
```
GET    /api/v1/users/me/sessions                 # list live sessions (current flagged)
DELETE /api/v1/users/me/sessions/{sid}           # revoke one owned session
POST   /api/v1/users/me/sessions/revoke-others   # revoke all but the current session
```

- [ ] **Step 3: Commit**

```bash
git add docs/AUTH.md docs/SERVICES.md
git commit -m "docs: session list model + routes"
```

---

## Final verification (after all tasks)

- [ ] `cd services/auth && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./... && GOWORK=off golangci-lint run ./internal/...`
- [ ] `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`
- [ ] Dispatch the final code + security review (auth-flow change: session creation, revoke gate).
