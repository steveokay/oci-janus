# FUT-031 MCP Server (read-only tools) Implementation Plan

> **✅ SHIPPED — PR #232. Plan complete; canonical status in `status.md` / `FE-STATUS.md`. Task checkboxes left unticked — this is a subagent-driven execution artifact, not a live tracker.**

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan.

**Goal:** Expose the registry as a Model Context Protocol (MCP) server so AI coding assistants (Claude Desktop, Cursor, Copilot Workspace) can query it in natural language: "which of our images have log4j 2.14?", "list stale API keys older than 60 days", "show me the last 10 promotions." **Read-only tools only** — no mutating tools this PR; those come in a Wave 2 follow-up with explicit consent UX.

**Architecture:** New `services/mcp` binary. Speaks MCP over stdio (Claude Desktop's transport) AND streamable-HTTP (Cursor / remote clients). Auth via a workspace-owned service-account API key (registry's own SA lifecycle, no new auth). Backend: calls the existing management BFF over HTTP so the tool surface stays honest to what an operator could do in the dashboard — no privileged shortcuts.

**Branch:** `feat/fut-031-mcp-server` (already off `main`).

**Spec anchor:** `futures.md` FUT-031.

**MCP protocol reference:** https://spec.modelcontextprotocol.io/ — implement server side. Use the official Go SDK `github.com/mark3labs/mcp-go` (widely used, permissively licensed).

---

## File Structure

**Created:**
- `services/mcp/cmd/server/main.go` — entrypoint (stdio + HTTP)
- `services/mcp/internal/config/config.go` — env-driven config
- `services/mcp/internal/client/registry.go` — small HTTP client wrapping the management BFF
- `services/mcp/internal/tools/tools.go` — tool registry + dispatch
- `services/mcp/internal/tools/repositories.go` — repository/tag tools
- `services/mcp/internal/tools/access.go` — service accounts / stale keys / API keys tools
- `services/mcp/internal/tools/security.go` — scan reports / signatures / signed-status tools
- `services/mcp/internal/tools/audit.go` — audit event query tool
- `services/mcp/internal/tools/promotions.go` — promotions list tool (depends on FUT-020 landing; ships in same wave)
- `services/mcp/internal/tools/*_test.go` — one test file per tool group
- `services/mcp/Dockerfile` — distroless build (mirror `services/audit/Dockerfile`)
- `services/mcp/.env.example` — documented env
- `services/mcp/go.mod` — self-contained module
- `services/mcp/Makefile` — build/test/lint targets
- `services/mcp/buf.gen.yaml` — even though this service doesn't own protos, keep the file for consistency
- `docs/MCP.md` — user-facing setup guide for Claude Desktop / Cursor / etc.

**Modified:**
- `go.work` — add `services/mcp` module
- `infra/docker-compose/docker-compose.yml` — new `registry-mcp` service (opt-in via `--profile mcp` — most operators don't need it running for the BFF/registry to work)
- `Makefile` — add `mcp` to `SERVICES` list

**Not modified:**
- `proto/**` — no new gRPC surface; MCP tools call the management BFF over HTTP.
- Other services — MCP is a pure consumer.

---

## Task 1: Scaffold the module

Follow `services/audit`'s layout as the closest sibling that's stdio-adjacent (background worker, no user-facing HTTP-first surface).

```
services/mcp/
├── cmd/server/main.go
├── internal/
│   ├── client/
│   ├── config/
│   └── tools/
├── Dockerfile
├── Makefile
├── go.mod
├── go.sum
└── .env.example
```

`go.mod`:

```
module github.com/steveokay/oci-janus/services/mcp
go 1.25.7

require (
    github.com/mark3labs/mcp-go v0.x.x  // latest at implementation time
    github.com/steveokay/oci-janus/libs v0.0.0
    github.com/spf13/viper v1.x.x
    github.com/google/uuid v1.x.x
)

replace github.com/steveokay/oci-janus/libs => ../../libs
```

Add to `go.work`:

```go
go.work: use ./services/mcp
```

Add to root `Makefile`'s `SERVICES` variable (find the existing list).

Commit: `git add ... && git commit -m "feat(mcp): scaffold services/mcp module (FUT-031)"`.

---

## Task 2: Config

`services/mcp/internal/config/config.go`:

```go
package config

// Config is the complete MCP server env surface.
type Config struct {
    loader.BaseConfig `mapstructure:",squash"`

    // Transport — "stdio" (Claude Desktop / Cursor) OR "http" (remote clients).
    // Default: "stdio". When "http", HTTPAddr is required.
    Transport string `mapstructure:"MCP_TRANSPORT"`
    HTTPAddr  string `mapstructure:"MCP_HTTP_ADDR"`

    // ManagementURL — the base URL of the management BFF. E.g.
    // "http://localhost:8091" in dev; "https://registry.example.com" in prod.
    // Every tool call proxies through this URL.
    ManagementURL string `mapstructure:"MCP_MANAGEMENT_URL"`

    // APIKey — the service-account API key the MCP server uses to auth
    // to the management BFF. Format: "key.<uuid>.<64-hex-secret>"
    // (FUT-006 bearer form). Operator provisions this once via /api-keys.
    // The MCP server's tools are effectively scoped to whatever this
    // key can do — no privileged shortcuts.
    APIKey string `mapstructure:"MCP_API_KEY"`

    // TenantID — the tenant whose data the MCP server exposes. Required
    // because the BFF derives tenant from JWT claims and API keys carry
    // an explicit tenant; but we still validate the caller can only
    // ever see this one tenant's data.
    TenantID string `mapstructure:"MCP_TENANT_ID"`
}
```

Validation: `ManagementURL` required; `APIKey` matches the `key.<uuid>.<64-hex>` regex; `TenantID` is a valid UUID; `Transport` is one of the two; if `http`, `HTTPAddr` required.

Test: coverage of every validation branch.

Commit: `git add ... && git commit -m "feat(mcp): env-driven config (FUT-031)"`.

---

## Task 3: Registry HTTP client

`services/mcp/internal/client/registry.go` — thin wrapper. `Get`/`GetJSON` helpers that attach `Authorization: Bearer <APIKey>`. Returns typed error for non-2xx.

Methods to implement (one per tool, but grouped by surface):

- `ListRepositories(ctx, org string) ([]Repository, error)` — proxies GET `/api/v1/repositories?org=...`
- `ListTags(ctx, org, repo string) ([]Tag, error)` — proxies GET `/api/v1/repositories/{org}/{repo}/tags`
- `GetManifest(ctx, org, repo, tag string) (*Manifest, error)`
- `ListServiceAccounts(ctx) ([]ServiceAccount, error)`
- `ListStaleKeys(ctx) ([]StaleKey, error)` — proxies GET `/api/v1/access/review/stale`
- `ListAuditEvents(ctx, filter AuditFilter) ([]AuditEvent, error)` — proxies GET `/api/v1/audit?...`
- `GetScanReport(ctx, org, repo, digest string) (*ScanReport, error)`
- `ListSignatures(ctx, org, repo, digest string) ([]Signature, error)`
- `ListPromotions(ctx, org, repo string) ([]Promotion, error)` — GET `/api/v1/repositories/{org}/{repo}/promotions` (added by FUT-020, landing this wave)

Types can be shallow — mirror the JSON shapes exposed by the BFF but only the fields the tools surface. Don't over-model.

Tests: one happy path + one non-2xx per method (use `httptest.NewServer`).

Commit: `git add ... && git commit -m "feat(mcp): registry HTTP client (FUT-031)"`.

---

## Task 4: Tool registry + dispatch

`services/mcp/internal/tools/tools.go` — a `Registry` type holding the client + slog logger. Methods return `mcp.Tool` values (from the SDK). Each tool has: name, description (LLM-readable — matters), input schema (JSON Schema), and a Go handler.

**Tool naming convention:** `registry_<verb>_<noun>`. E.g. `registry_list_repositories`, `registry_get_scan_report`, `registry_list_stale_keys`. Prefix ensures no collision with tools from other MCP servers a user might have connected.

Every tool description should include:
- What it returns
- What arguments it needs
- Example use case ("Use this to answer 'which images contain log4j?'")

**Empty-result and error shape:** every tool returns text content (MCP standard). On success, structured JSON in a code block. On error, a human-readable message the LLM can surface to the user. Errors NEVER include the API key or tenant ID; they DO include the underlying HTTP status + a short reason.

Commit at end of Task 4: `git add ... && git commit -m "feat(mcp): tool registry + dispatch shell (FUT-031)"`.

---

## Task 5: Tool implementations

Per file, per surface. Each tool: name, JSON schema for args, handler that calls the client + formats output.

### `repositories.go`

- `registry_list_repositories` — args: `{org?: string, limit?: int}`. Output: JSON array of `{org, name, created_at, immutable_tags, require_signature}`.
- `registry_list_tags` — args: `{org: string, repo: string}`. Output: JSON array of `{name, manifest_digest, size_bytes, last_pulled_at}`.
- `registry_get_manifest` — args: `{org, repo, tag}`. Output: raw manifest JSON + summary (`{layer_count, total_size_bytes, media_type}`).
- `registry_list_promotions` — args: `{org?, repo?}`. Output: JSON array from `client.ListPromotions`. Requires FUT-020 to have shipped (declare as a soft dependency; if the BFF returns 404 on this route, return "Promotions history requires FUT-020 which isn't deployed on this registry" as the tool result).

### `access.go`

- `registry_list_service_accounts` — args: `{}`. Output: JSON array of `{id, name, allowed_scopes, disabled_at, active_key_count}`.
- `registry_list_stale_keys` — args: `{}`. Output: JSON array from `client.ListStaleKeys` with `suggested_action` per row.

### `security.go`

- `registry_get_scan_report` — args: `{org, repo, digest}`. Output: summary `{vuln_count_by_severity, top_10_cves, sbom_present}` + link to full report.
- `registry_list_signatures` — args: `{org, repo, digest}`. Output: JSON array of `{key_id, algorithm, signed_at, signer}`.

### `audit.go`

- `registry_list_audit_events` — args: `{action_prefix?: string, actor_id?: string, resource?: string, since_iso?: string, limit?: int}`. Output: JSON array of audit events. **Important:** rate-limit this at the client level (cap `limit` at 500) so an LLM can't accidentally exfil the whole audit trail in one call.

Tests: per file, table-driven, one per tool with happy path + one error path. Mock the client interface.

Per-file commits — five commits total for the five files.

---

## Task 6: Entrypoint

`services/mcp/cmd/server/main.go`:

```go
func main() {
    cfg := config.MustLoad()
    logger := slog.New(...)  // stderr! stdio transport uses stdout for MCP protocol
    registry := client.NewRegistry(cfg.ManagementURL, cfg.APIKey, cfg.TenantID)
    toolReg := tools.NewRegistry(registry, logger)

    server := mcpsdk.NewServer("oci-janus-registry", "1.0.0")
    for _, t := range toolReg.All() {
        server.AddTool(t)
    }

    switch cfg.Transport {
    case "stdio":
        // Log to stderr, MCP protocol to stdout.
        server.ServeStdio()
    case "http":
        server.ServeHTTP(cfg.HTTPAddr)
    }
}
```

**Load-bearing detail for stdio transport:** log to stderr NEVER stdout. Claude Desktop treats stdout as MCP protocol frames — any stray print/log there breaks the client.

Commit: `git add ... && git commit -m "feat(mcp): server entrypoint (stdio + HTTP transports) (FUT-031)"`.

---

## Task 7: Dockerfile + compose

`services/mcp/Dockerfile` — mirror `services/audit/Dockerfile`. Distroless final. `MCP_TRANSPORT=http` is the default for compose (stdio doesn't make sense in a compose service).

`infra/docker-compose/docker-compose.yml` — add `registry-mcp` service under a new `--profile mcp` opt-in. Depends on `registry-management` healthy. Env: `MCP_TRANSPORT=http`, `MCP_HTTP_ADDR=:8087`, `MCP_MANAGEMENT_URL=http://registry-management:8085`, `MCP_TENANT_ID=${MCP_TENANT_ID}`, `MCP_API_KEY=${MCP_API_KEY}` (operator provides these — the compose block includes commented lines showing how to generate them via `/api-keys` in the dashboard).

Commit: `git add ... && git commit -m "feat(mcp): Dockerfile + opt-in compose service (FUT-031)"`.

---

## Task 8: User-facing docs

`docs/MCP.md`:

1. What MCP is (2 sentences + a link to the spec).
2. What tools the server exposes (list + one-line each).
3. Setup for Claude Desktop:
   - Create a service account + API key in `/api-keys` with read scopes (`repo:read`, `audit:read`, `scan:read`, `access:read`).
   - Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:
     ```json
     {
       "mcpServers": {
         "oci-janus-registry": {
           "command": "docker",
           "args": ["run", "-i", "--rm",
             "-e", "MCP_TRANSPORT=stdio",
             "-e", "MCP_MANAGEMENT_URL=https://registry.example.com",
             "-e", "MCP_API_KEY=key.xxx.yyy",
             "-e", "MCP_TENANT_ID=xxxx-xxxx-...",
             "steveokay/oci-janus-mcp:latest"]
         }
       }
     }
     ```
4. Setup for Cursor / continue.dev / etc. — HTTP transport variant.
5. Security notes: read-only in v1. Mutating tools coming in a Wave 2 PR with explicit consent UX. API key is a normal SA key — revocable from `/api-keys`.
6. Example prompts operators can try: "which of our images contain log4j?", "who pushed to prod/api yesterday?", "list stale keys older than 60d and suggest what to snooze."

Commit: `git add ... && git commit -m "docs(mcp): user-facing MCP setup guide (FUT-031)"`.

---

## Task 9: Local CI gate

```bash
cd services/mcp && go vet ./... && go build ./... && go test ./...
```

Zero test failures. Pre-existing REM-014 doesn't apply — this is a new service, all lint should be clean.

---

## Task 10: Tracker hygiene + 3-agent batch + PR

- Add REM-028 to `status-tracker.md` (small entry).
- Update `futures.md` FUT-031 → `**DONE — see status.md (REM-028)**` stub for the read-only surface; keep an open note that write tools are Wave 2.
- 3-agent batch BEFORE `gh pr create`. Priority items for security: API key exposure in errors/logs (must NOT appear); audit-events tool has `limit` cap; every tool call auth-forwarded to the BFF (no bypass). QA: happy-path per tool + error path per tool. Code review: package boundaries clean; tests cover the client interface substitution.

---

## Operating rules

- Per CLAUDE.md `feedback_code_comments`.
- TDD.
- **Stdio transport MUST NOT print to stdout for anything except MCP protocol frames.** All logs to stderr. Verify by piping the binary's stdout to a checker in the test.
- Tools are **read-only** — nothing that mutates. `registry_snooze_review`, `registry_revoke_key`, `registry_promote_tag` etc. are Wave 2 with consent UX.
- **API key never in errors/logs/tool responses.** Tests must assert this.
- `limit` cap on `list_audit_events` prevents accidental full-trail exfil.
- If the SDK changes API between plan-time and execution-time, adapt + document in report.

# Report back

- Status
- Number of commits (~10)
- `go test ./...` summary for services/mcp
- Manual test: run `MCP_TRANSPORT=stdio ./bin/registry-mcp` and paste a `tools/list` JSON-RPC request via stdin — confirm the 12 tools land in the response.
- Any adaptations from the plan.

Do NOT push branch or open PR — I handle tracker hygiene + 3-agent batch + PR after your DONE report.

Expected duration: ~2-3 hours. Drive through.
