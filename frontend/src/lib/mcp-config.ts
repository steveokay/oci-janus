// mcp-config — pure builder for the Claude Desktop (stdio) MCP config snippet.
//
// Kept out of the component so it is unit-testable and so the exact env-var
// contract (MCP_TRANSPORT / MCP_MANAGEMENT_URL / MCP_API_KEY / MCP_TENANT_ID,
// consumed by services/mcp/internal/config) lives in one place.
//
// Two deployment targets, because the first cut of this config connected in a
// real (gateway-fronted) deployment but FAILED on a local Docker-Compose dev
// box: there, the browser origin is the frontend SPA (localhost:3000), the MCP
// image is only built locally (never published), and `localhost` inside the
// ephemeral MCP container is the container itself — not the host. The local
// target emits the verified-working form instead: join the compose network and
// reach the BFF by its internal service DNS.

// McpTarget picks the environment the generated config runs against.
export type McpTarget = "hosted" | "local";

// ── Local Docker-Compose constants (verified against infra/docker-compose) ──
// The compose network is explicitly named `registry` (networks.registry.name),
// so it is stable regardless of the compose project name.
export const LOCAL_COMPOSE_NETWORK = "registry";
// The mcp service is built (not pulled) by compose → image `<project>-registry-mcp`.
// For the documented dev workflow the project is `docker-compose`.
export const LOCAL_MCP_IMAGE = "docker-compose-registry-mcp:latest";
// Inside the compose network the BFF is reached by service DNS + its container
// port (8085), NOT the published host port. The MCP client appends /api/v1.
export const LOCAL_MANAGEMENT_URL = "http://registry-management:8085";

// ── Hosted constants ────────────────────────────────────────────────────────
// The published image + the gateway origin the operator browses (which routes
// /api/v1 to the management BFF).
export const HOSTED_MCP_IMAGE = "steveokay/oci-janus-mcp:latest";

// detectDefaultTarget picks "local" for a loopback origin (a compose dev box)
// and "hosted" otherwise, so the card defaults to the config that will actually
// work where the operator is standing.
export function detectDefaultTarget(origin: string): McpTarget {
  return /^https?:\/\/(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d+)?\/?$/i.test(
    origin,
  )
    ? "local"
    : "hosted";
}

// StdioConfigInput carries the target plus the deployment-specific values baked
// into the generated config. managementURL is used only in hosted mode (local
// mode uses the fixed internal DNS).
export interface StdioConfigInput {
  target: McpTarget;
  tenantID: string;
  managementURL: string;
  apiKey: string;
}

// buildStdioConfig renders the claude_desktop_config.json snippet as a pretty
// JSON string for the chosen target. Empty inputs degrade to placeholders so a
// half-filled config is visibly incomplete instead of silently broken.
export function buildStdioConfig(input: StdioConfigInput): string {
  const tenant = input.tenantID || "<tenant-id>";
  const key = input.apiKey || "key.<uuid>.<secret>";
  const local = input.target === "local";
  const url = local
    ? LOCAL_MANAGEMENT_URL
    : input.managementURL || "https://your-registry.example.com";
  const image = local ? LOCAL_MCP_IMAGE : HOSTED_MCP_IMAGE;

  const runArgs = ["run", "-i", "--rm"];
  // Local mode joins the compose network so the container can resolve the BFF
  // by its service name; hosted mode reaches the gateway over the public URL.
  if (local) runArgs.push("--network", LOCAL_COMPOSE_NETWORK);
  runArgs.push(
    "-e",
    "MCP_TRANSPORT=stdio",
    "-e",
    `MCP_MANAGEMENT_URL=${url}`,
    "-e",
    `MCP_API_KEY=${key}`,
    "-e",
    `MCP_TENANT_ID=${tenant}`,
    image,
  );

  return JSON.stringify(
    {
      mcpServers: {
        "oci-janus-registry": {
          command: "docker",
          args: runArgs,
        },
      },
    },
    null,
    2,
  );
}
