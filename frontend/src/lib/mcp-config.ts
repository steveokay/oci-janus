// mcp-config — pure builder for the Claude Desktop (stdio) MCP config snippet.
//
// Kept out of the component so it is unit-testable and so the exact env-var
// contract (MCP_TRANSPORT / MCP_MANAGEMENT_URL / MCP_API_KEY / MCP_TENANT_ID,
// consumed by services/mcp/internal/config) lives in one place. Any field left
// empty renders as an obvious placeholder rather than a blank the user might
// miss.

// StdioConfigInput carries the three deployment-specific values baked into the
// generated config. The image tag and transport are fixed.
export interface StdioConfigInput {
  // tenantID is the caller's workspace tenant id (MCP_TENANT_ID).
  tenantID: string;
  // managementURL is the registry URL the MCP container calls (MCP_MANAGEMENT_URL).
  managementURL: string;
  // apiKey is the composed `key.<id>.<secret>` Bearer token (MCP_API_KEY).
  apiKey: string;
}

// buildStdioConfig renders the claude_desktop_config.json snippet as a pretty
// JSON string. Empty inputs degrade to placeholders so a half-filled config is
// visibly incomplete instead of silently broken.
export function buildStdioConfig(input: StdioConfigInput): string {
  const tenant = input.tenantID || "<tenant-id>";
  const url = input.managementURL || "https://your-registry.example.com";
  const key = input.apiKey || "key.<uuid>.<secret>";
  return JSON.stringify(
    {
      mcpServers: {
        "oci-janus-registry": {
          command: "docker",
          args: [
            "run",
            "-i",
            "--rm",
            "-e",
            "MCP_TRANSPORT=stdio",
            "-e",
            `MCP_MANAGEMENT_URL=${url}`,
            "-e",
            `MCP_API_KEY=${key}`,
            "-e",
            `MCP_TENANT_ID=${tenant}`,
            "steveokay/oci-janus-mcp:latest",
          ],
        },
      },
    },
    null,
    2,
  );
}
