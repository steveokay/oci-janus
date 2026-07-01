# FUT-001 Federated Workload Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift `/api-keys/trust` from preview to live — CI runners (GitHub Actions / GitLab CI / Buildkite / generic OIDC) exchange a workload OIDC token for a short-lived registry JWT mapped to a workspace service account, with no static API key.

**Architecture:** New `oidc_trust_configs` table on `services/auth` (generic OIDC — `issuer_url` / `audience` / `subject_pattern` glob; provider-agnostic). 4 admin RPCs (List/Create/Update/Delete) + 1 public exchange RPC (`ExchangeWorkloadToken`). Exchange endpoint at `POST /auth/token/workload` on `services/auth` (mirrors `/auth/token`), gated by an issuer allowlist (`OIDC_ALLOWED_ISSUERS` env, fail-closed default) and a per-(issuer,subject) Redis rate-limit. In-process mutex-guarded JWKS cache with TTL. New FE `TrustPanel` + `CreateOIDCTrustDialog` replaces the amber preview.

**Tech Stack:** Go 1.25 / `pgx/v5` / `crypto/rsa` + `crypto/ecdsa` / `golang-jwt/jwt/v5` (already vendored — used by `services/auth/internal/service/keyring.go`); React 18 / TanStack Query / Vitest.

**Spec:** [`../specs/2026-06-30-api-keys-tier2-backend-design.md`](../specs/2026-06-30-api-keys-tier2-backend-design.md) §Feature 2.

**Branch:** `feat/fut-001-federated-workload-identity` (already created off `main` at `ab62461`).

---

## File Structure

**Created:**

| Path | Responsibility |
|---|---|
| `services/auth/migrations/20260701000001_oidc_trust_configs.sql` | `oidc_trust_configs` table + indexes + UNIQUE constraint |
| `services/auth/internal/repository/oidc_trust.go` | CRUD repository for `oidc_trust_configs` |
| `services/auth/internal/repository/oidc_trust_test.go` | Migration + CRUD tests (testcontainers) |
| `services/auth/internal/service/oidc_subject.go` | Pure glob matcher (`subjectMatches(pattern, subject)`) |
| `services/auth/internal/service/oidc_subject_test.go` | Glob matcher unit tests |
| `services/auth/internal/service/oidc_issuer.go` | Issuer-allowlist matcher (parses `OIDC_ALLOWED_ISSUERS`, prefix-match) |
| `services/auth/internal/service/oidc_issuer_test.go` | Issuer allowlist unit tests |
| `services/auth/internal/service/oidc_jwks.go` | In-process JWKS cache (mutex-guarded, TTL-driven) |
| `services/auth/internal/service/oidc_jwks_test.go` | JWKS cache unit tests (uses `httptest.Server` stub IdP) |
| `services/auth/internal/service/oidc_trust.go` | Trust CRUD service methods |
| `services/auth/internal/service/oidc_trust_test.go` | Trust service tests (DB) |
| `services/auth/internal/service/oidc_exchange.go` | `ExchangeWorkloadToken` orchestration |
| `services/auth/internal/service/oidc_exchange_test.go` | Exchange flow tests (stub IdP, all 7 reject reasons) |
| `services/auth/internal/handler/grpc_oidc_trust.go` | 4 admin gRPC handlers |
| `services/auth/internal/handler/grpc_oidc_trust_test.go` | gRPC handler tests |
| `services/auth/internal/handler/http_workload_token.go` | `POST /auth/token/workload` HTTP handler + per-(issuer,subject) Redis rate limit |
| `services/auth/internal/handler/http_workload_token_test.go` | HTTP handler tests |
| `services/management/internal/handler/access_oidc_trust.go` | 4 BFF admin routes (`/api/v1/access/oidc-trust*`) |
| `services/management/internal/handler/access_oidc_trust_test.go` | BFF handler tests |
| `frontend/src/lib/api/oidc-trust.ts` | TanStack hooks: `useOIDCTrusts()` + `useCreateOIDCTrust()` + `useUpdateOIDCTrust()` + `useDeleteOIDCTrust()` |
| `frontend/src/lib/oidc-subject-glob.ts` | Pure glob validator (mirrors the BE matcher) for client-side validation |
| `frontend/src/lib/__tests__/oidc-subject-glob.test.ts` | Glob validator unit tests |
| `frontend/src/components/access/TrustPanel.tsx` | Live panel (replaces `TrustPreview`) |
| `frontend/src/components/access/CreateOIDCTrustDialog.tsx` | Create dialog with form validation |
| `frontend/src/components/access/__tests__/TrustPanel.test.tsx` | Component tests |
| `frontend/src/components/access/__tests__/CreateOIDCTrustDialog.test.tsx` | Dialog tests |

**Modified:**

| Path | Why |
|---|---|
| `proto/auth/v1/auth.proto` | Add 5 RPCs + messages (`OIDCTrust`, `ListOIDCTrustsRequest/Response`, `CreateOIDCTrustRequest`, `UpdateOIDCTrustRequest`, `DeleteOIDCTrustRequest`, `ExchangeWorkloadTokenRequest/Response`) |
| `proto/gen/go/auth/v1/*.pb.go` | Regenerated stubs (`buf generate`) |
| `libs/rabbitmq/events/events.go` | Add `RoutingOIDCTrustCreated/Updated/Deleted` + `RoutingWorkloadTokenExchanged/Rejected` constants + payload types |
| `services/audit/internal/eventconsumer/consumer.go` | Map the new events to `audit_events` rows (5 cases — fails the audit-catalogue lint test otherwise per CLAUDE.md §10) |
| `services/auth/internal/config/config.go` | Add `OIDCAllowedIssuers []string` (CSV-parsed) + production validation when feature is in use |
| `services/auth/internal/server/server.go` | Wire `WithOIDCTrustService`; register `POST /auth/token/workload` HTTP route |
| `services/auth/cmd/server/main.go` | Pass `cfg.OIDCAllowedIssuers` into the service constructor |
| `services/management/internal/handler/handler.go` | Register 4 new `/api/v1/access/oidc-trust*` routes (authMW-gated) |
| `services/management/internal/server/server.go` | Wire `WithOIDCTrustHandler` (or inline if pattern is direct) |
| `infra/docker-compose/docker-compose.yml` | Add `OIDC_ALLOWED_ISSUERS` env on `registry-auth` (default: dev-friendly stubs like `https://token.actions.githubusercontent.com`) |
| `frontend/src/routes/_authenticated.api-keys.trust.tsx` | Swap `TrustPreview` for `TrustPanel` |
| `frontend/src/components/access/AccessSubNav.tsx` | Move "Federated trust" out of the Preview section (Preview count 3 → 2 with FUT-002 already shipped) |
| `frontend/src/components/access/__tests__/AccessSubNav.test.tsx` | Update graduation regression test |

**Deleted:**

| Path | Why |
|---|---|
| `frontend/src/components/access/previews/TrustPreview.tsx` | Replaced by `TrustPanel` |

**Tracker:**
- `status-tracker.md` — add `REM-023` when work starts; remove on merge
- `status.md` — append resolution row on merge
- `futures.md` — collapse FUT-001 to a `**DONE — see status.md (REM-023)**` stub with the original design in a `<details>` block

---

## Task 1: Proto — add 5 RPCs + messages

**Files:**
- Modify: `proto/auth/v1/auth.proto`
- Regenerate: `proto/gen/go/auth/v1/*.pb.go`

- [ ] **Step 1.1: Add the OIDCTrust message**

Read `proto/auth/v1/auth.proto` to find the end of the existing message definitions (after `LookupUsernamesResponse`). Add the FUT-001 messages there. Use this exact block — `option go_package` and `package` declarations stay at the file top, untouched:

```protobuf
// FUT-001 — federated workload identity. Allows CI runners with an OIDC token
// to exchange it for a short-lived registry JWT mapped to a service account.

message OIDCTrust {
  string id                       = 1;
  string tenant_id                = 2;
  string service_account_id       = 3;
  string display_name             = 4;
  string issuer_url               = 5;
  string audience                 = 6;
  string subject_pattern          = 7;
  int32  jwks_cache_ttl_seconds   = 8;
  google.protobuf.Timestamp created_at   = 9;
  google.protobuf.Timestamp updated_at   = 10;
  google.protobuf.Timestamp last_used_at = 11;
}

message ListOIDCTrustsRequest {
  string tenant_id = 1;
}

message ListOIDCTrustsResponse {
  repeated OIDCTrust trusts = 1;
}

message CreateOIDCTrustRequest {
  string tenant_id              = 1;
  string service_account_id     = 2;
  string display_name           = 3;
  string issuer_url             = 4;
  string audience               = 5;
  string subject_pattern        = 6;
  int32  jwks_cache_ttl_seconds = 7;
}

message UpdateOIDCTrustRequest {
  string id                     = 1;
  string tenant_id              = 2;
  string display_name           = 3;
  string subject_pattern        = 4;
  int32  jwks_cache_ttl_seconds = 5;
}

message DeleteOIDCTrustRequest {
  string id        = 1;
  string tenant_id = 2;
}

message ExchangeWorkloadTokenRequest {
  string oidc_jwt = 1;
}

message ExchangeWorkloadTokenResponse {
  string access_token = 1;
  int32  expires_in   = 2;
  string token_type   = 3;
}
```

- [ ] **Step 1.2: Add the 5 RPCs to `service AuthService`**

After the existing `rpc SetGlobalAdmin(...)` (find the last RPC in the service block), add:

```protobuf
  // FUT-001 — federated workload identity.
  rpc ListOIDCTrusts(ListOIDCTrustsRequest) returns (ListOIDCTrustsResponse);
  rpc CreateOIDCTrust(CreateOIDCTrustRequest) returns (OIDCTrust);
  rpc UpdateOIDCTrust(UpdateOIDCTrustRequest) returns (OIDCTrust);
  rpc DeleteOIDCTrust(DeleteOIDCTrustRequest) returns (google.protobuf.Empty);
  rpc ExchangeWorkloadToken(ExchangeWorkloadTokenRequest) returns (ExchangeWorkloadTokenResponse);
```

- [ ] **Step 1.3: Regenerate stubs**

```bash
cd /c/Users/Athelos/Desktop/claude/image-registry && make proto
```

Expected: regenerates `proto/gen/go/auth/v1/auth.pb.go` + `auth_grpc.pb.go` with the new types + service methods. Confirm with `git diff --stat proto/gen/go/`.

- [ ] **Step 1.4: Commit**

```bash
git add proto/auth/v1/auth.proto proto/gen/go/auth/
git commit -m "feat(proto/auth): add OIDC trust + workload-token-exchange RPCs (FUT-001)"
```

---

## Task 2: Migration — `oidc_trust_configs` table

**Files:**
- Create: `services/auth/migrations/20260701000001_oidc_trust_configs.sql`

- [ ] **Step 2.1: Write the migration**

```sql
-- +goose Up
-- FUT-001 — federated workload identity. Trust relationship between a
-- workspace service account and an external OIDC IdP (GitHub Actions /
-- GitLab CI / Buildkite / any OIDC issuer in the OIDC_ALLOWED_ISSUERS env
-- allowlist). On a successful POST /auth/token/workload, the trust's
-- service_account_id receives a short-lived RS256 JWT.

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

-- +goose Down
DROP TABLE IF EXISTS oidc_trust_configs;
```

- [ ] **Step 2.2: Commit**

```bash
git add services/auth/migrations/20260701000001_oidc_trust_configs.sql
git commit -m "feat(auth): migration — oidc_trust_configs table (FUT-001)"
```

---

## Task 3: Repository — `oidc_trust.go`

**Files:**
- Create: `services/auth/internal/repository/oidc_trust.go`
- Create: `services/auth/internal/repository/oidc_trust_test.go`

- [ ] **Step 3.1: Write the failing test first**

Use the existing `migrations_test.go` testcontainers pattern (the file at `services/auth/internal/repository/migrations_test.go` is the reference — copy the `setupPostgres(t)` helper or call it directly). Write a test file that:
1. Spins up a Postgres testcontainer.
2. Runs migrations up through `20260701000001_oidc_trust_configs.sql`.
3. Inserts a service-account row (the trust's FK target).
4. Exercises Create/Get/List/Update/Delete on `OIDCTrustRepo`.
5. Verifies `UNIQUE (tenant_id, issuer_url, subject_pattern)` rejects duplicates.
6. Verifies `ON DELETE CASCADE` removes trusts when the service account is deleted.

Each test method is a single `t.Run("name", func(t *testing.T) {...})` under a single top-level `TestOIDCTrustRepo(t *testing.T)`. Aim for 6 sub-tests covering the cases above.

The repo file doesn't exist yet — the test will fail to compile. Confirm with `cd services/auth && go test ./internal/repository/ -run TestOIDCTrustRepo -v` showing `undefined: NewOIDCTrustRepo` (or similar).

- [ ] **Step 3.2: Implement the repository**

Mirror the existing `apikey.go` shape (read it first for conventions: `pgxpool.Pool` constructor, struct-tagged Row scanner, context propagation, `MapDBError` for `ResourceExhausted` mapping). The struct:

```go
package repository

import (
    "context"
    "errors"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/steveokay/oci-janus/libs/errors/codes"
)

// OIDCTrust is the in-memory shape of a row in oidc_trust_configs.
type OIDCTrust struct {
    ID                  uuid.UUID
    TenantID            uuid.UUID
    ServiceAccountID    uuid.UUID
    DisplayName         string
    IssuerURL           string
    Audience            string
    SubjectPattern      string
    JWKSCacheTTLSeconds int32
    CreatedAt           time.Time
    UpdatedAt           time.Time
    LastUsedAt          *time.Time
}

// OIDCTrustRepo persists FUT-001 trust relationships.
type OIDCTrustRepo struct {
    pool *pgxpool.Pool
}

func NewOIDCTrustRepo(pool *pgxpool.Pool) *OIDCTrustRepo {
    return &OIDCTrustRepo{pool: pool}
}

// Methods to implement (signatures only — the bodies are mechanical CRUD
// against the SQL columns; mirror the patterns in apikey.go):
func (r *OIDCTrustRepo) Create(ctx context.Context, in OIDCTrust) (*OIDCTrust, error)
func (r *OIDCTrustRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*OIDCTrust, error)
func (r *OIDCTrustRepo) List(ctx context.Context, tenantID uuid.UUID) ([]*OIDCTrust, error)
func (r *OIDCTrustRepo) Update(ctx context.Context, in OIDCTrust) (*OIDCTrust, error)
func (r *OIDCTrustRepo) Delete(ctx context.Context, tenantID, id uuid.UUID) error
// MarkUsed updates last_used_at and is called on every successful
// ExchangeWorkloadToken. Best-effort; failure does NOT block the exchange.
func (r *OIDCTrustRepo) MarkUsed(ctx context.Context, id uuid.UUID) error
```

Use `errors.Is(err, pgx.ErrNoRows)` for not-found mapping; use `codes.MapDBError(err)` for unwrapping pool exhaustion. The `Update` method mutates display_name, subject_pattern, jwks_cache_ttl_seconds only (the FK + issuer + audience are append-only — operators must Delete+Create to change them).

Run tests: `cd services/auth && go test ./internal/repository/ -run TestOIDCTrustRepo -v`. Expected: all 6 sub-tests PASS.

- [ ] **Step 3.3: Commit**

```bash
git add services/auth/internal/repository/oidc_trust.go services/auth/internal/repository/oidc_trust_test.go
git commit -m "feat(auth): OIDCTrustRepo CRUD + migration test (FUT-001)"
```

---

## Task 4: Pure helper — subject glob matcher

**Files:**
- Create: `services/auth/internal/service/oidc_subject.go`
- Create: `services/auth/internal/service/oidc_subject_test.go`

- [ ] **Step 4.1: Write the failing test**

```go
package service

import "testing"

// subjectMatches implements the glob format documented in the FUT-001 spec:
//   - `?` matches exactly one non-`/` character
//   - `*` matches zero or more non-`/` characters
//   - `**` matches zero or more characters INCLUDING `/`
//   - All other characters match literally
// The glob is anchored at both ends (no `^`/`$`).
//
// Examples that real CI subjects match:
//   pattern `repo:steveokay/oci-janus:ref:refs/heads/*` matches
//     `repo:steveokay/oci-janus:ref:refs/heads/main` but NOT
//     `repo:steveokay/oci-janus:ref:refs/heads/feat/x` (because `/` blocks `*`).
//   pattern `repo:steveokay/oci-janus:ref:refs/heads/**` matches both.
func TestSubjectMatches(t *testing.T) {
    cases := []struct {
        name    string
        pattern string
        subject string
        want    bool
    }{
        {"literal exact", "repo:steveokay/oci-janus:ref:refs/heads/main", "repo:steveokay/oci-janus:ref:refs/heads/main", true},
        {"literal mismatch", "repo:steveokay/oci-janus:ref:refs/heads/main", "repo:steveokay/oci-janus:ref:refs/heads/dev", false},
        {"star matches single segment", "repo:steveokay/oci-janus:ref:refs/heads/*", "repo:steveokay/oci-janus:ref:refs/heads/main", true},
        {"star does NOT cross slash", "repo:steveokay/oci-janus:ref:refs/heads/*", "repo:steveokay/oci-janus:ref:refs/heads/feat/x", false},
        {"doublestar crosses slash", "repo:steveokay/oci-janus:ref:refs/heads/**", "repo:steveokay/oci-janus:ref:refs/heads/feat/x", true},
        {"question matches single char", "repo:org/r:env:prod-?", "repo:org/r:env:prod-1", true},
        {"question does NOT cross slash", "repo:org/r:env:prod-?", "repo:org/r:env:prod-/", false},
        {"empty pattern rejects non-empty", "", "anything", false},
        {"empty pattern accepts empty", "", "", true},
        {"prefix wildcard", "*:ref:refs/heads/main", "repo:org/r:ref:refs/heads/main", false}, // leading * does not cross :
        {"suffix wildcard match", "repo:org/r:*", "repo:org/r:env-x", true},
        {"no anchor needed (pattern is whole subject)", "repo:a", "repo:a", true},
        {"partial match must be rejected", "repo:a", "repo:abc", false},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := subjectMatches(tc.pattern, tc.subject)
            if got != tc.want {
                t.Errorf("subjectMatches(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
            }
        })
    }
}
```

Run: `cd services/auth && go test ./internal/service/ -run TestSubjectMatches -v`. Expected: undefined: subjectMatches.

- [ ] **Step 4.2: Implement the matcher**

Use a recursive-descent approach (NOT regex — regex is harder to reason about for `**` semantics). For a ~150 LOC matcher, the rough shape:

```go
package service

// subjectMatches reports whether `subject` matches the glob `pattern`.
// See TestSubjectMatches for the supported metacharacters and their
// `/` semantics.
//
// Anchored at both ends (the entire subject must consume the entire pattern).
//
// This matcher is the security gate for FUT-001 federated workload identity.
// A wrong implementation here lets a CI runner mint tokens for the wrong
// service account, so the test cases above are non-negotiable — do not
// loosen them.
func subjectMatches(pattern, subject string) bool {
    return matchGlob(pattern, subject)
}

// matchGlob is the recursive matcher. Returns true iff `pat` (with glob
// metacharacters) matches `s` entirely.
func matchGlob(pat, s string) bool {
    // Walk both strings left-to-right.
    for len(pat) > 0 {
        switch pat[0] {
        case '*':
            // `**` matches any run of chars including `/`.
            // `*`  matches any run of chars excluding `/`.
            doublestar := len(pat) >= 2 && pat[1] == '*'
            if doublestar {
                rest := pat[2:]
                // Try matching the rest at every position (including end).
                for i := 0; i <= len(s); i++ {
                    if matchGlob(rest, s[i:]) {
                        return true
                    }
                }
                return false
            }
            rest := pat[1:]
            for i := 0; i <= len(s); i++ {
                // Bail if we'd cross a '/' inside the wildcard span.
                if i > 0 && s[i-1] == '/' {
                    return false
                }
                if matchGlob(rest, s[i:]) {
                    return true
                }
            }
            return false
        case '?':
            if len(s) == 0 || s[0] == '/' {
                return false
            }
            pat = pat[1:]
            s = s[1:]
        default:
            if len(s) == 0 || s[0] != pat[0] {
                return false
            }
            pat = pat[1:]
            s = s[1:]
        }
    }
    return len(s) == 0
}
```

Run tests: `cd services/auth && go test ./internal/service/ -run TestSubjectMatches -v`. Expected: all 13 sub-tests PASS.

- [ ] **Step 4.3: Commit**

```bash
git add services/auth/internal/service/oidc_subject.go services/auth/internal/service/oidc_subject_test.go
git commit -m "feat(auth): subject-glob matcher for OIDC trust (FUT-001)"
```

---

## Task 5: Pure helper — issuer allowlist matcher

**Files:**
- Create: `services/auth/internal/service/oidc_issuer.go`
- Create: `services/auth/internal/service/oidc_issuer_test.go`

- [ ] **Step 5.1: Write the failing test**

```go
package service

import "testing"

func TestIssuerAllowed(t *testing.T) {
    cases := []struct {
        name    string
        allow   []string
        issuer  string
        want    bool
    }{
        {"empty allowlist rejects everything", nil, "https://token.actions.githubusercontent.com", false},
        {"exact match", []string{"https://token.actions.githubusercontent.com"}, "https://token.actions.githubusercontent.com", true},
        {"prefix match", []string{"https://token.actions.githubusercontent.com"}, "https://token.actions.githubusercontent.com/", true},
        {"path-suffix is allowed by prefix", []string{"https://gitlab.com"}, "https://gitlab.com/group", true},
        {"different scheme rejected", []string{"https://token.actions.githubusercontent.com"}, "http://token.actions.githubusercontent.com", false},
        {"different host rejected", []string{"https://token.actions.githubusercontent.com"}, "https://attacker.example.com", false},
        {"trailing-slash allowlist still matches no-slash issuer", []string{"https://gitlab.com/"}, "https://gitlab.com", false}, // strict
        {"any of multiple", []string{"https://a.example", "https://b.example"}, "https://b.example/foo", true},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := issuerAllowed(tc.allow, tc.issuer)
            if got != tc.want {
                t.Errorf("issuerAllowed(%v, %q) = %v, want %v", tc.allow, tc.issuer, got, tc.want)
            }
        })
    }
}
```

Run: `cd services/auth && go test ./internal/service/ -run TestIssuerAllowed -v`. Expected: undefined.

- [ ] **Step 5.2: Implement**

```go
package service

import "strings"

// issuerAllowed reports whether `issuer` is a prefix-match of ANY entry
// in `allow`. Comparison is byte-identical (no case folding — the OIDC
// spec is case-sensitive on issuer URLs).
//
// Empty `allow` means NOTHING is allowed (fail-closed default for
// FUT-001 — operators MUST explicitly name the IdPs they trust at
// deploy time via OIDC_ALLOWED_ISSUERS).
func issuerAllowed(allow []string, issuer string) bool {
    for _, prefix := range allow {
        if strings.HasPrefix(issuer, prefix) {
            return true
        }
    }
    return false
}

// parseIssuerAllowlist splits a comma-separated env value into a list of
// trusted issuer prefixes. Whitespace-only entries are dropped; the rest
// are returned in original order. Used by Service constructor + tested
// independently of issuerAllowed.
func parseIssuerAllowlist(csv string) []string {
    if csv == "" {
        return nil
    }
    parts := strings.Split(csv, ",")
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        p = strings.TrimSpace(p)
        if p == "" {
            continue
        }
        out = append(out, p)
    }
    return out
}
```

Add a second test `TestParseIssuerAllowlist` covering: empty → nil; single entry; multiple comma-separated; leading/trailing whitespace; consecutive commas; trailing comma.

Run: tests pass.

- [ ] **Step 5.3: Commit**

```bash
git add services/auth/internal/service/oidc_issuer.go services/auth/internal/service/oidc_issuer_test.go
git commit -m "feat(auth): issuer allowlist matcher + CSV parser (FUT-001)"
```

---

## Task 6: JWKS cache

**Files:**
- Create: `services/auth/internal/service/oidc_jwks.go`
- Create: `services/auth/internal/service/oidc_jwks_test.go`

- [ ] **Step 6.1: Write the failing test**

The test must use `net/http/httptest.Server` to stand up a fake IdP that:
1. Serves `/.well-known/openid-configuration` returning `{"jwks_uri": "<test-server>/jwks"}`.
2. Serves `/jwks` returning a valid JWKS document with one RSA public key.
3. Tracks call counts so the test can assert caching skips the second fetch.

```go
package service

import (
    "context"
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "math/big"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"
    "time"
)

// startStubIdP returns a test IdP server whose JWKS is served from the
// returned key. Tests use the returned `calls` pointer to assert the
// cache behaviour (we expect Fetch to hit the server once, then serve
// from cache until TTL expires).
func startStubIdP(t *testing.T, key *rsa.PrivateKey, kid string) (*httptest.Server, *atomic.Int64) {
    t.Helper()
    var calls atomic.Int64
    mux := http.NewServeMux()
    mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
        host := "http://" + r.Host
        _ = json.NewEncoder(w).Encode(map[string]any{"jwks_uri": host + "/jwks"})
    })
    mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
        calls.Add(1)
        jwk := map[string]any{
            "kty": "RSA",
            "use": "sig",
            "kid": kid,
            "alg": "RS256",
            "n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
            "e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
        }
        _ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk}})
    })
    return httptest.NewServer(mux), &calls
}

func TestJWKSCache(t *testing.T) {
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil { t.Fatal(err) }

    srv, calls := startStubIdP(t, key, "kid-1")
    defer srv.Close()

    cache := newJWKSCache(http.DefaultClient)
    ctx := context.Background()

    t.Run("first fetch hits network", func(t *testing.T) {
        got, err := cache.Fetch(ctx, srv.URL, time.Hour)
        if err != nil { t.Fatal(err) }
        if k := got["kid-1"]; k == nil {
            t.Fatalf("kid-1 missing from cache")
        }
        if c := calls.Load(); c != 1 {
            t.Errorf("first fetch should hit network once, got %d", c)
        }
    })

    t.Run("second fetch within TTL is cached", func(t *testing.T) {
        _, err := cache.Fetch(ctx, srv.URL, time.Hour)
        if err != nil { t.Fatal(err) }
        if c := calls.Load(); c != 1 {
            t.Errorf("second fetch within TTL should not hit network, got %d", c)
        }
    })

    t.Run("expired entry triggers refresh", func(t *testing.T) {
        _, err := cache.Fetch(ctx, srv.URL, 0)
        if err != nil { t.Fatal(err) }
        if c := calls.Load(); c != 2 {
            t.Errorf("expired entry should hit network, got %d", c)
        }
    })

    t.Run("16-issuer cap is enforced", func(t *testing.T) {
        for i := 0; i < 20; i++ {
            extra, _ := startStubIdP(t, key, "kid-extra")
            defer extra.Close()
            _, _ = cache.Fetch(ctx, extra.URL, time.Hour)
        }
        if size := cache.size(); size > 16 {
            t.Errorf("cache size = %d, want <= 16 (SEC-048 analog)", size)
        }
    })
}
```

Run: `cd services/auth && go test ./internal/service/ -run TestJWKSCache -v`. Expected: undefined: newJWKSCache.

- [ ] **Step 6.2: Implement the cache**

```go
package service

import (
    "context"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "math/big"
    "net/http"
    "sync"
    "time"
)

// maxJWKSCacheSize bounds the number of distinct issuer URLs the
// process holds in memory. Mirrors SEC-048's keyring cap — defence
// against an attacker who can register many trust configs causing
// unbounded memory growth.
const maxJWKSCacheSize = 16

// cachedJWKS is a single issuer's keys plus the fetched-at timestamp
// used to gate TTL expiry.
type cachedJWKS struct {
    keys      map[string]*rsa.PublicKey
    fetchedAt time.Time
}

// jwksCache is the process-wide cache. Mutex-guarded so concurrent
// exchanges against the same issuer coalesce to one HTTP fetch.
type jwksCache struct {
    mu      sync.Mutex
    entries map[string]*cachedJWKS // keyed by issuer URL
    client  *http.Client
}

func newJWKSCache(client *http.Client) *jwksCache {
    return &jwksCache{
        entries: make(map[string]*cachedJWKS),
        client:  client,
    }
}

// Fetch returns the issuer's public keys keyed by `kid`. Hits the network
// on first request or after TTL expiry; mutex-guarded so concurrent
// callers share a single inflight fetch.
//
// Fail-closed on network errors: returns an error rather than serving
// stale entries. The caller (oidc_exchange.go) translates the error to
// codes.Unavailable so the CI runner gets a retryable response.
func (c *jwksCache) Fetch(ctx context.Context, issuer string, ttl time.Duration) (map[string]*rsa.PublicKey, error) {
    c.mu.Lock()
    defer c.mu.Unlock()

    if entry, ok := c.entries[issuer]; ok {
        if time.Since(entry.fetchedAt) < ttl {
            return entry.keys, nil
        }
    }

    // SEC-048 analog: enforce the 16-issuer hard cap.
    if _, present := c.entries[issuer]; !present && len(c.entries) >= maxJWKSCacheSize {
        // Evict the oldest entry by fetchedAt.
        var oldestKey string
        var oldestTime time.Time
        for k, v := range c.entries {
            if oldestKey == "" || v.fetchedAt.Before(oldestTime) {
                oldestKey = k
                oldestTime = v.fetchedAt
            }
        }
        delete(c.entries, oldestKey)
    }

    keys, err := c.fetchJWKS(ctx, issuer)
    if err != nil {
        return nil, err
    }
    c.entries[issuer] = &cachedJWKS{keys: keys, fetchedAt: time.Now()}
    return keys, nil
}

// fetchJWKS resolves the issuer's `/.well-known/openid-configuration`,
// reads the `jwks_uri`, and parses the JWKS document.
func (c *jwksCache) fetchJWKS(ctx context.Context, issuer string) (map[string]*rsa.PublicKey, error) {
    // Discovery
    disc, err := c.getJSON(ctx, issuer+"/.well-known/openid-configuration")
    if err != nil {
        return nil, fmt.Errorf("fetch discovery: %w", err)
    }
    jwksURI, _ := disc["jwks_uri"].(string)
    if jwksURI == "" {
        return nil, fmt.Errorf("discovery document missing jwks_uri")
    }

    // JWKS
    raw, err := c.getJSON(ctx, jwksURI)
    if err != nil {
        return nil, fmt.Errorf("fetch jwks: %w", err)
    }

    out := make(map[string]*rsa.PublicKey)
    keys, _ := raw["keys"].([]any)
    for _, k := range keys {
        m, ok := k.(map[string]any)
        if !ok { continue }
        if kty, _ := m["kty"].(string); kty != "RSA" { continue }
        kid, _ := m["kid"].(string)
        nb64, _ := m["n"].(string)
        eb64, _ := m["e"].(string)
        if kid == "" || nb64 == "" || eb64 == "" { continue }

        nbytes, err := base64.RawURLEncoding.DecodeString(nb64)
        if err != nil { continue }
        ebytes, err := base64.RawURLEncoding.DecodeString(eb64)
        if err != nil { continue }

        pub := &rsa.PublicKey{
            N: new(big.Int).SetBytes(nbytes),
            E: int(new(big.Int).SetBytes(ebytes).Int64()),
        }
        out[kid] = pub
    }
    return out, nil
}

func (c *jwksCache) getJSON(ctx context.Context, url string) (map[string]any, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    resp, err := c.client.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("status %d", resp.StatusCode)
    }
    var out map[string]any
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return nil, err
    }
    return out, nil
}

// size is exposed for tests asserting the SEC-048 cap.
func (c *jwksCache) size() int {
    c.mu.Lock()
    defer c.mu.Unlock()
    return len(c.entries)
}
```

Run tests: all 4 sub-tests pass.

- [ ] **Step 6.3: Commit**

```bash
git add services/auth/internal/service/oidc_jwks.go services/auth/internal/service/oidc_jwks_test.go
git commit -m "feat(auth): in-process JWKS cache with 16-issuer cap (FUT-001)"
```

---

## Task 7: Service — `OIDCTrustService` (CRUD methods)

**Files:**
- Create: `services/auth/internal/service/oidc_trust.go`
- Create: `services/auth/internal/service/oidc_trust_test.go`

Wires the repo + glob validator + issuer-allowlist check into a service struct that the handler layer calls. Methods:

- `List(ctx, tenantID) → []*repository.OIDCTrust`
- `Create(ctx, in CreateInput) → *repository.OIDCTrust` — validates `subject_pattern` parses as a glob (no syntax errors); validates `issuer_url` is in the allowlist; validates `audience` is non-empty; validates `service_account_id` belongs to the same tenant. Rejects with `codes.InvalidArgument` on any failure.
- `Update(ctx, in UpdateInput) → *repository.OIDCTrust` — same validations on the mutable fields.
- `Delete(ctx, tenantID, id) → error`

- [ ] **Step 7.1: Write the failing tests (testcontainers; cover create/update/delete + every rejection branch)**

Sub-tests:
- `Create_Success`
- `Create_RejectsIssuerNotInAllowlist`
- `Create_RejectsEmptyAudience`
- `Create_RejectsInvalidGlobSyntax`
- `Create_RejectsServiceAccountFromOtherTenant`
- `Create_RejectsDuplicateSubject`
- `Update_OnlyMutatesAllowedFields`
- `Delete_CascadesOnServiceAccountDeletion` (already covered by repo test but worth a service-layer regression)

- [ ] **Step 7.2: Implement the service**

Sketch:

```go
package service

import (
    "context"
    "errors"
    "strings"

    "github.com/google/uuid"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"

    "github.com/steveokay/oci-janus/services/auth/internal/repository"
)

type OIDCTrustService struct {
    repo            *repository.OIDCTrustRepo
    serviceAccounts *repository.ServiceAccountRepo // existing
    allowedIssuers  []string
}

func NewOIDCTrustService(repo *repository.OIDCTrustRepo, sa *repository.ServiceAccountRepo, allowedIssuers []string) *OIDCTrustService {
    return &OIDCTrustService{repo: repo, serviceAccounts: sa, allowedIssuers: allowedIssuers}
}

type CreateOIDCTrustInput struct {
    TenantID, ServiceAccountID uuid.UUID
    DisplayName, IssuerURL, Audience, SubjectPattern string
    JWKSCacheTTLSeconds int32
}

func (s *OIDCTrustService) Create(ctx context.Context, in CreateOIDCTrustInput) (*repository.OIDCTrust, error) {
    if err := s.validateOnCreate(ctx, in); err != nil {
        return nil, err
    }
    return s.repo.Create(ctx, repository.OIDCTrust{
        TenantID:            in.TenantID,
        ServiceAccountID:    in.ServiceAccountID,
        DisplayName:         in.DisplayName,
        IssuerURL:           in.IssuerURL,
        Audience:            in.Audience,
        SubjectPattern:      in.SubjectPattern,
        JWKSCacheTTLSeconds: defaultIfZero(in.JWKSCacheTTLSeconds, 3600),
    })
}

func (s *OIDCTrustService) validateOnCreate(ctx context.Context, in CreateOIDCTrustInput) error {
    if strings.TrimSpace(in.DisplayName) == "" {
        return status.Error(codes.InvalidArgument, "display_name is required")
    }
    if strings.TrimSpace(in.Audience) == "" {
        return status.Error(codes.InvalidArgument, "audience is required")
    }
    if !issuerAllowed(s.allowedIssuers, in.IssuerURL) {
        return status.Errorf(codes.InvalidArgument, "issuer_url not in OIDC_ALLOWED_ISSUERS")
    }
    if err := validateGlobSyntax(in.SubjectPattern); err != nil {
        return status.Errorf(codes.InvalidArgument, "subject_pattern: %v", err)
    }
    // FK pre-check — enforces same-tenant containment beyond the table's
    // raw FK so we can return a clean InvalidArgument rather than a 5xx.
    sa, err := s.serviceAccounts.GetByID(ctx, in.ServiceAccountID)
    if errors.Is(err, repository.ErrNotFound) || sa == nil {
        return status.Error(codes.InvalidArgument, "service_account_id not found")
    }
    if err != nil { return err }
    if sa.TenantID != in.TenantID {
        return status.Error(codes.InvalidArgument, "service_account_id belongs to a different tenant")
    }
    return nil
}

// (similar shape for Update + Delete — omitted for brevity, mirror Create)
```

Implement `validateGlobSyntax(pattern string) error` in `oidc_subject.go` — checks balanced characters, rejects `***` or other malformed sequences, returns nil on success. Add a corresponding test in `oidc_subject_test.go`.

Run: `cd services/auth && go test ./internal/service/ -run TestOIDCTrustService -v`. All sub-tests pass.

- [ ] **Step 7.3: Commit**

```bash
git add services/auth/internal/service/oidc_trust.go services/auth/internal/service/oidc_trust_test.go services/auth/internal/service/oidc_subject.go services/auth/internal/service/oidc_subject_test.go
git commit -m "feat(auth): OIDCTrustService CRUD with validation (FUT-001)"
```

---

## Task 8: Service — `ExchangeWorkloadToken`

The flow is in the spec's diagram: parse JWT header → fetch+cache JWKS → verify sig → check `iss` in allowlist → check `aud == config.audience` → check `sub` matches `config.subject_pattern` → check `exp/nbf` → load SA → mint RS256 JWT (15-min TTL).

**Files:**
- Create: `services/auth/internal/service/oidc_exchange.go`
- Create: `services/auth/internal/service/oidc_exchange_test.go`

- [ ] **Step 8.1: Write the failing tests — every rejection reason gets its own sub-test**

Use the `startStubIdP` helper from Task 6. Test fixtures:
- A valid OIDC JWT signed by the stub IdP's key.
- A mutator function that produces variants for each rejection branch.

Sub-tests (all 7 spec rejection reasons + happy path):
- `Happy` — successful exchange returns a registry JWT with the SA's id as `sub` + `principal_kind=service_account` + `source=workload_oidc` + `trust_id` claim.
- `Rejects_IssuerNotAllowed` — JWT issuer not in `OIDC_ALLOWED_ISSUERS`.
- `Rejects_AudienceMismatch` — JWT `aud` doesn't match trust config's `audience`.
- `Rejects_SubjectMismatch` — JWT `sub` doesn't match trust config's `subject_pattern`.
- `Rejects_SignatureInvalid` — JWT signed by a different key.
- `Rejects_Expired` — JWT `exp` in the past.
- `Rejects_SAdisabled` — trust's service account has been disabled.
- `Rejects_NotBeforeFuture` — JWT `nbf` in the future.

Each rejection sub-test asserts:
1. The returned `codes.Unauthenticated` (NOT `InvalidArgument` — these are auth failures).
2. The returned error message is generic (no leak of which check failed). The reason classification goes into the audit event, not the response body.

Last_used_at on the trust row is updated on success only.

- [ ] **Step 8.2: Implement**

Use `github.com/golang-jwt/jwt/v5` (already imported by `services/auth/internal/service/keyring.go` — confirm with `grep -l "golang-jwt" services/auth/`). Sketch:

```go
package service

import (
    "context"
    "crypto/rsa"
    "errors"
    "fmt"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"

    "github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// WorkloadTokenResult is the synthesised access-token returned to the caller.
type WorkloadTokenResult struct {
    AccessToken string
    ExpiresIn   int32 // seconds
    TokenType   string // always "Bearer"
}

// rejectReason enumerates the 7 audit reasons. Reported to the audit
// event but NOT to the caller — the caller always gets a generic
// codes.Unauthenticated to avoid leaking which gate failed.
type rejectReason string

const (
    rejectIssuerNotAllowed rejectReason = "issuer_not_allowed"
    rejectAudienceMismatch rejectReason = "audience_mismatch"
    rejectSubjectMismatch  rejectReason = "subject_mismatch"
    rejectSignatureInvalid rejectReason = "signature_invalid"
    rejectExpired          rejectReason = "expired"
    rejectNotYetValid      rejectReason = "not_yet_valid"
    rejectSADisabled       rejectReason = "sa_disabled"
)

// ExchangeWorkloadToken implements the FUT-001 token-exchange flow.
// On success, emits an auth.workload_token.exchanged audit event and
// returns a short-lived RS256 registry JWT scoped to the trust's SA.
// On failure, emits an auth.workload_token.rejected audit with the
// rejectReason and returns codes.Unauthenticated.
func (s *OIDCTrustService) ExchangeWorkloadToken(ctx context.Context, rawJWT string) (*WorkloadTokenResult, error) {
    // Parse without verifying signature — we need the claims to find the
    // matching trust config before we can decide which key to verify
    // against.
    unverified, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseUnverified(rawJWT, jwt.MapClaims{})
    if err != nil {
        return s.rejectAndEmit(ctx, rawJWT, nil, rejectSignatureInvalid, "malformed token")
    }
    claims, _ := unverified.Claims.(jwt.MapClaims)
    iss, _ := claims["iss"].(string)
    sub, _ := claims["sub"].(string)
    aud := claimsAudience(claims) // helper: returns first aud whether string or []string

    // (1) issuer allowlist
    if !issuerAllowed(s.allowedIssuers, iss) {
        return s.rejectAndEmit(ctx, rawJWT, nil, rejectIssuerNotAllowed, "issuer not allowed")
    }

    // (2) find a matching trust config by (iss, sub_pattern matches sub).
    // We do NOT yet know which trust — multiple may share an issuer.
    // Iterate trusts for this issuer and pick the first matching one.
    candidates, err := s.repo.ListByIssuer(ctx, iss) // new repo method
    if err != nil { return nil, err }
    var matched *repository.OIDCTrust
    for _, t := range candidates {
        if t.Audience == aud && subjectMatches(t.SubjectPattern, sub) {
            matched = t
            break
        }
    }
    if matched == nil {
        // Could be audience or subject mismatch — bias the audit reason
        // toward the more informative classification.
        if anyAudienceMatch(candidates, aud) {
            return s.rejectAndEmit(ctx, rawJWT, nil, rejectSubjectMismatch, "subject mismatch")
        }
        return s.rejectAndEmit(ctx, rawJWT, nil, rejectAudienceMismatch, "audience mismatch")
    }

    // (3) fetch + cache JWKS for this issuer
    ttl := time.Duration(matched.JWKSCacheTTLSeconds) * time.Second
    keys, err := s.jwks.Fetch(ctx, iss, ttl)
    if err != nil {
        // Fail-closed: surface as Unavailable so CI retries.
        return nil, status.Errorf(codes.Unavailable, "fetch jwks: %v", err)
    }

    // (4) verify signature
    parsed, err := jwt.Parse(rawJWT, func(tok *jwt.Token) (any, error) {
        kid, _ := tok.Header["kid"].(string)
        if k, ok := keys[kid]; ok {
            return k, nil
        }
        return nil, fmt.Errorf("kid %q not found in JWKS", kid)
    }, jwt.WithValidMethods([]string{"RS256"}))
    if err != nil {
        switch {
        case errors.Is(err, jwt.ErrTokenExpired):
            return s.rejectAndEmit(ctx, rawJWT, matched, rejectExpired, "expired")
        case errors.Is(err, jwt.ErrTokenNotValidYet):
            return s.rejectAndEmit(ctx, rawJWT, matched, rejectNotYetValid, "not yet valid")
        default:
            return s.rejectAndEmit(ctx, rawJWT, matched, rejectSignatureInvalid, "signature invalid")
        }
    }
    if !parsed.Valid {
        return s.rejectAndEmit(ctx, rawJWT, matched, rejectSignatureInvalid, "signature invalid")
    }

    // (5) load + validate SA
    sa, err := s.serviceAccounts.GetByID(ctx, matched.ServiceAccountID)
    if err != nil { return nil, err }
    if sa == nil || sa.DisabledAt != nil {
        return s.rejectAndEmit(ctx, rawJWT, matched, rejectSADisabled, "sa disabled")
    }

    // (6) mint a 15-min RS256 registry JWT keyed to the SA.
    // Delegate to the existing Service.IssueToken with workload claims.
    access, err := s.issueWorkloadToken(ctx, sa, matched)
    if err != nil { return nil, err }

    // (7) best-effort last_used_at
    _ = s.repo.MarkUsed(ctx, matched.ID)

    // (8) emit success audit
    s.emitExchangedAudit(ctx, matched, sub)

    return &WorkloadTokenResult{
        AccessToken: access,
        ExpiresIn:   900,
        TokenType:   "Bearer",
    }, nil
}

// rejectAndEmit is the single chokepoint for failed exchanges. Emits
// auth.workload_token.rejected with the reason; returns a generic
// codes.Unauthenticated to the caller.
func (s *OIDCTrustService) rejectAndEmit(ctx context.Context, rawJWT string, trust *repository.OIDCTrust, reason rejectReason, internal string) (*WorkloadTokenResult, error) {
    s.emitRejectedAudit(ctx, rawJWT, trust, reason)
    return nil, status.Error(codes.Unauthenticated, "workload token rejected")
}
```

`issueWorkloadToken` builds a `jwt.MapClaims` with:
- `sub`: SA shadow user id
- `tenant_id`: SA's tenant
- `access`: SA's effective scopes (intersected, as today)
- `principal_kind`: `"service_account"`
- `source`: `"workload_oidc"`
- `trust_id`: matched.ID.String()
- `iat`, `exp` (now+15m), `jti` (uuid)

It then calls `s.IssueTokenFromClaims(claims)` (a thin wrapper around the existing `IssueToken` to be added in `auth.go`). Reuses the keyring.

Run tests: `cd services/auth && go test ./internal/service/ -run TestExchangeWorkloadToken -v`. All 8 sub-tests pass.

- [ ] **Step 8.3: Commit**

```bash
git add services/auth/internal/service/oidc_exchange.go services/auth/internal/service/oidc_exchange_test.go services/auth/internal/service/auth.go
git commit -m "feat(auth): ExchangeWorkloadToken with 7 reject reasons (FUT-001)"
```

---

## Task 9: gRPC handlers — 4 admin RPCs

**Files:**
- Create: `services/auth/internal/handler/grpc_oidc_trust.go`
- Create: `services/auth/internal/handler/grpc_oidc_trust_test.go`

Implements the 4 admin RPCs as thin wrappers around `OIDCTrustService`:

```go
func (s *GRPCServer) ListOIDCTrusts(ctx context.Context, req *authv1.ListOIDCTrustsRequest) (*authv1.ListOIDCTrustsResponse, error) {
    tenantID, err := uuid.Parse(req.GetTenantId())
    if err != nil { return nil, status.Error(codes.InvalidArgument, "invalid tenant_id") }
    trusts, err := s.oidc.List(ctx, tenantID)
    if err != nil { return nil, err }
    out := make([]*authv1.OIDCTrust, 0, len(trusts))
    for _, t := range trusts {
        out = append(out, toProto(t))
    }
    return &authv1.ListOIDCTrustsResponse{Trusts: out}, nil
}
// + Create, Update, Delete — same shape
```

Implement `toProto(*repository.OIDCTrust) *authv1.OIDCTrust` as a private helper.

Each handler is small enough to test inline with the in-process service (no testcontainers required — those are already exercised by the service-layer tests).

- [ ] **Step 9.1: Write the failing tests** (`TestListOIDCTrusts_HappyPath`, `_RejectsBadTenantID`, etc.)
- [ ] **Step 9.2: Implement the 4 handlers**
- [ ] **Step 9.3: Commit**

```bash
git add services/auth/internal/handler/grpc_oidc_trust.go services/auth/internal/handler/grpc_oidc_trust_test.go
git commit -m "feat(auth): gRPC handlers for OIDC trust CRUD (FUT-001)"
```

---

## Task 10: HTTP handler — `POST /auth/token/workload` + Redis rate-limit

**Files:**
- Create: `services/auth/internal/handler/http_workload_token.go`
- Create: `services/auth/internal/handler/http_workload_token_test.go`

The HTTP entry point. Public (no Bearer). Accepts `{oidc_jwt: "..."}` JSON body OR `Authorization: Bearer <oidc_jwt>` header (mirrors the way GitHub Actions presents it).

Per-(issuer, subject) Redis rate limit using `SET NX EX 60` with a value of the request count. Threshold: 100/min. On exceed: `429 Too Many Requests` with `Retry-After` header.

- [ ] **Step 10.1: Write the failing test**

Use `httptest.NewRecorder` against the handler function directly (the rate limit interacts with a real Redis client — use `testutil` for a Redis testcontainer, or accept a `redis.Cmdable` interface and use a fake).

Sub-tests:
- `Success_FromJSONBody`
- `Success_FromAuthorizationHeader`
- `Rejects_MissingJWT_400`
- `RateLimit_100PerMinute` (loop 101 requests, assert last returns 429)
- `RateLimit_KeyIsPerIssuerAndSubject` (concurrent different subjects each get their own bucket)

- [ ] **Step 10.2: Implement**

```go
package handler

import (
    "context"
    "encoding/json"
    "errors"
    "net/http"
    "strconv"
    "strings"

    "github.com/golang-jwt/jwt/v5"
    "github.com/redis/go-redis/v9"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

const workloadRateLimitPerMin = 100

// HandleWorkloadTokenExchange is the public POST /auth/token/workload
// route. Accepts the OIDC JWT in the JSON body or the Authorization
// header. Applies per-(issuer, subject) rate-limiting via Redis BEFORE
// invoking the exchange flow so a compromised CI can't burn unbounded
// Argon2 / JWKS-fetch cycles.
func (h *HTTPHandler) HandleWorkloadTokenExchange(w http.ResponseWriter, r *http.Request) {
    rawJWT := extractWorkloadJWT(r)
    if rawJWT == "" {
        http.Error(w, `{"error":"missing oidc_jwt"}`, http.StatusBadRequest)
        return
    }

    // Pre-parse (without verifying) to derive the rate-limit key. A
    // malformed token gets a generic 401 — same shape as a signature
    // failure so an attacker can't enumerate parseability.
    iss, sub, err := peekIssuerAndSubject(rawJWT)
    if err != nil {
        http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
        return
    }

    if exceeded, retryAfter, err := h.checkWorkloadRateLimit(r.Context(), iss, sub); err != nil {
        // Redis down: fail-open (same posture as the API-key cache —
        // rate limit is an optimisation, not a security boundary).
        h.logger.Warn("workload rate-limit check failed; failing open", "err", err)
    } else if exceeded {
        w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
        http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
        return
    }

    result, err := h.svc.OIDCTrust().ExchangeWorkloadToken(r.Context(), rawJWT)
    if err != nil {
        if s, ok := status.FromError(err); ok {
            switch s.Code() {
            case codes.Unauthenticated:
                http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
            case codes.Unavailable:
                http.Error(w, `{"error":"idp unreachable"}`, http.StatusServiceUnavailable)
            default:
                http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
            }
            return
        }
        http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "access_token": result.AccessToken,
        "expires_in":   result.ExpiresIn,
        "token_type":   result.TokenType,
    })
}

// extractWorkloadJWT pulls the JWT from JSON body OR Authorization header.
// Body takes precedence so a Bearer header with a malformed value doesn't
// override an explicit body field.
func extractWorkloadJWT(r *http.Request) string {
    var body struct{ OIDCJWT string `json:"oidc_jwt"` }
    _ = json.NewDecoder(r.Body).Decode(&body)
    if body.OIDCJWT != "" { return body.OIDCJWT }

    if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
        return strings.TrimPrefix(h, "Bearer ")
    }
    return ""
}

// peekIssuerAndSubject parses the JWT WITHOUT verifying. Used to derive
// the rate-limit bucket key.
func peekIssuerAndSubject(rawJWT string) (iss, sub string, err error) {
    tok, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseUnverified(rawJWT, jwt.MapClaims{})
    if err != nil { return "", "", err }
    claims, _ := tok.Claims.(jwt.MapClaims)
    iss, _ = claims["iss"].(string)
    sub, _ = claims["sub"].(string)
    if iss == "" || sub == "" { return "", "", errors.New("missing iss or sub") }
    return iss, sub, nil
}

// checkWorkloadRateLimit increments a Redis counter keyed on (iss, sub).
// Returns (exceeded, retryAfterSeconds, err).
func (h *HTTPHandler) checkWorkloadRateLimit(ctx context.Context, iss, sub string) (bool, int, error) {
    key := "workload:rate:" + iss + ":" + sub
    pipe := h.redis.TxPipeline()
    incr := pipe.Incr(ctx, key)
    pipe.Expire(ctx, key, 60_000_000_000) // 60s in nanoseconds; pgx-style
    if _, err := pipe.Exec(ctx); err != nil {
        if errors.Is(err, redis.Nil) { return false, 0, nil }
        return false, 0, err
    }
    n := incr.Val()
    if n > workloadRateLimitPerMin {
        return true, 60, nil
    }
    return false, 0, nil
}
```

Run tests: all 5 sub-tests pass.

- [ ] **Step 10.3: Register the route + commit**

In `services/auth/internal/server/server.go`, add to the HTTP mux setup:

```go
mux.Handle("POST /auth/token/workload", http.HandlerFunc(handler.HandleWorkloadTokenExchange))
```

Commit:

```bash
git add services/auth/internal/handler/http_workload_token.go services/auth/internal/handler/http_workload_token_test.go services/auth/internal/server/server.go
git commit -m "feat(auth): POST /auth/token/workload + per-(iss,sub) rate-limit (FUT-001)"
```

---

## Task 11: Audit events

**Files:**
- Modify: `libs/rabbitmq/events/events.go`
- Modify: `services/audit/internal/eventconsumer/consumer.go`

- [ ] **Step 11.1: Add 5 routing constants + payloads**

In `libs/rabbitmq/events/events.go`, append:

```go
const (
    RoutingOIDCTrustCreated         = "auth.oidc_trust.created"
    RoutingOIDCTrustUpdated         = "auth.oidc_trust.updated"
    RoutingOIDCTrustDeleted         = "auth.oidc_trust.deleted"
    RoutingWorkloadTokenExchanged   = "auth.workload_token.exchanged"
    RoutingWorkloadTokenRejected    = "auth.workload_token.rejected"
)

type OIDCTrustPayload struct {
    TrustID            string `json:"trust_id"`
    TenantID           string `json:"tenant_id"`
    ServiceAccountID   string `json:"service_account_id"`
    DisplayName        string `json:"display_name"`
    IssuerURL          string `json:"issuer_url"`
    Audience           string `json:"audience"`
    SubjectPattern     string `json:"subject_pattern"`
    ActorID            string `json:"actor_id"`
}

type WorkloadTokenPayload struct {
    TrustID          string `json:"trust_id,omitempty"`
    IssuerURL        string `json:"issuer_url"`
    Subject          string `json:"subject"` // truncated to 256 chars by emitter
    ServiceAccountID string `json:"service_account_id,omitempty"`
    Reason           string `json:"reason,omitempty"` // for rejected only
}
```

- [ ] **Step 11.2: Wire 5 cases in `mapEvent`**

In `services/audit/internal/eventconsumer/consumer.go`, find the switch statement (`mapEvent`) and add 5 cases that translate each routing key + payload into an `audit_events` row. Follow the existing patterns (e.g., the `rbac.role_granted` case is the closest shape).

This satisfies the CLAUDE.md §10 invariant ("every event type registered in libs/rabbitmq/events must either map to a row in audit_events OR carry a `// audit: skip` annotation"). The audit-catalogue lint test (`TestAuditCatalogueComplete` or similar) will fail if any of these 5 events is missing — confirm with `cd services/audit && go test ./internal/eventconsumer/ -run Catalogue -v`.

- [ ] **Step 11.3: Commit**

```bash
git add libs/rabbitmq/events/events.go services/audit/internal/eventconsumer/consumer.go
git commit -m "feat(audit): catalogue OIDC trust + workload token events (FUT-001)"
```

---

## Task 12: Config + main wiring

**Files:**
- Modify: `services/auth/internal/config/config.go`
- Modify: `services/auth/cmd/server/main.go`
- Modify: `services/auth/internal/server/server.go`
- Modify: `infra/docker-compose/docker-compose.yml`

- [ ] **Step 12.1: Config field**

Add to `services/auth/internal/config/config.go`:

```go
// OIDCAllowedIssuers is the CSV of trusted OIDC issuer URL prefixes.
// Used by FUT-001 federated workload identity to gate which IdPs may
// participate in token exchange. Empty/unset rejects ALL trust creation
// and ALL exchange requests (fail-closed default for self-hosters who
// haven't named their CI runners' IdPs yet).
OIDCAllowedIssuers string `mapstructure:"OIDC_ALLOWED_ISSUERS"`
```

No production validation — empty is a valid (fail-closed) state.

- [ ] **Step 12.2: main + server wiring**

Pass `parseIssuerAllowlist(cfg.OIDCAllowedIssuers)` into `service.NewOIDCTrustService(...)` in the existing service construction chain. Register the new gRPC service on the existing gRPC server registry.

- [ ] **Step 12.3: docker-compose env**

Add to the `registry-auth` environment block in `infra/docker-compose/docker-compose.yml`:

```yaml
      # FUT-001 — CSV of trusted OIDC issuer URL prefixes for workload
      # identity. Empty rejects all trust creation + exchange. Dev list
      # is GitHub Actions + GitLab + Buildkite so the developer can
      # exercise the flow without editing this file.
      OIDC_ALLOWED_ISSUERS: "https://token.actions.githubusercontent.com,https://gitlab.com,https://agent.buildkite.com"
```

- [ ] **Step 12.4: Commit**

```bash
git add services/auth/internal/config/config.go services/auth/cmd/server/main.go services/auth/internal/server/server.go infra/docker-compose/docker-compose.yml
git commit -m "feat(auth): wire OIDC_ALLOWED_ISSUERS + OIDCTrustService through main (FUT-001)"
```

---

## Task 13: BFF — 4 admin routes on `services/management`

**Files:**
- Create: `services/management/internal/handler/access_oidc_trust.go`
- Create: `services/management/internal/handler/access_oidc_trust_test.go`
- Modify: `services/management/internal/handler/handler.go` (route registration)

4 routes, all authMW-gated + admin-gated (use the existing `requireTenantAdmin` helper):

- `GET /api/v1/access/oidc-trust` → calls `auth.ListOIDCTrusts`
- `POST /api/v1/access/oidc-trust` → calls `auth.CreateOIDCTrust`
- `PATCH /api/v1/access/oidc-trust/{id}` → calls `auth.UpdateOIDCTrust`
- `DELETE /api/v1/access/oidc-trust/{id}` → calls `auth.DeleteOIDCTrust`

The BFF translates HTTP requests into gRPC calls; passes the tenant ID from the JWT claims (NOT from a request body field — defence against a tenant trying to call the API with a different tenant_id).

- [ ] **Step 13.1: Write tests** (4 happy-path + 4 admin-deny tests, `httptest.NewRecorder` pattern matching existing `access_oidc_trust_test.go` siblings — see `access_*.go` and their tests in `services/management/internal/handler/`)
- [ ] **Step 13.2: Implement the 4 handler methods**
- [ ] **Step 13.3: Register routes in `handler.go`**

```go
mux.Handle("GET /api/v1/access/oidc-trust",        authMW(http.HandlerFunc(h.handleListOIDCTrust)))
mux.Handle("POST /api/v1/access/oidc-trust",       authMW(http.HandlerFunc(h.handleCreateOIDCTrust)))
mux.Handle("PATCH /api/v1/access/oidc-trust/{id}", authMW(http.HandlerFunc(h.handleUpdateOIDCTrust)))
mux.Handle("DELETE /api/v1/access/oidc-trust/{id}", authMW(http.HandlerFunc(h.handleDeleteOIDCTrust)))
```

- [ ] **Step 13.4: Commit**

```bash
git add services/management/internal/handler/access_oidc_trust.go services/management/internal/handler/access_oidc_trust_test.go services/management/internal/handler/handler.go
git commit -m "feat(management): 4 OIDC trust admin BFF routes (FUT-001)"
```

---

## Task 14: FE — TanStack hooks

**Files:**
- Create: `frontend/src/lib/api/oidc-trust.ts`

4 hooks mirroring the BFF routes. Follow the `service-accounts.ts` axios destructure pattern (sibling reference).

```typescript
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface OIDCTrust {
  id: string;
  tenant_id: string;
  service_account_id: string;
  display_name: string;
  issuer_url: string;
  audience: string;
  subject_pattern: string;
  jwks_cache_ttl_seconds: number;
  created_at: string;
  updated_at: string;
  last_used_at?: string | null;
}

export function useOIDCTrusts() {
  return useQuery<OIDCTrust[]>({
    queryKey: ["oidc-trusts"],
    queryFn: async () => {
      const { data } = await apiClient.get<{ trusts: OIDCTrust[] }>("/access/oidc-trust");
      return data.trusts;
    },
  });
}

export interface CreateOIDCTrustInput {
  service_account_id: string;
  display_name: string;
  issuer_url: string;
  audience: string;
  subject_pattern: string;
  jwks_cache_ttl_seconds?: number;
}

export function useCreateOIDCTrust() {
  const qc = useQueryClient();
  return useMutation<OIDCTrust, Error, CreateOIDCTrustInput>({
    mutationFn: async (in_) => {
      const { data } = await apiClient.post<OIDCTrust>("/access/oidc-trust", in_);
      return data;
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ["oidc-trusts"] }),
  });
}
// + useUpdateOIDCTrust + useDeleteOIDCTrust — same shape
```

Vite proxy: `/api/v1/access/*` already maps to port 8080 in `frontend/vite.config.ts:65`. Since the BFF routes live on management (port 8091), we need to either (a) add an `/api/v1/access/oidc-trust` entry mapping to 8091 BEFORE the catchall `/api/v1/access` → 8080, OR (b) put the routes on auth instead. Per the spec they live on management. So:

- [ ] **Step 14.1: Update `frontend/vite.config.ts` proxy**

Add ABOVE the `/api/v1/access` entry:

```typescript
"/api/v1/access/oidc-trust": { target: "http://localhost:8091", changeOrigin: true },
```

(Vite picks first matching key, so this must come before the catchall.)

- [ ] **Step 14.2: Commit**

```bash
git add frontend/src/lib/api/oidc-trust.ts frontend/vite.config.ts
git commit -m "feat(frontend): useOIDCTrust TanStack hooks + Vite proxy entry (FUT-001)"
```

---

## Task 15: FE — pure glob validator (mirror of BE matcher)

**Files:**
- Create: `frontend/src/lib/oidc-subject-glob.ts`
- Create: `frontend/src/lib/__tests__/oidc-subject-glob.test.ts`

Client-side validation so the form rejects malformed globs before the API call. Implements the SAME glob semantics as the BE `subjectMatches` — but here we only need `validateGlobSyntax(pattern: string): {ok: true} | {ok: false, error: string}`. We do NOT need the matcher itself (the BE is the source of truth at exchange time).

- [ ] **Step 15.1: Write tests** (10+ cases mirroring the BE syntax tests)
- [ ] **Step 15.2: Implement** the validator (small — checks balanced characters, rejects `***`, rejects empty)
- [ ] **Step 15.3: Commit**

```bash
git add frontend/src/lib/oidc-subject-glob.ts frontend/src/lib/__tests__/oidc-subject-glob.test.ts
git commit -m "feat(frontend): client-side OIDC subject-glob validator (FUT-001)"
```

---

## Task 16: FE — `CreateOIDCTrustDialog`

**Files:**
- Create: `frontend/src/components/access/CreateOIDCTrustDialog.tsx`
- Create: `frontend/src/components/access/__tests__/CreateOIDCTrustDialog.test.tsx`

Form fields:
- Display name (text, required)
- Service account (`<Select>` from `useServiceAccounts()`)
- Issuer URL (text, required, must start with `https://` — show inline error if not)
- Audience (text, required)
- Subject pattern (text, required, must pass `validateGlobSyntax` — show inline error)
- JWKS cache TTL seconds (number, optional, default 3600)

On submit: call `useCreateOIDCTrust().mutate(...)`. On 4xx: render error message. On success: close dialog.

- [ ] **Step 16.1: Write component tests** (happy path + each validation branch)
- [ ] **Step 16.2: Implement the dialog**
- [ ] **Step 16.3: Commit**

```bash
git add frontend/src/components/access/CreateOIDCTrustDialog.tsx frontend/src/components/access/__tests__/CreateOIDCTrustDialog.test.tsx
git commit -m "feat(frontend): CreateOIDCTrustDialog with client-side validation (FUT-001)"
```

---

## Task 17: FE — `TrustPanel` (replaces `TrustPreview`)

**Files:**
- Create: `frontend/src/components/access/TrustPanel.tsx`
- Create: `frontend/src/components/access/__tests__/TrustPanel.test.tsx`
- Delete: `frontend/src/components/access/previews/TrustPreview.tsx`
- Modify: `frontend/src/routes/_authenticated.api-keys.trust.tsx`

The live panel:
- Heading + description (drops `<PreviewBanner>`)
- "New trust relationship" button → opens `CreateOIDCTrustDialog`
- Trust cards rendered from `useOIDCTrusts()` data (replace the preview's `DUMMY_TRUSTS`)
- Each card has a kebab menu: `Edit` / `Delete`
- Each card shows `Last verified: <last_used_at>` or `Last verified: never` if `null`
- Keep the existing flow diagram (it's still accurate)

Loading / error / empty states (mirror HelpersPanel's shape).

- [ ] **Step 17.1: Write component tests** (mirror `HelpersPanel.test.tsx` shape — render with 0/N trusts, assert no preview banner, assert "New trust relationship" button is now enabled)
- [ ] **Step 17.2: Implement**
- [ ] **Step 17.3: Swap route + delete preview**
- [ ] **Step 17.4: Commit**

```bash
git add frontend/src/components/access/TrustPanel.tsx frontend/src/components/access/__tests__/TrustPanel.test.tsx frontend/src/routes/_authenticated.api-keys.trust.tsx frontend/src/components/access/previews/TrustPreview.tsx
git commit -m "feat(frontend): live TrustPanel + route swap + preview deletion (FUT-001)"
```

---

## Task 18: FE — sidebar graduation

**Files:**
- Modify: `frontend/src/components/access/AccessSubNav.tsx`
- Modify: `frontend/src/components/access/__tests__/AccessSubNav.test.tsx`

Move `Federated trust` from the Preview section into the Workspace section (alongside Service accounts, Activity, Credential helpers). Drop the `preview: true` flag. Preview count goes from 3 → 2 (Token policies + Access review remain).

Update the existing graduation regression test ("shows Credential helpers in Workspace section…") to ALSO assert Federated trust is in Workspace — or add a parallel test for it.

- [ ] **Step 18.1: Move the entry**
- [ ] **Step 18.2: Update test**
- [ ] **Step 18.3: Commit**

```bash
git add frontend/src/components/access/AccessSubNav.tsx frontend/src/components/access/__tests__/AccessSubNav.test.tsx
git commit -m "feat(frontend): graduate Federated trust out of Preview section (FUT-001)"
```

---

## Task 19: Tracker hygiene

**Files:**
- Modify: `status-tracker.md`
- Modify: `futures.md`

Same pattern as FUT-002's REM-021:

- [ ] **Step 19.1: Add REM-023 to `status-tracker.md`** (between REM-021 and REM-014). Body:

```markdown
### REM-023 — FUT-001 Federated workload identity (in flight)

**Affects:** `services/auth` (new `oidc_trust_configs` table + 4 admin RPCs + `POST /auth/token/workload` HTTP exchange + JWKS cache + issuer-allowlist), `services/management` (4 BFF admin routes), `frontend` (new TrustPanel + CreateOIDCTrustDialog).

**Status:** IN FLIGHT on `feat/fut-001-federated-workload-identity`. Second of the FUT-001..FUT-004 batch (FUT-002 shipped in PR #221). Spec: `docs/superpowers/specs/2026-06-30-api-keys-tier2-backend-design.md` §Feature 2.

**Plan:** `docs/superpowers/plans/2026-07-01-fut-001-federated-workload-identity.md`.

**On merge:** remove this entry; append a resolution row to `status.md`.
```

- [ ] **Step 19.2: Stub FUT-001 in `futures.md`** with the same `<details>` pattern FUT-002 used.

- [ ] **Step 19.3: Commit**

```bash
git add status-tracker.md futures.md
git commit -m "chore(trackers): REM-023 FUT-001 in-flight entry + futures.md stub"
```

---

## Task 20: Local CI gate (CLAUDE.md §15)

- [ ] **Step 20.1: BE gate**

```bash
cd services/auth && go vet ./... && go build ./... && go test ./...
cd ../management && go vet ./... && go build ./... && go test ./...
cd ../audit && go vet ./... && go build ./... && go test ./...
cd ../..
```

Expected: every command exits 0; no test failures. Pre-existing vet warning in `services/management/internal/handler/admin_tenants_test.go:162` is REM-014 main-rot — flag, don't fix.

- [ ] **Step 20.2: FE gate (all 4 CI equivalents)**

```bash
cd frontend && npm run lint && npm run typecheck && npm run test && npm run build && cd ..
```

Expected: every command exits 0.

- [ ] **Step 20.3: spec-lint regression check**

```bash
go build -C tools/spec-lint -o spec-lint.exe .
./tools/spec-lint/spec-lint.exe .
rm tools/spec-lint/spec-lint.exe
```

Expected: `spec-lint: all 13 rules passed`. Audit catalogue rule (#11) will fail unless the 5 new events from Task 11 are wired into `mapEvent` — that's the point of that rule.

---

## Task 21: 3-agent review batch (BEFORE `gh pr create`)

Per memory `feedback_review_agents_batch.md`. Spawn `security-agent` + `qa-agent` + `code-review-agent` in a single message (parallel). Each gets:

- The branch name
- The spec doc path
- The plan doc path
- The scope summary: "FUT-001 only — federated workload identity. New auth table + 4 admin RPCs + exchange endpoint + FE TrustPanel. No tracker hygiene (separate); no FUT-003/004 (separate PRs)."

Specific FUT-001 must-checks beyond the standard scope:

**security-agent priority items:**
- Subject-glob matcher tests cover `/` semantics correctly (`*` vs `**`) — wrong implementation means token-mint for the wrong SA.
- JWKS cache is fail-CLOSED on network errors (must NOT serve stale entries after TTL).
- Exchange returns generic `codes.Unauthenticated` on every rejection — no leak of which check failed.
- Per-(issuer, subject) rate limit applies BEFORE JWKS fetch + Argon2-equivalent work.
- `OIDC_ALLOWED_ISSUERS` checked at BOTH create-time AND exchange-time (an issuer removed from the env after creation stops minting).

**qa-agent priority items:**
- Migration test creates `oidc_trust_configs` cleanly + the FK + the UNIQUE constraint behaves as advertised.
- All 7 reject-reason audit events fire with the right `reason` enum value.
- HTTP handler tests cover both `{oidc_jwt: ...}` body form AND `Authorization: Bearer ...` header form.

**code-review-agent priority items:**
- Glob matcher implementation reads cleanly (recursive descent, not regex)
- JWKS cache mutex correctness (no lock-while-doing-HTTP gotchas)
- Audit event payload uses `truncate-to-256` on the subject claim to avoid unbounded row size

Fold must-fixes inline. Log should-fixes as REM-023 follow-ups.

---

## Task 22: Open the PR

- [ ] **Step 22.1: Push the branch**

```bash
git push -u origin feat/fut-001-federated-workload-identity
```

- [ ] **Step 22.2: Create the PR** with body covering: summary, all 8 reject reasons covered, links to spec + plan, REM-023 reference, manual test plan (incl. `curl -X POST /auth/token/workload` with a stub IdP token).

---

## Notes for the executor

- **Per CLAUDE.md `feedback_code_comments`:** every new file gets a top-of-file comment + per-function doc strings. Encoded in the code blocks.
- **Per `feedback_git_workflow`:** commit on the current branch. Never commit to main.
- **Per `feedback_review_pace.md`:** small must-fixes inline; should-fixes → REM-023 follow-ups in `status-tracker.md`.
- **Per CLAUDE.md §15.1:** all 4 FE CI equivalents (lint + typecheck + test + build), not just typecheck + vitest.
- **Per CLAUDE.md §12:** new gRPC methods use snake_case field names; pagination uses `page_token` + `page_size` (FUT-001 has none — single-list returns all trusts per tenant, which is fine for the realistic ~10s of trusts).
- **Per CLAUDE.md §11:** raw SQL only, parameterised, never `fmt.Sprintf` for SQL.
- **Per CLAUDE.md §10:** audit catalogue completeness — every routing key MUST be in `mapEvent` or carry `// audit: skip`. The spec-lint test enforces this.

**If a step's expected output diverges:** stop, read the actual output, fix the underlying issue. Don't proceed past a divergence.
