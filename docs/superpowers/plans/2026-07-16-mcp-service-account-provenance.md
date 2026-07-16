# MCP Service-Account Provenance + Connected-Agents Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make MCP one-click-connect service accounts discoverable — an `origin` marker on service accounts (backfilled for existing `mcp-agent-*` rows), surfaced as a badge + advisory-scope tooltip in the SA list, plus a Settings → Connected Agents view that filters to MCP SAs with last-used + revoke.

**Architecture:** SA create is HTTP-only on registry-auth (there is **no** Create gRPC RPC). `origin` threads FE → auth HTTP `POST /service-accounts` → service → repository → new `service_accounts.origin` column. It's read back on the auth HTTP list response (which the dashboard FE consumes) and additionally on the canonical proto/gRPC/BFF read model. `last_used_at` and `created_at` are already wired end-to-end — no work there. The Connected-Agents view filters `origin==='mcp-connect'` **client-side** (no server filter).

**Tech Stack:** Go 1.25 (pgx, goose migrations, gRPC/buf), React/TypeScript (TanStack Query + Router, vitest), Postgres 16.

**Spec:** `docs/superpowers/specs/2026-07-16-mcp-service-account-provenance-design.md` (Parts 1+2; Part 3 — enforcing the `*:read` scopes — is a separate deferred spec).

**Branch:** `feat/mcp-sa-provenance` (already created; the spec is committed on it).

---

## Conventions

- Auth service module is isolated: build/test with `cd services/auth && GOWORK=off go test ./...`. Same for `services/management`. Windows box; use `go` directly with `GOWORK=off`.
- Proto change → run `make proto` (or `buf generate` from `proto/`) to regenerate the committed stubs in `proto/gen/go/`; `buf lint` + `buf breaking` must pass (additive only).
- FE: run all 4 gates before considering FE done — `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build` (CLAUDE.md §15.1).
- Commit messages end with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`; use a bash heredoc, never PowerShell here-strings.
- Do NOT `git stash` or `git checkout`/`switch`/`reset` other branches — shared working tree, stay on `feat/mcp-sa-provenance`. Never `git push` unless the finishing step says so.
- `origin` is a closed app-level enum: `'manual'` (default) | `'mcp-connect'`. Stored as TEXT (not a PG enum) so future origins don't need a type migration.

---

## File Structure

- `services/auth/migrations/20260716000001_service_account_origin.sql` (**new**) — add `origin` column + backfill + down.
- `services/auth/internal/repository/service_account.go` (**modify**) — `Origin` on the `ServiceAccount` row + `CreateServiceAccountInput`; INSERT + list SELECT/scan.
- `services/auth/internal/service/service_account.go` (**modify**) — `Origin` on `ServiceAccountInput`; pass-through.
- `services/auth/internal/handler/http_service_accounts.go` (**modify**) — decode + validate + default `origin`; expose it on the list response.
- `proto/auth/v1/auth.proto` (**modify**) — `string origin = 10;` on `ServiceAccountSummary`.
- `services/auth/internal/handler/grpc.go` (**modify**) — map `origin` into the proto summary.
- `services/management/internal/handler/mcp_bff_routes.go` (**modify**) — `origin` on the BFF DTO.
- `frontend/src/lib/api/service-accounts.ts` (**modify**) — `origin?` on the type.
- `frontend/src/lib/api/mcp.ts` (**modify**) — send `origin:'mcp-connect'` on mint.
- `frontend/src/components/access/ServiceAccountsTable.tsx` (**modify**) — MCP badge + advisory tooltip.
- `frontend/src/components/settings/ConnectedAgentsPanel.tsx` (**new**) — the Connected-Agents list + revoke.
- `frontend/src/routes/_authenticated.settings.connected-agents.tsx` (**new**) — the route.
- `frontend/src/routes/_authenticated.settings.tsx` (**modify**) — register the tab + eyebrow.
- Test files alongside each.

---

## Task 1: `origin` column migration (auth)

**Files:**
- Create: `services/auth/migrations/20260716000001_service_account_origin.sql`

- [ ] **Step 1: Write the migration** (goose format, up + down)

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE service_accounts
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual';
-- +goose StatementEnd

-- +goose StatementBegin
-- Backfill existing MCP one-click-connect rows by their name convention so the
-- current mcp-agent-* service accounts become discoverable retroactively.
-- Idempotent: safe to re-run (only flips rows still marked 'manual').
UPDATE service_accounts
    SET origin = 'mcp-connect'
    WHERE name LIKE 'mcp-agent-%' AND origin = 'manual';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE service_accounts DROP COLUMN origin;
-- +goose StatementEnd
```

> Verify the goose directive style matches the other files in `services/auth/migrations/` (some use `-- +goose StatementBegin/End`, some don't for single statements). Match the existing convention in that directory. Confirm `20260716000001` sorts after the latest existing migration (`20260711120100_scim_config.sql`).

- [ ] **Step 2: Apply it against a scratch DB to confirm up+down are clean**

If a migration test harness exists (`grep -rl "goose" services/auth/internal` / a `migrate_test.go`), run it. Otherwise defer verification to Task 2's repository test (testcontainers applies all migrations on the embedded FS). Either way the column must exist for Task 2.

- [ ] **Step 3: Commit**

```bash
git add services/auth/migrations/20260716000001_service_account_origin.sql
git commit -m "$(cat <<'EOF'
feat(auth): add service_accounts.origin column + backfill mcp-agent-* rows

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Repository — persist + read `origin` (auth)

**Files:**
- Modify: `services/auth/internal/repository/service_account.go` (`ServiceAccount` row struct; `CreateServiceAccountInput` ~line 61; `CreateAtomic` INSERT ~line 159; `listSQL` ~line 217)
- Test: `services/auth/internal/repository/service_account_test.go`

- [ ] **Step 1: Write the failing test**

Add a test that a created SA round-trips `origin`, defaults to `'manual'` when empty, and that `List` returns it. Mirror the existing repository test harness in that file (it already uses a testcontainers PG + applies migrations — copy its setup/fixtures exactly):

```go
func TestCreateServiceAccount_persistsOrigin(t *testing.T) {
	ctx, repo, tenantID, creatorID := setupSARepo(t) // MATCH the real helper name/signature in this file

	// explicit origin
	sa, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID: tenantID, Name: "mcp-agent-testx", Description: "d",
		AllowedScopes: []string{"repo:read"}, CreatedBy: creatorID, Origin: "mcp-connect",
	})
	if err != nil { t.Fatalf("create: %v", err) }
	if sa.Origin != "mcp-connect" { t.Errorf("Origin = %q, want mcp-connect", sa.Origin) }

	// empty origin defaults to 'manual'
	sa2, _, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
		TenantID: tenantID, Name: "plain-sa", Description: "", CreatedBy: creatorID,
	})
	if err != nil { t.Fatalf("create2: %v", err) }
	if sa2.Origin != "manual" { t.Errorf("default Origin = %q, want manual", sa2.Origin) }

	// List surfaces origin
	rows, _, err := repo.List(ctx, tenantID, true, 50, "")
	if err != nil { t.Fatalf("list: %v", err) }
	got := map[string]string{}
	for _, r := range rows { got[r.Name] = r.Origin }
	if got["mcp-agent-testx"] != "mcp-connect" || got["plain-sa"] != "manual" {
		t.Errorf("list origins = %v", got)
	}
}
```

> Read the file first: match the real setup helper, the `CreateAtomic` return signature `(sa, shadowUserID, error)`, and the `List` signature `(ctx, tenantID, includeDisabled, pageSize, pageToken)`. Adjust names to reality; do not invent helpers.

- [ ] **Step 2: Run to verify it fails**

`cd services/auth && GOWORK=off go test ./internal/repository/ -run TestCreateServiceAccount_persistsOrigin -v`
Expected: compile failure (`Origin` field undefined) or fail.

- [ ] **Step 3: Implement**

(a) Add `Origin string` to the `ServiceAccount` row struct (the struct scanned from `service_accounts`).
(b) Add `Origin string` to `CreateServiceAccountInput` (~line 61).
(c) In `CreateAtomic`, default + include `origin` in the INSERT. Before the INSERT, normalise:
```go
origin := in.Origin
if origin == "" {
	origin = "manual"
}
```
Change the SA INSERT (~line 159-161) to include the column + value:
```sql
INSERT INTO service_accounts
    (id, tenant_id, shadow_user_id, name, description, allowed_scopes, created_by, origin)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING ...            -- add origin to RETURNING + scan into sa.Origin
```
(d) In `listSQL` (~line 217-229) add `sa.origin` to the SELECT column list and scan it into `ServiceAccount.Origin` in the row-scan loop. Keep the `GROUP BY sa.id` — `sa.origin` is functionally dependent on the grouped PK so it needs no extra GROUP BY entry in Postgres, but if the linter/PG complains, add `sa.origin` to `GROUP BY`.

> Read the exact RETURNING clause + scan target list in `CreateAtomic` and the exact SELECT + scan in `List`, and add `origin` in the SAME position in both the SQL and the `.Scan(...)` argument list. Column/scan order must stay aligned.

- [ ] **Step 4: Run to verify it passes**

`cd services/auth && GOWORK=off go test ./internal/repository/ -run TestCreateServiceAccount_persistsOrigin -v` → PASS. Then full package: `GOWORK=off go test ./internal/repository/ -count=1`.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/repository/service_account.go services/auth/internal/repository/service_account_test.go
git commit -m "$(cat <<'EOF'
feat(auth): persist + read service-account origin in the repository

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Service + HTTP handler — accept, validate, expose `origin` (auth)

**Files:**
- Modify: `services/auth/internal/service/service_account.go` (`ServiceAccountInput` ~line 155; `Create` passes Origin to repo ~line 216)
- Modify: `services/auth/internal/handler/http_service_accounts.go` (`createServiceAccountBody` ~line 229; `createServiceAccount` ~line 236; the list response struct ~line 44 + `saWithStatsToResponse`)
- Test: `services/auth/internal/handler/http_service_accounts_test.go`

- [ ] **Step 1: Write the failing test**

Add handler tests: create with `origin:"mcp-connect"` stores it; invalid origin → 400; list response carries `origin`. Mirror existing create/list handler tests in the file (copy their request/httptest harness):

```go
func TestCreateServiceAccount_originValidatedAndStored(t *testing.T) {
	h, _ := newSATestHandler(t) // MATCH the real test-handler constructor in this file

	// invalid origin -> 400
	rr := doCreateSA(t, h, `{"name":"x-sa","origin":"bogus"}`) // MATCH the real request helper
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid origin: got %d, want 400", rr.Code)
	}

	// valid mcp-connect origin -> 201 and echoed back
	rr = doCreateSA(t, h, `{"name":"mcp-agent-h1","origin":"mcp-connect"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("valid create: got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"origin":"mcp-connect"`) {
		t.Errorf("response missing origin: %s", rr.Body.String())
	}
}
```

> If the handler tests in this file use a mock/stub service rather than a real repo, assert on what the stub received (the `ServiceAccountInput.Origin`) instead of a DB round-trip. Match the file's existing testing style exactly.

- [ ] **Step 2: Run to verify it fails**

`cd services/auth && GOWORK=off go test ./internal/handler/ -run TestCreateServiceAccount_originValidatedAndStored -v` → fail/compile error.

- [ ] **Step 3: Implement**

(a) `service_account.go`: add `Origin string` to `ServiceAccountInput` (~line 155) and pass it into the `repository.CreateServiceAccountInput{...}` literal in `Create` (~line 216): `Origin: in.Origin,`.

(b) `http_service_accounts.go`:
- Add `Origin string \`json:"origin"\`` to `createServiceAccountBody` (~line 229).
- In `createServiceAccount` (~line 236), after the existing validation and before calling the service, validate + default origin:
```go
origin := body.Origin
if origin == "" {
	origin = "manual"
}
if origin != "manual" && origin != "mcp-connect" {
	writeError(w, http.StatusBadRequest, "BADREQUEST", "invalid origin: must be 'manual' or 'mcp-connect'")
	return
}
```
Then pass `Origin: origin` into the `service.ServiceAccountInput{...}` at the `h.saService.Create(...)` call (~line 278).
- Add `Origin string \`json:"origin"\`` to the list/response struct (~line 44-61) and set it in `saWithStatsToResponse` from the row's `Origin`. (Confirm this same response struct is what the create handler returns — if create returns a different response struct, add `origin` there too so the create response echoes it, which the test above asserts.)

- [ ] **Step 4: Run to verify it passes + package green**

`cd services/auth && GOWORK=off go test ./internal/handler/ -run TestCreateServiceAccount_originValidatedAndStored -v` → PASS. Then `GOWORK=off go test ./internal/handler/ ./internal/service/ -count=1` and `GOWORK=off go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/service/service_account.go services/auth/internal/handler/http_service_accounts.go services/auth/internal/handler/http_service_accounts_test.go
git commit -m "$(cat <<'EOF'
feat(auth): accept + validate + expose service-account origin over HTTP

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Proto + gRPC + BFF — origin on the canonical read model

**Files:**
- Modify: `proto/auth/v1/auth.proto` (`ServiceAccountSummary` ~line 598)
- Regenerate: `proto/gen/go/**`
- Modify: `services/auth/internal/handler/grpc.go` (`ListServiceAccounts` mapping ~line 754-805)
- Modify: `services/management/internal/handler/mcp_bff_routes.go` (`serviceAccountResponse` ~line 47; populate ~line 89)

- [ ] **Step 1: Add the proto field**

In `ServiceAccountSummary` (after `last_used_at = 9;`):
```proto
  string origin = 10; // 'manual' | 'mcp-connect' (FUT — MCP provenance)
```

- [ ] **Step 2: Regenerate + verify contracts**

From repo root: `make proto` (or `cd proto && buf generate`). Then `cd proto && buf lint && buf breaking --against '.git#branch=main'` (or the CI's breaking baseline) → both clean (additive field only).

- [ ] **Step 3: Map origin in the gRPC handler**

In `grpc.go` `ListServiceAccounts`, where each `ServiceAccountWithStats` is mapped to `&authv1.ServiceAccountSummary{...}` (~line 785-798), add `Origin: r.Origin,` (the row now carries `Origin` from Task 2). No test-first needed for a pure field map, but if the file has a gRPC list test, extend it to assert `Origin` propagates; otherwise rely on the BFF test in Step 4.

- [ ] **Step 4: Add origin to the BFF DTO (test-first)**

Add a failing test in `services/management/internal/handler/` (mirror the existing `mcp_bff_routes` test that stubs the `AuthServiceClient` and asserts the JSON DTO) that a summary with `Origin:"mcp-connect"` surfaces as `"origin":"mcp-connect"` in the response. Then:
- Add `Origin string \`json:"origin,omitempty"\`` to `serviceAccountResponse` (~line 47-56).
- Populate it (~line 89): `Origin: sa.GetOrigin(),`.

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run ServiceAccount -count=1` → PASS; `GOWORK=off go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add proto/auth/v1/auth.proto proto/gen/go services/auth/internal/handler/grpc.go services/management/internal/handler/mcp_bff_routes.go services/management/internal/handler/*_test.go
git commit -m "$(cat <<'EOF'
feat(proto,auth,management): expose service-account origin on the read model

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Frontend Part 1 — type + mint origin + list badge

**Files:**
- Modify: `frontend/src/lib/api/service-accounts.ts` (`ServiceAccount` interface ~line 18)
- Modify: `frontend/src/lib/api/mcp.ts` (`useGenerateMcpKey` create body ~line 63-70)
- Modify: `frontend/src/components/access/ServiceAccountsTable.tsx` (`SARow` ~line 115-218)
- Tests: `frontend/src/lib/__tests__/mcp-config.test.ts` (or the mcp-connect-card test) + a `ServiceAccountsTable` test

- [ ] **Step 1: Extend the type + mint body**

- `service-accounts.ts` `ServiceAccount` interface: add `origin?: string | null;`.
- `mcp.ts` `useGenerateMcpKey` SA-create body (~line 65-69): add `origin: "mcp-connect",` alongside `name`/`description`/`allowed_scopes`.

- [ ] **Step 2: Write the failing test — mint sends origin**

In the mcp test (mirror `frontend/src/components/settings/__tests__/mcp-connect-card.test.tsx` or `frontend/src/lib/__tests__/mcp-config.test.ts` mocking `apiFetch`), assert the `POST /service-accounts` body includes `origin: "mcp-connect"`. Run `cd frontend && npm run test -- mcp` → fails (origin absent) then passes after Step 1. (If Step 1 was applied first, write the assertion and confirm it passes; keep the assertion as the regression guard.)

- [ ] **Step 3: Write the failing test — badge renders only for MCP SAs**

Add a `ServiceAccountsTable` test (mock `useServiceAccounts` to return two rows, one `origin:"mcp-connect"`, one `origin:"manual"`): assert an "MCP" badge appears for the mcp row and not the manual row. Run → fails.

- [ ] **Step 4: Implement the badge + advisory tooltip**

In `SARow`, next to the account name/avatar, render when `sa.origin === "mcp-connect"`:
```tsx
{sa.origin === "mcp-connect" && (
  <Popover>
    <PopoverTrigger asChild>
      <span><Badge tone="accent">MCP</Badge></span>
    </PopoverTrigger>
    <PopoverContent className="max-w-xs text-xs">
      Minted by the MCP one-click connect (Settings › Integrations). The
      <span className="font-mono"> *:read </span> scopes are advisory — MCP read
      access is governed by the key’s reader role, not by these labels.
    </PopoverContent>
  </Popover>
)}
```
Use the existing `Badge` (`@/components/ui/badge`) and `Popover` (`@/components/ui/popover`) imports the Explore map identified. Pick a `tone` that reads as informational (`accent`).

- [ ] **Step 5: Run FE checks**

`cd frontend && npm run test -- ServiceAccountsTable mcp` → all pass. Defer full 4-gate run to Task 7 (but you may run `npm run typecheck` now).

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/api/service-accounts.ts frontend/src/lib/api/mcp.ts frontend/src/components/access/ServiceAccountsTable.tsx frontend/src/components/settings/__tests__ frontend/src/lib/__tests__ frontend/src/components/access/__tests__ 2>/dev/null
git commit -m "$(cat <<'EOF'
feat(fe): stamp mcp-connect origin + badge MCP service accounts with advisory-scope note

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Frontend Part 2 — Connected Agents (MCP) Settings view

**Files:**
- Create: `frontend/src/components/settings/ConnectedAgentsPanel.tsx`
- Create: `frontend/src/routes/_authenticated.settings.connected-agents.tsx`
- Modify: `frontend/src/routes/_authenticated.settings.tsx` (tab union ~line 39; tabs array ~line 79-117; eyebrow ~line 123-131)
- Test: `frontend/src/components/settings/__tests__/connected-agents-panel.test.tsx`

- [ ] **Step 1: Write the failing test**

Mock `useServiceAccounts` to return a mix of origins; assert the panel lists only `origin==='mcp-connect'` rows, formats last-used via `formatRelativeDate` ("never" when null), shows the empty state when none, and that clicking Revoke opens the confirm dialog and calls `useDeleteServiceAccount().mutateAsync(id)`. Mirror `mcp-connect-card.test.tsx` mocking style (`vi.mock` the hooks + sonner). Run → fails (component doesn't exist).

- [ ] **Step 2: Implement `ConnectedAgentsPanel.tsx`**

```tsx
// Connected Agents (MCP) — lists service accounts minted by the MCP one-click
// connect (origin='mcp-connect'), with last-used + one-click revoke so operators
// can find and prune agent keys. Filters client-side (SA counts are small on a
// single-tenant box); no server-side origin filter.
import { useState } from "react";
import { useServiceAccounts, useDeleteServiceAccount } from "@/lib/api/service-accounts";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDestructiveDialog } from "@/components/ui/confirm-destructive-dialog";
import { formatRelativeDate } from "@/lib/format";

export function ConnectedAgentsPanel() {
  const { data, isLoading, isError } = useServiceAccounts({ includeDisabled: true });
  const del = useDeleteServiceAccount();
  const [revoking, setRevoking] = useState<{ id: string; name: string } | null>(null);

  const agents = (data ?? []).filter((sa) => sa.origin === "mcp-connect");

  if (isLoading) return <div className="text-sm text-muted">Loading…</div>;
  if (isError) return <div className="text-sm text-danger">Failed to load connected agents.</div>;
  if (agents.length === 0) {
    return (
      <EmptyState
        title="No connected agents"
        description="MCP agents you connect via Settings › Integrations appear here so you can review and revoke them."
      />
    );
  }

  return (
    <>
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-muted">
            <th>Agent</th><th>Keys</th><th>Last used</th><th>Created</th><th></th>
          </tr>
        </thead>
        <tbody>
          {agents.map((sa) => (
            <tr key={sa.id} className="border-t">
              <td className="font-mono">{sa.name} <Badge tone="accent">MCP</Badge></td>
              <td>{sa.active_key_count}</td>
              <td>{sa.last_used_at ? formatRelativeDate(sa.last_used_at) : "never"}</td>
              <td>{sa.created_at ? formatRelativeDate(sa.created_at) : "—"}</td>
              <td>
                <button className="text-danger" onClick={() => setRevoking({ id: sa.id, name: sa.name })}>
                  Revoke
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <ConfirmDestructiveDialog
        open={revoking !== null}
        onOpenChange={(o) => { if (!o) setRevoking(null); }}
        title="Revoke connected agent?"
        description="This deletes the service account and its MCP key. The agent will stop working immediately."
        severity="medium"
        resourceName={revoking?.name ?? ""}
        loading={del.isPending}
        onConfirm={async () => {
          if (revoking) { await del.mutateAsync(revoking.id); setRevoking(null); }
        }}
      />
    </>
  );
}
```

> Match the real prop names of `ConfirmDestructiveDialog` and `EmptyState` (the Explore map gives the confirm-dialog props: `open`, `onOpenChange`, `title`, `description`, `severity`, `resourceName`, `onConfirm`, `loading`). Adjust class names to the table styling used in `ServiceAccountsTable.tsx` for visual consistency (reuse its `<thead>`/`Badge` idiom rather than the bare markup above if it differs).

- [ ] **Step 3: Create the route**

`_authenticated.settings.connected-agents.tsx` — mirror `_authenticated.settings.integrations.tsx` exactly (same `createFileRoute` + guard pattern), rendering `<div className="space-y-6"><ConnectedAgentsPanel /></div>`.

- [ ] **Step 4: Register the tab**

In `_authenticated.settings.tsx`:
- Add `| "connected-agents"` to the `SettingsTab` union (~line 39-44).
- Push a `TabDef` in the tabs array (~before line 117), gated like the SA-admin surface (`hasAnyAdminScope`):
```tsx
if (hasAnyAdminScope) {
  out.push({ key: "connected-agents", to: "/settings/connected-agents", label: "Connected Agents" });
}
```
- Add the eyebrow branch (~line 123-131): `location.pathname.startsWith("/settings/connected-agents") ? "Connected Agents" :`.

> The `routeTree.gen.ts` is regenerated by the `pre{lint,typecheck,test}` npm hooks — do not hand-edit it. Running the gates in Task 7 picks up the new route.

- [ ] **Step 5: Run FE tests**

`cd frontend && npm run test -- connected-agents` → pass.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/settings/ConnectedAgentsPanel.tsx frontend/src/routes/_authenticated.settings.connected-agents.tsx frontend/src/routes/_authenticated.settings.tsx frontend/src/components/settings/__tests__/connected-agents-panel.test.tsx
git commit -m "$(cat <<'EOF'
feat(fe): Connected Agents (MCP) settings view with last-used + revoke

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Gates, docs, tracker, live-verify

**Files:** `docs/MCP.md`, `docs/SERVICES.md`, `status.md` (+ regenerated `openapi.json`/postman if BFF DTO changed)

- [ ] **Step 1: Backend gates**

```
cd services/auth       && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go vet ./...
cd ../management && GOWORK=off go build ./... && GOWORK=off go test ./... && GOWORK=off go vet ./...
cd ../../proto && buf lint && buf breaking --against '.git#branch=main'
```
All green. Run `golangci-lint run ./...` in auth + management if installed.

- [ ] **Step 2: Frontend — all 4 gates (CLAUDE.md §15.1)**

```
cd frontend
npm run lint        # 0 errors
npm run typecheck   # 0 errors
npm run test        # all pass
npm run build       # builds cleanly (regenerates routeTree.gen.ts with the new route)
```

- [ ] **Step 3: Regenerate OpenAPI/postman if the BFF DTO changed**

If the management BFF exposes an OpenAPI/postman artifact generator (as prior BFF-DTO changes did), run it so the `origin` field is reflected. `grep -rl "openapi" services/management` to find the generator target.

- [ ] **Step 4: Live-verify on the compose stack**

Rebuild the touched services + FE:
```
cd infra/docker-compose && docker compose build registry-auth registry-management frontend && docker compose up -d registry-auth registry-management frontend
```
Then, via the running stack:
- Run the auth migration (compose auto-migrates on boot or via the bootstrap step) and confirm the pre-existing `mcp-agent-*` SAs now report `origin='mcp-connect'` (`GET /api/v1/service-accounts` — check the `origin` field), i.e. the **backfill** worked.
- Create a normal SA via the form/API → `origin='manual'`, unbadged.
- Click **MCP connect** (Settings › Integrations) → the new SA reports `origin='mcp-connect'`, shows the **MCP badge** + advisory tooltip in the SA list, and appears in **Settings › Connected Agents** with last-used + a working **Revoke**.
- Revoke it there → SA + key gone from the list.
Record the observed JSON + a note per check in the PR description.

- [ ] **Step 5: Docs + tracker**

- `docs/MCP.md`: MCP connect mints an `origin='mcp-connect'` SA; view/prune it under Settings › Connected Agents; the `*:read` scopes are **advisory today** (Part 3 will enforce them).
- `docs/SERVICES.md`: auth `service_accounts.origin` column + it flows onto the HTTP list response and the `ServiceAccountSummary` proto (field 10) + BFF DTO.
- `status.md`: prepend a row; note Part 3 (scope enforcement) is the deferred follow-up.

- [ ] **Step 6: Commit**

```bash
git add docs/MCP.md docs/SERVICES.md status.md services/management/**/openapi.json 2>/dev/null
git commit -m "$(cat <<'EOF'
docs: MCP service-account provenance + Connected Agents view

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review notes (reconciled)

- **Spec coverage:** Part 1 provenance = Tasks 1-4 (migration+backfill, repo, service/HTTP, proto/gRPC/BFF); honest labels = Task 5 (badge + advisory tooltip). Part 2 Connected-Agents = Task 6 (view + tab + revoke), riding on the already-wired `last_used_at`/`created_at`. Testing + live-verify + docs = Task 7. Part 3 (scope enforcement) intentionally absent — separate spec.
- **Deviations from spec, with rationale:** (a) No Create-proto change — there is no Create RPC; origin threads through auth HTTP only. (b) No server-side origin *filter* — the view filters client-side (YAGNI on a single-tenant handful of SAs). Both are simplifications the code-map justified; noted here so a reviewer isn't surprised.
- **Type consistency:** `origin` is the same closed enum string (`'manual'`|`'mcp-connect'`) at every layer — DB default, repo `Origin`, service `Origin`, HTTP `origin`, proto field 10, BFF DTO `origin`, FE `origin?`. `last_used_at` and `created_at` are pre-existing and untouched.
- **Verify-in-place points flagged inline:** goose directive style in the migrations dir; the real repository test-setup helper + `CreateAtomic`/`List` signatures; whether the create handler returns the same response struct as list (so the create response echoes origin); the handler test style (stub vs real repo); exact `ConfirmDestructiveDialog`/`EmptyState` prop names; the `buf breaking` baseline ref.
- **Placeholder scan:** migration timestamp is concrete (`20260716000001`); proto field number concrete (10); no TBDs.
