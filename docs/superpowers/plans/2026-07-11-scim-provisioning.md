# SCIM 2.0 Provisioning (Users-only v1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add SCIM 2.0 `/scim/v2/Users` provisioning + deprovisioning to `registry-auth` so an enterprise IdP (Okta/Entra) can create, list, update, and deactivate users, authenticated by a dedicated global SCIM bearer token.

**Architecture:** All in `registry-auth`. A dedicated Argon2-verified SCIM token (single `scim_config` row) gates a new `/scim/v2/*` route group isolated from user JWTs. Provisioning reuses `CreateSSOUser`'s passwordless pattern + a baseline `reader@*` grant; deprovisioning routes `active:false`/`DELETE` to the existing `SetUserDisabled` (JTI + API-key revoke). Users carry a new `external_id` for IdP correlation.

**Tech Stack:** Go 1.25.11, `pgx/v5` (raw SQL, `pgxpool`), `pressly/goose` migrations, `libs/crypto/argon2`, `libs/auth/bearer`, Go 1.22 `net/http` method-pattern routes. Tests: standard `testing` + testcontainers (`libs/testutil/containers`) for the integration lane (`//go:build integration`).

**Spec:** [`docs/superpowers/specs/2026-07-11-scim-provisioning-design.md`](../specs/2026-07-11-scim-provisioning-design.md). This plan covers **Phases 1–2** (schema + auth boundary + Users endpoints — the shippable backend core). Phase 3 (admin token-management UI) is a separate follow-up plan.

**Conventions for the implementer:**
- Build/test with `GOWORK=off` from `services/auth` (the module is self-contained; the workspace confuses `go test`). Local Go auto-selects the repo's 1.25.11 toolchain.
- Trust `GOWORK=off go build ./...` over editor/gopls diagnostics (they are chronically stale here).
- Commit after every green step. Use a POSIX heredoc for multi-line commit messages (the Bash tool is Git Bash; PowerShell here-strings mangle subjects).
- Integration tests are `//go:build integration` and run via `GOWORK=off go test -tags integration ./...` (needs Docker).

---

## File Structure

| File | Responsibility |
|---|---|
| `services/auth/migrations/20260711120000_users_external_id.sql` | Add `users.external_id` + partial unique index + `provisioned_via`. |
| `services/auth/migrations/20260711120100_scim_config.sql` | `scim_config` singleton table + runtime-role grants. |
| `services/auth/internal/repository/scim.go` | All SCIM SQL: token get/set, `GetUserByExternalID`, `CreateSCIMUser`, `SetExternalID`, `ListSCIMUsers`. |
| `services/auth/internal/service/scim_token.go` | Token generate/verify (Argon2) over the repo. |
| `services/auth/internal/service/scim_users.go` | Provision (create/collision), list (filter+page), get/put/patch/delete → disable. |
| `services/auth/internal/handler/scim_types.go` | SCIM wire types (User, ListResponse, Error, ServiceProviderConfig) + `toSCIMUser` mapper + `parseUserFilter`. |
| `services/auth/internal/handler/http_scim.go` | `requireSCIMAuth`, `RegisterSCIM(mux)`, discovery + Users HTTP handlers, SCIM error writer. |
| `services/auth/internal/handler/http.go` | Wire `RegisterSCIM(mux)` in `Register`. |
| `services/auth/internal/handler/scim_test.go` | Unit tests (token verify, filter parse, mapper, collision, active toggle). |
| `services/auth/internal/handler/scim_integration_test.go` | `//go:build integration` full-lifecycle test. |
| `services/auth/.env.example`, `docs/SERVICES.md`, `docs/AUTH.md` | Document the SCIM surface. |

---

## Phase 1 — Schema, token, auth boundary, discovery

### Task 1: Migration — `users.external_id`

**Files:**
- Create: `services/auth/migrations/20260711120000_users_external_id.sql`

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
ALTER TABLE users ADD COLUMN external_id TEXT;
ALTER TABLE users ADD COLUMN provisioned_via TEXT;
-- IdP-provisioned users have a stable externalId; the partial unique index lets
-- every non-SCIM user keep external_id NULL without colliding.
CREATE UNIQUE INDEX users_tenant_external_id_uniq
    ON users (tenant_id, external_id) WHERE external_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS users_tenant_external_id_uniq;
ALTER TABLE users DROP COLUMN IF EXISTS provisioned_via;
ALTER TABLE users DROP COLUMN IF EXISTS external_id;
```

- [ ] **Step 2: Verify migrations apply**

Run: `cd services/auth && GOWORK=off go build ./...`
Expected: builds (migrations are embedded via `embed.FS`; a syntax error fails the embed-backed goose validation at startup). Full apply is exercised by the integration test in Task 13.

- [ ] **Step 3: Commit**

```bash
git add services/auth/migrations/20260711120000_users_external_id.sql
git commit -m "feat(auth): add users.external_id for SCIM correlation"
```

---

### Task 2: Migration — `scim_config` singleton

**Files:**
- Create: `services/auth/migrations/20260711120100_scim_config.sql`

**Context:** the auth pgx pool authenticates as a low-privilege runtime role in some deployments. Grant the verbs the repository issues **in the same migration** (the #290 lesson — a missing grant surfaces only at runtime as `permission denied`). Confirm the role name used by existing auth tables by grepping `services/auth/migrations` for `GRANT ... TO` before writing; use the same role. If auth grants to no runtime role (owner-only), omit the GRANT block.

- [ ] **Step 1: Check the runtime-role grant pattern**

Run: `grep -rn "GRANT" services/auth/migrations/ | head`
Expected: either a `GRANT ... TO <role>` pattern to copy, or no output (owner-only — then skip the grant in Step 2).

- [ ] **Step 2: Write the migration** (include the GRANT block only if Step 1 found a runtime role; substitute the real role name)

```sql
-- +goose Up
CREATE TABLE scim_config (
    id           SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1), -- singleton row
    tenant_id    UUID NOT NULL,
    token_hash   TEXT,                     -- Argon2id PHC string; NULL = disabled
    enabled      BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);
-- GRANT SELECT, INSERT, UPDATE ON scim_config TO <runtime_role>;  -- if applicable

-- +goose Down
DROP TABLE IF EXISTS scim_config;
```

- [ ] **Step 3: Verify build**

Run: `cd services/auth && GOWORK=off go build ./...`
Expected: builds.

- [ ] **Step 4: Commit**

```bash
git add services/auth/migrations/20260711120100_scim_config.sql
git commit -m "feat(auth): add scim_config singleton table"
```

---

### Task 3: Repository — SCIM token get/set

**Files:**
- Create: `services/auth/internal/repository/scim.go`
- Test: covered by the integration test (Task 13); the pure logic is tested at the service layer (Task 4).

**Context:** mirror the style of `services/auth/internal/repository/user.go` (raw SQL, `r.pool`, context, parameterised). The singleton is upserted on `id = 1`.

- [ ] **Step 1: Write the token repository methods**

```go
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SCIMConfig is the single global SCIM provisioning config row.
type SCIMConfig struct {
	TenantID   uuid.UUID
	TokenHash  string // Argon2id PHC; "" when never set / disabled
	Enabled    bool
	LastUsedAt *time.Time
}

// GetSCIMConfig returns the singleton scim_config row, or ErrNotFound when it
// has never been written (feature never configured).
func (r *UserRepository) GetSCIMConfig(ctx context.Context) (*SCIMConfig, error) {
	const q = `SELECT tenant_id, COALESCE(token_hash, ''), enabled, last_used_at
	           FROM scim_config WHERE id = 1`
	var c SCIMConfig
	err := r.pool.QueryRow(ctx, q).Scan(&c.TenantID, &c.TokenHash, &c.Enabled, &c.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertSCIMToken writes the singleton row with a new Argon2 token hash and
// enables the feature. tokenHash == "" with enabled=false disables it.
func (r *UserRepository) UpsertSCIMToken(ctx context.Context, tenantID uuid.UUID, tokenHash string, enabled bool) error {
	const q = `
		INSERT INTO scim_config (id, tenant_id, token_hash, enabled, rotated_at)
		VALUES (1, $1, NULLIF($2, ''), $3, now())
		ON CONFLICT (id) DO UPDATE
		  SET tenant_id = EXCLUDED.tenant_id,
		      token_hash = EXCLUDED.token_hash,
		      enabled = EXCLUDED.enabled,
		      rotated_at = now()`
	_, err := r.pool.Exec(ctx, q, tenantID, tokenHash, enabled)
	return err
}

// TouchSCIMLastUsed best-effort stamps last_used_at. Errors are ignored by the
// caller (it is an audit convenience, not a security gate).
func (r *UserRepository) TouchSCIMLastUsed(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `UPDATE scim_config SET last_used_at = now() WHERE id = 1`)
	return err
}
```

- [ ] **Step 2: Verify build**

Run: `cd services/auth && GOWORK=off go build ./...`
Expected: builds. (`ErrNotFound` already exists in the repository package — confirm with `grep -rn "ErrNotFound" services/auth/internal/repository`.)

- [ ] **Step 3: Commit**

```bash
git add services/auth/internal/repository/scim.go
git commit -m "feat(auth): scim_config token repository methods"
```

---

### Task 4: Service — SCIM token generate/verify

**Files:**
- Create: `services/auth/internal/service/scim_token.go`
- Test: `services/auth/internal/service/scim_token_test.go`

**Context:** `argon2.Hash(pw string) (string, error)` and `argon2.Verify(pw, encoded string) (bool, error)` live in `libs/crypto/argon2`. The raw token format is `scim.<32-hex-bytes>` (a recognisable prefix + 256 bits of entropy from `crypto/rand`). The service holds a `scimRepo` interface so it can be faked in tests.

- [ ] **Step 1: Write the failing test**

```go
package service

import (
	"context"
	"testing"
)

type fakeSCIMRepo struct {
	cfg      *fakeCfg
	touched  bool
}
type fakeCfg struct {
	tokenHash string
	enabled   bool
}

func (f *fakeSCIMRepo) getHash() (string, bool)      { return f.cfg.tokenHash, f.cfg.enabled }
func (f *fakeSCIMRepo) setHash(h string, en bool)    { f.cfg = &fakeCfg{tokenHash: h, enabled: en} }
func (f *fakeSCIMRepo) touch()                       { f.touched = true }

func TestSCIMToken_generate_thenVerify(t *testing.T) {
	svc := newSCIMTokenSvc(&fakeSCIMRepo{cfg: &fakeCfg{}})
	raw, err := svc.generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(raw) < 20 || raw[:5] != "scim." {
		t.Fatalf("raw token should carry the scim. prefix, got %q", raw)
	}
	ok, err := svc.verify(raw)
	if err != nil || !ok {
		t.Fatalf("verify of freshly-generated token must pass, got ok=%v err=%v", ok, err)
	}
	if ok, _ := svc.verify("scim.deadbeef"); ok {
		t.Fatal("verify of a wrong token must fail")
	}
	// A disabled config must never verify, even with the right token.
	svc.repo.(*fakeSCIMRepo).cfg.enabled = false
	if ok, _ := svc.verify(raw); ok {
		t.Fatal("verify must fail when the config is disabled")
	}
}
```

Note: adapt the fake to whatever minimal interface `newSCIMTokenSvc` needs — the test defines the contract. Keep the fake in this test file.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSCIMToken -v`
Expected: FAIL (undefined `newSCIMTokenSvc`).

- [ ] **Step 3: Write the implementation**

```go
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/argon2"
)

// scimRepo is the slice of the repository the SCIM token service needs.
type scimRepo interface {
	getHash() (string, bool) // (argon2 hash, enabled)
	setHash(hash string, enabled bool)
	touch()
}

type scimTokenSvc struct {
	repo scimRepo
}

func newSCIMTokenSvc(r scimRepo) *scimTokenSvc { return &scimTokenSvc{repo: r} }

// generate mints a new raw token (`scim.<64-hex>`), stores its Argon2 hash, and
// enables the feature. The raw value is returned once and never persisted.
func (s *scimTokenSvc) generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("scim token entropy: %w", err)
	}
	raw := "scim." + hex.EncodeToString(b)
	hash, err := argon2.Hash(raw)
	if err != nil {
		return "", fmt.Errorf("scim token hash: %w", err)
	}
	s.repo.setHash(hash, true)
	return raw, nil
}

// verify returns true iff the config is enabled and the presented token matches
// the stored Argon2 hash. Fail-closed: disabled or unset config never verifies.
func (s *scimTokenSvc) verify(raw string) (bool, error) {
	hash, enabled := s.repo.getHash()
	if !enabled || hash == "" {
		return false, nil
	}
	ok, err := argon2.Verify(raw, hash)
	if err != nil {
		return false, err
	}
	if ok {
		s.repo.touch()
	}
	return ok, nil
}
```

Wire the real `scimRepo` adapter over `repository.UserRepository` at the point where the `Service` is constructed (mirror how other repo-backed services are wired). The adapter's `getHash` calls `GetSCIMConfig` (returns `"", false` on `ErrNotFound`); `setHash` calls `UpsertSCIMToken(bootstrapTenantID, ...)`; `touch` calls `TouchSCIMLastUsed`. Keep `ctx` on the adapter (capture the request context per call).

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSCIMToken -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/scim_token.go services/auth/internal/service/scim_token_test.go
git commit -m "feat(auth): SCIM token generate/verify (argon2, fail-closed)"
```

---

### Task 5: Handler — SCIM error envelope + `requireSCIMAuth`

**Files:**
- Create: `services/auth/internal/handler/scim_types.go` (error envelope only for now)
- Create: `services/auth/internal/handler/http_scim.go` (`requireSCIMAuth`)
- Test: `services/auth/internal/handler/scim_test.go`

**Context:** `bearer.Extract(authHeader) (string, bool)` lives in `libs/auth/bearer`. The middleware verifies the token via the service (Task 4) and, on success, calls the wrapped handler. It writes a SCIM-shaped `401` on failure. No RBAC — the SCIM principal is not a user.

- [ ] **Step 1: Write the failing test**

```go
package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireSCIMAuth_rejectsBadToken(t *testing.T) {
	// scimVerifier is the tiny interface requireSCIMAuth depends on.
	verify := func(raw string) (bool, error) { return raw == "scim.good", nil }
	h := newSCIMAuthTestHandler(t, verify) // helper wires requireSCIMAuth(verify, next)

	// No/blank Authorization → 401.
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", rec.Code)
	}

	// Wrong token → 401.
	req = httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer scim.wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}

	// Right token → reaches next (200).
	req = httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer scim.good")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("good token: want 200, got %d", rec.Code)
	}
}
```

Add the `newSCIMAuthTestHandler` helper in the same test file: it builds an `http.ServeMux`, registers `GET /scim/v2/Users` wrapped by `requireSCIMAuth(verify, func(w,r){w.WriteHeader(200)})`, and returns the mux.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestRequireSCIMAuth -v`
Expected: FAIL (undefined `requireSCIMAuth`).

- [ ] **Step 3: Write the error envelope + middleware**

`scim_types.go`:

```go
package handler

import (
	"encoding/json"
	"net/http"
)

// scimError is the RFC 7644 §3.12 error response shape.
type scimError struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	SCIMType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// writeSCIMError writes a SCIM-shaped error with the given HTTP status.
func writeSCIMError(w http.ResponseWriter, status int, scimType, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(scimError{
		Schemas:  []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		Status:   http.StatusText(status), // numeric string set below
		SCIMType: scimType,
		Detail:   detail,
	})
}
```

Note: RFC wants `status` as the numeric string (e.g. `"401"`). Replace `http.StatusText(status)` with `strconv.Itoa(status)` and import `strconv`. (Kept explicit so the implementer sets it correctly.)

`http_scim.go`:

```go
package handler

import (
	"net/http"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
)

// scimVerifier verifies a raw SCIM bearer token. Returns (true, nil) only when
// the config is enabled and the token matches.
type scimVerifier func(raw string) (bool, error)

// requireSCIMAuth gates a SCIM handler on the global SCIM token. It is NOT the
// user auth path: the SCIM principal carries no RBAC roles and is valid only on
// /scim/v2/*. Fail-closed — any error or mismatch is a 401.
func requireSCIMAuth(verify scimVerifier, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearer.Extract(r.Header.Get("Authorization"))
		if !ok || raw == "" {
			writeSCIMError(w, http.StatusUnauthorized, "", "missing or malformed bearer token")
			return
		}
		valid, err := verify(raw)
		if err != nil || !valid {
			writeSCIMError(w, http.StatusUnauthorized, "", "invalid SCIM token")
			return
		}
		next(w, r)
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestRequireSCIMAuth -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/handler/scim_types.go services/auth/internal/handler/http_scim.go services/auth/internal/handler/scim_test.go
git commit -m "feat(auth): SCIM error envelope + requireSCIMAuth (fail-closed)"
```

---

### Task 6: Handler — discovery endpoints + `RegisterSCIM` + wire-up

**Files:**
- Modify: `services/auth/internal/handler/http_scim.go` (add discovery handlers + `RegisterSCIM`)
- Modify: `services/auth/internal/handler/http.go` (call `RegisterSCIM(mux)` in `Register`, ~line 186 area)
- Test: `services/auth/internal/handler/scim_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSCIMDiscovery_serviceProviderConfig(t *testing.T) {
	h := newSCIMDiscoveryTestHandler(t) // registers discovery under requireSCIMAuth with an always-true verifier
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	req.Header.Set("Authorization", "Bearer scim.good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["patch"]; !ok {
		t.Errorf("ServiceProviderConfig must advertise a patch capability")
	}
	if _, ok := body["authenticationSchemes"]; !ok {
		t.Errorf("ServiceProviderConfig must advertise authenticationSchemes")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestSCIMDiscovery -v`
Expected: FAIL.

- [ ] **Step 3: Implement discovery + RegisterSCIM**

Add to `http_scim.go`:

```go
import "encoding/json"

// serviceProviderConfig is the static SCIM capability advertisement.
func (h *HTTPHandler) scimServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	cfg := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":       map[string]any{"supported": true},
		"bulk":        map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":      map[string]any{"supported": true, "maxResults": 200},
		"changePassword": map[string]any{"supported": false},
		"sort":        map[string]any{"supported": false},
		"etag":        map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "OAuth Bearer Token",
			"description": "Authentication via the deployment SCIM bearer token.",
		}},
	}
	writeSCIMJSON(w, http.StatusOK, cfg)
}

// scimResourceTypes / scimSchemas return the static User resource type + schema.
// (Return the minimal RFC 7643 User resourceType + core:2.0:User schema JSON —
// see the spec §3. Hard-code the JSON; these never change at runtime.)

// writeSCIMJSON marshals v with the application/scim+json content type.
func writeSCIMJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RegisterSCIM mounts all /scim/v2/* routes, each gated by requireSCIMAuth.
// verify is the token verifier (Task 4); users is the SCIM users service (Phase 2).
func (h *HTTPHandler) RegisterSCIM(mux *http.ServeMux, verify scimVerifier) {
	g := func(fn http.HandlerFunc) http.HandlerFunc { return requireSCIMAuth(verify, fn) }
	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", g(h.scimServiceProviderConfig))
	mux.HandleFunc("GET /scim/v2/ResourceTypes", g(h.scimResourceTypes))
	mux.HandleFunc("GET /scim/v2/Schemas", g(h.scimSchemas))
	// Users routes are added in Task 12.
}
```

Implement `scimResourceTypes` + `scimSchemas` returning the static JSON from spec §3 (minimal `User` resourceType with `endpoint:"/Users"` + the `core:2.0:User` schema listing `userName`, `name`, `emails`, `active`, `externalId`). Use the same `writeSCIMJSON` helper.

In `http.go` `Register`, after the existing route block, add:

```go
h.RegisterSCIM(mux, h.svc.VerifySCIMToken)
```

where `VerifySCIMToken(raw string) (bool, error)` is a thin `Service` method delegating to the `scimTokenSvc` (Task 4). Add that method on `Service`.

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestSCIMDiscovery -v`
Expected: PASS. Then full package build: `GOWORK=off go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/handler/http_scim.go services/auth/internal/handler/http.go services/auth/internal/handler/scim_test.go services/auth/internal/service/scim_token.go
git commit -m "feat(auth): SCIM discovery endpoints + RegisterSCIM wire-up"
```

---

## Phase 2 — Users endpoints

### Task 7: Repository — SCIM user queries

**Files:**
- Modify: `services/auth/internal/repository/scim.go`
- Test: covered by integration (Task 13).

**Context:** mirror `CreateSSOUser` (`user.go:244`) for the INSERT, adding `external_id` + `provisioned_via='scim'`. `CreateSSOUserRequest` fields: `TenantID, Username, Email, DisplayName, SSOProviderID, SSOSubject`. The scan column list must match the `User` struct's `scanOne` — copy the exact `RETURNING`/scan list from `CreateSSOUser` and append `COALESCE(external_id, '')` if you add it to the `User` struct; simplest is to NOT add external_id to `User` and instead expose it via a dedicated `GetUserByExternalID` returning `*User` (reusing the standard select).

- [ ] **Step 1: Write the methods**

```go
// CreateSCIMUser inserts an IdP-provisioned user: passwordless, kind='human',
// stamped with external_id + provisioned_via='scim'. Returns ErrAlreadyExists on
// a (tenant_id, username|email|external_id) collision so the service can fall
// back to link-by-email (spec D3).
func (r *UserRepository) CreateSCIMUser(ctx context.Context, tenantID uuid.UUID, username, email, displayName, externalID string) (*User, error) {
	const q = `
		INSERT INTO users (tenant_id, username, email, password_hash, display_name,
		                   external_id, provisioned_via, kind)
		VALUES ($1, $2, NULLIF($3, ''), '', NULLIF($4, ''), $5, 'scim', 'human')
		RETURNING id, tenant_id, username, COALESCE(email, ''), display_name,
		          password_hash, is_active, failed_logins, locked_until,
		          last_login_at, created_at, updated_at, kind, is_global_admin,
		          onboarding_complete, COALESCE(sso_subject, '')`
	return r.scanUserRow(ctx, q, tenantID, username, email, displayName, externalID)
}
```

Use whatever row-scan helper `CreateSSOUser` uses (copy its `.Scan(...)` block verbatim into a local `scanUserRow` or inline it — match the exact column order at `user.go:255-258`). If `CreateSSOUser` maps unique-violation → `ErrAlreadyExists`, replicate that mapping (grep `ErrAlreadyExists` in `user.go`).

```go
// GetUserByExternalID returns the user with this external_id in the tenant, or
// ErrNotFound. Used for SCIM read + re-provision idempotency.
func (r *UserRepository) GetUserByExternalID(ctx context.Context, tenantID uuid.UUID, externalID string) (*User, error) {
	// Reuse the standard user select with a WHERE on (tenant_id, external_id).
	// Copy the SELECT column list + scan from GetByID (user.go) and swap the WHERE.
	// ... (identical scan to GetByID)
}

// SetExternalID backfills external_id + provisioned_via on an existing user
// (the link-passwordless path, spec D3). Scoped by tenant_id + id.
func (r *UserRepository) SetExternalID(ctx context.Context, tenantID, userID uuid.UUID, externalID string) error {
	const q = `UPDATE users SET external_id = $3, provisioned_via = 'scim', updated_at = now()
	           WHERE id = $2 AND tenant_id = $1`
	_, err := r.pool.Exec(ctx, q, tenantID, userID, externalID)
	return err
}

// ListSCIMUsers returns up to `count` users starting at 1-based `startIndex`,
// filtered by the optional exact-match predicates, plus the total count. Passing
// empty filter strings matches all. Ordered by created_at for a stable page.
func (r *UserRepository) ListSCIMUsers(ctx context.Context, tenantID uuid.UUID, byUsername, byExternalID string, activeFilter *bool, startIndex, count int) (users []*User, total int, err error) {
	// Build a parameterised WHERE from the non-empty filters (never fmt.Sprintf
	// user values into SQL). COUNT(*) for total, then the page with LIMIT/OFFSET
	// (OFFSET = startIndex-1). Scan each row with the same column list as GetByID.
}
```

Implement the `GetUserByExternalID` and `ListSCIMUsers` bodies by copying the standard user SELECT column list + `.Scan` from `GetByID`/`scanOne` in `user.go` (do NOT invent columns — match the existing scan exactly). For `ListSCIMUsers`, assemble `where := []string{"tenant_id = $1"}` + `args := []any{tenantID}` and append predicates (`username = $N`, `external_id = $N`, `is_active = $N`) only for provided filters.

- [ ] **Step 2: Verify build**

Run: `cd services/auth && GOWORK=off go build ./...`
Expected: builds.

- [ ] **Step 3: Commit**

```bash
git add services/auth/internal/repository/scim.go
git commit -m "feat(auth): SCIM user queries (create/get-by-externalid/list/link)"
```

---

### Task 8: Handler — SCIM User wire types + mapper + filter parser

**Files:**
- Modify: `services/auth/internal/handler/scim_types.go`
- Test: `services/auth/internal/handler/scim_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestParseUserFilter(t *testing.T) {
	cases := []struct {
		in                       string
		wantUser, wantExt        string
		wantActive               *bool
		wantErr                  bool
	}{
		{in: ``, wantUser: "", wantExt: ""},
		{in: `userName eq "alice"`, wantUser: "alice"},
		{in: `externalId eq "ext-1"`, wantExt: "ext-1"},
		{in: `active eq true`, wantActive: boolPtr(true)},
		{in: `displayName co "x"`, wantErr: true}, // unsupported
	}
	for _, c := range cases {
		u, e, a, err := parseUserFilter(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil || u != c.wantUser || e != c.wantExt {
			t.Errorf("%q: got user=%q ext=%q err=%v", c.in, u, e, err)
		}
		if (a == nil) != (c.wantActive == nil) || (a != nil && *a != *c.wantActive) {
			t.Errorf("%q: active mismatch", c.in)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestParseUserFilter -v`
Expected: FAIL (undefined `parseUserFilter`).

- [ ] **Step 3: Implement types, mapper, filter parser**

```go
// scimUser is the SCIM core:2.0:User wire shape (subset we support).
type scimUser struct {
	Schemas     []string      `json:"schemas"`
	ID          string        `json:"id,omitempty"`
	ExternalID  string        `json:"externalId,omitempty"`
	UserName    string        `json:"userName"`
	Name        *scimName     `json:"name,omitempty"`
	DisplayName string        `json:"displayName,omitempty"`
	Emails      []scimEmail   `json:"emails,omitempty"`
	Active      bool          `json:"active"`
	Meta        *scimMeta     `json:"meta,omitempty"`
}
type scimName struct { Formatted string `json:"formatted,omitempty"` }
type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
}
type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Location     string `json:"location,omitempty"`
}

// scimListResponse is the RFC 7644 paged list envelope.
type scimListResponse struct {
	Schemas      []string    `json:"schemas"`
	TotalResults int         `json:"totalResults"`
	StartIndex   int         `json:"startIndex"`
	ItemsPerPage int         `json:"itemsPerPage"`
	Resources    []scimUser  `json:"Resources"`
}

// primaryEmail returns the primary (or first) email value.
func (u scimUser) primaryEmail() string {
	for _, e := range u.Emails {
		if e.Primary { return e.Value }
	}
	if len(u.Emails) > 0 { return u.Emails[0].Value }
	return ""
}
```

Add a `toSCIMUser(u *repository.User, extID string) scimUser` mapper (sets `schemas`, `id`, `externalId`, `userName`, `displayName`, `emails[{value,primary:true}]`, `active` from `u.IsActive`, `meta{resourceType:"User", created, lastModified, location:"/scim/v2/Users/"+id}`).

Filter parser:

```go
import (
	"fmt"
	"regexp"
	"strings"
)

var (
	reEqStr  = regexp.MustCompile(`^(userName|externalId)\s+eq\s+"([^"]*)"$`)
	reActive = regexp.MustCompile(`^active\s+eq\s+(true|false)$`)
)

// parseUserFilter supports exactly `userName eq "x"`, `externalId eq "y"`, and
// `active eq true|false` (spec D6). Empty filter matches all. Anything else is
// an error the handler maps to 400 scimType=invalidFilter.
func parseUserFilter(f string) (byUsername, byExternalID string, active *bool, err error) {
	f = strings.TrimSpace(f)
	if f == "" {
		return "", "", nil, nil
	}
	if m := reEqStr.FindStringSubmatch(f); m != nil {
		if m[1] == "userName" {
			return m[2], "", nil, nil
		}
		return "", m[2], nil, nil
	}
	if m := reActive.FindStringSubmatch(f); m != nil {
		b := m[1] == "true"
		return "", "", &b, nil
	}
	return "", "", nil, fmt.Errorf("unsupported filter: %q", f)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestParseUserFilter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/handler/scim_types.go services/auth/internal/handler/scim_test.go
git commit -m "feat(auth): SCIM User wire types + mapper + filter parser"
```

---

### Task 9: Service — provision (create + collision)

**Files:**
- Create: `services/auth/internal/service/scim_users.go`
- Test: `services/auth/internal/service/scim_users_test.go`

**Context:** the collision rule (D3): on `POST`, if no user has this email → create; if a user exists → link (backfill external_id) only if `password_hash == ""` (passwordless), else return a typed `ErrSCIMConflict`. After create, grant `reader` @ org `*` via `s.GrantRole` (`auth.go:1604`, takes a `repository.RoleAssignment`). `DeriveSSOUsername(email)` derives a username when none supplied. Use the bootstrap tenant id the Service already holds.

- [ ] **Step 1: Write the failing test** (fake user repo with GetByEmail/CreateSCIMUser/SetExternalID/GrantRole)

```go
func TestSCIMProvision_newUser_createsAndGrantsReader(t *testing.T) {
	// fake repo: GetByEmail → ErrNotFound; CreateSCIMUser → returns a user;
	// records the GrantRole call.
	...
	res, err := svc.Provision(ctx, ScimProvisionInput{Email: "a@x.io", UserName: "a", ExternalID: "ext1"})
	if err != nil { t.Fatalf("provision: %v", err) }
	if !res.GrantedReader { t.Error("expected a reader grant on the new user") }
}

func TestSCIMProvision_collisionPasswordless_links(t *testing.T) {
	// fake: GetByEmail → existing user with password_hash=="" → expect SetExternalID called, no create.
}

func TestSCIMProvision_collisionLocalPassword_conflict(t *testing.T) {
	// fake: GetByEmail → existing user with password_hash!="" → expect ErrSCIMConflict, no create/link.
}
```

Define the fakes in the test file implementing the minimal `scimUserRepo` interface the service needs.

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSCIMProvision -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ErrSCIMConflict signals a provision that collides with a local-password
// account (spec D3) — the handler maps it to 409 Conflict.
var ErrSCIMConflict = errors.New("scim: user exists with a local password")

type ScimProvisionInput struct {
	Email       string
	UserName    string // optional; derived from email when empty
	DisplayName string
	ExternalID  string
}

type ScimProvisionResult struct {
	User          *repository.User
	Linked        bool // adopted an existing passwordless user
	GrantedReader bool
}

// Provision creates (or links) a SCIM user per spec §7 + D3/D5.
func (s *Service) Provision(ctx context.Context, in ScimProvisionInput) (*ScimProvisionResult, error) {
	tenantID := s.bootstrapTenantID // the Service already resolves this in single mode
	username := in.UserName
	if username == "" {
		username = DeriveSSOUsername(in.Email)
	}

	existing, err := s.users.GetByEmail(ctx, tenantID, in.Email)
	switch {
	case err == nil:
		// Collision (D3): link only a passwordless account.
		if existing.PasswordHash != "" {
			return nil, ErrSCIMConflict
		}
		if err := s.users.SetExternalID(ctx, tenantID, existing.ID, in.ExternalID); err != nil {
			return nil, err
		}
		return &ScimProvisionResult{User: existing, Linked: true}, nil
	case errors.Is(err, repository.ErrNotFound):
		// fall through to create
	default:
		return nil, err
	}

	u, err := s.users.CreateSCIMUser(ctx, tenantID, username, in.Email, in.DisplayName, in.ExternalID)
	if err != nil {
		return nil, err
	}
	// D5 — baseline reader @ org "*" (SSO parity).
	granted := false
	if gerr := s.GrantRole(ctx, repository.RoleAssignment{
		TenantID: tenantID, UserID: u.ID, RoleName: "reader", ScopeType: "org", ScopeValue: "*",
	}); gerr == nil {
		granted = true
	} else {
		return nil, gerr
	}
	return &ScimProvisionResult{User: u, GrantedReader: granted}, nil
}
```

Confirm `GetByEmail`'s exact signature + `RoleAssignment`'s field names (`grep -n "type RoleAssignment" services/auth/internal/repository/rbac.go` and `func .*GetByEmail` in `user.go`) and adjust. `s.users` is the repository interface the `Service` already holds — extend that interface (and its fakes) with `GetUserByExternalID`, `CreateSCIMUser`, `SetExternalID`, `ListSCIMUsers`, and the SCIM config methods. **Every fake implementation of the `userRepo` interface in the auth codebase must gain these methods** — grep `func (f *fakeUserRepo)` and `func (f *handlerFakeUserRepo)` and add stubs (return zero values) so the build stays green.

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSCIMProvision -v`
Expected: PASS. Then `GOWORK=off go build ./...` (fixes any missing fake methods).

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/scim_users.go services/auth/internal/service/scim_users_test.go
git commit -m "feat(auth): SCIM provision (create + link-passwordless collision + reader grant)"
```

---

### Task 10: Service — deprovision (active toggle) + list

**Files:**
- Modify: `services/auth/internal/service/scim_users.go`
- Test: `services/auth/internal/service/scim_users_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSCIMSetActive_false_disables(t *testing.T) {
	// fake Service.SetUserDisabled records (userID, disabled). Provide a userID.
	err := svc.SetActive(ctx, userID, false)
	if err != nil { t.Fatalf("SetActive: %v", err) }
	if !fakeDisabled { t.Error("active:false must call SetUserDisabled(...true)") }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSCIMSetActive -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// SetActive maps SCIM `active` to the existing disable primitive (spec D4):
// active=false → SetUserDisabled(true); active=true → SetUserDisabled(false).
func (s *Service) SetActive(ctx context.Context, userID uuid.UUID, active bool) error {
	_, err := s.SetUserDisabled(ctx, s.bootstrapTenantID, userID, !active)
	return err
}

// ListSCIMUsers is a thin pass-through to the repository, applying the parsed
// filter + pagination and returning the page + total for the handler's envelope.
func (s *Service) ListSCIMUsers(ctx context.Context, byUsername, byExternalID string, active *bool, startIndex, count int) ([]*repository.User, int, error) {
	if startIndex < 1 { startIndex = 1 }
	if count <= 0 || count > 200 { count = 200 }
	return s.users.ListSCIMUsers(ctx, s.bootstrapTenantID, byUsername, byExternalID, active, startIndex, count)
}

// GetSCIMUser / DeleteSCIMUser round out the set.
func (s *Service) GetSCIMUserByID(ctx context.Context, userID uuid.UUID) (*repository.User, error) {
	u, err := s.users.GetByID(ctx, userID)
	if err != nil { return nil, err }
	if u.TenantID != s.bootstrapTenantID { return nil, repository.ErrNotFound }
	return u, nil
}
```

`SetUserDisabled` returns `(string, error)` — discard the string. Confirm the `Service` exposes `bootstrapTenantID` (or the field name it uses for the single-mode tenant); if not, resolve it the same way `SetUserDisabled`'s callers do.

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/service/ -run TestSCIMSetActive -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/scim_users.go services/auth/internal/service/scim_users_test.go
git commit -m "feat(auth): SCIM deprovision (active→SetUserDisabled) + list/get"
```

---

### Task 11: Handler — Users HTTP handlers

**Files:**
- Modify: `services/auth/internal/handler/http_scim.go`
- Test: `services/auth/internal/handler/scim_test.go` (handler-level with a fake service)

- [ ] **Step 1: Write the failing test** (POST /Users → 201 with body; GET /Users?filter → ListResponse; PATCH active:false → 200; unsupported filter → 400)

```go
func TestSCIMUsers_postCreates(t *testing.T) {
	h := newSCIMUsersTestHandler(t, fakeSCIMService{ provision: func(in service.ScimProvisionInput)(*service.ScimProvisionResult,error){
		return &service.ScimProvisionResult{User: &repository.User{ID: uuid.New(), Username: in.UserName, Email: in.Email, IsActive: true}}, nil
	}})
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice","externalId":"ext1","emails":[{"value":"a@x.io","primary":true}],"active":true}`
	req := httptest.NewRequest(http.MethodPost, "/scim/v2/Users", strings.NewReader(body))
	req.Header.Set("Authorization","Bearer scim.good")
	rec := httptest.NewRecorder(); h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated { t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body) }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestSCIMUsers -v`
Expected: FAIL.

- [ ] **Step 3: Implement the handlers + extend RegisterSCIM**

Add handlers `scimCreateUser`, `scimListUsers`, `scimGetUser`, `scimPutUser`, `scimPatchUser`, `scimDeleteUser` on `HTTPHandler`. Each: decode/validate, call the corresponding `Service` method, map errors to the SCIM envelope:
- `ErrSCIMConflict` → `409` (`scimType:"uniqueness"`).
- `repository.ErrNotFound` → `404`.
- filter parse error → `400` (`scimType:"invalidFilter"`).
- validation (bad userName/email) → `400` (`scimType:"invalidValue"`).

`scimCreateUser` reads `scimUser`, builds `ScimProvisionInput{Email: body.primaryEmail(), UserName: body.UserName, DisplayName: body.DisplayName, ExternalID: body.ExternalID}`, calls `h.svc.Provision`, and writes `toSCIMUser(res.User, body.ExternalID)` with `201`.

`scimPatchUser` supports the `active` replace op (the one Okta/Entra send for deprovision): parse the PATCH body's `Operations`, find an `op` on path `active`, call `h.svc.SetActive(id, value)`, then re-read + return the user. For `userName`/`displayName`/`emails` replace ops, update via a small repo update (or return `501` for unsupported paths in v1 — acceptable, but `active` MUST work).

Extend `RegisterSCIM` (Task 6) with:

```go
mux.HandleFunc("POST /scim/v2/Users", g(h.scimCreateUser))
mux.HandleFunc("GET /scim/v2/Users", g(h.scimListUsers))
mux.HandleFunc("GET /scim/v2/Users/{id}", g(h.scimGetUser))
mux.HandleFunc("PUT /scim/v2/Users/{id}", g(h.scimPutUser))
mux.HandleFunc("PATCH /scim/v2/Users/{id}", g(h.scimPatchUser))
mux.HandleFunc("DELETE /scim/v2/Users/{id}", g(h.scimDeleteUser))
```

Parse `{id}` with `uuid.Parse(r.PathValue("id"))` → `400 invalidValue` on error.

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run TestSCIMUsers -v`
Expected: PASS. Then `GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...`.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/handler/http_scim.go services/auth/internal/handler/scim_test.go
git commit -m "feat(auth): SCIM Users HTTP handlers (create/list/get/put/patch/delete)"
```

---

### Task 12: golangci-lint + full unit suite

**Files:** none (verification task).

- [ ] **Step 1: Run the full auth gate**

Run:
```bash
cd services/auth && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./... && GOWORK=off golangci-lint run ./...
```
Expected: all green. Fix any lint findings in the new files (gocritic/unparam/gofmt) inline.

- [ ] **Step 2: Commit any fixes**

```bash
git add -A && git commit -m "chore(auth): lint/vet fixes for the SCIM surface"
```

(If nothing to fix, skip.)

---

### Task 13: Integration test — full lifecycle

**Files:**
- Create: `services/auth/internal/handler/scim_integration_test.go` (`//go:build integration`)

**Context:** use `libs/testutil/containers.Postgres(t)` for a real DB + apply the auth migrations (mirror an existing auth integration test's setup — grep `//go:build integration` under `services/auth` for the harness that boots the schema + a `Service`). Seed a SCIM token via the repo, then drive the HTTP surface.

- [ ] **Step 1: Write the test**

Cover, in one test (real Postgres, migrations applied):
1. Generate a SCIM token (`svc.generate()` equivalent) + assert `requireSCIMAuth` accepts it and rejects a wrong one (401).
2. `POST /scim/v2/Users` (new email) → 201; assert the user exists, is passwordless, has `external_id`, and holds a `reader@*` grant.
3. `GET /scim/v2/Users?filter=userName eq "<name>"` → 200 ListResponse with 1 result; `?filter=externalId eq "<ext>"` → 1 result.
4. `PATCH active:false` → 200; assert `is_active=false` and (via the DB) that the disable path ran.
5. `PATCH active:true` → 200; assert re-enabled.
6. `POST` a second user whose email matches an existing **local-password** user → 409.

- [ ] **Step 2: Run it**

Run: `cd services/auth && GOWORK=off go test -tags integration ./internal/handler/ -run TestSCIM_Lifecycle -v`
Expected: PASS (needs Docker).

- [ ] **Step 3: Commit**

```bash
git add services/auth/internal/handler/scim_integration_test.go
git commit -m "test(auth): SCIM full-lifecycle integration test"
```

---

### Task 14: Docs + `.env.example`

**Files:**
- Modify: `services/auth/.env.example` (note: SCIM token is DB-stored, not an env var — document the `/scim/v2` base URL + that the token is generated via the admin API, coming in Phase 3).
- Modify: `docs/SERVICES.md` §2 (registry-auth) — add the `/scim/v2/*` surface.
- Modify: `docs/AUTH.md` — a short "SCIM provisioning" section (token model, endpoints, disable-not-delete, link-only-passwordless).

- [ ] **Step 1: Write the docs**

Add a `docs/SERVICES.md` §2 subsection listing the SCIM endpoints + the single-global-token auth model, and a `docs/AUTH.md` section covering the security posture (isolated principal, fail-closed, D3/D4/D5). Keep prose tight; reference the spec for full detail.

- [ ] **Step 2: Verify docs build (if the docs site is in scope)**

Run: `grep -rn "scim" docs/SERVICES.md docs/AUTH.md` — confirm the sections landed.

- [ ] **Step 3: Commit**

```bash
git add services/auth/.env.example docs/SERVICES.md docs/AUTH.md
git commit -m "docs(auth): document the SCIM /scim/v2 provisioning surface"
```

---

## Tracker hygiene (final task, before the review batch)

- [ ] Prepend a `status.md` row (SCIM Users-only v1, PR #NNN).
- [ ] Mark Tier 1 #5 in `futures.md` as DONE (Phase 1–2; Phase 3 admin UI + Groups deferred).
- [ ] Add an `FE-STATUS.md` note only if any FE shipped (Phase 3 — not in this plan).

---

## Post-implementation

After Task 14 + tracker hygiene, run the standard review batch (security + qa + code-review, worktree-isolated, read-only) per the project's review-pace convention. SCIM is a superuser surface — the **security** review is load-bearing: verify the token is fail-closed, the principal is isolated to `/scim/v2/*`, D3 prevents local-account takeover, and deprovision fully revokes (JTIs + API keys). Apply must-fixes inline; accept should-fixes as follow-ups.

**Phase 3 (admin token-management UI)** gets its own plan: 3 admin-gated endpoints (generate/rotate/disable) + BFF passthrough + a Settings panel showing the SCIM base URL and token state.
```
