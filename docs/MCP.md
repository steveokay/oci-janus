# MCP — Model Context Protocol server

The `registry-mcp` service exposes the registry as a
[Model Context Protocol](https://modelcontextprotocol.io) server so AI
coding assistants (Claude Desktop, Cursor, continue.dev, Copilot
Workspace) can query it in natural language.

**Read-only tools only in v1.** Mutating tools (promote-tag, revoke-key,
snooze-review) ship in a Wave 2 PR with explicit consent UX.

---

## Tools

| Tool | Args | What it returns |
|---|---|---|
| `registry_list_repositories` | `org?` | Repos in the workspace, filtered by org. |
| `registry_list_tags` | `org`, `repo` | Tags in one repo with digest + size + last-pulled. |
| `registry_get_manifest` | `org`, `repo`, `tag` | OCI manifest for a tag with layer summary. |
| `registry_list_service_accounts` | – | Service accounts with SA name + active key count. |
| `registry_list_stale_keys` | – | API keys not used recently, with `suggested_action`. |
| `registry_get_scan_report` | `org`, `repo`, `digest` | Vulnerability report — severity counts + top CVEs + SBOM state. |
| `registry_list_signatures` | `org`, `repo`, `digest` | Cosign / Notary signatures attached to a digest. |
| `registry_list_audit_events` | `action_prefix?`, `actor_id?`, `resource?`, `since_iso?`, `limit?` | Recent audit events. Capped at 500 per call. |
| `registry_list_promotions` | `org?`, `repo?` | Image promotions between repositories (soft-dep on FUT-020). |
| `registry_ping` | – | Returns `pong` — connectivity check. |
| `registry_version` | – | MCP server version string. |
| `registry_get_deployment_info` | – | Management URL + tenant id + transport. |

Every tool name is prefixed `registry_` so it never collides with tools
from another MCP server the operator has configured.

---

## Setup: Claude Desktop (stdio transport)

1. **Provision a service-account API key.** In the dashboard, go to
   `/api-keys`, create a service account named something like
   "claude-desktop", and issue a key with these scopes:

   - `repo:read`
   - `audit:read`
   - `scan:read`
   - `access:read`
   - `signer:read` (only if you want signature queries)

   Copy the key (`key.<uuid>.<64-hex-secret>`) — the UI shows it once.

2. **Grab the tenant id** from `/settings` (or `/admin/tenants` in
   multi-mode). In single-mode this is the bootstrap tenant id
   emitted by the `registry-auth bootstrap` CLI.

3. **Edit Claude Desktop's config.** File location:

   - macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
   - Windows: `%APPDATA%\Claude\claude_desktop_config.json`

   Add:

   ```json
   {
     "mcpServers": {
       "oci-janus-registry": {
         "command": "docker",
         "args": [
           "run", "-i", "--rm",
           "-e", "MCP_TRANSPORT=stdio",
           "-e", "MCP_MANAGEMENT_URL=https://registry.example.com",
           "-e", "MCP_API_KEY=key.xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx.yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy",
           "-e", "MCP_TENANT_ID=98dbe36b-ef28-4903-b25c-bff1b2921c9e",
           "steveokay/oci-janus-mcp:latest"
         ]
       }
     }
   }
   ```

4. **Restart Claude Desktop.** The MCP server appears in the tools
   panel; you can now ask questions like the examples below.

---

## Setup: Cursor remote / continue.dev (HTTP transport)

1. Bring up the compose profile:

   ```bash
   cd infra/docker-compose
   # Add MCP_API_KEY and MCP_TENANT_ID to .env
   docker compose --profile mcp up -d registry-mcp
   ```

2. Point the remote MCP client at `http://<host>:8092/`. Cursor's
   config format for remote MCP servers:

   ```json
   {
     "mcpServers": {
       "oci-janus-registry": { "url": "http://localhost:8092/" }
     }
   }
   ```

The HTTP transport uses the SDK's default localhost-only protection.
For a public deployment, put a reverse proxy in front and terminate TLS
+ Bearer-token auth at the proxy (the MCP protocol itself doesn't
authenticate the CLIENT to the SERVER — API-key auth happens between
the MCP server and the BFF, not between the LLM and the MCP server).

---

## Security notes

- **Read-only in v1.** No tool in this release mutates registry state.
  Wave 2 adds `registry_snooze_review`, `registry_revoke_key`,
  `registry_promote_tag` etc. behind an explicit consent flow.

- **API key never leaks to the LLM.** The MCP server scrubs the key
  literal from every error path before returning to the LLM.
  Regression test: `TestAPIKeyNeverAppearsInAnyToolOutput` in
  `services/mcp/internal/tools/tools_test.go`.

- **Audit-event queries capped at 500 per call.** `registry_list_audit_events`
  enforces a client-side cap so an LLM prompt can't accidentally exfil
  the whole audit trail in one request. Iterate with a tighter
  `since_iso` filter for more history.

- **The key is revocable.** It's a normal SA API key — revoke any time
  from `/api-keys` in the dashboard. The MCP server treats it as opaque.

- **Stdio transport requires the key to be a Docker `-e` var** in
  Claude Desktop's config, which is stored in cleartext on disk.
  Same posture as any other CLI credential.

---

## Example prompts to try

- "Which of our images contain log4j 2.14?"
- "Who pushed to prod/api yesterday?"
- "List service accounts and their active key counts."
- "Show me the last 10 API-key revocations."
- "What's the digest of prod/api:latest, and is it signed?"
- "List stale keys older than 60 days and suggest what to snooze."
- "When was prod/api:v1.2.3 last promoted, and by whom?"
- "How many critical CVEs are in prod/api's latest scan?"

---

## Troubleshooting

**"Claude sees no tools."** — Confirm the container runs by checking
`docker ps` (for HTTP mode) or by watching `docker logs` on a Claude
Desktop stdio session. The startup log line is
`{"level":"INFO","msg":"registry-mcp starting",...}` on stderr.

**"Tools return 'unauthorized' errors."** — The MCP_API_KEY probably
lacks a scope. Check `/api-keys` in the dashboard for what the key has
and add the missing `read` scope.

**"Audit events return empty."** — The BFF filters by the caller's
tenant; confirm MCP_TENANT_ID matches the tenant whose events you
expect. In single-mode this is the bootstrap tenant id, printed at
the end of `make dev-bootstrap`.

**"Claude Desktop crashes right after connecting."** — Almost always a
stdout leak from a log line. File a bug — the invariant test
(`TestStdioStdoutIsOnlyJSONRPC`) is supposed to catch this before
merge, but a stray `fmt.Println` in a Wave 2 tool could slip past.
